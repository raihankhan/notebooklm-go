// Package paths resolves the on-disk layout under the user's
// notebooklm home: the base directory itself, per-profile sub-directories,
// and the config.json sidecar. It exists so the rest of the codebase has
// exactly one source of truth for "~/.notebooklm/..." and never has to
// repeat the env-var / user-home-dir dance.
//
// Three rules from the Python original (notebooklm-py configuration docs
// + docs/13-operations.md) are encoded here:
//
//  1. NOTEBOOKLM_HOME overrides ~/.notebooklm. Empty / whitespace-only
//     values are treated as unset, matching the Python precedence rule.
//  2. Profile directories live under <home>/profiles/<name>/. The
//     `default` profile also accepts a legacy-fallback layout where
//     files live directly under <home>/ (no profiles/ subdir). See
//     profile.go for the carve-out.
//  3. config.json has an mtime cache keyed by its absolute path so the
//     refresh ladder does not re-stat it on every read. The cache is
//     invalidated by MarkWritten (called by every writer) — see
//     config.go.
//
// This package is intentionally small: it has no dependency on
// internal/web or internal/auth and is safe to import from anywhere
// inside the module.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// envHome is the name of the override env var. Defined as a constant so
// the same spelling appears once across the codebase; the precedence
// table in docs/13-operations.md is the source of truth.
const envHome = "NOTEBOOKLM_HOME"

// HomeDirName is the default base directory under the user's home when
// NOTEBOOKLM_HOME is unset. It must match the Python original and the
// precedence table in docs/13-operations.md (the operator's existing
// systemd / docker env file depends on the exact spelling).
const HomeDirName = ".notebooklm"

// Home returns the notebooklm home directory. The precedence is:
//
//  1. NOTEBOOKLM_HOME, if set and non-empty (whitespace-only counts as
//     unset, per docs/13-operations.md).
//  2. ~/.notebooklm/ (via os.UserHomeDir).
//
// The returned path is cleaned and absolute. A trailing separator is
// stripped so callers can append filenames without worrying about
// double separators on platforms that normalize them differently.
func Home() (string, error) {
	if v := strings.TrimSpace(os.Getenv(envHome)); v != "" {
		return filepath.Clean(v), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(userHome, HomeDirName)), nil
}
