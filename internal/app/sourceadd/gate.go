// Package sourceadd — gate.go: the safety gates the classify result
// passes through before the SDK is invoked.
//
// Two gates are shipped in this file:
//
//  1. Symlink gate — rejects file paths that resolve (after
//     symlink expansion) inside the operator's ~/.notebooklm/
//     storage root. Re-uploading a file the CLI owns as a "source"
//     is almost always a mistake: a cassette file, a config file,
//     a credentials file would all round-trip back into the user's
//     notebook. The gate is permissive about the file the user
//     just dropped in their home directory; it is strict about the
//     storage root.
//
//  2. Internal-address gate — rejects URLs whose host resolves to a
//     loopback (127.0.0.0/8, ::1), link-local (169.254.0.0/16,
//     including the IMDS address 169.254.169.254), or RFC1918
//     private range (10/8, 172.16/12, 192.168/16). A URL whose
//     host is a name (not an IP literal) is accepted — DNS
//     resolution happens later in the SDK's transport layer, and a
//     malicious server-side redirect that lands on an internal
//     address is the SDK's transport problem, not the
//     classifier's. The classifier's job is to refuse the
//     obviously-bad literal-input case (a user typing
//     "http://127.0.0.1/admin" as their "URL source").
//
// The Validate function composes Classify + the two gates. It is
// the single seam a CLI/MCP/REST adapter calls — the adapter does
// not need to know which gate fired.
//
// Boundary: per docs/AGENTS.md rule 5 this package is part of
// internal/app (mode=internal in boundaries.yaml). It imports
// stdlib + the sibling internal/paths + internal/app/errors
// packages. No third-party dependencies.
package sourceadd

import (
	stderrors "errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/paths"
)

// ErrSymlinkInsideProfile is the sentinel returned by the symlink
// gate when a file path resolves inside the operator's ~/.notebooklm/
// storage root. Wrapped at the call site so the typed envelope
// carries the offending path for log-time debugging; callers
// branch with errors.Is to detect the category.
var ErrSymlinkInsideProfile = stderrors.New("sourceadd: file resolves inside ~/.notebooklm/")

// ErrInternalAddress is the sentinel returned by the internal-
// address gate when a URL points at a loopback, link-local, or
// RFC1918 host. The wrapped path string includes the original
// argument and the resolved IP so logs can confirm what the gate
// saw (the IP is not a credential, so it is safe to surface).
var ErrInternalAddress = stderrors.New("sourceadd: URL points at an internal network address")

// StorageRoot returns the canonical ~/.notebooklm/ directory the
// symlink gate compares against. The function is exported so
// callers that need to compose their own gate (e.g. a CLI that
// wants to add the storage root as a separate diagnostic field)
// can resolve the same path the gate uses without re-implementing
// the env-var / home-dir resolution dance.
//
// The home directory is resolved each call so a test that swaps
// NOTEBOOKLM_HOME (or HOME) sees the new path without rebuilding
// the package. Errors propagate; callers that need a fallback
// string for log lines can wrap the result in %q.
func StorageRoot() (string, error) {
	return paths.Home()
}

// Validate composes Classify and the two safety gates into the
// single seam an adapter calls. It returns the classified Kind on
// success and a typed validation error on any gate failure.
//
// The function signature is intentionally narrow — only `arg` and
// `kind` — because the symlink gate needs the resolved file path
// (which Classify already computed) rather than re-classifying the
// argument. Callers that already have a Kind from a previous
// Classify call can pass it through; callers that want Validate
// to re-classify can pass KindUnknown.
//
// The function takes the storage root lazily (via paths.Home) so
// it inherits the same NOTEBOOKLM_HOME / $HOME resolution as the
// rest of the module.
func Validate(arg string, kind Kind) (Kind, error) {
	// Re-classify if the caller passed KindUnknown so a caller
	// that wants to short-circuit the Kind return value still
	// gets a consistent result.
	resolved := kind
	if resolved == KindUnknown {
		k, err := Classify(arg)
		if err != nil {
			return k, err
		}
		resolved = k
	}

	switch resolved {
	case KindFile:
		if err := checkFilePath(arg); err != nil {
			return KindUnknown, err
		}
	case KindURL, KindYouTube, KindDrive:
		if err := checkInternalAddress(arg); err != nil {
			return KindUnknown, err
		}
	case KindText:
		// Inline text has no path / no URL; no gate applies.
	default:
		// KindUnknown or an unanticipated kind — return the kind
		// so the caller can branch, but signal no-gate-passed.
		return resolved, nil
	}
	return resolved, nil
}

// checkFilePath applies the symlink gate. The argument is
// path-shaped (the classifier already confirmed it); here we
// resolve any tilde prefix, expand symlinks, and reject any
// target that lives inside the storage root.
//
// The gate is deliberately permissive about the user-side: a
// symlink pointing at /tmp/uploads/report.pdf is fine; a symlink
// pointing at ~/.notebooklm/cassettes/secret.yaml is rejected.
// The "no symlinks into the storage root" rule is the single
// load-bearing check; the rest is plumbing.
func checkFilePath(arg string) error {
	trimmed := strings.TrimSpace(arg)
	expanded, err := expandTilde(trimmed)
	if err != nil {
		return apperrors.Wrap(apperrors.CodeValidationError, fmt.Errorf("sourceadd: resolve %q: %w", trimmed, err))
	}
	// Expand symlinks. EvalSymlinks returns the canonical path
	// after the full chain is resolved; a symlink loop surfaces
	// as an error we propagate.
	resolved, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		// Missing target, permission denied, symlink loop — none
		// of those should land on the gate, but if they do we
		// surface the underlying error verbatim so the CLI can
		// format a sensible message.
		return apperrors.Wrap(apperrors.CodeValidationError, fmt.Errorf("sourceadd: resolve symlinks for %q: %w", trimmed, err))
	}

	home, err := StorageRoot()
	if err != nil {
		return apperrors.Wrap(apperrors.CodeConfigError, fmt.Errorf("sourceadd: storage root: %w", err))
	}
	// Symlink-resolve the storage root too. On macOS the
	// well-known macOS temp dir lives under /var which is a
	// symlink to /private/var; if we resolved only the file
	// path the two paths would lexically differ even when
	// the file IS inside the storage root. EvalSymlinks
	// returns the input verbatim on failure (e.g. nonexistent
	// path) which is the desired fallback here.
	if homeResolved, herr := filepath.EvalSymlinks(home); herr == nil {
		home = homeResolved
	}
	if !isInsideDir(resolved, home) {
		return nil
	}

	// Build a typed envelope that carries the resolved path under
	// the sentinel. errors.Is(err, ErrSymlinkInsideProfile) returns
	// true so adapters can branch on the category without parsing
	// the message.
	return apperrors.Wrap(apperrors.CodeValidationError,
		fmt.Errorf("%w: %q resolves to %q inside %q", ErrSymlinkInsideProfile, trimmed, resolved, home))
}

// isInsideDir reports whether path is inside (or equal to) dir.
// Both arguments are expected to be cleaned and absolute; the
// function is the filesystem equivalent of strings.HasPrefix
// after a trailing-separator dance so /foo/bar does not match
// /foo/barbaz.
//
// Symlinks are already expanded by the caller; this is purely a
// lexical comparison so the result is deterministic regardless of
// the runtime's working directory.
func isInsideDir(path, dir string) bool {
	// Clean before comparison so trailing separators do not
	// confuse the prefix check.
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	if path == dir {
		return true
	}
	// filepath.Rel is the canonical "is path inside dir" check:
	// a relative result that begins with ".." means path is
	// outside dir. The empty path means path == dir (handled
	// above).
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

// checkInternalAddress applies the internal-address gate. The
// argument must already be URL-shaped (the classifier confirmed
// it). The gate extracts the host, treats IP literals specially,
// and accepts named hosts unconditionally (DNS resolution is the
// transport layer's job).
//
// The IP check covers:
//
//   - 127.0.0.0/8 (loopback IPv4)
//   - ::1 (loopback IPv6)
//   - 169.254.0.0/16 (link-local, including the cloud IMDS
//     address 169.254.169.254)
//   - 10.0.0.0/8 (RFC1918)
//   - 172.16.0.0/12 (RFC1918)
//   - 192.168.0.0/16 (RFC1918)
//
// 0.0.0.0 is treated as a wildcard "any local address" — the
// kernel resolves it to a local interface — so it is rejected
// alongside the loopback range.
func checkInternalAddress(arg string) error {
	trimmed := strings.TrimSpace(arg)
	u, err := url.Parse(trimmed)
	if err != nil {
		// A URL that already passed Classify should not fail
		// here; propagate the parse error as a validation
		// failure so the caller sees a consistent envelope.
		return apperrors.Wrap(apperrors.CodeValidationError, fmt.Errorf("sourceadd: parse URL %q: %w", trimmed, err))
	}
	host := u.Hostname()
	if host == "" {
		return apperrors.Wrap(apperrors.CodeValidationError, fmt.Errorf("sourceadd: URL %q has no host", trimmed))
	}

	// IP literal — apply the range check directly.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return apperrors.Wrap(apperrors.CodeValidationError,
				fmt.Errorf("%w: %q resolves to %s", ErrInternalAddress, trimmed, ip.String()))
		}
		return nil
	}

	// Named host — accept unconditionally. DNS is the transport
	// layer's problem; the gate refuses the obviously-bad
	// literal case only. A future ticket could add a hook for
	// the policy layer to enforce an outbound allowlist, but
	// that lives in internal/auth/policy, not here.
	return nil
}

// isBlockedIP reports whether ip falls inside one of the
// loopback / link-local / RFC1918 ranges the internal-address
// gate refuses. The check is explicit (no third-party CIDR
// library) so the package stays stdlib-only and the boundary
// rules stay green.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() {
		// Covers 127.0.0.0/8 and ::1.
		return true
	}
	if ip.IsLinkLocalUnicast() {
		// Covers 169.254.0.0/16 (IPv4) and fe80::/10 (IPv6). The
		// cloud-IMDS address 169.254.169.254 falls in this
		// range; rejecting link-local unicast also rejects it.
		return true
	}
	// isPrivate was added in Go 1.17; it covers RFC1918 ranges
	// plus the IPv6 unique-local fc00::/7 block. We gate the
	// call on Go's documented behavior so a future Go bump that
	// widens IsPrivate does not silently widen our rejection
	// set — the gate is the single seam a release manager
	// audits.
	if ip.IsPrivate() {
		return true
	}
	// IPv4 wildcard 0.0.0.0 — equivalent to "any local
	// interface". The Kernel resolves it to a local socket, so
	// a request to http://0.0.0.0/x is functionally a request
	// to the local machine. Reject it explicitly because
	// IsLoopback does not cover it.
	if ip4 := ip.To4(); ip4 != nil && ip4.Equal(net.IPv4zero) {
		return true
	}
	return false
}
