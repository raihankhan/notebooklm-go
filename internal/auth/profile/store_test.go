// Package profile: tests for DiskStore, FakeStore, and the path
// resolver.
//
// AC4: Reader + DiskStore read the committed fixture and
// surface the parsed values. AC6: read-only on disk — these tests
// open the file read-only and never call any write API on the
// store (the package has no write API at all in Sprint 2; see the
// package docstring).
//
// Boundary: per docs/AGENTS.md rule 5 this test file is part of the
// mode=internal package; it imports stdlib only.
package profile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestFakeStoreFound: the in-memory FakeStore returns the typed
// Profile for a present name.
func TestFakeStoreFound(t *testing.T) {
	store := &FakeStore{
		Profiles: map[Name]Profile{
			Name("work"): {
				Backend: BackendStorageFile,
				Account: Account{AuthUser: 2, Email: "alice@example.invalid"},
				Cookies: []Cookie{{Name: "SID", Value: "v"}},
			},
		},
	}
	p, err := store.Read(context.Background(), Name("work"))
	if err != nil {
		t.Fatalf("FakeStore.Read(present) error = %v", err)
	}
	if p.Name != Name("work") {
		t.Fatalf("Read name = %q, want %q", p.Name, "work")
	}
	if p.Account.Email != "alice@example.invalid" {
		t.Fatalf("Read account email = %q, want alice@example.invalid", p.Account.Email)
	}
	if len(p.Cookies) != 1 || p.Cookies[0].Name != "SID" {
		t.Fatalf("Read cookies = %+v, want one SID cookie", p.Cookies)
	}
}

// TestFakeStoreMissing: a missing name yields ErrProfileNotFound so
// callers can errors.Is against it (mirrors the DiskStore contract).
func TestFakeStoreMissing(t *testing.T) {
	store := &FakeStore{Profiles: map[Name]Profile{}}
	_, err := store.Read(context.Background(), Name("missing"))
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("FakeStore.Read(missing) error = %v, want ErrProfileNotFound", err)
	}
}

// TestFakeStoreReadErr: a store configured with ReadErr surfaces
// it verbatim — used by tests to simulate permission/parse
// failures.
func TestFakeStoreReadErr(t *testing.T) {
	want := errors.New("simulated IO failure")
	store := &FakeStore{ReadErr: want}
	_, err := store.Read(context.Background(), Name("work"))
	if !errors.Is(err, want) {
		t.Fatalf("FakeStore.Read(readerr) error = %v, want %v", err, want)
	}
}

// TestFakeStoreCanceled: ctx.Done before the call short-circuits
// the read. The FakeStore does NOT touch the in-memory map in this
// case, matching the DiskStore's pre-read cancellation contract.
func TestFakeStoreCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &FakeStore{Profiles: map[Name]Profile{Name("work"): {}}}
	_, err := store.Read(ctx, Name("work"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FakeStore.Read(canceled) error = %v, want context.Canceled", err)
	}
}

// TestDiskStoreReadFixture: DiskStore.Read against the committed
// fixture parses the in-band namespace and surfaces every cookie
// row. The fixture is a scrubbed Python profile so a leak of the
// test data into a log line is detectable (the value strings all
// carry the FAKE_* marker).
//
// This is the load-bearing test for AC4. The store does NOT touch
// the file beyond a single read; AC6 (read-only on disk) is
// mechanically satisfied because the DiskStore has no Write
// method.
func TestDiskStoreReadFixture(t *testing.T) {
	store := &DiskStore{
		Path: func(name Name) (string, error) {
			return fixturePath(t, "work_profile.json"), nil
		},
	}
	p, err := store.Read(context.Background(), Name("work"))
	if err != nil {
		t.Fatalf("DiskStore.Read(fixture) error = %v", err)
	}
	if p.Name != Name("work") {
		t.Fatalf("Read name = %q, want %q", p.Name, "work")
	}
	if p.Backend != BackendStorageFile {
		t.Fatalf("Read backend = %v, want BackendStorageFile", p.Backend)
	}
	if len(p.Cookies) != 3 {
		t.Fatalf("Read cookies = %d, want 3", len(p.Cookies))
	}
	// Verify the OSID row on notebooklm.google.com is preserved
	// with its domain — this is the exact OSID-on-two-hosts case
	// the cookiejar package's identity keying exists to keep
	// distinct (issue #369).
	var sawOSID bool
	for _, c := range p.Cookies {
		if c.Name == "OSID" && c.Domain == "notebooklm.google.com" {
			sawOSID = true
		}
	}
	if !sawOSID {
		t.Fatalf("Read cookies missing OSID on notebooklm.google.com: %+v", p.Cookies)
	}
	if p.Account.AuthUser != 2 || p.Account.Email != "fixture@example.invalid" {
		t.Fatalf("Read account = %+v, want authuser=2 email=fixture@example.invalid", p.Account)
	}
}

// TestDiskStoreReadMissing: a path that does not exist yields
// ErrProfileNotFound, not a wrapped os.PathError, so the ladder
// can match the sentinel.
func TestDiskStoreReadMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-profile", "storage_state.json")
	store := &DiskStore{
		Path: func(name Name) (string, error) { return missing, nil },
	}
	_, err := store.Read(context.Background(), Name("work"))
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("DiskStore.Read(missing) error = %v, want ErrProfileNotFound", err)
	}
}

// TestDefaultPathFunc: the default resolver rejects unsafe names
// before any IO, so a bad CLI argument cannot pass through to the
// filesystem layer.
func TestDefaultPathFunc(t *testing.T) {
	pf := DefaultPathFunc()
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"work", false},
		{"default", false},
		{"", true},
		{"..", true},
		{"../escape", true},
		{".hidden", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			path, err := pf(Name(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("DefaultPathFunc(%q) path = %q, want error", tc.in, path)
				}
				return
			}
			if err != nil {
				t.Fatalf("DefaultPathFunc(%q) error = %v", tc.in, err)
			}
			if path == "" {
				t.Fatalf("DefaultPathFunc(%q) returned empty path", tc.in)
			}
			if filepath.Base(path) != "storage_state.json" {
				t.Fatalf("DefaultPathFunc(%q) base = %q, want storage_state.json", tc.in, filepath.Base(path))
			}
		})
	}
}

// TestDefaultPathFuncRespectsEnv: a NOTEBOOKLM_HOME override
// determines the root. This is the same precedence as
// notebooklm.paths.get_storage_path so the Go port and the Python
// CLI share a filesystem view.
func TestDefaultPathFuncRespectsEnv(t *testing.T) {
	override := t.TempDir()
	t.Setenv("NOTEBOOKLM_HOME", override)
	pf := DefaultPathFunc()
	path, err := pf(Name("work"))
	if err != nil {
		t.Fatalf("DefaultPathFunc(work) error = %v", err)
	}
	want := filepath.Join(override, "profiles", "work", "storage_state.json")
	if path != want {
		t.Fatalf("DefaultPathFunc(work) = %q, want %q", path, want)
	}
}

// TestDiskStoreNilPath: a store with no PathFunc fails closed
// rather than panicking, so a misconfigured dependency surfaces
// as an error not a runtime crash.
func TestDiskStoreNilPath(t *testing.T) {
	store := &DiskStore{}
	_, err := store.Read(context.Background(), Name("work"))
	if err == nil {
		t.Fatalf("DiskStore.Read(nil-path) succeeded, want error")
	}
}

// TestDiskStoreBadJSON: a path that exists but does not parse
// returns a non-NotFound error so the caller can distinguish a
// corrupted profile from an absent one.
func TestDiskStoreBadJSON(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "storage_state.json")
	if err := os.WriteFile(bad, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	store := &DiskStore{
		Path: func(name Name) (string, error) { return bad, nil },
	}
	_, err := store.Read(context.Background(), Name("work"))
	if err == nil {
		t.Fatalf("DiskStore.Read(bad-json) succeeded, want error")
	}
	if errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("DiskStore.Read(bad-json) returned ErrProfileNotFound, want parse error: %v", err)
	}
}

// TestDiskStoreCanceled: ctx.Done before the call short-circuits
// the read.
func TestDiskStoreCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &DiskStore{
		Path: func(name Name) (string, error) {
			return fixturePath(t, "work_profile.json"), nil
		},
	}
	_, err := store.Read(ctx, Name("work"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DiskStore.Read(canceled) error = %v, want context.Canceled", err)
	}
}

// fixturePath returns the absolute path to a committed testdata
// file. #nosec G304 — the path is operator-controlled test input;
// the fixture is committed alongside the test.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}
