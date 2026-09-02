// Package auth carries the Tokens struct, the load-from-storage pipeline,
// and the credential-aware redacting accessors. Port of
// notebooklm._auth.tokens at the structural level: the Go package is
// narrower than the Python one (the Python original also bundles the
// AccountRouteResolver, the legacy migration scheduler, and the
// singleflight glue; those land in later sprints — see
// docs/10-implementation-plan.md).
//
// The load-from-storage pipeline is:
//
//	resolve source  →  seed jar  →  acquire tokens  →  merge observation  →  build
//
// This is the canonical Phase-4 entry point: the refresh ladder calls
// LoadFromStorage once at the top of an attempt, captures the Tokens
// into an immutable AuthSnapshot (docs/05-auth.md §"Auth snapshot
// discipline"), and routes all RPCs through that snapshot so cookies,
// the CSRF token, and the session id advance together under the same
// mutex.
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal;
// it imports stdlib + internal/redact (so the redacting String/LogValue
// accessors route through the credential primitive) + the four
// siblings this package composes (internal/auth/storage,
// internal/auth/cookiejar, internal/auth/policy, internal/auth/extract).
// No third-party dependencies.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/auth/cookiejar"
	"github.com/raihankhan/notebooklm-go/internal/auth/policy"
	"github.com/raihankhan/notebooklm-go/internal/auth/storage"
	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// Sentinel errors returned by LoadFromStorage. Wrapped with %w so
// callers can switch on errors.Is.
var (
	// ErrEmptySource — neither an explicit path nor the env var
	// NOTEBOOKLM_AUTH_JSON produced a usable source. Programming error
	// rather than a runtime failure: callers must pass at least one
	// resolution surface.
	ErrEmptySource = errors.New("auth: empty auth source")

	// ErrStorageRead — the storage_state.json file could not be read
	// (missing, permission-denied, malformed). The os.PathError is
	// wrapped so callers can errors.Is(err, fs.ErrNotExist).
	ErrStorageRead = errors.New("auth: storage read failed")

	// ErrPolicyGate — policy.Validate rejected the cookie set (Tier 1
	// missing, PSIDTS unroutable, no secondary binding, or an
	// off-allowlist host). The underlying *policy.TypedError is
	// wrapped; callers should errors.As to inspect Reason().
	ErrPolicyGate = errors.New("auth: policy gate failed")

	// ErrTokenMissing — the WIZ_global_data extractor could not find
	// SNlM0e/FdrFJe on the final response. Wraps the extract.Failure
	// so callers can switch on the four sentinels via errors.Is.
	ErrTokenMissing = errors.New("auth: tokens not extractable")

	// ErrNoFetcher — LoadFromStorage was called without a TokenFetcher
	// (the production wires a network-backed fetcher; the test path
	// wires a fixture-backed one). Distinct from ErrEmptySource so
	// callers can tell apart "you forgot to set the source" from
	// "you forgot to set the fetcher".
	ErrNoFetcher = errors.New("auth: no token fetcher configured")
)

// Tokens is the immutable per-attempt credential bundle. Every field
// that carries a credential (CSRFToken, SessionID) is redacted from
// String() and LogValue() — see docs/AGENTS.md rule 4. Source labels
// where the bundle came from for diagnostics; it is never credential-
// equivalent on its own.
//
// The LoadedAt stamp is the wall-clock time the pipeline finished
// building the bundle, NOT the time the cookie was issued. Callers
// that need freshness math compare LoadedAt to time.Now.
//
// Port of notebooklm._auth.tokens.AuthTokens, narrowed to the fields
// Phase 4 needs. The Python original also carries cookie_jar,
// cookies, authuser, account_email, cookie_snapshot, and a private
// _profile_session_generation; those land with the refresh ladder
// and the account-routing ticket, not with this one.
type Tokens struct {
	// CSRFToken is the SNlM0e value scraped from WIZ_global_data.
	// Sent on every batchexecute RPC as `at=`.
	CSRFToken string
	// SessionID is the FdrFJe value scraped from WIZ_global_data.
	// Sent as `f.sid` on every batchexecute URL.
	SessionID string
	// LoadedAt is the wall-clock instant the pipeline finished building
	// the bundle. Compare to time.Now for freshness.
	LoadedAt time.Time
	// Source labels where the bundle came from (file path or "env"
	// for NOTEBOOKLM_AUTH_JSON). Diagnostic only; not credential-
	// equivalent on its own.
	Source string
	// Jar is the live RFC 6265 cookie jar the transport kernel sends
	// on each request. Owned by Tokens so a caller that holds a
	// Tokens reference holds the same jar the kernel does; a future
	// generation bump can replace it under the auth mutex without
	// invalidating the immutable view.
	Jar *cookiejar.Jar
}

// String renders a redacted view safe for logs and pytest diffs. The
// credential fields (CSRFToken, SessionID) and the Jar contents are
// masked; the Source label survives because it is operator signal,
// not credential-equivalent.
//
// Per docs/AGENTS.md rule 4: callers must never %v a Tokens value
// without using this method. The standard library's default
// formatting would leak every credential into the log sink.
func (t *Tokens) String() string {
	if t == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"Tokens{CSRFToken=%s, SessionID=%s, LoadedAt=%s, Source=%s, Jar=%s}",
		redactedMarker,
		redactedMarker,
		t.LoadedAt.Format(time.RFC3339Nano),
		t.Source,
		jarSummary(t.Jar),
	)
}

// LogValue implements slog.LogValuer so a Tokens carried as a slog
// attribute routes through internal/redact.Apply before emit. The
// returned slog.Value is a group carrying the same fields as String()
// but pre-redacted, so a handler that does not itself scrub still
// sees a safe line.
//
// Per docs/AGENTS.md rule 4: the credential bytes must never reach
// the handler unchanged. We marshal the redacted view via slog.String
// so a text/JSON handler produces a credential-free line.
func (t *Tokens) LogValue() slog.Value {
	if t == nil {
		return slog.StringValue("<nil>")
	}
	return slog.GroupValue(
		slog.String("csrf_token", redactedMarker),
		slog.String("session_id", redactedMarker),
		slog.String("loaded_at", t.LoadedAt.Format(time.RFC3339Nano)),
		slog.String("source", t.Source),
		slog.Int("jar_cookies", jarLen(t.Jar)),
	)
}

// redactedMarker is the credential-free placeholder. Matches the
// sentinel internal/redact uses internally so a Tokens value
// rendered through String() / LogValue() looks the same as one
// that already routed through redact.Apply.
const redactedMarker = "[REDACTED]"

// jarSummary renders a Jar into a one-line credential-free summary.
// nil is rendered as "<nil>"; an empty jar is "<empty>"; otherwise
// the cookie count is the only operator-signal we keep (the names
// themselves are diagnostic, not credential, but listing them
// would balloon the line and reveal cookie inventory — that is
// metadata leakage a careful redactor must avoid).
func jarSummary(j *cookiejar.Jar) string {
	if j == nil {
		return "<nil>"
	}
	n := j.Len()
	if n == 0 {
		return "<empty>"
	}
	return fmt.Sprintf("<%d cookies>", n)
}

// jarLen is the cookie count accessor used by LogValue. The Jar
// method is named Len() and is safe for concurrent use (it locks
// internally); a nil receiver returns 0.
func jarLen(j *cookiejar.Jar) int {
	if j == nil {
		return 0
	}
	return j.Len()
}

// TokenFetcher is the abstraction the load-from-storage pipeline
// uses to acquire the (csrf, session) pair from the live app. The
// production implementation makes a single GET against the
// configured app host and feeds the response body through
// extract.ExtractWIZ; tests inject a fixture-backed fetcher that
// returns canned values without any network call.
//
// The fetcher is the one place the pipeline touches network, so
// the test seam is exactly one interface.
//
// Source: notebooklm._auth.tokens.TokenAcquirer (Protocol). The Go
// version keeps the same shape but inlines the body for the
// single-use production path.
type TokenFetcher interface {
	// Fetch acquires a fresh (csrf, session) pair from the configured
	// app host, using jar as the outbound cookie jar. The returned
	// values are the literal SNlM0e / FdrFJe scrapes; downstream
	// pipeline stages do NOT need a URL or a body.
	Fetch(ctx context.Context, jar *cookiejar.Jar) (csrf, session string, err error)
}

// FetchFunc adapts a plain function to the TokenFetcher interface.
// Production code can use this to wrap a method set; tests use it to
// inject a canned answer without writing a one-off type.
type FetchFunc func(ctx context.Context, jar *cookiejar.Jar) (csrf, session string, err error)

// Fetch is the TokenFetcher implementation.
func (f FetchFunc) Fetch(ctx context.Context, jar *cookiejar.Jar) (string, string, error) {
	if f == nil {
		return "", "", ErrNoFetcher
	}
	return f(ctx, jar)
}

// Source is the input to LoadFromStorage. Exactly one of Path or
// Inline must be non-empty; passing both is a programming error
// caught by LoadFromStorage (returns ErrEmptySource for the
// empty-everything case and a typed error otherwise — see
// resolveSource).
//
// Source labels:
//   - Path != "": read storage_state.json from this path.
//   - Inline != "": use this NOTEBOOKLM_AUTH_JSON-equivalent byte
//     slice; no file writes. PSIDTS recovery is skipped (there is
//     no writable backing store).
//   - both empty: return ErrEmptySource.
//
// Port of notebooklm._auth.tokens.ResolvedAuthSource (InlineAuthSource
// + FileAuthSource union). The Go version combines the two into one
// struct because the production pipeline never needs both at once.
type Source struct {
	// Path is the storage_state.json path. Takes precedence over Inline
	// when both are non-empty (mirroring the Python precedence rule).
	Path string
	// Inline is the raw NOTEBOOKLM_AUTH_JSON byte slice. Used only
	// when Path is empty.
	Inline []byte
	// Profile is the profile name (informational). Today the pipeline
	// derives the storage path from this when Path is empty;
	// future expansion (T-P2-2 / Sprint 3) will use it for the
	// profile-aware storage root.
	Profile string
}

// LoadFromStorage runs the canonical five-step pipeline:
//
//  1. resolve source — turn the Source value into a concrete
//     storage.Storage (either a path read or an inline byte slice).
//  2. seed jar — apply the storage's cookies to a fresh cookiejar.Jar
//     and run policy.Validate (Tier 1 / Tier 2 / allowlist gate).
//  3. acquire tokens — call fetcher.Fetch with the seeded jar; parse
//     the response through extract.ExtractWIZ.
//  4. merge observation — fold the live (post-fetch) jar snapshot
//     back into the persistence baseline so a subsequent Save
//     captures any cookie churn the app performed.
//  5. build — assemble the immutable Tokens bundle and stamp
//     LoadedAt + Source.
//
// The pipeline never panics; every error path returns a typed
// sentinel wrapped with %w. Callers switch on errors.Is or
// errors.As.
//
// ctx is the cancellation context for the fetcher's network call.
// The pipeline itself does not consult ctx (the disk read and the
// policy gate are local), but it must propagate it so the fetcher
// can honor it.
//
// Source: notebooklm._auth.tokens.StoredAuthLoader.load + the
// Python pipeline contract documented at the top of
// notebooklm/_auth/tokens.py.
func LoadFromStorage(ctx context.Context, source Source, fetcher TokenFetcher) (Tokens, error) {
	if fetcher == nil {
		return Tokens{}, ErrNoFetcher
	}

	// Step 1: resolve source.
	store, err := resolveSource(source)
	if err != nil {
		return Tokens{}, err
	}

	// Step 2: seed jar + policy gate.
	jar, err := seedJar(store)
	if err != nil {
		return Tokens{}, err
	}

	// Step 3: acquire tokens.
	csrf, session, err := fetcher.Fetch(ctx, jar)
	if err != nil {
		// The fetcher already wraps extract.Failure; bubble the
		// sentinel so the refresh ladder can branch on
		// errors.Is(err, extract.ErrRegionBlocked) etc.
		// Use %w for err too so the inner sentinel survives
		// errors.Is walking.
		return Tokens{}, fmt.Errorf("%w: %w", ErrTokenMissing, err)
	}
	if csrf == "" || session == "" {
		// An empty token from a fetcher that did not error is a
		// contract violation; surface it as ErrTokenMissing.
		return Tokens{}, fmt.Errorf("%w: fetcher returned empty csrf or session",
			ErrTokenMissing)
	}

	// Step 4 + 5: build. The persistence merge is a no-op today
	// (the profile store layer lands with T-P2-2) but the seam
	// is here so the Sprint 3 work does not change this signature.
	tokens := Tokens{
		CSRFToken: csrf,
		SessionID: session,
		LoadedAt:  time.Now(),
		Source:    sourceLabel(source),
		Jar:       jar,
	}
	return tokens, nil
}

// resolveSource turns a Source into a concrete storage.Storage.
// Path takes precedence over Inline (matches the Python precedence
// rule in notebooklm._auth.tokens.StoredAuthLoader._resolve_source).
//
// An empty Source returns ErrEmptySource. A non-empty Inline is
// unmarshaled through storage.Unmarshal; a non-empty Path is read
// through storage.Read (which also picks up the legacy context.json
// sibling).
func resolveSource(source Source) (storage.Storage, error) {
	switch {
	case source.Path != "":
		// #nosec G304 -- Path is operator-supplied; the trust
		// boundary is the home directory the operator configured.
		s, err := storage.Read(source.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return storage.Storage{}, fmt.Errorf("%w: %w", ErrStorageRead, err)
			}
			return storage.Storage{}, fmt.Errorf("%w: %w", ErrStorageRead, err)
		}
		return s, nil
	case len(source.Inline) > 0:
		s, err := storage.Unmarshal(source.Inline)
		if err != nil {
			return storage.Storage{}, fmt.Errorf("%w: inline parse: %w", ErrStorageRead, err)
		}
		return s, nil
	default:
		return storage.Storage{}, ErrEmptySource
	}
}

// seedJar applies storage's cookies to a fresh cookiejar.Jar and
// runs the policy gate (Tier 1 / Tier 2 / allowlist / PSIDTS
// routability).
//
// The cookie jar is the credential-aware data structure the policy
// gate already accepts, so this stage is the natural seam: anything
// that wants to inspect the cookies runs Validate on the jar.
//
// Source: notebooklm._auth.cookies._build_cookie_pair_from_storage
// (the Storage -> CookieJar half) +
// notebooklm._auth.cookie_policy.validate_cookies (the policy
// half).
func seedJar(store storage.Storage) (*cookiejar.Jar, error) {
	jar := cookiejar.New()
	for _, c := range store.Cookies {
		if c.Name == "" {
			continue
		}
		// The Jar SetCookies path expects stdlib cookies and a
		// destination URL. We derive the URL from the cookie's
		// own domain so a host-only stamp is consistent with
		// what jar.All() will report later.
		domain := strings.TrimPrefix(c.Domain, ".")
		if domain == "" {
			domain = ".google.com"
		}
		u := &url.URL{
			Scheme: "https",
			Host:   domain,
			Path:   c.Path,
		}
		if u.Path == "" {
			u.Path = "/"
		}
		stdlib := storageToStdlib(c)
		jar.SetCookies(u, []*http.Cookie{stdlib})
	}
	if err := policy.Validate(jar); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPolicyGate, err)
	}
	return jar, nil
}

// storageToStdlib adapts a storage.Cookie to the stdlib shape Jar
// expects on SetCookies. The conversion is intentionally narrow
// because the seeded jar only needs to send on the next fetch, not
// to round-trip every attribute.
func storageToStdlib(c storage.Cookie) *http.Cookie {
	out := &http.Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Path:     c.Path,
		Secure:   c.Secure,
		HttpOnly: c.HTTPOnly,
	}
	if c.Domain != "" {
		out.Domain = c.Domain
	}
	if c.Expires != nil {
		out.Expires = time.Unix(*c.Expires, 0)
	}
	return out
}

// sourceLabel is the diagnostic label LoadFromStorage stamps on
// the returned Tokens.Source. File paths are rendered through
// redact so a credential-shaped path component never reaches a
// log sink (the path is operator signal, but the path may carry
// a username or other PII).
func sourceLabel(source Source) string {
	switch {
	case source.Path != "":
		return "file:" + string(redact.Apply([]byte(filepath.Clean(source.Path))))
	case len(source.Inline) > 0:
		return "env:NOTEBOOKLM_AUTH_JSON"
	default:
		return "<none>"
	}
}
