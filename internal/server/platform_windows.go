//go:build windows

package server

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	createNoWindow   = 0x08000000
	detachedProcess  = 0x00000008
	createNewProcGrp = 0x00000200
)

// spawnServer launches this same binary as a detached background server with
// no console window, so a double-clicked file does not flash a black box.
func spawnServer() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow | detachedProcess | createNewProcGrp,
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// openInBrowser hands the URL to whatever the user has set as their default
// browser. rundll32 is used rather than `cmd /c start` because start mangles
// URLs containing & and needs a quoting dance.
func openInBrowser(u string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", u)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd.Start()
}
