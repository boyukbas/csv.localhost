package server

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

const maxLogBytes = 512 << 10

// setupLogging mirrors server logs into a file. A detached background server
// has no console, so without this its diagnostics would vanish.
func setupLogging() func() {
	log.SetFlags(log.LstdFlags)

	dir, err := stateDir()
	if err != nil {
		return func() {}
	}
	path := filepath.Join(dir, "server.log")

	// Keep one previous run's log, and never let it grow without bound.
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		_ = os.Rename(path, path+".old")
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return func() {}
	}

	if isDetached() {
		log.SetOutput(f)
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	return func() { _ = f.Close() }
}

// isDetached reports whether stderr is unusable, which is the case for the
// background server spawned by `csvlocalhost open`.
func isDetached() bool {
	if os.Stderr == nil {
		return true
	}
	_, err := os.Stderr.Stat()
	return err != nil
}
