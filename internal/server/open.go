package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Open makes sure a server is running, registers the file with it and hands
// the resulting URL to the default browser.
func Open(abs string, hostOverride string, opts ParseOptions) error {
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("cannot open %s: %w", abs, err)
	}
	abs = filepath.Clean(abs)

	st, err := ensureServer()
	if err != nil {
		return err
	}

	u, err := requestOpen(st, abs, opts)
	if err != nil {
		// A server that was alive a moment ago may have just gone idle.
		clearState()
		if st, err = ensureServer(); err != nil {
			return err
		}
		if u, err = requestOpen(st, abs, opts); err != nil {
			return err
		}
	}

	if hostOverride != "" {
		u = replaceHost(u, hostOverride, st.Port)
	}
	return openInBrowser(u)
}

// ensureServer returns a live server record, starting a detached one if needed.
func ensureServer() (*state, error) {
	if st, err := readState(); err == nil && st.alive() {
		return st, nil
	}

	locked, unlock := acquireSpawnLock()
	defer unlock()
	if !locked {
		// Another launcher is already starting the server; wait for it.
		if st, err := waitForServer(15 * time.Second); err == nil {
			return st, nil
		}
	} else if err := spawnServer(); err != nil {
		return nil, fmt.Errorf("could not start the viewer service: %w", err)
	}
	return waitForServer(10 * time.Second)
}

func waitForServer(limit time.Duration) (*state, error) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		time.Sleep(120 * time.Millisecond)
		if st, err := readState(); err == nil && st.alive() {
			return st, nil
		}
	}
	return nil, fmt.Errorf("the viewer service did not start within %s", limit)
}

// acquireSpawnLock returns whether this process won the right to start the
// server. A lock left behind by a crash goes stale after 20 seconds.
func acquireSpawnLock() (bool, func()) {
	dir, err := stateDir()
	if err != nil {
		return true, func() {}
	}
	lock := filepath.Join(dir, "spawn.lock")

	if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) > 20*time.Second {
		_ = os.Remove(lock)
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, func() {}
	}
	_ = f.Close()
	return true, func() { _ = os.Remove(lock) }
}

func requestOpen(st *state, abs string, opts ParseOptions) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"path":      abs,
		"delimiter": opts.Delimiter,
		"header":    opts.Header,
		"encoding":  opts.Encoding,
	})
	req, err := http.NewRequest(http.MethodPost, st.controlURL("/open"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set(tokenHeader, st.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK || out.URL == "" {
		if out.Error != "" {
			return "", fmt.Errorf("%s", out.Error)
		}
		return "", fmt.Errorf("server returned status %d", resp.StatusCode)
	}
	return out.URL, nil
}

func replaceHost(u, host string, port int) string {
	parsed, err := parseLoose(u)
	if err != nil {
		return u
	}
	parsed.Host = hostPort(host, port)
	return parsed.String()
}

// OpenHome launches the browser on the index of recently viewed files.
func OpenHome() error {
	st, err := ensureServer()
	if err != nil {
		return err
	}
	key, err := requestKey(st)
	if err != nil {
		return err
	}
	return openInBrowser(displayOrigin(st.Host, st.Port) + "/?k=" + key)
}

// requestKey mints a single-use browser handoff key without registering a file.
func requestKey(st *state) (string, error) {
	req, err := http.NewRequest(http.MethodPost, st.controlURL("/key"), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(tokenHeader, st.Token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Key == "" {
		return "", fmt.Errorf("server did not return a session key")
	}
	return out.Key, nil
}
