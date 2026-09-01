package atomicio

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

// LockType names the two lock modes POSIX flock(2) supports. They are mapped
// 1-to-1 onto LOCK_SH / LOCK_EX.
type LockType int

const (
	// LockShared is a read lock: multiple holders may share it, but it
	// conflicts with an exclusive holder.
	LockShared LockType = iota
	// LockExclusive is a write lock: only one holder may hold it, and it
	// conflicts with every shared holder.
	LockExclusive
)

// Release is the handle returned by every Lock helper. Calling it releases
// the underlying lock and is safe to call exactly once; calling it twice is
// a no-op the second time. The returned error is the unlock error (always
// nil in the current implementation; reserved for future portability).
type Release func() error

// pathMuMu guards pathMu (the per-path in-process mutex registry).
var pathMuMu sync.Mutex

// pathMu is the per-path in-process mutex. Two goroutines that lock the
// same path from the same process serialize on this; cross-process locking
// is handled by the OS-level flock(2) below.
var pathMu = make(map[string]*sync.Mutex)

// lockForPath returns the per-path mutex, creating it lazily. The map
// registration is itself guarded by pathMuMu so concurrent first-touches
// do not race.
func lockForPath(p string) *sync.Mutex {
	pathMuMu.Lock()
	defer pathMuMu.Unlock()
	if m, ok := pathMu[p]; ok {
		return m
	}
	m := &sync.Mutex{}
	pathMu[p] = m
	return m
}

// deriveLockPath turns a target file path into its companion lock file
// path. The convention is "<name>.lock" (non-dotted), matching the Python
// atomic_update_json helper from _atomic_io.py. Using a non-dotted suffix
// keeps the canonical dotted ".storage_state.json.lock" sentinel that
// storage_state mutators serialize on clearly distinct — see the ticket
// body and the Python source for the lost-update-race rationale.
//
// The derivation is centralized here so all four lock helpers stay
// consistent.
func deriveLockPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("atomicio: empty path")
	}
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	base := filepath.Base(path)
	return filepath.Join(dir, base+".lock"), nil
}

// Shared acquires a shared (read) lock on the lock file derived from path.
// Multiple goroutines and processes may hold the shared lock concurrently;
// an exclusive holder blocks until release.
//
// The returned Release unlocks both the OS-level flock and the per-path
// in-process mutex. Call it exactly once.
func Shared(path string) (Release, error) {
	return lockAt(path, LockShared, true)
}

// Exclusive acquires an exclusive (write) lock on the lock file derived
// from path. It blocks until both any other exclusive holder and every
// shared holder release.
func Exclusive(path string) (Release, error) {
	return lockAt(path, LockExclusive, true)
}

// TryShared attempts to acquire a shared lock without blocking. The boolean
// return is false if another holder currently owns an exclusive lock (or
// if the OS-level acquire failed for a non-fatal reason). On success the
// returned Release unlocks both the OS-level flock and the per-path
// in-process mutex.
func TryShared(path string) (Release, bool, error) {
	r, err := lockAt(path, LockShared, false)
	if err != nil {
		// lockAt for Try* never returns ErrLocked — distinguish between
		// "could not lock" and a real I/O failure.
		var locked *ErrLockedError
		if errors.As(err, &locked) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return r, true, nil
}

// TryExclusive attempts to acquire an exclusive lock without blocking.
// See TryShared for the boolean / error contract.
func TryExclusive(path string) (Release, bool, error) {
	r, err := lockAt(path, LockExclusive, false)
	if err != nil {
		var locked *ErrLockedError
		if errors.As(err, &locked) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return r, true, nil
}

// ErrLockedError is the typed error returned by lockAt when blocking=false
// and the OS-level acquire could not complete because the lock is held
// elsewhere. The Try* helpers unwrap it to produce the (release, false,
// nil) return shape.
type ErrLockedError struct{ Path string }

func (e *ErrLockedError) Error() string {
	return fmt.Sprintf("atomicio: %s is locked by another holder", e.Path)
}

// pathIsLockPath returns true if path already ends in ".lock" (i.e. is
// itself a lock file passed in by mistake). Used to detect a caller bug
// without preventing the call — we simply warn via the returned path.
func pathIsLockPath(path string) bool {
	return filepath.Ext(path) == ".lock"
}
