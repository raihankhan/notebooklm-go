// Package atomicio provides atomic, durable filesystem primitives used by every
// later credential, config, and profile write in this module. It exists so the
// rest of the codebase never has to repeat the temp+chmod+fsync+rename dance
// by hand.
//
// Two invariants matter:
//
//  1. Rename-atomicity. Every write goes to a temp file in the same directory
//     as the destination, fsyncs, and is then renamed over the destination.
//     A reader sees either the old contents or the new contents — never a
//     partial file.
//  2. Permission discipline. On POSIX, files written here land at the
//     caller-requested mode (default 0o600, the credential minimum) and
//     directories at 0o700. On Windows, POSIX permissions are a no-op and
//     can confuse ACLs, so the chmod/fchmod call is skipped.
//
// Ported from notebooklm-py/src/notebooklm/_atomic_io.py::atomic_write_json.
// The Go port keeps the public surface narrower (no JSON-specific layer; that
// lives in internal/web/wire).
package atomicio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// ErrAborted is returned by WriteFile when an optional Abort hook (passed via
// WithAbort) signals that the write should be canceled between temp-write
// and rename. The destination file is left untouched.
var ErrAborted = errors.New("atomicio: write aborted by caller")

// AbortFunc is invoked by WriteFile between the temp-file write/fsync step and
// the rename step. If it returns a non-nil error, WriteFile unlinks the temp
// file and returns that error wrapped in ErrAborted. It is used by tests to
// simulate a crash window; production code does not need it.
type AbortFunc func() error

// WriteFile writes data to path atomically: it stages the bytes in a sibling
// temp file, fsyncs them, then renames the temp file over the destination.
// On POSIX the final file's permission bits are forced to perm; on Windows
// perm is accepted for source-compatibility but the chmod is skipped.
//
// A crash, signal, or process exit between temp-write and rename leaves the
// destination file untouched (the temp file may remain in the parent
// directory and is cleaned up on the next WriteFile call to the same path).
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm, nil)
}

// WriteFileWithAbort is WriteFile with a caller-supplied abort hook invoked
// between the temp-write/fsync step and the rename step. It is exposed for
// tests that need to simulate the post-fsync / pre-rename crash window; the
// zero-value AbortFunc (nil) makes it equivalent to WriteFile.
func WriteFileWithAbort(path string, data []byte, perm os.FileMode, abort AbortFunc) error {
	return writeFile(path, data, perm, abort)
}

func writeFile(path string, data []byte, perm os.FileMode, abort AbortFunc) error {
	if path == "" {
		return errors.New("atomicio: empty path")
	}
	if perm == 0 {
		perm = 0o600
	}

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}

	// Sibling temp file in the same directory so os.Rename is atomic across
	// filesystems. The dot prefix matches Python's NamedTemporaryFile default
	// and keeps the temp hidden in normal `ls` listings.
	base := filepath.Base(path)
	f, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return fmt.Errorf("atomicio: create temp: %w", err)
	}
	tempPath := f.Name()

	cleanup := func() {
		// Best-effort cleanup; the temp file may already be gone after a
		// successful rename, which is fine.
		_ = os.Remove(tempPath)
	}

	success := false
	defer func() {
		if !success {
			cleanup()
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomicio: write temp: %w", err)
	}

	// Force the bytes (and the permission-bit change) to stable storage
	// before the rename. Without this, a crash in the post-replace window
	// can leave the destination pointing at a truncated file.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomicio: fsync temp: %w", err)
	}

	// POSIX-only: enforce perm before close so the chmod metadata is itself
	// flushed along with the data (via the Sync above).
	if runtime.GOOS != "windows" {
		if err := f.Chmod(perm); err != nil {
			_ = f.Close()
			return fmt.Errorf("atomicio: chmod temp: %w", err)
		}
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("atomicio: close temp: %w", err)
	}

	// Optional pre-rename abort hook. Tests inject failures here to prove
	// that the destination file is preserved across a simulated crash.
	if abort != nil {
		if err := abort(); err != nil {
			return fmt.Errorf("%w: %w", ErrAborted, err)
		}
	}

	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("atomicio: rename: %w", err)
	}

	success = true

	// Best-effort chmod after rename: in case the underlying filesystem
	// or umask altered the mode, re-assert it on POSIX.
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, perm)
	}

	return nil
}

// WriteFileWithBak writes data to path atomically with a `.bak` rollback
// safety net: it first copies the existing destination (if any) to
// `path + ".bak"`, then performs the atomic write. If the atomic write
// fails, the previous contents are restored from the `.bak` before the
// error is returned.
//
// The `.bak` file is overwritten on every call (so a crashed-but-recovered
// write does not leave an even-older `.bak` lying around) and is left on
// disk after a successful write — operators can use it to manually roll
// back if a write turns out to be semantically wrong even though it was
// syntactically atomic.
//
// On POSIX the final file's permission bits are forced to perm (same as
// WriteFile).
func WriteFileWithBak(path string, data []byte, perm os.FileMode) error {
	return writeFileWithBak(path, data, perm, nil)
}

// WriteFileWithBakAndAbort is WriteFileWithBak with a caller-supplied
// abort hook invoked between the temp-write/fsync step and the rename
// step. It is exposed for tests that need to simulate the post-fsync /
// pre-rename crash window; nil makes it equivalent to WriteFileWithBak.
func WriteFileWithBakAndAbort(path string, data []byte, perm os.FileMode, abort AbortFunc) error {
	return writeFileWithBak(path, data, perm, abort)
}

func writeFileWithBak(path string, data []byte, perm os.FileMode, abort AbortFunc) error {
	if path == "" {
		return errors.New("atomicio: empty path")
	}
	if perm == 0 {
		perm = 0o600
	}

	bakPath := path + ".bak"

	// Snapshot the previous contents, if any. Best-effort: a pre-existing
	// `.bak` is overwritten (the most-recent-good version wins).
	existing, readErr := os.ReadFile(path) // #nosec G304 -- path is operator-provided.
	switch {
	case readErr == nil:
		if writeErr := os.WriteFile(bakPath, existing, perm); writeErr != nil {
			return fmt.Errorf("atomicio: write .bak: %w", writeErr)
		}
	case errors.Is(readErr, os.ErrNotExist):
		// no previous contents — first write
	default:
		return fmt.Errorf("atomicio: read existing: %w", readErr)
	}

	if err := writeFile(path, data, perm, abort); err != nil {
		// Rollback: restore from .bak if it exists.
		bak, bakErr := os.ReadFile(bakPath) // #nosec G304 -- bakPath derived from operator path.
		if bakErr == nil {
			if restoreErr := os.WriteFile(path, bak, perm); restoreErr != nil {
				// Both the write and the rollback failed. Surface the
				// original write error but mention the rollback failure.
				return fmt.Errorf("atomicio: write failed (%w) and rollback failed (%w)", err, restoreErr)
			}
		}
		return err
	}
	return nil
}

// MkdirAll creates dir and any necessary parents at the requested permission
// bits. On POSIX the final directory's mode is forced to perm; on Windows
// perm is accepted for source-compatibility but the chmod is skipped (Windows
// ACLs are inherited from the parent and a posix-style chmod can break them).
//
// If the directory already exists, its mode is re-asserted to perm on POSIX
// (so a previous call with a different mode is normalized). The function
// never raises on an existing directory.
func MkdirAll(path string, perm os.FileMode) error {
	if path == "" {
		return errors.New("atomicio: empty path")
	}
	if perm == 0 {
		perm = 0o700
	}

	if err := os.MkdirAll(path, perm); err != nil {
		return fmt.Errorf("atomicio: mkdir %s: %w", path, err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, perm); err != nil {
			// chmod can fail if a concurrent process tightened the mode,
			// but the directory exists, so we do not fail the caller.
			if !errors.Is(err, os.ErrPermission) {
				return fmt.Errorf("atomicio: chmod %s: %w", path, err)
			}
		}
	}

	return nil
}

// FileExists reports whether path exists and is a regular file (or symlink
// to a regular file). It returns false for missing paths, directories, and
// other non-regular files. The error is reserved for unexpected I/O failures.
func FileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// SanitizeModeForDisplay returns the POSIX permission bits of a file mode
// as an octal string (e.g. "0600"). It is used by `status --paths` to render
// file permissions without exposing the higher bits of os.FileMode. Windows
// callers can still use it; the result reflects whatever the underlying
// filesystem reports.
func SanitizeModeForDisplay(m os.FileMode) string {
	return "0" + strconv.FormatUint(uint64(m.Perm()), 8)
}

// ReadFile is a tiny wrapper that adds io.ReadAll-style bounded reads. It is
// kept here so callers that already import atomicio for writes do not need a
// second package for the read path.
func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- path is operator-provided.
	if err != nil {
		return nil, fmt.Errorf("atomicio: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
