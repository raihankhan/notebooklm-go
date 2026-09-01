package atomicio

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWriteFile_Permissions asserts that a file written via WriteFile lands
// at the requested permission bits on POSIX. Windows does not honor POSIX
// permissions, so the assertion is gated by runtime.GOOS.
func TestWriteFile_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are a no-op on Windows; the chmod is intentionally skipped")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")

	original := []byte(`{"token":"hunter2"}`)
	if err := WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	got := info.Mode().Perm()
	if got != 0o600 {
		t.Fatalf("mode = %o, want 0o600", got)
	}

	// Sanity: bytes round-trip.
	gotBytes, err := os.ReadFile(path) // #nosec G304 -- test-controlled path.
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(gotBytes) != string(original) {
		t.Fatalf("read back = %q, want %q", gotBytes, original)
	}
}

// TestWriteFile_Overwrite verifies a subsequent WriteFile overwrites the
// destination and the new bytes / new mode both take effect.
func TestWriteFile_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(path) // #nosec G304 -- test-controlled path.
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("read = %q, want %q", got, "second")
	}
}

// TestWriteFile_CrashSimulation is the AC2 acceptance test: it injects a
// failure between the temp-write/fsync step and the rename step, then
// asserts the destination file's bytes are exactly the pre-write content.
//
// This proves the rename-atomic contract: a crash in the post-fsync /
// pre-rename window cannot corrupt the destination.
func TestWriteFile_CrashSimulation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storage_state.json")

	original := []byte(`{"cookies":[{"name":"__Secure-1PSID","value":"old"}]}`)
	if err := WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	attempted := []byte(`{"cookies":[{"name":"__Secure-1PSID","value":"new"}]}`)
	abortErr := errors.New("simulated crash mid-write")
	err := WriteFileWithAbort(path, attempted, 0o600, func() error {
		return abortErr
	})
	if err == nil {
		t.Fatalf("WriteFileWithAbort returned nil; expected an error from the abort hook")
	}
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if !strings.Contains(err.Error(), abortErr.Error()) {
		t.Fatalf("err = %v, want it to wrap %q", err, abortErr)
	}

	// Destination must still be the original.
	got, err := os.ReadFile(path) // #nosec G304 -- test-controlled path.
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("destination corrupted: got %q, want %q", got, original)
	}
}

// TestWriteFile_LeavesNoTemp asserts that on a successful WriteFile, the
// only file in the parent directory is the destination itself — no temp
// leftovers.
func TestWriteFile_LeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected 1 entry, got %d: %v", len(entries), names)
	}
	if entries[0].Name() != "config.json" {
		t.Fatalf("unexpected entry: %q", entries[0].Name())
	}
}

// TestWriteFile_Permissions_DefaultZero checks that a zero perm defaults
// to 0o600 — the credential-handling minimum.
func TestWriteFile_Permissions_DefaultZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are a no-op on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")

	if err := WriteFile(path, []byte("x"), 0); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 0o600 (default for zero perm)", got)
	}
}

// TestMkdirAll_Permissions asserts the final directory is at the
// requested perm on POSIX.
func TestMkdirAll_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are a no-op on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "deep", "config")

	if err := MkdirAll(target, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %o, want 0o700", got)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", target)
	}
}

// TestMkdirAll_Idempotent asserts calling MkdirAll on an existing dir is
// a no-op (it must not return an error).
func TestMkdirAll_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("second call (existing dir): %v", err)
	}
}

// TestWriteFileWithBak_OverwritesAndPreserves proves the AC: a write via
// WriteFileWithBak first snapshots the existing destination to .bak, then
// performs the atomic write. The .bak contains the previous contents.
func TestWriteFileWithBak_OverwritesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	bak := path + ".bak"

	original := []byte("first")
	if err := WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	updated := []byte("second")
	if err := WriteFileWithBak(path, updated, 0o600); err != nil {
		t.Fatalf("WriteFileWithBak: %v", err)
	}

	got, err := os.ReadFile(path) // #nosec G304 -- test-controlled path.
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("dest = %q, want %q", got, "second")
	}

	bakBytes, err := os.ReadFile(bak) // #nosec G304 -- test-controlled path.
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if string(bakBytes) != "first" {
		t.Fatalf(".bak = %q, want %q", bakBytes, "first")
	}
}

// TestWriteFileWithBak_Rollback proves that a failed WriteFile inside
// WriteFileWithBak restores the previous contents from .bak.
//
// We simulate the failure with an abort hook injected between the
// temp-write/fsync step and the rename step. WriteFileWithBakAndAbort
// rolls back from .bak; the destination ends up at its original content.
func TestWriteFileWithBak_Rollback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	bak := path + ".bak"

	original := []byte("first")
	if err := WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	abortErr := errors.New("simulated crash mid-write")
	err := WriteFileWithBakAndAbort(path, []byte("second"), 0o600, func() error {
		return abortErr
	})
	if err == nil {
		t.Fatalf("WriteFileWithBakAndAbort returned nil; expected an error from the abort hook")
	}

	// Destination should be back to the original (rolled back from .bak).
	got, err := os.ReadFile(path) // #nosec G304 -- test-controlled path.
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "first" {
		t.Fatalf("dest after rollback = %q, want %q (rollback failed)", got, "first")
	}

	// The .bak should still hold the original.
	bakBytes, err := os.ReadFile(bak) // #nosec G304 -- test-controlled path.
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if string(bakBytes) != "first" {
		t.Fatalf(".bak after rollback = %q, want %q", bakBytes, "first")
	}
}

// TestSanitizeModeForDisplay ensures the display helper formats POSIX
// permission bits consistently (always a leading zero + 3 octal digits).
func TestSanitizeModeForDisplay(t *testing.T) {
	cases := []struct {
		mode os.FileMode
		want string
	}{
		{0o600, "0600"},
		{0o700, "0700"},
		{0o644, "0644"},
		{0o755, "0755"},
	}
	for _, c := range cases {
		got := SanitizeModeForDisplay(c.mode)
		if got != c.want {
			t.Fatalf("SanitizeModeForDisplay(%o) = %q, want %q", c.mode, got, c.want)
		}
	}
}

// TestFileExists covers the trivial file-existence helper.
func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.json")
	exists, err := FileExists(missing)
	if err != nil {
		t.Fatalf("FileExists(missing): %v", err)
	}
	if exists {
		t.Fatalf("FileExists(missing) = true, want false")
	}

	present := filepath.Join(dir, "yes.json")
	if err := WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	exists, err = FileExists(present)
	if err != nil {
		t.Fatalf("FileExists(present): %v", err)
	}
	if !exists {
		t.Fatalf("FileExists(present) = false, want true")
	}

	// Directory should not count as a regular file.
	exists, err = FileExists(dir)
	if err != nil {
		t.Fatalf("FileExists(dir): %v", err)
	}
	if exists {
		t.Fatalf("FileExists(dir) = true, want false (dir is not a regular file)")
	}
}

// TestMkdirAll_Nested asserts the recursive create case.
func TestMkdirAll_Nested(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are a no-op on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c", "d")

	if err := MkdirAll(target, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, p := range []string{target, filepath.Join(dir, "a"), filepath.Join(dir, "a", "b")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", p)
		}
	}
}

// TestMkdirAll_Empty covers the error path.
func TestMkdirAll_Empty(t *testing.T) {
	if err := MkdirAll("", 0o700); err == nil {
		t.Fatalf("MkdirAll(\"\") returned nil error")
	}
}

// TestWriteFile_EmptyPath covers the error path.
func TestWriteFile_EmptyPath(t *testing.T) {
	if err := WriteFile("", []byte("x"), 0o600); err == nil {
		t.Fatalf("WriteFile(\"\") returned nil error")
	}
	if err := WriteFileWithBak("", []byte("x"), 0o600); err == nil {
		t.Fatalf("WriteFileWithBak(\"\") returned nil error")
	}
}

// TestFileExists_Missing covers the not-exist path.
func TestFileExists_Missing(t *testing.T) {
	exists, err := FileExists("/this/path/does/not/exist/at/all.json")
	if err != nil {
		t.Fatalf("FileExists on missing path: %v", err)
	}
	if exists {
		t.Fatalf("FileExists returned true on missing path")
	}
}

// TestReadFile covers the read wrapper.
func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	want := []byte(`{"k":"v"}`)
	if err := WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("ReadFile = %q, want %q", got, want)
	}

	// Missing path -> error.
	if _, err := ReadFile(filepath.Join(dir, "nope.json")); err == nil {
		t.Fatalf("ReadFile on missing path returned nil error")
	}
}

// TestErrLockedError_Error covers the typed error string formatting.
func TestErrLockedError_Error(t *testing.T) {
	e := &ErrLockedError{Path: "/tmp/foo.lock"}
	msg := e.Error()
	if !strings.Contains(msg, "/tmp/foo.lock") {
		t.Fatalf("Error() = %q, want it to mention the path", msg)
	}
	if !strings.Contains(msg, "locked") {
		t.Fatalf("Error() = %q, want it to contain 'locked'", msg)
	}
}

// TestPathIsLockPath exercises the small helper.
func TestPathIsLockPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/tmp/foo.lock", true},
		{"/tmp/foo.json.lock", true},
		{"/tmp/foo.json", false},
		{"./data.json", false},
	}
	for _, c := range cases {
		if got := pathIsLockPath(c.in); got != c.want {
			t.Fatalf("pathIsLockPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestMkdirAll_DefaultZero asserts the zero-perm default for MkdirAll.
func TestMkdirAll_DefaultZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are a no-op on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "x")
	if err := MkdirAll(target, 0); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %o, want 0o700 (default for zero perm)", got)
	}
}

// TestMkdirAll_AlreadyExistsPerm asserts MkdirAll re-asserts perm on
// an existing directory on POSIX (so a previous call with a different
// mode is normalized).
func TestMkdirAll_AlreadyExistsPerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are a no-op on Windows")
	}
	dir := t.TempDir()
	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("second: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode after re-assert = %o, want 0o755", got)
	}
}
