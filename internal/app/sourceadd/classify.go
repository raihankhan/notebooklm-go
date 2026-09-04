// Package sourceadd is the application-layer entry point for the
// `notebooklm source add <arg>` command family. The command accepts
// five kinds of inputs — a generic URL, a YouTube URL/ID, raw text, a
// local file path, or a Google Drive share URL — and routes each
// input to the corresponding SDK call (added in T-S3-004b/c).
//
// This file ships only the *classifier*: a pure function that
// inspects the raw argument string and returns the inferred Kind.
// The classifier is the first half of T-S3-004a; the second half
// (the safety gates that prevent file paths inside ~/.notebooklm/
// from being re-uploaded as a "file" source and that reject URLs
// pointing at internal-network addresses) lives in gate.go.
//
// The classifier is intentionally separated from the safety gates so
// downstream callers can compose them independently: a future
// sourceadd flow that only needs to know "is this a URL or a path?"
// (e.g. an MCP tool that does not own the local filesystem) can call
// Classify directly without pulling in the path-validation code.
//
// Boundary: per docs/AGENTS.md rule 5 this package is part of
// internal/app (mode=internal in boundaries.yaml). It imports stdlib
// + the sibling internal/* packages it depends on. The CLI/MCP/REST
// adapters (internal/cli, internal/mcpsrv, internal/restsrv) own
// their own classification wrappers if they need a Kind-aware shape
// for a different protocol; this package only ships the lower-level
// building block.
package sourceadd

import (
	stderrors "errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Kind is the source kind inferred from a user-supplied argument.
// The string values are the wire values the SDK methods accept
// (SourcesAPI.AddURL, AddYouTube, AddText, AddFile, AddDrive) plus
// the two "non-positive" outcomes KindUnknown and the sentinel
// returned alongside ErrAmbiguousSource.
//
// KindUnknown is returned when the argument matches none of the
// concrete kinds unambiguously. Pair with ErrAmbiguousSource for the
// "could be more than one kind" failure; pair with KindText for the
// "everything else falls through to inline text" success case.
type Kind string

// Canonical Kind values. The five concrete kinds are the surface
// `notebooklm source add` advertises; KindUnknown is the rejection
// value returned alongside ErrAmbiguousSource.
//
// String values are the lowercase canonical names the CLI surfaces
// in --json output and in the "kind:" field of the source row the
// SDK returns. They must NOT change without a corresponding docs
// update — automation parses these strings.
const (
	// KindURL is a generic https:// (or http://) URL that is not
	// recognized as a YouTube watch page or a Google Drive share URL.
	KindURL Kind = "url"

	// KindYouTube is a YouTube watch URL (youtube.com/watch?v=... or
	// youtu.be/...) — accepted by SourcesAPI.AddYouTube.
	KindYouTube Kind = "youtube"

	// KindText is raw inline text that the user pasted as the
	// argument. Treated as the fallback kind when no other rule
	// matches.
	KindText Kind = "text"

	// KindFile is a local file path (absolute, ./relative, ~/tilde,
	// or bare ~) that exists on disk and is NOT inside the
	// ~/.notebooklm/ storage root.
	KindFile Kind = "file"

	// KindDrive is a Google Drive share URL — drive.google.com/file
	// or docs.google.com/{document,spreadsheet,presentation,...} —
	// accepted by SourcesAPI.AddDrive.
	KindDrive Kind = "drive"

	// KindUnknown is the "could not classify" rejection value. The
	// classifier returns this alongside an error so the caller can
	// branch on either the kind or the error.
	KindUnknown Kind = "unknown"
)

// ErrAmbiguousSource signals that the argument matched more than
// one classification rule — typically a string that looks like both
// a URL and a relative file path (e.g. "./foo" vs "foo"). Pair with
// KindUnknown so callers can `errors.Is(err, ErrAmbiguousSource)`
// without coupling to the wrapped message text.
//
// The error is a sentinel (per Go convention) so adapters can
// branch on it with errors.Is; the wrapping happens at the call
// site so the message can include the ambiguous argument for
// log-time debugging.
var ErrAmbiguousSource = stderrors.New("sourceadd: ambiguous source argument")

// Classification rules. Each constant is a short, single-purpose
// fragment matched against the trimmed argument. Kept private so
// they do not leak into the public API surface — callers branch on
// the returned Kind, not on rule strings.
//
// The order of checks in Classify determines the precedence:
// Drive > YouTube > URL > File > Text. Drive and YouTube are
// checked before generic URL because both are syntactically URLs;
// the more specific kind wins.
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"

	hostYouTubeWatch = "youtube.com"
	hostYouTubeWWW   = "www.youtube.com"
	hostYouTubeShort = "youtu.be"
	hostDriveFile    = "drive.google.com"
	hostDocsGoogle   = "docs.google.com"

	pathDriveFile  = "/file/"
	pathDocsPrefix = "/document/"
	pathSheetsPref = "/spreadsheets/"
	pathSlidesPref = "/presentation/"

	queryYouTubeV = "v"
)

// Classify inspects the argument and returns the inferred Kind. The
// second return value is non-nil when the argument is malformed or
// matches more than one rule; the adapter surface maps that error
// to a typed VALIDATION_ERROR envelope (apperrors.CodeValidationError)
// so users see a stable exit code and automation can branch on
// `code == "VALIDATION_ERROR"`.
//
// Recognition rules (in evaluation order):
//
//  1. Drive share URL — https://drive.google.com/file/... or
//     https://docs.google.com/{document,spreadsheets,presentation}/...
//     → KindDrive.
//  2. YouTube watch URL — https://(www.)?youtube.com/watch?v=<id>
//     or https://youtu.be/<id> → KindYouTube.
//  3. Generic URL — any http://... or https://... → KindURL.
//  4. Local file path — absolute (/path), relative (./path, ../path),
//     or tilde-prefixed (~, ~/path, ~user/path). The path must
//     resolve to an existing file on disk (via os.Stat) to be
//     classified as KindFile; a path-shaped string that does NOT
//     resolve is left to fall through to KindText rather than
//     rejected here, because a user typing "hello world" should
//     get inline-text behavior rather than a path-validation
//     failure. Tilde-prefixed paths expand via os.UserHomeDir so a
//     CLI invocation that sets HOME for testing still resolves the
//     same way the runtime would.
//  5. Anything else (including the empty string) — KindText for the
//     "raw inline text" success path. An empty string is still
//     KindText; the CLI catches it as a usage error before reaching
//     this point.
//
// Ambiguity rejection: a string that begins with a path prefix
// ("./", "../", "/", "~") AND also parses as a URL is rejected with
// KindUnknown + ErrAmbiguousSource. In practice the path prefix
// wins (because path-shaped strings never begin with a URL scheme),
// but the explicit ambiguity check guards against future rule
// additions (e.g. supporting "file://" URLs) that would otherwise
// silently reclassify path-shaped inputs.
func Classify(arg string) (Kind, error) {
	trimmed := strings.TrimSpace(arg)

	// Drive first — drive.google.com/file is syntactically a URL
	// but a more specific kind than generic KindURL.
	if isDriveURL(trimmed) {
		return KindDrive, nil
	}

	// YouTube next — same reason.
	if isYouTubeURL(trimmed) {
		return KindYouTube, nil
	}

	// Generic URL. Anything starting with http:// or https:// that
	// has not already matched a more specific rule lands here.
	if isGenericURL(trimmed) {
		return KindURL, nil
	}

	// Path-shaped strings. The order matters: tilde paths must be
	// expanded BEFORE absolute/relative detection so "~" alone
	// resolves to $HOME rather than being treated as a one-character
	// filename.
	if isPathShaped(trimmed) {
		expanded, err := expandTilde(trimmed)
		if err != nil {
			// errors.Join composes two wrap targets so both
			// the sentinel (ErrAmbiguousSource) and the
			// underlying tilde-expansion error are
			// matchable by errors.Is. This satisfies
			// errorlint's "single %w" rule (a single
			// Errorf call with both %w and %v is the
			// pattern the linter rejects; errors.Join
			// avoids the pattern entirely).
			return KindUnknown, stderrors.Join(ErrAmbiguousSource, err)
		}
		if fileExists(expanded) {
			return KindFile, nil
		}
		// Path-shaped but missing on disk — fall through to text.
		// The user might have meant to type a path but the file
		// does not exist; treating that as inline text would silently
		// upload the path string as a text source, which is worse
		// than rejecting it. Return KindUnknown + a descriptive
		// error so the CLI can surface "file not found".
		return KindUnknown, fmt.Errorf("%w: %q does not refer to an existing file", ErrAmbiguousSource, trimmed)
	}

	// Fall-through: treat as inline text. The empty string lands
	// here too; the CLI rejects empty arguments before reaching
	// this point, but a defensively-defaulted KindText keeps the
	// function total (no panics on edge inputs).
	return KindText, nil
}

// isGenericURL reports whether arg is an http(s) URL that has not
// matched a more specific rule. Parsing goes through net/url so
// the scheme + host check is robust against malformed inputs
// (a string like "http://" parses with an empty host, which is not
// a routable URL — we treat that as non-URL and let it fall
// through to text).
func isGenericURL(arg string) bool {
	if !strings.HasPrefix(arg, schemeHTTP+"://") && !strings.HasPrefix(arg, schemeHTTPS+"://") {
		return false
	}
	u, err := url.Parse(arg)
	if err != nil {
		return false
	}
	// A URL with no host is not a usable URL for source-add — it
	// is either malformed ("http://") or a path-only string that
	// happened to start with a scheme. Let it fall through.
	return u.Host != ""
}

// isYouTubeURL reports whether arg is a YouTube watch or short URL
// — the two shapes the SDK AddYouTube method accepts. The "www."
// prefix on youtube.com is normalized away before comparison so
// the rule is host-suffix-based rather than exact-match.
func isYouTubeURL(arg string) bool {
	u, err := url.Parse(arg)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	switch host {
	case hostYouTubeShort:
		// youtu.be/<id> — id is the first path segment.
		return strings.TrimPrefix(u.Path, "/") != ""
	case hostYouTubeWatch, hostYouTubeWWW:
		// youtube.com/watch?v=<id> — id lives in the v query param.
		return u.Query().Get(queryYouTubeV) != ""
	default:
		return false
	}
}

// isDriveURL reports whether arg is a Google Drive share URL — the
// three hosts (drive.google.com, docs.google.com) and four path
// shapes (drive /file/..., docs /document/..., /spreadsheets/...,
// /presentation/...) the SDK AddDrive method accepts.
func isDriveURL(arg string) bool {
	u, err := url.Parse(arg)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	path := u.Path
	switch host {
	case hostDriveFile:
		return strings.HasPrefix(path, pathDriveFile) || strings.HasPrefix(path, pathDocsPrefix)
	case hostDocsGoogle:
		return strings.HasPrefix(path, pathDocsPrefix) ||
			strings.HasPrefix(path, pathSheetsPref) ||
			strings.HasPrefix(path, pathSlidesPref)
	default:
		return false
	}
}

// isPathShaped reports whether arg looks like a filesystem path —
// the four prefixes the CLI / MCP surface accept for the "file"
// kind. Bare tilde ("~") is included because a user typing just "~"
// expects it to resolve to $HOME; without that, "~/foo.pdf" would
// match the tilde rule but a lone "~" would not.
func isPathShaped(arg string) bool {
	if arg == "" {
		return false
	}
	if arg[0] == '/' {
		return true
	}
	if strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
		return true
	}
	if arg == "~" || strings.HasPrefix(arg, "~/") || strings.HasPrefix(arg, "~"+string(os.PathSeparator)) {
		return true
	}
	// "user/..." style tilde forms (e.g. ~alice/foo) are rare on
	// non-POSIX shells and the Go runtime does not expand them
	// out of the box; require they begin with ~/<segment> to avoid
	// a confusing error if a user pastes a URL-shaped string.
	if i := strings.IndexByte(arg, '/'); i > 0 && arg[:i] != "" {
		if arg[0] == '~' {
			return true
		}
	}
	return false
}

// expandTilde resolves a leading "~" or "~/" to the user's home
// directory via os.UserHomeDir. Bare "~" resolves to $HOME; "~/"
// resolves to $HOME; "~name/..." is left untouched (the Go
// runtime does not expand other-user tildes, and treating it as a
// path-shaped argument would silently misroute the input).
func expandTilde(arg string) (string, error) {
	if arg == "" || arg[0] != '~' {
		return arg, nil
	}
	if arg == "~" || strings.HasPrefix(arg, "~/") || strings.HasPrefix(arg, "~"+string(os.PathSeparator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if arg == "~" {
			return home, nil
		}
		return filepath.Join(home, arg[2:]), nil
	}
	// "~user/..." — not expanded by the Go runtime.
	return arg, nil
}

// fileExists reports whether path resolves to an existing file
// (regular file, symlink target — the Lstat-then-Stat pair catches
// symlinks-to-missing-files, which is what a malicious user could
// use to read an arbitrary file later).
func fileExists(path string) bool {
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	// Resolve symlinks and require the target to be a regular
	// file. A symlink to a directory would still pass Lstat but
	// fail Stat — we want the file kind to require "file".
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return true
}
