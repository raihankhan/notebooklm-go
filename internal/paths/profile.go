package paths

import (
	"errors"
	"os"
	"path/filepath"
)

// ProfilesDirName is the per-profile parent directory under Home(). It
// is exported so callers building user-visible paths (e.g. status
// --paths) can render the layout verbatim.
const ProfilesDirName = "profiles"

// DefaultProfile is the profile name used when no profile is named. It
// matches the Python original and the precedence table in
// docs/13-operations.md (NOTEBOOKLM_PROFILE defaults to "default").
const DefaultProfile = "default"

// legacyStorageState is the sentinel filename used to detect the
// legacy unprofiled layout. When <Home>/storage_state.json exists and
// <Home>/profiles/default/ does not, ProfileDir("default") returns
// <Home>/ directly so existing Python-CLI users do not silently lose
// their credentials. The names below mirror the Python storage layer
// (see _atomic_io.py and _auth/storage.py).
const legacyStorageState = "storage_state.json"

// ProfileDir returns the per-profile directory for the named profile.
//
// Standard layout: <Home>/profiles/<profile>/
//
// Legacy fallback: when profile == DefaultProfile AND
// <Home>/profiles/default/ does not exist AND <Home>/ has the legacy
// unprofiled layout (sentinel: <Home>/storage_state.json), the function
// returns <Home>/ directly. This is what lets an existing Python-CLI
// user switch to the Go binary without re-running `notebooklm login`.
//
// Non-default profiles never get the legacy fallback — they must be a
// fresh subdirectory under profiles/.
func ProfileDir(profile string) (string, error) {
	if profile == "" {
		return "", errors.New("paths: empty profile name")
	}
	home, err := Home()
	if err != nil {
		return "", err
	}

	if profile == DefaultProfile && hasLegacyLayout(home) {
		return home, nil
	}
	return filepath.Clean(filepath.Join(home, ProfilesDirName, profile)), nil
}

// hasLegacyLayout reports whether home already holds the unprofiled
// Python-CLI layout: <home>/storage_state.json exists AND the
// per-profile directory does not.
//
// We check both signals because a user who has run `notebooklm login`
// once with the Go binary will have BOTH <home>/storage_state.json AND
// <home>/profiles/default/ — and they want the per-profile layout in
// that case. The "no per-profile dir" check makes the legacy fallback
// strictly opt-in for first-time Go users.
func hasLegacyLayout(home string) bool {
	profileDir := filepath.Join(home, ProfilesDirName, DefaultProfile)
	if _, err := os.Stat(profileDir); err == nil {
		return false
	}
	legacy := filepath.Join(home, legacyStorageState)
	if _, err := os.Stat(legacy); err != nil {
		return false
	}
	return true
}

// MustProfileDir is the panicking variant of ProfileDir for use in
// initialization paths where Home() is not allowed to fail (e.g. CLI
// flag defaults). Production code should use ProfileDir and surface
// the error.
func MustProfileDir(profile string) string {
	p, err := ProfileDir(profile)
	if err != nil {
		panic(err)
	}
	return p
}

// StoragePath returns the canonical storage_state.json path for the
// given profile. It composes ProfileDir with the canonical filename.
//
// Profile storage layout:
//
//	default profile (legacy fallback): <Home>/storage_state.json
//	default profile (per-profile layout): <Home>/profiles/default/storage_state.json
//	other profiles:                       <Home>/profiles/<name>/storage_state.json
func StoragePath(profile string) (string, error) {
	dir, err := ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, legacyStorageState), nil
}
