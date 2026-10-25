package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// handleEvents streams a server-sent event whenever the watched file changes,
// which is what gives the viewer live reload while the file is being written.
func (s *srv) handleEvents(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("p")
	if raw == "" {
		http.Error(w, "missing p", http.StatusBadRequest)
		return
	}
	target, err := urlPathToPath(raw)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	target = filepath.Clean(target)
	if !s.authorized(target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		http.Error(w, "cannot watch file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer watcher.Close()

	// Watch the containing folder: exporters routinely save by writing a
	// temporary file and renaming it over the original, which destroys a
	// watch registered on the file itself.
	dir := filepath.Dir(target)
	if err := watcher.Add(dir); err != nil {
		http.Error(w, "cannot watch folder: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.sseCount.Add(1)
	defer s.sseCount.Add(-1)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	base := strings.ToLower(filepath.Base(target))
	lastSent := time.Now()
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()

	send := func(event string) {
		fmt.Fprintf(w, "event: %s\ndata: %d\n\n", event, time.Now().UnixMilli())
		flusher.Flush()
		lastSent = time.Now()
	}

	for {
		select {
		case <-r.Context().Done():
			return

		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !strings.EqualFold(filepath.Base(ev.Name), base) {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			// Coalesce the burst of events a single save produces. Large
			// exports are written in chunks, so wait a little longer than a
			// text editor would need.
			debounce.Reset(400 * time.Millisecond)

		case <-debounce.C:
			if _, err := os.Stat(target); err != nil {
				send("gone")
				continue
			}
			if time.Since(lastSent) < 200*time.Millisecond {
				continue
			}
			send("reload")

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watch error: %v", err)

		case <-keepAlive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
