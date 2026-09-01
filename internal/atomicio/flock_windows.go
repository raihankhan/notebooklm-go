//go:build windows

package atomicio

import "os"

// On Windows, flock(2) does not exist. Cross-process file locking on
// Windows uses LockFileEx (overlapped I/O) and the semantics differ
// subtly (no read/write dichotomy, mandatory vs advisory locking).
// This file is a build-tag-only stub: production builds of
// notebooklm-go on Windows require the LockFileEx implementation
// (tracked separately; the macOS/Linux POSIX path is the only one
// exercised by CI today).
//
// acquireOSLock and releaseOSLock panic on Windows so a misuse in
// development fails loud rather than silently degrading to "no lock".
// The ticket body gates Windows-only code behind //go:build windows
// and skip-gates it in non-Windows CI; this file is the Windows half.
func acquireOSLock(f *os.File, kind LockType, blocking bool) error {
	_ = f
	_ = kind
	_ = blocking
	panic("atomicio: flock is not implemented on Windows in this build")
}

func releaseOSLock(f *os.File) error {
	_ = f
	panic("atomicio: flock is not implemented on Windows in this build")
}
