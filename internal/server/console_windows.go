//go:build windows

package server

import (
	"os"
	"syscall"
)

const attachParentProcess = ^uintptr(0) // (DWORD)-1

// AttachParentConsole reconnects standard output to the console that launched
// this process. The binary is linked with -H=windowsgui so that a
// double-clicked .csv file does not flash a console window; that also detaches
// stdout, so command-line use needs the console handed back explicitly.
//
// It is a no-op when there is no parent console, which is exactly the
// double-click case.
func AttachParentConsole() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	attach := kernel32.NewProc("AttachConsole")
	if ret, _, _ := attach.Call(attachParentProcess); ret == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
	}
	if errOut, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stderr = errOut
	}
	if in, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = in
	}
}
