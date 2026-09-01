package paths

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// withTempHome sets the user's home dir (via $HOME) and NOTEBOOKLM_HOME
// to a t.TempDir() so each test sees a clean slate. Returns the
// underlying temp directory so callers can seed files inside it.
//
// The home-dir paths are restored at the end of the test via t.Setenv.
// The returned nbHome path is also created with 0o700 so callers can
// write files inside it (Home() does not create the directory by
// design — only writes do, via atomicio.MkdirAll).
func withTempHome(t *testing.T) (homeDir, nbHome string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // Windows reads this for os.UserHomeDir.
	t.Setenv(envHome, "")
	nbHome = filepath.Join(dir, HomeDirName)
	if err := os.MkdirAll(nbHome, 0o700); err != nil {
		t.Fatalf("seed nbHome: %v", err)
	}
	return dir, nbHome
}

// TestHome_Default verifies the default "~/.notebooklm/" path when no
// override is set.
func TestHome_Default(t *testing.T) {
	homeDir, _ := withTempHome(t)

	got, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	want := filepath.Join(homeDir, HomeDirName)
	if got != want {
		t.Fatalf("Home = %q, want %q", got, want)
	}
}

// TestHome_OverrideEnv verifies NOTEBOOKLM_HOME takes precedence over
// the default.
func TestHome_OverrideEnv(t *testing.T) {
	_, _ = withTempHome(t)
	override := t.TempDir()
	t.Setenv(envHome, override)

	got, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if got != filepath.Clean(override) {
		t.Fatalf("Home = %q, want %q", got, filepath.Clean(override))
	}
}

// TestHome_OverrideEmpty verifies empty / whitespace-only env values
// count as unset (docs/13-operations.md).
func TestHome_OverrideEmpty(t *testing.T) {
	homeDir, _ := withTempHome(t)

	cases := []string{"", " ", "\t", "  \t\n "}
	for _, c := range cases {
		t.Run("env="+c, func(t *testing.T) {
			t.Setenv(envHome, c)
			got, err := Home()
			if err != nil {
				t.Fatalf("Home: %v", err)
			}
			want := filepath.Join(homeDir, HomeDirName)
			if got != want {
				t.Fatalf("Home = %q, want %q (env=%q should be treated as unset)", got, want, c)
			}
		})
	}
}

// TestProfileDir_Default asserts the standard per-profile layout for
// the default profile.
func TestProfileDir_Default(t *testing.T) {
	_, nbHome := withTempHome(t)

	got, err := ProfileDir(DefaultProfile)
	if err != nil {
		t.Fatalf("ProfileDir: %v", err)
	}
	want := filepath.Join(nbHome, ProfilesDirName, DefaultProfile)
	if got != want {
		t.Fatalf("ProfileDir(%q) = %q, want %q", DefaultProfile, got, want)
	}
}

// TestProfileDir_Other asserts named (non-default) profiles get the
// per-profile layout with no legacy fallback.
func TestProfileDir_Other(t *testing.T) {
	_, nbHome := withTempHome(t)

	got, err := ProfileDir("work")
	if err != nil {
		t.Fatalf("ProfileDir: %v", err)
	}
	want := filepath.Join(nbHome, ProfilesDirName, "work")
	if got != want {
		t.Fatalf("ProfileDir(%q) = %q, want %q", "work", got, want)
	}
}

// TestProfileDir_LegacyFallback exercises the AC6 carve-out: a Python
// install that wrote storage_state.json directly under <Home>/ is
// readable as the `default` profile.
func TestProfileDir_LegacyFallback(t *testing.T) {
	_, nbHome := withTempHome(t)

	// Seed the legacy unprofiled layout: <Home>/storage_state.json
	// exists, <Home>/profiles/default/ does NOT.
	legacy := filepath.Join(nbHome, legacyStorageState)
	if err := os.WriteFile(legacy, []byte(`{"cookies":[]}`), 0o600); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	got, err := ProfileDir(DefaultProfile)
	if err != nil {
		t.Fatalf("ProfileDir: %v", err)
	}
	if got != nbHome {
		t.Fatalf("ProfileDir(%q) = %q, want %q (legacy fallback)", DefaultProfile, got, nbHome)
	}
}

// TestProfileDir_LegacyFallback_NotForOtherProfiles asserts the
// fallback ONLY triggers for the default profile. A "work" profile
// with a stray storage_state.json in <Home>/ must not be misled into
// the fallback.
func TestProfileDir_LegacyFallback_NotForOtherProfiles(t *testing.T) {
	_, nbHome := withTempHome(t)

	if err := os.WriteFile(filepath.Join(nbHome, legacyStorageState),
		[]byte(`{"cookies":[]}`), 0o600); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	got, err := ProfileDir("work")
	if err != nil {
		t.Fatalf("ProfileDir: %v", err)
	}
	want := filepath.Join(nbHome, ProfilesDirName, "work")
	if got != want {
		t.Fatalf("ProfileDir(%q) = %q, want %q (no fallback for non-default)", "work", got, want)
	}
}

// TestProfileDir_LegacyFallback_IgnoredWhenPerProfileExists: if both
// layouts coexist (user has both written a Python storage_state.json
// AND run `notebooklm login` under the Go binary), the per-profile
// layout wins because it is the more-specific signal.
func TestProfileDir_LegacyFallback_IgnoredWhenPerProfileExists(t *testing.T) {
	_, nbHome := withTempHome(t)

	// Seed the legacy sentinel.
	if err := os.WriteFile(filepath.Join(nbHome, legacyStorageState),
		[]byte(`{"cookies":[]}`), 0o600); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	// ALSO create <Home>/profiles/default/. The per-profile layout
	// should win.
	profileDir := filepath.Join(nbHome, ProfilesDirName, DefaultProfile)
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatalf("seed profile dir: %v", err)
	}

	got, err := ProfileDir(DefaultProfile)
	if err != nil {
		t.Fatalf("ProfileDir: %v", err)
	}
	if got != profileDir {
		t.Fatalf("ProfileDir(%q) = %q, want %q (per-profile layout must win)",
			DefaultProfile, got, profileDir)
	}
}

// TestProfileDir_Empty is the error case.
func TestProfileDir_Empty(t *testing.T) {
	withTempHome(t)
	if _, err := ProfileDir(""); err == nil {
		t.Fatalf("ProfileDir(\"\") returned nil error")
	}
}

// TestConfig_Path asserts the path and that the cache is populated
// once the file exists.
func TestConfig_Path(t *testing.T) {
	_, nbHome := withTempHome(t)

	path, err := Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	want := filepath.Join(nbHome, ConfigFilename)
	if path != want {
		t.Fatalf("Config = %q, want %q", path, want)
	}

	// Before the file exists, the cache should be empty.
	if _, ok := CachedMtime(path); ok {
		t.Fatalf("CachedMtime before file exists: present (want absent)")
	}

	// Create the file. The next Config() call should populate the cache.
	if err := os.WriteFile(path, []byte(`{"language":"en"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Force a small mtime tick so the OS reports a strictly-greater
	// mtime when MarkWritten fires below. On filesystems with second-
	// resolution mtimes (some CI filesystems) two writes in the same
	// second would otherwise look like a no-op to the cache.
	time.Sleep(10 * time.Millisecond)

	path2, err := Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if path2 != path {
		t.Fatalf("Config returned %q, want %q", path2, path)
	}
	mtime1, ok := CachedMtime(path)
	if !ok {
		t.Fatalf("CachedMtime after Config: absent (want present)")
	}
	if mtime1.IsZero() {
		t.Fatalf("CachedMtime is zero")
	}
}

// TestConfig_MarkWrittenInvalidates verifies the AC7 contract: a write
// + MarkWritten refreshes the cached mtime, so a subsequent read sees
// the latest value.
func TestConfig_MarkWrittenInvalidates(t *testing.T) {
	withTempHome(t)

	path, err := Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"language":"en"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Force a distinct mtime on the second write. On filesystems with
	// second-resolution timestamps, two writes in the same second
	// produce the same mtime — useless for an invalidation test.
	first := time.Now().Add(-time.Hour).Round(time.Second)
	if err := os.Chtimes(path, first, first); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, err := Config(); err != nil {
		t.Fatalf("Config (1): %v", err)
	}
	mtime1, ok := CachedMtime(path)
	if !ok {
		t.Fatalf("CachedMtime after first Config: absent")
	}
	if !mtime1.Equal(first) {
		t.Fatalf("CachedMtime = %v, want %v", mtime1, first)
	}

	// Sleep past the mtime tick, write fresh content, MarkWritten.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"language":"de"}`), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	MarkWritten(path)

	mtime2, ok := CachedMtime(path)
	if !ok {
		t.Fatalf("CachedMtime after MarkWritten: absent")
	}
	if !mtime2.After(mtime1) {
		t.Fatalf("mtime did not advance: before=%v after=%v", mtime1, mtime2)
	}
}

// TestConfig_InvalidateAndReset exercises the explicit cache invalidators.
func TestConfig_InvalidateAndReset(t *testing.T) {
	withTempHome(t)
	path, err := Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Config(); err != nil {
		t.Fatalf("Config: %v", err)
	}
	if _, ok := CachedMtime(path); !ok {
		t.Fatalf("CachedMtime absent after Config")
	}

	Invalidate(path)
	if _, ok := CachedMtime(path); ok {
		t.Fatalf("CachedMtime present after Invalidate")
	}

	if _, err := Config(); err != nil {
		t.Fatalf("Config (after Invalidate): %v", err)
	}
	ResetCache()
	if _, ok := CachedMtime(path); ok {
		t.Fatalf("CachedMtime present after ResetCache")
	}
}

// TestResolve_PathInfo: the helper used by `status --paths`.
func TestResolve_PathInfo(t *testing.T) {
	_, nbHome := withTempHome(t)

	info, err := Resolve("default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.Home != nbHome {
		t.Fatalf("info.Home = %q, want %q", info.Home, nbHome)
	}
	if info.Profile != "default" {
		t.Fatalf("info.Profile = %q, want %q", info.Profile, "default")
	}
	if info.IsLegacyFallback {
		t.Fatalf("IsLegacyFallback = true, want false")
	}
	if info.ProfileDir != filepath.Join(nbHome, ProfilesDirName, "default") {
		t.Fatalf("info.ProfileDir = %q", info.ProfileDir)
	}
	if info.StoragePath != filepath.Join(nbHome, ProfilesDirName, "default", legacyStorageState) {
		t.Fatalf("info.StoragePath = %q", info.StoragePath)
	}
	if info.ConfigPath != filepath.Join(nbHome, ConfigFilename) {
		t.Fatalf("info.ConfigPath = %q", info.ConfigPath)
	}

	// JSON round-trip.
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"is_legacy_fallback":false`) {
		t.Fatalf("Marshal JSON = %s", b)
	}
}

// TestResolve_LegacyFallback_Flag flips true under the legacy layout.
func TestResolve_LegacyFallback_Flag(t *testing.T) {
	_, nbHome := withTempHome(t)
	if err := os.WriteFile(filepath.Join(nbHome, legacyStorageState),
		[]byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	info, err := Resolve("default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !info.IsLegacyFallback {
		t.Fatalf("IsLegacyFallback = false, want true")
	}
	if info.ProfileDir != nbHome {
		t.Fatalf("info.ProfileDir = %q, want %q (legacy)", info.ProfileDir, nbHome)
	}
}

// TestStoragePath_Default asserts the storage_state.json path for the
// default profile and a non-default profile.
func TestStoragePath_Default(t *testing.T) {
	_, nbHome := withTempHome(t)

	got, err := StoragePath("default")
	if err != nil {
		t.Fatalf("StoragePath: %v", err)
	}
	want := filepath.Join(nbHome, ProfilesDirName, "default", legacyStorageState)
	if got != want {
		t.Fatalf("StoragePath = %q, want %q", got, want)
	}

	got2, err := StoragePath("work")
	if err != nil {
		t.Fatalf("StoragePath: %v", err)
	}
	want2 := filepath.Join(nbHome, ProfilesDirName, "work", legacyStorageState)
	if got2 != want2 {
		t.Fatalf("StoragePath = %q, want %q", got2, want2)
	}
}

// TestProfileDir_OnWindows: the test suite must compile on Windows;
// POSIX permission assertions live in atomicio, paths is path-only.
func TestProfileDir_OnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("not running on Windows; included to keep Windows coverage in the test set")
	}
	withTempHome(t)
	if _, err := ProfileDir("default"); err != nil {
		t.Fatalf("ProfileDir on Windows: %v", err)
	}
}
