//go:build windows

package server

import (
	"os"
	"syscall"
)

const (
	attachParentProcess = ^uintptr(0) // (DWORD)-1
	stdInputHandle      = ^uintptr(9) // (DWORD)-10
	stdOutputHandle     = ^uintptr(10)
	stdErrorHandle      = ^uintptr(11)
	invalidHandle       = ^uintptr(0)
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")

// AttachParentConsole reconnects standard output to the console that launched
// this process. The binary is linked with -H=windowsgui so that a
// double-clicked .csv file does not flash a console window; that also detaches
// stdout, so command-line use needs the console handed back explicitly.
//
// It is a no-op when there is no parent console, which is exactly the
// double-click case.
func AttachParentConsole() {
	attach := kernel32.NewProc("AttachConsole")
	if ret, _, _ := attach.Call(attachParentProcess); ret == 0 {
		return
	}
	// Adopt the console only for the streams the caller did not redirect:
	// `csvlocalhost status > out.txt` already has a perfectly good stdout, and
	// pointing it at CONOUT$ would send the output to the screen and leave the
	// file empty.
	if !stdHandleValid(stdOutputHandle) {
		if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stdout = out
		}
	}
	if !stdHandleValid(stdErrorHandle) {
		if errOut, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stderr = errOut
		}
	}
	if !stdHandleValid(stdInputHandle) {
		if in, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
			os.Stdin = in
		}
	}
}

// stdHandleValid reports whether the process already has a usable standard
// handle, which is how a redirected pipe or file is told apart from the
// nothing-at-all a GUI-subsystem process is given.
func stdHandleValid(which uintptr) bool {
	get := kernel32.NewProc("GetStdHandle")
	h, _, _ := get.Call(which)
	return h != 0 && h != invalidHandle
}
