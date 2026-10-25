//go:build !windows

package server

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnServer launches a detached background server. This non-Windows build
// exists so the project can be developed and tested outside Windows.
func spawnServer() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func openInBrowser(u string) error {
	for _, opener := range []string{"xdg-open", "open"} {
		if path, err := exec.LookPath(opener); err == nil {
			return exec.Command(path, u).Start()
		}
	}
	// No desktop opener available (headless container): print it instead.
	os.Stdout.WriteString(u + "\n")
	return nil
}
