//go:build !windows

package server

// AttachParentConsole is a no-op outside Windows, where stdout is never
// detached in the first place.
func AttachParentConsole() {}
