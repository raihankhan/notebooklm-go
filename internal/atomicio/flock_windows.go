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
// acquireOSLock and releaseOSLock return ErrLockedError on Windows so
// any accidental runtime use surfaces as the typed Try*-shape error
// rather than a panic. The tests skip on Windows (see skipOnWindows
// in flock_test.go), so the Windows build is exercised at compile
// time only.
func acquireOSLock(f *os.File, kind LockType, blocking bool) error {
	_ = f
	_ = kind
	_ = blocking
	return lockedError("(windows stub: flock not implemented in this build)")
}

func releaseOSLock(f *os.File) error {
	_ = f
	return lockedError("(windows stub: flock not implemented in this build)")
}
