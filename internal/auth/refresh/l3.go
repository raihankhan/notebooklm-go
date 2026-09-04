// Package refresh: the L3 ladder rung — headless re-auth against
// accounts.google.com.
//
// L3 is the "headless browser re-mint" rung documented in docs/05-auth.md
// §"The refresh ladder". When the cookies have aged out beyond what
// L2.0 / L2.5 can repair, L3 drives a headless-mint flow against the
// canonical Google sign-in surface (accounts.google.com) using a
// profile-derived cookie jar and re-extracts SNlM0e / FdrFJe from the
// resulting page body.
//
// In T-S3-001c the rung is delivered as a minimal transport-shaped
// function: ReloadL3 issues the mint POST against a typed L3MintClient
// transport, follows the redirect chain, rejects off-allowlist hops,
// and parses the final body. The full Playwright / chromedp browser
// orchestration wires in via DefaultLadder.Step in T-S3-001d; this
// rung deliberately exposes a small L3MintClient interface so the
// ladder-wiring ticket can inject the production browser-backed
// implementation without changing the rung's signature.
//
// The contract:
//
//	tokens, err := refresh.ReloadL3(ctx, l3Client, logger)
//
// l3Client is the typed transport the rung consumes. The interface
// exposes only the one method the rung needs (Mint issues the POST,
// follows any 302/303 chain, rejects off-allowlist hops) so a fake
// test double satisfies it without dragging a browser into the unit
// suite. The interface is intentionally named L3MintClient (NOT
// "Storage" — see the W1a collision lesson in
// docs/sprint-reports/2026-09-04-w1a-merged.md): L3's read surface is
// "issue a POST and parse a response", which is a different shape
// from L2.0's "read storage_state.json from disk". Two parallel
// agents in the same package that independently re-declare a type
// named "Storage" collide at vet time; the L3-prefixed name is the
// fix from W1a's fixup commit.
//
// logger is any function-shaped slog receiver (mirrors the L2.5
// surface). nil drops every line.
//
// Behavior (per docs/sprint-reports/S03-split-tickets.md §"T-S3-001c"):
//
//  1. Issues a headless mint against accounts.google.com (the test
//     harness stands up a fake server that mimics that surface).
//
//  2. On 302/303 to a non-allowed host: returns
//     ErrReloadL3AuthRedirect (typed sentinel).
//
//  3. On missing CSRF / SessionID in the response body: returns
//     ErrReloadL3TokenMissing (typed sentinel).
//
//  4. Returns Tokens with CSRF + SessionID populated and
//     Backend = BackendStorageFile (the canonical "loaded from the
//     persistent profile" descriptor — L3 re-uses the file-backed
//     descriptor because in T-S3-001d the production caller reads
//     cookies via the same Storage L2.0 uses).
//
// Boundary: per docs/AGENTS.md rule 5, this file is part of the
// mode=internal package; it imports stdlib + internal/redact only.
//
// docs/AGENTIC_LOOP.md §3.2 — T-S3-001c scope. Refs T-S3-001.
package refresh

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// ErrReloadL3AuthRedirect is the typed sentinel ReloadL3 returns
// when the accounts.google.com mint POST issues a 302/303 to a host
// that is not on the auth-path allowlist
// (accounts.google.com / oauth2.googleapis.com /
// oauth2.googleusercontent.com — see authPathRedactHostsL3 below).
//
// Callers detect it with errors.Is and decide whether to surface a
// "your session belongs to a different account" diagnostic or
// escalate to L4 (master-token re-mint). It is distinct from
// L2.0's exhausted sentinel and L2.5's not-configured /
// not-mid-session sentinels so a future refactor that silently
// merges them fails the sentinel-distinctness tests in l2_5_test
// and l3_test.
var ErrReloadL3AuthRedirect = errors.New("refresh: L3 accounts.google.com mint redirected off-allowlist")

// ErrReloadL3TokenMissing is the typed sentinel ReloadL3 returns
// when the mint response body does not carry extractable SNlM0e or
// FdrFJe values. The body shape is otherwise legitimate (no
// redirect, no transport error) — this is a page-structure problem
// worth filing a bug about, not a transient retry signal.
//
// Like ErrReloadL3AuthRedirect this sentinel is distinct from the
// other rungs' sentinels.
var ErrReloadL3TokenMissing = errors.New("refresh: L3 mint response missing CSRF or SessionID")

// l3MaxAttempts caps the bounded-attempts loop. Pinned at 2 (NOT 3
// like L2.0; NOT 1) so the contract stays distinct from its sibling
// rungs: L3's bounded retry covers a transient POST hiccup (the
// accounts.google.com surface is a stable endpoint; a flaked POST
// should be re-issued once) without burning through the
// auth-redirect budget if the underlying account is actually
// rejected at the cookie-scoping layer.
const l3MaxAttempts = 2

// l3BaseBackoff is the small wait between the two attempts of the
// bounded loop. Kept short because the accounts.google.com surface
// is a stable endpoint — a transient hiccup usually resolves
// within a fraction of a second.
const l3BaseBackoff = 100 * time.Millisecond

// csrfKeyL3 / sessionKeyL3 are the WIZ_global_data keys L3
// expects. They mirror internal/auth/extract/wiz.go's
// csrfField / sessionField constants; inlined here so the rung
// does not pull a transitive dependency on internal/auth/extract
// (that wiring is T-S3-001d's concern).
const (
	csrfKeyL3    = "SNlM0e"
	sessionKeyL3 = "FdrFJe"
)

// csrfPatternsL3 / sessionPatternsL3 are the pre-compiled regex
// patterns tried in extractCSRFAndSession. Built once at init time
// from wizFieldPatternsL3 to avoid the per-call compile cost.
//
// The pattern set mirrors
// internal/auth/extract/wiz.go::wizFieldPatterns: canonical
// double-quoted first (the common case), then single-quoted
// (rare, observed in some debug renders), then HTML-escaped (when
// the script block is rendered inside an attribute or escaped
// fragment).
var (
	csrfPatternsL3    = wizFieldPatternsL3(csrfKeyL3)
	sessionPatternsL3 = wizFieldPatternsL3(sessionKeyL3)
)

// wizFieldPatternsL3 returns the ordered list of regex patterns
// tried for one WIZ_global_data key. The order is load-bearing:
// canonical double-quoted first (the common case), then
// single-quoted (rare), then HTML-escaped (rare; observed when
// the script block is rendered inside an attribute).
//
// Source: notebooklm._auth.extraction._build_wiz_field_patterns;
// mirrored verbatim from internal/auth/extract/wiz.go.
func wizFieldPatternsL3(key string) []*regexp.Regexp {
	escaped := regexp.QuoteMeta(key)
	return []*regexp.Regexp{
		regexp.MustCompile(`"` + escaped + `"\s*:\s*"([^"\\]*(?:\\.[^"\\]*)*)"`),
		regexp.MustCompile(`'` + escaped + `'\s*:\s*'([^'\\]*(?:\\.[^'\\]*)*)'`),
		regexp.MustCompile(`&quot;` + escaped + `&quot;\s*:\s*&quot;([^&]*?)&quot;`),
	}
}

// l3LoggerFunc is the minimal log sink the L3 rung uses for
// progress / failure breadcrumbs. Mirrors L2.5's loggerFunc so the
// ladder-wiring ticket (T-S3-001d) can pass one logger interface
// uniformly across rungs.
type l3LoggerFunc func(msg string, attrs ...any)

// nopLoggerL3 is the default no-op logger. Defining a sentinel
// (rather than a nil-check at every call site) lets a test swap
// it out with a recorder while keeping the package-internal
// default allocation-free.
var nopLoggerL3 l3LoggerFunc = func(string, ...any) {}

// L3MintClient is the transport surface the L3 rung consumes.
// The interface is deliberately small — just one operation
// (Mint) — so a fake test double satisfies it without dragging
// a browser into the unit suite.
//
// Naming: this is the L3-specific interface; it is NOT a generic
// "Storage" re-declaration in the same package (the W1a
// collision in docs/sprint-reports/2026-09-04-w1a-merged.md
// teaches that two parallel agents in the same package will
// independently pick the natural name and collide at vet time —
// so this type is named L3MintClient and the production caller
// passes its concrete browser-backed implementation as that
// interface).
type L3MintClient interface {
	// Mint issues the headless-mint POST against
	// accounts.google.com, follows any 302/303 chain
	// (rejecting non-allowed hosts with a typed error at
	// the boundary), and returns the final response body
	// and the final status code. A non-2xx final status
	// returns a non-nil error; the body is left empty.
	//
	// A 302/303 to a host that is not on the auth-path
	// allowlist (authPathRedactHostsL3) MUST surface
	// ErrReloadL3AuthRedirect directly so the rung can
	// pass it through unchanged. Other transport errors
	// surface verbatim and are wrapped by the rung.
	Mint(ctx context.Context) (body []byte, statusCode int, err error)
}

// ReloadL3 issues a headless-mint POST against accounts.google.com
// and parses the response body into Tokens. See package docstring
// for the full contract.
//
// On success the returned Tokens has CSRF + SessionID populated
// from the response body, Backend = BackendStorageFile, and
// FetchedAt = wall-clock time of the successful parse. Cookies /
// AuthUser / AccountEmail are zero — those fields are owned by
// L1 / L2.0, not the L3 rung; the ladder wiring in T-S3-001d
// merges the L3 result with the L2.0 storage view to assemble a
// complete Tokens value.
//
// logger is the structured-log sink; nil drops everything.
//
// Errors (all errors.Is-able):
//
//   - context.Canceled / context.DeadlineExceeded — ctx was
//     canceled mid-flight.
//   - ErrReloadL3AuthRedirect — the chain left the auth-path
//     allowlist; the wrapped error carries the rejected
//     location's safeURL redacted through internal/redact.
//   - ErrReloadL3TokenMissing — the body did not carry an
//     extractable SNlM0e or FdrFJe.
//   - any transport error from the L3MintClient — wrapped
//     verbatim in ErrReloadL3TokenMissing so callers can
//     errors.As if the client exposes a typed failure.
//
// The function is safe for concurrent use; the only shared
// mutable state is the regex compilation, which is read-only
// after package init.
func ReloadL3(ctx context.Context, client L3MintClient, logger l3LoggerFunc) (Tokens, error) {
	if err := ctx.Err(); err != nil {
		return Tokens{}, err
	}
	if client == nil {
		return Tokens{}, fmt.Errorf("refresh: L3 client is nil")
	}
	if logger == nil {
		logger = nopLoggerL3
	}

	logger("refresh: L3 headless mint firing")

	var lastErr error
	for attempt := 1; attempt <= l3MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return Tokens{}, fmt.Errorf("%w: %w (after attempt %d)", ErrReloadL3TokenMissing, lastErr, attempt-1)
			}
			return Tokens{}, err
		}

		body, status, err := client.Mint(ctx)
		if err != nil {
			// Auth-redirect errors short-circuit
			// immediately: re-trying a redirected
			// mint is not the right move — the cookie
			// scoping is wrong. The error message is
			// routed through internal/redact before
			// surfacing.
			if errors.Is(err, ErrReloadL3AuthRedirect) {
				return Tokens{}, fmt.Errorf("%w: %s", ErrReloadL3AuthRedirect, redact.Apply([]byte(err.Error())))
			}
			lastErr = err
			logger("refresh: L3 attempt failed",
				"attempt", attempt,
				"status", status,
				"error", err.Error(),
			)
			// Backoff between attempts, honoring
			// ctx along the way so a canceled
			// deadline still aborts the loop
			// promptly.
			if attempt < l3MaxAttempts {
				select {
				case <-ctx.Done():
					if lastErr != nil {
						return Tokens{}, fmt.Errorf("%w: %w", ErrReloadL3TokenMissing, lastErr)
					}
					return Tokens{}, ctx.Err()
				case <-time.After(l3BaseBackoff):
				}
			}
			continue
		}

		// Success-side: parse the response body for
		// the SNlM0e / FdrFJe pair. The extraction
		// lives in the rung (rather than the
		// L3MintClient) because the rung's contract
		// is "return Tokens"; pushing the regex into
		// every client impl would let them drift.
		csrf, session, extractErr := extractCSRFAndSession(body)
		if extractErr != nil {
			lastErr = extractErr
			logger("refresh: L3 body extraction failed",
				"attempt", attempt,
				"status", status,
			)
			if attempt < l3MaxAttempts {
				select {
				case <-ctx.Done():
					return Tokens{}, fmt.Errorf("%w: %w", ErrReloadL3TokenMissing, lastErr)
				case <-time.After(l3BaseBackoff):
				}
			}
			continue
		}

		logger("refresh: L3 headless mint succeeded",
			"attempt", attempt,
			"status", status,
		)
		return Tokens{
			CSRF:      csrf,
			SessionID: session,
			Backend:   BackendStorageFile,
			FetchedAt: time.Now().UTC(),
		}, nil
	}

	// The loop completes only on failure — both
	// attempts returned err. Surface the last
	// attempt's error wrapped in
	// ErrReloadL3TokenMissing so callers can branch
	// on it via errors.Is. The wrapped cause is
	// preserved so a caller can errors.As or
	// errors.Unwrap to recover the underlying
	// transport detail.
	return Tokens{}, fmt.Errorf("%w: %w", ErrReloadL3TokenMissing, lastErr)
}

// extractCSRFAndSession pulls SNlM0e and FdrFJe out of the
// response body. Both fields are extracted independently: a
// body carrying SNlM0e but not FdrFJe still returns
// (csrf, "") so the rung's caller can decide whether the
// missing field is worth surfacing.
//
// On any missing field the function returns a typed error
// carrying the concrete missing-key list so the rung can wrap
// it in ErrReloadL3TokenMissing without losing the diagnostic.
//
// Source: ported from internal/auth/extract/wiz.go. The
// pattern set is the same; only the dispatch site moved.
func extractCSRFAndSession(body []byte) (string, string, error) {
	csrf, csrfFound := extractFieldFromBytes(body, csrfPatternsL3)
	if !csrfFound {
		csrf = ""
	}
	session, sessionFound := extractFieldFromBytes(body, sessionPatternsL3)
	if !sessionFound {
		session = ""
	}
	if csrfFound && sessionFound {
		return csrf, session, nil
	}
	var missing []string
	if !csrfFound {
		missing = append(missing, "SNlM0e")
	}
	if !sessionFound {
		missing = append(missing, "FdrFJe")
	}
	return "", "", &extractFailure{missing: missing}
}

// extractFieldFromBytes tries the ordered pattern list
// against body and returns the first match. The boolean is
// false when no pattern matched; the value is the empty
// string in that case. An empty capture group is a legitimate
// answer (some Google endpoints emit empty tokens), so the
// boolean is the only signal of presence.
//
// Source: ported from internal/auth/extract/wiz.go.
func extractFieldFromBytes(body []byte, patterns []*regexp.Regexp) (string, bool) {
	for _, re := range patterns {
		m := re.FindSubmatch(body)
		if m == nil {
			continue
		}
		if len(m) < 2 {
			return "", false
		}
		return string(m[1]), true
	}
	return "", false
}

// extractFailure carries the missing-key list off the rung's
// hot path. The type is unexported because the L3 rung is the
// only caller; the missing-key diagnostic surfaces through
// Error() and the wrapped ErrReloadL3TokenMissing sentinel.
type extractFailure struct {
	missing []string
}

// Error renders the missing-key list. The list is routed
// through redact.Apply so a body that echoed a credential-
// shaped substring in its surrounding HTML does not leak
// through the rung's error message.
func (e *extractFailure) Error() string {
	if e == nil {
		return ""
	}
	msg := "missing keys: " + strings.Join(e.missing, ",")
	return string(redact.Apply([]byte(msg)))
}

// authPathRedactHostsL3 is the closed list of hosts the L3
// rung considers safe to follow a 302/303 redirect to.
// Mirrors internal/auth/extract/failure.go::authPathRedactHosts.
// Kept locally because pulling the extract package into l3.go
// would force dependency ordering decisions that are
// T-S3-001d's concern.
var authPathRedactHostsL3 = []string{
	"accounts.google.com",
	"oauth2.googleapis.com",
	"oauth2.googleusercontent.com",
}

// isAllowedAuthHostL3 returns true when host is on the
// auth-path allowlist, exactly or as a subdomain. Mirrors
// internal/auth/extract/failure.go::isAuthHost so the L3 rung
// rejects off-allowlist redirects at the boundary before they
// can contaminate the cookie jar / follow chain.
func isAllowedAuthHostL3(host string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(host)
	for _, h := range authPathRedactHostsL3 {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// safeURLL3 strips credential-shaped parts of a URL for
// error display. Mirrors
// internal/auth/extract/failure.go::safeURL. Kept here for the
// same reason as authPathRedactHostsL3.
//
// The path component is redacted to "/<redacted>" when host is
// on the auth-path allowlist (the path on
// accounts.google.com/OAuthLogin can carry an opaque token
// that must never reach a log line).
func safeURLL3(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return raw
	}
	host := strings.ToLower(u.Hostname())
	netloc := host
	if port := u.Port(); port != "" {
		netloc = host + ":" + port
	}
	path := u.EscapedPath()
	if path != "" && path != "/" && isAllowedAuthHostL3(host) {
		path = "/<redacted>"
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + netloc + path
}
