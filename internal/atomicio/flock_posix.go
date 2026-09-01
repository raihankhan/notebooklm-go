//go:build !windows

package atomicio

import (
	"errors"
	"os"
	"syscall"
)

// flock(2) operation bits. These are the Linux/macOS values; on BSDs they
// match because flock(2) is the historical BSD API and Linux adopted it
// verbatim. Kept as untyped constants because syscall package versions
// across platforms disagree on whether LOCK_SH/LOCK_EX are exported.
const (
	flockSH = 1 // LOCK_SH — shared lock
	flockEX = 2 // LOCK_EX — exclusive lock
	flockNB = 4 // LOCK_NB — non-blocking
)

// acquireOSLock performs a flock(2) syscall on f. On non-blocking, an
// EAGAIN / EWOULDBLOCK from flock is mapped to ErrLockedError so the
// Try* helpers can detect it without depending on syscall.EAGAIN
// (which the syscall package does not export on every target).
func acquireOSLock(f *os.File, kind LockType, blocking bool) error {
	op := flockSH
	if kind == LockExclusive {
		op = flockEX
	}
	if !blocking {
		op |= flockNB
	}
	if err := syscall.Flock(int(f.Fd()), op); err != nil {
		if !blocking && isLockBusy(err) {
			return lockedError(f.Name())
		}
		return err
	}
	return nil
}

// releaseOSLock drops the flock(2) lock. It always uses LOCK_UN with
// LOCK_NB — the man page notes that some kernels require LOCK_NB when
// releasing through a different fd than the one that acquired, but in
// practice for our usage (same fd, same goroutine) LOCK_UN alone is
// sufficient. We use 8 (LOCK_UN) here.
func releaseOSLock(f *os.File) error {
	const lockUN = 8 // LOCK_UN
	return syscall.Flock(int(f.Fd()), lockUN)
}

// isLockBusy reports whether err from a non-blocking flock is the "lock
// held elsewhere" errno. We accept both EAGAIN and EWOULDBLOCK (same
// numeric value on every POSIX target we ship to) and the wrapped
// equivalents the syscall package returns through os.File.SyscallConn.
func isLockBusy(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK
	}
	return false
}
