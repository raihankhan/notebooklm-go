// Package profile: read-only disk-backed Reader and a small
// in-memory FakeStore used by Phase 4 tests.
//
// The on-disk path is storage_state.json under the operator's profile
// directory; this file is the single canonical source of truth for the
// cookies, the in-band notebooklm.account metadata, and (optionally)
// the persisted domain-selection options. Master tokens and the
// browser user-data-dir live in sibling files and are out of scope for
// Phase 4.
//
// Reads use internal/atomicio.ReadFile so a path that is being
// concurrently rewritten (atomic temp + rename) is observed either at
// the pre-rename or the post-rename state — never at a half-written
// byte. Writes are intentionally absent in this package: they land in
// Sprint 3 (the parent ticket #49 schedules the write path under
// T-P4-3 / T-P4-4 once the load profile generator is stable).
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal;
// it imports stdlib + internal/atomicio (the read path) +
// internal/auth/storage (the lossless storage_state read layer with
// the Python-CLI normalization built in). The cookie-jar package is
// not imported here: the profile package keeps its own Cookie shape
// so the read layer is not coupled to the jar's lock state.
package profile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/atomicio"
	"github.com/raihankhan/notebooklm-go/internal/auth/storage"
)

// PathFunc resolves the canonical storage_state.json path for a
// profile name. Implementations must return an absolute, profile-scoped
// path; the resolved path is the trust boundary (per docs/AGENTS.md
// rule 4) — every read/write below is keyed off this path.
type PathFunc func(name Name) (string, error)

// DefaultPathFunc returns a PathFunc that resolves a profile name to
// <home>/profiles/<name>/storage_state.json. The home directory is
// resolved each call so a test that swaps NOTEBOOKLM_HOME (or the
// equivalent package-level setter) sees the new path without needing
// to rebuild the store.
//
// The legacy home-root fallback for the "default" profile — older
// builds of the notebooklm CLI placed storage_state.json directly
// under ~/.notebooklm/ — is NOT consulted here. That path is handled
// in internal/paths (T-P2-4) and is out of scope for the Phase-4
// ladder: the ladder always talks to a named profile.
func DefaultPathFunc() PathFunc {
	return func(name Name) (string, error) {
		if _, err := NewName(string(name)); err != nil {
			return "", err
		}
		return storagePathFor(name), nil
	}
}

// storagePathFor composes <home>/profiles/<name>/storage_state.json
// using the current value of NOTEBOOKLM_HOME (or the platform
// default). Keeping the helper here rather than importing
// internal/paths is deliberate: the profile package is the
// smallest read layer the ladder needs and must stay importable
// from any later phase without an internal/paths dependency. A
// future ticket will swap this for a thin internal/paths call
// (T-P4-2 ticket body leaves that as a follow-up).
func storagePathFor(name Name) string {
	home := os.Getenv("NOTEBOOKLM_HOME")
	if home == "" {
		home = defaultHome()
	}
	return joinProfilePath(home, name)
}

// joinProfilePath composes <home>/profiles/<name>/storage_state.json.
// Extracted so tests can exercise the path layout without polluting
// NOTEBOOKLM_HOME.
func joinProfilePath(home string, name Name) string {
	return fmt.Sprintf("%s/profiles/%s/storage_state.json", home, string(name))
}

// defaultHome returns the platform-default notebooklm home directory.
// Mirrors notebooklm.paths.default_home: on POSIX it is
// $HOME/.notebooklm; on Windows %USERPROFILE%/.notebooklm.
func defaultHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h + "/.notebooklm"
	}
	if u := os.Getenv("USERPROFILE"); u != "" {
		return u + "\\.notebooklm"
	}
	return ".notebooklm"
}

// ErrProfileNotFound is returned by DiskStore.Read when the resolved
// storage_state.json path does not exist. Callers can errors.Is(err,
// ErrProfileNotFound) to distinguish "no profile" from a permission
// or parse error on an existing file.
var ErrProfileNotFound = errors.New("profile: not found")

// Reader is the read-only contract the refresh ladder (Phase
// 4, T-P4-2) consumes. The package-qualified name
// `profile.Reader` avoids the stutter the linter rejects. Two
// implementations exist today: the production DiskStore and the
// test-only FakeStore. Both are safe for concurrent use; the
// disk-backed DiskStore reads through internal/atomicio which
// serializes a process's own concurrent reads on the file handle,
// and relies on the canonical atomic-rename writer pattern
// (temp + chmod + fsync + rename) to be safe across processes.
type Reader interface {
	// Read returns the typed Profile for name. The returned
	// Profile is a fresh, independent snapshot — the caller may
	// mutate it without affecting subsequent reads.
	//
	// Errors:
	//   - ErrProfileNotFound — the storage file does not exist
	//     (errors.Is(err, ErrProfileNotFound)).
	//   - any wrapped error from internal/auth/storage for a
	//     parse, permission, or read failure on an existing
	//     file.
	Read(ctx context.Context, name Name) (Profile, error)
}

// DiskStore implements Reader against the on-disk
// storage_state.json under the operator home. It is read-only
// (writes land in Sprint 3) and safe for concurrent use.
type DiskStore struct {
	// Path is the PathFunc the store uses to resolve a name to
	// an absolute storage_state.json path. Tests inject a
	// directory-prefixed PathFunc for fixture isolation.
	Path PathFunc
	// Now is the wall-clock supplier; tests inject a fixed
	// clock. Nil falls back to time.Now.
	Now func() time.Time
}

// NewDiskStore returns a DiskStore that resolves profile names
// through the default PathFunc. The Now clock defaults to time.Now.
func NewDiskStore() *DiskStore {
	return &DiskStore{Path: DefaultPathFunc()}
}

// Read loads the storage_state.json document at the resolved path,
// applies internal/auth/storage's import-time normalizers, and
// projects the result into the Profile value the ladder consumes.
//
// ctx is currently honored only as a cancellation signal before the
// actual file I/O starts; the read itself is a single
// atomicio.ReadFile call that is fast enough not to need finer
// cancellation. A future Sprint may extend the read to streaming
// for very large profiles; until then the contract is "ctx.Done
// before the read starts cancels the read".
func (s *DiskStore) Read(ctx context.Context, name Name) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	if s.Path == nil {
		return Profile{}, fmt.Errorf("profile: DiskStore.Path is nil")
	}
	path, err := s.Path(name)
	if err != nil {
		return Profile{}, err
	}
	data, err := atomicio.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Profile{}, ErrProfileNotFound
		}
		return Profile{}, fmt.Errorf("profile: read %s: %w", name, err)
	}
	store, err := storage.Unmarshal(data)
	if err != nil {
		return Profile{}, fmt.Errorf("profile: parse %s: %w", path, err)
	}

	p := Profile{
		Name:    name,
		Backend: BackendStorageFile,
		Cookies: toProfileCookies(store.Cookies),
	}
	// In-band namespace: Account. Optional; an absent namespace
	// means the profile has not been upgraded to multi-account
	// routing yet.
	if store.NotebookLM != nil {
		p.Account = Account{
			AuthUser: store.NotebookLM.Account.AuthUser,
			Email:    store.NotebookLM.Account.Email,
		}
	}

	// Profile directory mtime gives us a usable CreatedAt for
	// the on-disk path. A profile that has never been used
	// still has a directory; we surface the directory-mtime as
	// CreatedAt and leave LastUsedAt at the zero value so
	// callers can tell the two apart.
	if info, statErr := os.Stat(directoryOf(path)); statErr == nil {
		p.CreatedAt = info.ModTime().UTC()
	}
	return p, nil
}

// directoryOf returns the parent directory of path. storage_state.json
// is always one level deep under <home>/profiles/<name>/, so the
// immediate parent is the profile-root directory.
func directoryOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

// toProfileCookies lifts storage.Cookie rows into profile.Cookie rows.
// The cookie-jar conversion lives in the Phase-5 column; the profile
// package keeps its own shape to avoid pulling the jar lock state
// across this read boundary.
func toProfileCookies(src []storage.Cookie) []Cookie {
	if len(src) == 0 {
		return nil
	}
	out := make([]Cookie, len(src))
	for i, c := range src {
		var expires time.Time
		if c.Expires != nil {
			expires = time.Unix(*c.Expires, 0).UTC()
		}
		out[i] = Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  expires,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
			HostOnly: c.HostOnly,
		}
	}
	return out
}

// FakeStore is a Reader backed by an in-memory map. Tests
// use it to exercise the L1 reload path without touching disk; the
// production ladder uses DiskStore.
type FakeStore struct {
	// Profiles maps Name -> Profile. Missing entries read as
	// ErrProfileNotFound, mirroring the disk-backed behavior.
	Profiles map[Name]Profile
	// ReadErr is returned (when non-nil) on every Read, so a
	// test can simulate IO failures without mutating Profiles.
	ReadErr error
}

// Read is the Reader implementation.
func (f *FakeStore) Read(ctx context.Context, name Name) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	if f.ReadErr != nil {
		return Profile{}, f.ReadErr
	}
	p, ok := f.Profiles[name]
	if !ok {
		return Profile{}, ErrProfileNotFound
	}
	p.Name = name
	return p, nil
}
