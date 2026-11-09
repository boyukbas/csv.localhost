package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boyukbas/csv.localhost/web"
)

const tokenHeader = "X-CSVL-Token"
const cookieName = "csvl_session"

// cellLimit is how much of a long cell travels with a page of rows. The full
// value is one request away, via /api/cell, when the reader selects it.
const cellLimit = 300

// pageLimit caps how many rows a single /api/rows call may return.
const pageLimit = 2000

// Config controls a server instance.
type Config struct {
	PreferredPort int
	Host          string
	IdleMinutes   int
}

type recentEntry struct {
	Path string    `json:"path"`
	Name string    `json:"name"`
	Seen time.Time `json:"seen"`
}

type srv struct {
	cfg   Config
	port  int
	token string

	tmplPage *template.Template
	tmplList *template.Template

	tables *tableCache

	mu      sync.Mutex
	roots   map[string]bool      // directories the user has explicitly opened
	oneTime map[string]time.Time // single-use browser handoff keys
	recent  []recentEntry        // most recently opened files, newest first

	sseCount     atomic.Int64
	lastActivity atomic.Int64 // unix nanos

	shutdown context.CancelFunc
}

// Serve runs the HTTP server until it is stopped or goes idle.
func Serve(cfg Config) error {
	if cfg.Host == "" {
		cfg.Host = DefaultHost
	}
	if cfg.PreferredPort == 0 {
		cfg.PreferredPort = DefaultPort
	}

	ln, port, err := listen(cfg.PreferredPort)
	if err != nil {
		return err
	}
	defer ln.Close()

	s := &srv{
		cfg:     cfg,
		port:    port,
		token:   newToken(),
		tables:  newTableCache(),
		roots:   map[string]bool{},
		oneTime: map[string]time.Time{},
	}
	s.touch()
	s.loadRecent()
	closeLog := setupLogging()
	defer closeLog()

	s.tmplPage = template.Must(template.ParseFS(web.FS, "page.html"))
	s.tmplList = template.Must(template.ParseFS(web.FS, "list.html"))

	if err := writeState(&state{
		Port:    port,
		Host:    cfg.Host,
		PID:     os.Getpid(),
		Token:   s.token,
		Version: Version,
		Started: time.Now().Format(time.RFC3339),
	}); err != nil {
		return fmt.Errorf("writing state file: %w", err)
	}
	defer clearState()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.shutdown = cancel

	httpSrv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: server-sent events hold connections open.
		ErrorLog: log.New(log.Writer(), "http: ", log.LstdFlags),
	}

	go s.idleWatch(ctx)

	log.Printf("csv.localhost %s listening on %s", Version, displayOrigin(cfg.Host, port))
	if port != cfg.PreferredPort {
		log.Printf("note: port %d was unavailable, using %d", cfg.PreferredPort, port)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = httpSrv.Shutdown(shutCtx)
		return nil
	}
}

// listen binds the preferred port, falling back to a range of alternatives so
// a busy port 80 (IIS, another dev server) never blocks startup.
func listen(preferred int) (net.Listener, int, error) {
	candidates := []int{preferred}
	for _, p := range []int{8080, 8088, 8181, 9080, 0} {
		if p != preferred {
			candidates = append(candidates, p)
		}
	}
	var lastErr error
	for _, p := range candidates {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			return ln, ln.Addr().(*net.TCPAddr).Port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("could not bind a local port: %w", lastErr)
}

func (s *srv) touch() { s.lastActivity.Store(time.Now().UnixNano()) }

// idleWatch exits the process once no viewer has been connected for a while,
// so the server does not linger forever after the last tab is closed.
func (s *srv) idleWatch(ctx context.Context) {
	if s.cfg.IdleMinutes <= 0 {
		return
	}
	limit := time.Duration(s.cfg.IdleMinutes) * time.Minute
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.sseCount.Load() > 0 {
				s.touch()
				continue
			}
			if time.Since(time.Unix(0, s.lastActivity.Load())) > limit {
				log.Printf("idle for %s with no open viewers, shutting down", limit)
				s.shutdown()
				return
			}
		}
	}
}

func (s *srv) routes() http.Handler {
	mux := http.NewServeMux()

	// Public: no authentication, no filesystem access.
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.HandlerFunc(s.handleAsset)))
	mux.HandleFunc("/favicon.ico", s.handleFavicon)

	// Control plane: requires the shared token from the state file.
	mux.HandleFunc("/open", s.requireToken(s.handleOpen))
	mux.HandleFunc("/quit", s.requireToken(s.handleQuit))
	mux.HandleFunc("/key", s.requireToken(s.handleKey))

	// Browser plane: requires the session cookie.
	mux.HandleFunc("/view/", s.requireSession(s.handleView))
	mux.HandleFunc("/api/table", s.requireSession(s.handleTable))
	mux.HandleFunc("/api/rows", s.requireSession(s.handleRows))
	mux.HandleFunc("/api/stats", s.requireSession(s.handleStats))
	mux.HandleFunc("/api/cell", s.requireSession(s.handleCell))
	mux.HandleFunc("/events", s.requireSession(s.handleEvents))
	mux.HandleFunc("/", s.requireSession(s.handleIndex))

	return s.baseMiddleware(mux)
}

// baseMiddleware pins the Host header and records activity. Pinning Host
// defeats DNS rebinding, where a hostile page points its own domain at
// 127.0.0.1 to read local files through this server.
func (s *srv) baseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		switch host {
		case s.cfg.Host, "csv.localhost", "localhost", "127.0.0.1", "::1":
		default:
			http.Error(w, "unrecognised Host header", http.StatusMisdirectedRequest)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		s.touch()
		next.ServeHTTP(w, r)
	})
}

func (s *srv) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtleCompare(r.Header.Get(tokenHeader), s.token) {
			next(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}
}

// requireSession accepts either a valid session cookie or a single-use handoff
// key, and refuses requests initiated by another site.
func (s *srv) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		}
		if c, err := r.Cookie(cookieName); err == nil && subtleCompare(c.Value, s.token) {
			next(w, r)
			return
		}
		if k := r.URL.Query().Get("k"); k != "" && s.consumeOneTime(k) {
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    s.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			// Redirect to the same URL without the key so it does not linger
			// in the address bar or browser history.
			q := r.URL.Query()
			q.Del("k")
			clean := *r.URL
			clean.RawQuery = q.Encode()
			clean.Scheme, clean.Host = "", ""
			http.Redirect(w, r, clean.String(), http.StatusSeeOther)
			return
		}
		http.Error(w, "this page must be opened with the csv.localhost launcher", http.StatusForbidden)
	}
}

func (s *srv) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": Version,
		"port":    s.port,
		"host":    s.cfg.Host,
	})
}

func (s *srv) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.shutdown()
	}()
}

// handleOpen is called by `csvlocalhost open`. It authorises the file's
// folder, mints a one-time handoff key and returns the URL for the browser.
func (s *srv) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path      string `json:"path"`
		Delimiter string `json:"delimiter"`
		Header    string `json:"header"`
		Encoding  string `json:"encoding"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(req.Path)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(abs); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	s.authorize(filepath.Dir(abs))
	s.addRecent(abs)

	key := newToken()
	s.mu.Lock()
	s.oneTime[key] = time.Now().Add(60 * time.Second)
	s.mu.Unlock()

	opts := ParseOptions{Delimiter: req.Delimiter, Header: req.Header, Encoding: req.Encoding}
	u := viewURL(s.cfg.Host, s.port, abs, opts)
	if strings.Contains(u, "?") {
		u += "&k=" + url.QueryEscape(key)
	} else {
		u += "?k=" + url.QueryEscape(key)
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": u, "port": s.port})
}

// handleKey mints a single-use browser handoff key with no file attached.
func (s *srv) handleKey(w http.ResponseWriter, r *http.Request) {
	key := newToken()
	s.mu.Lock()
	s.oneTime[key] = time.Now().Add(60 * time.Second)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

func (s *srv) consumeOneTime(k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.oneTime[k]
	if !ok {
		return false
	}
	delete(s.oneTime, k)
	for key, e := range s.oneTime { // opportunistic sweep
		if time.Now().After(e) {
			delete(s.oneTime, key)
		}
	}
	return time.Now().Before(exp)
}

func (s *srv) authorize(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roots[filepath.Clean(dir)] = true
}

func (s *srv) authorized(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for root := range s.roots {
		if withinRoot(root, target) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// subtleCompare is a constant-time string comparison.
func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// ---------------------------------------------------------------- assets

func (s *srv) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+r.URL.Path)), "/")
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}

	data, err := web.FS.ReadFile("assets/" + name)
	gzipped := false
	if err != nil {
		if data, err = web.FS.ReadFile("assets/" + name + ".gz"); err != nil {
			http.NotFound(w, r)
			return
		}
		gzipped = true
	}

	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if gzipped {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *srv) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := web.FS.ReadFile("assets/favicon.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

// ---------------------------------------------------------------- recents

func (s *srv) recentPath() string {
	dir, err := stateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "recent.json")
}

func (s *srv) loadRecent() {
	p := s.recentPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.recent)
}

func (s *srv) addRecent(abs string) {
	s.mu.Lock()
	out := []recentEntry{{Path: abs, Name: filepath.Base(abs), Seen: time.Now()}}
	for _, e := range s.recent {
		if !strings.EqualFold(e.Path, abs) {
			out = append(out, e)
		}
		if len(out) >= 25 {
			break
		}
	}
	s.recent = out
	snapshot := append([]recentEntry(nil), out...)
	s.mu.Unlock()

	if p := s.recentPath(); p != "" {
		if b, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
			_ = os.WriteFile(p, b, 0o600)
		}
	}
}

type listRow struct {
	Name    string
	Meta    string
	URL     string
	Missing bool
}

func (s *srv) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	entries := append([]recentEntry(nil), s.recent...)
	s.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].Seen.After(entries[j].Seen) })

	rows := make([]listRow, 0, len(entries))
	for _, e := range entries {
		_, err := os.Stat(e.Path)
		rows = append(rows, listRow{
			Name:    e.Name,
			Meta:    filepath.Dir(e.Path),
			URL:     (&url.URL{Path: "/view/" + pathToURLPath(e.Path)}).String(),
			Missing: err != nil,
		})
	}

	s.renderList(w, listData{
		Title:    "csv.localhost",
		Subtitle: displayOrigin(s.cfg.Host, s.port),
		Heading:  "Recently opened",
		Empty:    "Nothing yet. Double-click a .csv file in Explorer and it will show up here.",
		Rows:     rows,
	})
}

type listData struct {
	Title    string
	Subtitle string
	Heading  string
	Empty    string
	Parent   string
	Rows     []listRow
	Version  string
}

func (s *srv) renderList(w http.ResponseWriter, d listData) {
	d.Version = Version
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmplList.Execute(w, d); err != nil {
		log.Printf("list template: %v", err)
	}
}

// ---------------------------------------------------------------- viewing

// requestOptions reads the parse overrides carried in the query string.
func requestOptions(r *http.Request) ParseOptions {
	q := r.URL.Query()
	return ParseOptions{
		Delimiter: q.Get("d"),
		Header:    q.Get("header"),
		Encoding:  q.Get("enc"),
	}.normalised()
}

// resolveTarget turns a request into an authorised absolute path.
func (s *srv) resolveTarget(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	target, err := urlPathToPath(raw)
	if err != nil {
		http.Error(w, "bad path: "+err.Error(), http.StatusBadRequest)
		return "", false
	}
	target = filepath.Clean(target)

	// A link followed from a page we already serve implicitly authorises its
	// folder, so browsing between files works; a bare URL typed or probed
	// from elsewhere does not.
	if !s.authorized(target) {
		if ref := r.Header.Get("Referer"); ref != "" && s.refererAuthorized(ref) {
			s.authorize(filepath.Dir(target))
		} else {
			http.Error(w, "that folder has not been opened in this session", http.StatusForbidden)
			return "", false
		}
	}
	return target, true
}

func (s *srv) handleView(w http.ResponseWriter, r *http.Request) {
	target, ok := s.resolveTarget(w, r, strings.TrimPrefix(r.URL.Path, "/view/"))
	if !ok {
		return
	}

	info, err := os.Stat(target)
	if err != nil {
		http.Error(w, "cannot open file: "+err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		s.serveDirectory(w, r, target)
		return
	}
	if !isDelimited(target) {
		// Anything else linked from a folder listing: hand over the bytes.
		http.ServeFile(w, r, target)
		return
	}

	opts := requestOptions(r)
	boot := s.bootPayload(target, info, opts)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data := map[string]any{
		"Title":    filepath.Base(target),
		"FileName": filepath.Base(target),
		"Dir":      filepath.Dir(target),
		"FullPath": target,
		"Boot":     boot,
		"Version":  Version,
	}
	if err := s.tmplPage.Execute(w, data); err != nil {
		log.Printf("page template: %v", err)
	}
}

// bootPayload is everything the viewer needs to draw the first screen: the
// file's identity, the dialect that was detected, the columns, and enough rows
// to fill the window. Sending it with the page avoids a blank flash while a
// separate request goes out.
func (s *srv) bootPayload(target string, info os.FileInfo, opts ParseOptions) map[string]any {
	boot := map[string]any{
		"version":   Version,
		"path":      target,
		"urlPath":   pathToURLPath(target),
		"name":      filepath.Base(target),
		"dir":       filepath.Dir(target),
		"modified":  info.ModTime().Format("2006-01-02 15:04"),
		"size":      info.Size(),
		"sizeText":  humanSize(info.Size()),
		"options":   map[string]string{"delimiter": opts.Delimiter, "header": opts.Header, "encoding": opts.Encoding},
		"encodings": encodingChoices,
		"parentURL": (&url.URL{Path: "/view/" + pathToURLPath(filepath.Dir(target))}).String(),
		"eventsURL": "/events?p=" + url.QueryEscape(pathToURLPath(target)),
	}

	t, err := s.tables.get(target, opts)
	if err != nil {
		boot["error"] = err.Error()
		return boot
	}
	idx := t.view("", -1, "asc")
	boot["delimiter"] = string(t.Delim)
	boot["delimiterName"] = delimiterName(t.Delim)
	boot["encoding"] = t.Encoding
	boot["hasHeader"] = t.HasHeader
	boot["columns"] = t.Columns
	boot["total"] = len(t.Rows)
	boot["truncated"] = t.Truncated
	boot["warnings"] = t.Warnings
	boot["firstPage"] = pageOf(t, idx, 0, 120)
	return boot
}

// handleTable re-sends the boot payload, which is how the viewer picks up a
// file that changed on disk without losing the reader's place.
func (s *srv) handleTable(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("p")
	if raw == "" {
		http.Error(w, "missing p", http.StatusBadRequest)
		return
	}
	target, ok := s.resolveTarget(w, r, raw)
	if !ok {
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.bootPayload(target, info, requestOptions(r)))
}

func (s *srv) refererAuthorized(ref string) bool {
	u, err := url.Parse(ref)
	if err != nil || !strings.HasPrefix(u.Path, "/view/") {
		return false
	}
	p, err := urlPathToPath(strings.TrimPrefix(u.Path, "/view/"))
	if err != nil {
		return false
	}
	return s.authorized(filepath.Clean(p))
}

// serveDirectory lists delimited files and subfolders so a link to a folder
// does something useful instead of erroring.
func (s *srv) serveDirectory(w http.ResponseWriter, r *http.Request, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, "cannot list folder: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var dirs, files []listRow
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		link := (&url.URL{Path: "/view/" + pathToURLPath(full)}).String()
		if e.IsDir() {
			dirs = append(dirs, listRow{Name: e.Name() + "/", Meta: "folder", URL: link})
			continue
		}
		if !isDelimited(e.Name()) {
			continue
		}
		meta := ""
		if info, err := e.Info(); err == nil {
			meta = humanSize(info.Size()) + " · " + info.ModTime().Format("2006-01-02 15:04")
		}
		files = append(files, listRow{Name: e.Name(), Meta: meta, URL: link})
	}

	s.renderList(w, listData{
		Title:    filepath.Base(dir),
		Subtitle: dir,
		Heading:  filepath.Base(dir),
		Empty:    "No CSV files in this folder.",
		Parent:   (&url.URL{Path: "/view/" + pathToURLPath(filepath.Dir(dir))}).String(),
		Rows:     append(dirs, files...),
	})
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ---------------------------------------------------------------- data api

// tableFor resolves and parses the file named by the ?p= parameter.
func (s *srv) tableFor(w http.ResponseWriter, r *http.Request) (*Table, bool) {
	raw := r.URL.Query().Get("p")
	if raw == "" {
		http.Error(w, "missing p", http.StatusBadRequest)
		return nil, false
	}
	target, ok := s.resolveTarget(w, r, raw)
	if !ok {
		return nil, false
	}
	t, err := s.tables.get(target, requestOptions(r))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return nil, false
	}
	return t, true
}

// sortParams reads the ordering and filter from the query string.
func sortParams(r *http.Request) (col int, dir string, q string) {
	col = -1
	if v := r.URL.Query().Get("sort"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			col = n
		}
	}
	dir = r.URL.Query().Get("dir")
	if dir != "desc" {
		dir = "asc"
	}
	return col, dir, r.URL.Query().Get("q")
}

// pageOf slices a window of rows out of an ordering, trimming long cells.
func pageOf(t *Table, idx []int32, offset, limit int) map[string]any {
	if offset < 0 {
		offset = 0
	}
	if offset > len(idx) {
		offset = len(idx)
	}
	end := offset + limit
	if end > len(idx) {
		end = len(idx)
	}
	window := idx[offset:end]

	rows := make([][]string, len(window))
	ids := make([]int32, len(window))
	for i, r := range window {
		ids[i] = r + 1 // 1-based row number as it sits in the file
		rec := t.Rows[r]
		cells := make([]string, len(t.Columns))
		for c := range cells {
			if c < len(rec) {
				cells[c] = clip(rec[c], cellLimit)
			}
		}
		rows[i] = cells
	}
	return map[string]any{
		"offset": offset,
		"total":  len(idx),
		"rows":   rows,
		"ids":    ids,
	}
}

func clip(s string, n int) string {
	if len(s) <= n { // byte length is a cheap upper bound on rune count
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (s *srv) handleRows(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tableFor(w, r)
	if !ok {
		return
	}
	col, dir, q := sortParams(r)
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	if limit > pageLimit {
		limit = pageLimit
	}
	writeJSON(w, http.StatusOK, pageOf(t, t.view(q, col, dir), offset, limit))
}

func (s *srv) handleStats(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tableFor(w, r)
	if !ok {
		return
	}
	col, err := strconv.Atoi(r.URL.Query().Get("col"))
	if err != nil || col < 0 || col >= len(t.Columns) {
		http.Error(w, "bad column", http.StatusBadRequest)
		return
	}
	_, _, q := sortParams(r)
	writeJSON(w, http.StatusOK, t.Stats(col, t.view(q, -1, "asc")))
}

// handleCell returns one cell in full, for the inspector at the foot of the
// page. Only clipped copies travel with a page of rows.
func (s *srv) handleCell(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tableFor(w, r)
	if !ok {
		return
	}
	row, err1 := strconv.Atoi(r.URL.Query().Get("r"))
	col, err2 := strconv.Atoi(r.URL.Query().Get("c"))
	if err1 != nil || err2 != nil {
		http.Error(w, "bad cell", http.StatusBadRequest)
		return
	}
	row-- // ids are 1-based
	if row < 0 || row >= len(t.Rows) || col < 0 || col >= len(t.Columns) {
		http.Error(w, "no such cell", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"value":  t.cell(row, col),
		"column": t.Columns[col].Name,
		"row":    row + 1,
	})
}
