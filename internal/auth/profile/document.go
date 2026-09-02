// Package profile implements the on-disk profile read path used by the
// Phase-4 refresh ladder (T-P4-2). It mirrors notebooklm-py's
// `_auth/profile_document.py::ProfileDocument` and the smaller loaders in
// `_auth/profile_store.py` and `_auth/tokens.py`, but stays strictly
// read-only: writes are deferred to Sprint 3 (T-P4-2 ticket body, "Profile
// read is read-only; write is Sprint 3").
//
// The on-disk shape under ~/.notebooklm/profiles/<name>/ follows
// docs/05-auth.md "On-disk layout":
//
//	storage_state.json    cookies + in-band notebooklm.account
//	master_token.json     durable Google master token (Phase 6, optional)
//
// The Go Profile value is the in-memory, redacted view that the ladder's
// L1 rung consumes: identity (name, created_at, last_used_at), backend
// descriptor, cookies, account, tokens, and options. Cookie values are
// credential-equivalent and are exposed through Cookie.String() via the
// internal/redact primitive so a Profile that ever reaches a log/error
// sink cannot leak the underlying value (docs/AGENTS.md rule 4).
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal;
// it imports stdlib + internal/redact + internal/auth/cookiejar only.
package profile

import (
	"fmt"
	"strings"
	"time"
)

// Name is the validated profile directory name. The set of allowed
// characters matches the Python original's resolve_profile; we keep the
// type as a string with a NewName constructor so callers cannot
// construct an unsafe path by accident.
type Name string

// NewName validates and returns a profile Name. Empty, "." / "..",
// names containing path separators, and any name starting with a dot
// are rejected. These rules match the Python implementation in
// notebooklm._auth.paths.resolve_profile.
func NewName(raw string) (Name, error) {
	if raw == "" {
		return "", fmt.Errorf("profile: empty name")
	}
	if raw == "." || raw == ".." {
		return "", fmt.Errorf("profile: name %q is reserved", raw)
	}
	if strings.ContainsAny(raw, "/\\") {
		return "", fmt.Errorf("profile: name %q contains a path separator", raw)
	}
	if raw[0] == '.' {
		return "", fmt.Errorf("profile: name %q starts with a dot", raw)
	}
	return Name(raw), nil
}

// String returns the validated name. Implementations of fmt.Stringer
// across the codebase rely on this being safe to log.
func (n Name) String() string { return string(n) }

// Account mirrors the in-band notebooklm.account namespace that lives
// under storage_state.json. It is the canonical home for the authuser
// index and the stable account email (see docs/05-auth.md "Account
// routing"); pre-v0.5.0 legacy context.json siblings are promoted into
// this shape on load (see internal/auth/storage for the promotion
// rules), so a freshly-read Profile always has the merged account view.
type Account struct {
	// AuthUser is the integer Google account index. Zero is the
	// default profile; absent in-band metadata yields AuthUser=0.
	AuthUser int
	// Email is the stable Google account email when known. Empty
	// when the profile has not recorded an email yet.
	Email string
}

// Options captures the operator-selected domain policy. Both fields
// come from the notebooklm namespace; see notebooklm-py
// _auth/profile_account.parse_domain_selection.
//
// IncludeDomains is a deduped, case-preserving list of optional
// domain labels (youtube / docs / myaccount / mail / all) the
// operator opted into when they imported the profile. IncludeOptional
// is the boolean mirror of the same intent.
type Options struct {
	IncludeDomains  []string
	IncludeOptional bool
}

// Tokens is the typed auth-token bundle inside a profile. It is the
// stored view — not the live view; the CSRF and session id captured
// here is whatever was persisted on the last successful login (or, in
// the L1 reload path, the empty values Google never returns to disk).
//
// Read-side only: callers must never log the literal CSRF or session
// id. The redacting String() method on this type honors
// docs/AGENTS.md rule 4.
type Tokens struct {
	// CSRF is the SNlM0e token snapshot. Empty when not persisted.
	CSRF string
	// SessionID is the FdrFJe value snapshot. Empty when not
	// persisted.
	SessionID string
	// AuthUser is the routing account index at the time of the
	// last successful fetch. The reload path may overwrite this
	// from the in-band account metadata.
	AuthUser int
	// AccountEmail is the stable routing email at the time of the
	// last successful fetch. Empty when not known.
	AccountEmail string
	// FetchedAt is the wall-clock time of the last successful
	// fetch. Zero when never fetched.
	FetchedAt time.Time
}

// Backend is the typed backend descriptor the profile was loaded
// through. The set of values is closed: StorageFileAuth (the default
// on-disk path) and InlineEnvAuth (NOTEBOOKLM_AUTH_JSON, no file).
// Future backends (master token re-mint, browser re-auth) extend
// this enum.
type Backend int

const (
	// BackendUnknown is the zero value. Read paths treat it as
	// "no profile has been read yet"; Write paths (Sprint 3)
	// refuse it.
	BackendUnknown Backend = iota
	// BackendStorageFile is the storage_state.json on-disk path
	// under ~/.notebooklm/profiles/<name>/.
	BackendStorageFile
	// BackendInlineEnv is NOTEBOOKLM_AUTH_JSON — no on-disk
	// profile, read at process start.
	BackendInlineEnv
)

// String returns the canonical, redacted-safe label for the
// backend. Callers may safely log or surface this string; no
// credential material appears.
func (b Backend) String() string {
	switch b {
	case BackendStorageFile:
		return "storage_file"
	case BackendInlineEnv:
		return "inline_env"
	default:
		return "unknown"
	}
}

// redactedToken is the placeholder substituted for the CSRF and
// session id in Tokens.String(). Mirrors internal/redact so a
// Profile that reaches a log sink can never leak the credential.
const redactedToken = "[REDACTED]"

// String returns the redacted-safe summary of the Tokens value. The
// non-secret fields (AuthUser, AccountEmail, FetchedAt) are kept
// verbatim so a log line remains useful for diagnosing "which
// profile?", while the credential fields collapse to the standard
// redact marker.
func (t Tokens) String() string {
	var fetchedAt string
	if !t.FetchedAt.IsZero() {
		fetchedAt = t.FetchedAt.UTC().Format(time.RFC3339)
	} else {
		fetchedAt = "zero"
	}
	return fmt.Sprintf("Tokens(csrf=%s session=%s authuser=%d email=%q fetched_at=%s)",
		redactedToken, redactedToken, t.AuthUser, t.AccountEmail, fetchedAt)
}

// Cookie is a single credential row from the profile's
// storage_state.json. It mirrors internal/auth/cookiejar.Cookie
// but stays in its own type so the profile value can be moved
// across layer boundaries without dragging the cookie-jar lock
// semantics along. Backend code that needs a cookie-jar Cookie
// can convert via AsCookieJar.
type Cookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	Expires  time.Time
	Secure   bool
	HTTPOnly bool
	SameSite string
	HostOnly bool
	Raw      string
}

// String renders the cookie's Name= pair with the value run through
// the redaction marker. Per docs/AGENTS.md rule 4 this is the ONLY
// form of a cookie that may reach a log or error sink; transport
// code that needs the literal value builds the header from Name +
// Value directly.
func (c Cookie) String() string {
	if c.Name == "" {
		return redactedToken
	}
	return c.Name + "=" + redactedToken
}

// Profile is the top-level read-shape the Phase-4 ladder consumes.
// It is the union of the storage_state.json contents plus the
// profile-level metadata (name, timestamps, backend) the cookie
// document alone does not carry. The Cookies, Account, Tokens, and
// Options slices are all read-only snapshots that share no memory
// with the on-disk file.
//
// Profile never carries the literal CSRF token or cookie values
// after String(); it is therefore safe to log via %v / %s.
type Profile struct {
	// Name is the validated profile directory name.
	Name Name
	// Backend is the typed backend the profile was loaded
	// through.
	Backend Backend
	// CreatedAt is the wall-clock creation time of the profile
	// directory, when known. Zero when unknown (e.g. an inline
	// env-var profile never creates a directory).
	CreatedAt time.Time
	// LastUsedAt is the wall-clock time of the most recent
	// successful auth fetch. Zero when never used.
	LastUsedAt time.Time
	// Cookies is the cookie set in storage-load order. Two cookies
	// sharing a name at distinct domains stay distinct entries
	// (RFC 6265 §5.3 identity; see internal/auth/cookiejar).
	Cookies []Cookie
	// Account is the in-band notebooklm.account metadata. A
	// profile loaded from NOTEBOOKLM_AUTH_JSON (no file) can
	// carry an empty Account.
	Account Account
	// Tokens is the typed CSRF / session id / authuser bundle
	// from the last successful fetch. Empty on a freshly
	// imported profile.
	Tokens Tokens
	// Options is the operator-selected domain policy parsed
	// from the notebooklm.include_domains /
	// notebooklm.include_optional namespace keys.
	Options Options
}

// String returns the redacted, log-safe representation of the
// Profile. Cookie count and account identity are preserved so the
// line stays useful in -vv output; cookie values and CSRF /
// session id collapse to the standard redact marker.
func (p Profile) String() string {
	created := "zero"
	if !p.CreatedAt.IsZero() {
		created = p.CreatedAt.UTC().Format(time.RFC3339)
	}
	lastUsed := "never"
	if !p.LastUsedAt.IsZero() {
		lastUsed = p.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"Profile(name=%q backend=%s created=%s last_used=%s cookies=%d account=%q)",
		p.Name.String(), p.Backend, created, lastUsed, len(p.Cookies), p.Account.Email,
	)
}
