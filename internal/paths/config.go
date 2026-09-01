package paths

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConfigFilename is the name of the per-home config.json sidecar.
// Lives at <Home>/config.json, NOT inside the profile directory — it
// is home-wide (active profile, default language, etc.) not per-profile.
const ConfigFilename = "config.json"

// mtimeCacheMu guards mtimeCache (see below). Reads and writes of the
// cache are infrequent (one entry per known config path) so a single
// mutex is sufficient; no sharding needed.
var mtimeCacheMu sync.Mutex

// mtimeCache is the per-path mtime cache that backs Config(). It is
// keyed by the absolute path of config.json (so different profiles or
// test harnesses do not collide) and stores the most-recently-observed
// modification time.
//
// Callers must invoke MarkWritten after every write so the cache does
// not serve a stale value. Reads are served from the cache when the
// on-disk mtime still matches the cached value; the stat is repeated
// otherwise.
var mtimeCache = make(map[string]time.Time)

// Config returns the absolute path to the home-wide config.json file
// (typically <Home>/config.json).
//
// It does NOT read the file. Its sole job is path resolution; the
// actual read is done by internal/config (Phase 3). It does, however,
// update the mtime cache as a side-effect — callers that already know
// the path want the cache to reflect the current mtime without an
// extra stat.
//
// The cache invalidation contract is:
//
//   - Every writer that mutates config.json must call MarkWritten
//     AFTER the write commits. The cache entry is updated to the
//     post-write mtime so subsequent Config() calls return the
//     freshest value.
//   - Reads via the higher-level config loader use Config() to learn
//     the path; they then stat the file themselves (the loader owns
//     the actual read).
func Config() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ConfigFilename)

	// Best-effort cache refresh: if the file exists, record its current
	// mtime. If it does not, the cache entry is left alone (and absent
	// entries remain absent) so a first-time reader sees no stale entry.
	info, err := os.Stat(path)
	if err == nil {
		mtimeCacheMu.Lock()
		mtimeCache[path] = info.ModTime()
		mtimeCacheMu.Unlock()
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	return path, nil
}

// MarkWritten updates the mtime cache for path, recording the file's
// current mtime as the most-recently-observed value. Writers MUST call
// this after a successful atomic write to config.json so subsequent
// Config() / loader calls see the freshest mtime.
//
// If path does not exist or cannot be stat'd, MarkWritten is a no-op;
// the next Config() call will re-evaluate the cache on its own.
func MarkWritten(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	mtimeCacheMu.Lock()
	mtimeCache[path] = info.ModTime()
	mtimeCacheMu.Unlock()
}

// CachedMtime returns the cached mtime for path, plus a boolean that
// is false if no cache entry exists. It is exported for the config
// loader (Phase 3) so a reader can compare its own stat result against
// the cache and decide whether to re-read.
//
// This is a small helper exposed primarily so tests can probe the
// cache state without poking private internals.
func CachedMtime(path string) (time.Time, bool) {
	mtimeCacheMu.Lock()
	defer mtimeCacheMu.Unlock()
	t, ok := mtimeCache[path]
	return t, ok
}

// Invalidate drops the cache entry for path. It is used by tests to
// reset state between cases and is not part of the public writer
// contract (MarkWritten is — it both updates and, in effect, "re-invalidates"
// by overwriting).
func Invalidate(path string) {
	mtimeCacheMu.Lock()
	delete(mtimeCache, path)
	mtimeCacheMu.Unlock()
}

// ResetCache clears every cache entry. Reserved for tests; production
// code must use Invalidate or MarkWritten so the per-path contract is
// preserved.
func ResetCache() {
	mtimeCacheMu.Lock()
	mtimeCache = make(map[string]time.Time)
	mtimeCacheMu.Unlock()
}

// PathInfo describes the resolved on-disk layout for a profile. It is
// the value `notebooklm status --paths` emits (Phase 4+); the type
// lives here so the layout and the resolver are co-located.
type PathInfo struct {
	Home             string `json:"home"`
	Profile          string `json:"profile"`
	ProfileDir       string `json:"profile_dir"`
	StoragePath      string `json:"storage_path"`
	ConfigPath       string `json:"config_path"`
	IsLegacyFallback bool   `json:"is_legacy_fallback"`
}

// Resolve returns a PathInfo describing the resolved layout for the
// given profile. It does not touch the filesystem apart from the
// hasLegacyLayout check inside ProfileDir, so it is cheap to call.
//
// Returns an error if Home() fails. The path fields are always
// absolute; IsLegacyFallback is true only for the legacy-fallback case
// of the default profile.
func Resolve(profile string) (PathInfo, error) {
	home, err := Home()
	if err != nil {
		return PathInfo{}, err
	}
	profileDir, err := ProfileDir(profile)
	if err != nil {
		return PathInfo{}, err
	}
	storage, err := StoragePath(profile)
	if err != nil {
		return PathInfo{}, err
	}
	configPath := filepath.Join(home, ConfigFilename)
	return PathInfo{
		Home:             home,
		Profile:          profile,
		ProfileDir:       profileDir,
		StoragePath:      storage,
		ConfigPath:       configPath,
		IsLegacyFallback: profileDir == home,
	}, nil
}
