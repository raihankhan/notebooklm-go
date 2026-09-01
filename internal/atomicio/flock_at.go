package atomicio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// lockAt is the single path function every Lock helper derives from. The
// blocking flag selects blocking vs non-blocking acquire at the OS level.
//
// Concurrency model:
//
//   - The per-path in-process mutex (pathMu) is taken only across the
//     acquireOSLock call. Multiple shared holders do not block each other
//     once they have acquired the OS-level flock, because the mutex is
//     released before returning to the caller.
//   - Two callers that race on the same path from the same process
//     serialize on pathMu while their fds hit the OS flock. The OS flock
//     arbitrates cross-process contention (the actual safety property).
//   - Cleanup-on-error: if acquireOSLock fails after the per-path mutex
//     was taken, the per-path mutex is released before returning so a
//     later caller does not deadlock on a stuck in-process mutex.
func lockAt(path string, kind LockType, blocking bool) (Release, error) {
	lockPath, err := deriveLockPath(path)
	if err != nil {
		return nil, err
	}

	// Ensure the parent dir exists with 0700 so the lock file is not
	// world-readable.
	dir := filepath.Dir(lockPath)
	if dir != "" && dir != "." {
		if mkErr := MkdirAll(dir, 0o700); mkErr != nil {
			return nil, fmt.Errorf("atomicio: mkdir for lock: %w", mkErr)
		}
	}

	// Create the lock file if it does not already exist. A zero-byte file
	// is fine — flock(2) operates on the inode, not the contents. We use
	// O_CREATE|O_RDWR so two callers racing to create the file see a
	// single inode.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- operator-controlled path.
	if err != nil {
		return nil, fmt.Errorf("atomicio: open lock file: %w", err)
	}

	// Take the per-path in-process mutex across the OS-level acquire so
	// two goroutines in the same process never both believe they hold
	// the same OS-level lock (flock is per-fd on Linux).
	m := lockForPath(lockPath)
	m.Lock()

	if err := acquireOSLock(f, kind, blocking); err != nil {
		m.Unlock()
		_ = f.Close()
		if pathIsLockPath(path) {
			return nil, fmt.Errorf("atomicio: %w (input already had .lock suffix)", err)
		}
		return nil, err
	}

	// Release the per-path mutex immediately — it only protects the
	// acquire window. The OS-level flock handles concurrent shared
	// holders correctly.
	m.Unlock()

	released := false
	release := func() error {
		if released {
			return nil
		}
		released = true
		if err := releaseOSLock(f); err != nil {
			_ = f.Close()
			return fmt.Errorf("atomicio: unlock: %w", err)
		}
		if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			return fmt.Errorf("atomicio: close lock fd: %w", err)
		}
		return nil
	}
	return release, nil
}
