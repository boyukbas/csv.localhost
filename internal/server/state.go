package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Version is the release identifier reported by `csvlocalhost version`.
const Version = "1.0.0"

const (
	// DefaultPort is tried first so that plain http://csv.localhost/ works.
	DefaultPort = 80
	// DefaultHost is the hostname used in generated URLs. Browsers resolve
	// any *.localhost name to the loopback interface without a hosts entry.
	DefaultHost = "csv.localhost"
	// DefaultIdleMinutes is how long the server lingers with no open viewers.
	DefaultIdleMinutes = 30
)

// state is the on-disk handshake between `open` and a running `serve`.
type state struct {
	Port    int    `json:"port"`
	Host    string `json:"host"`
	PID     int    `json:"pid"`
	Token   string `json:"token"`
	Version string `json:"version"`
	Started string `json:"started"`
}

// stateDir returns the per-user directory holding the state file.
func stateDir() (string, error) {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.Getenv("APPDATA")
		}
	default:
		base = os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "state")
		}
	}
	if base == "" {
		return "", fmt.Errorf("cannot determine a user state directory")
	}
	dir := filepath.Join(base, "csv.localhost")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func statePath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "server.json"), nil
}

func readState() (*state, error) {
	p, err := statePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Port == 0 {
		return nil, fmt.Errorf("state file has no port")
	}
	return &s, nil
}

func writeState(s *state) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func clearState() {
	if p, err := statePath(); err == nil {
		_ = os.Remove(p)
	}
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is fatal for our security model.
		panic("csv.localhost: no source of randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// controlURL builds the loopback origin used for control requests. Control
// traffic always goes to 127.0.0.1 so it never depends on name resolution.
func (s *state) controlURL(path string) string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port)) + path
}

// alive reports whether the recorded server is answering health checks and is
// the same build as this binary.
func (s *state) alive() bool {
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get(s.controlURL("/healthz"))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var h struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return false
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return false
	}
	return h.OK && h.Version == Version
}

// Status prints where the server is, if anywhere.
func Status(w io.Writer) error {
	s, err := readState()
	if err != nil {
		fmt.Fprintln(w, "server: not running")
		return nil
	}
	if !s.alive() {
		fmt.Fprintf(w, "server: not running (stale record for pid %d on port %d)\n", s.PID, s.Port)
		return nil
	}
	fmt.Fprintf(w, "server:  running (pid %d)\n", s.PID)
	fmt.Fprintf(w, "port:    %d\n", s.Port)
	fmt.Fprintf(w, "url:     %s\n", displayOrigin(s.Host, s.Port))
	fmt.Fprintf(w, "started: %s\n", s.Started)
	fmt.Fprintf(w, "version: %s\n", s.Version)
	return nil
}

// Stop asks a running server to exit.
func Stop(w io.Writer) error {
	s, err := readState()
	if err != nil || !s.alive() {
		fmt.Fprintln(w, "server: not running")
		clearState()
		return nil
	}
	req, err := http.NewRequest(http.MethodPost, s.controlURL("/quit"), nil)
	if err != nil {
		return err
	}
	req.Header.Set(tokenHeader, s.Token)
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server refused to stop (status %d)", resp.StatusCode)
	}
	fmt.Fprintln(w, "server: stopping")
	return nil
}

// displayOrigin is the pretty origin shown to humans and used in browser URLs.
func displayOrigin(host string, port int) string {
	if host == "" {
		host = DefaultHost
	}
	if port == 80 {
		return "http://" + host
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}
