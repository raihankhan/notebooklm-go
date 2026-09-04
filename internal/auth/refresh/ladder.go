// Package refresh owns the auth-refresh ladder described in
// docs/05-auth.md "The refresh ladder". The ladder has five rungs:
//
//	L1   Token refresh — read profile, seed jar, return Tokens
//	L2.0 Storage cookie reload — sibling-process disk sample
//	L2.5 NOTEBOOKLM_REFRESH_CMD — operator-supplied refresh command
//	L3   Headless re-auth — drive a browser against the persistent profile
//	L4   Master-token re-mint — re-mint from the durable token
//
// Only L1 is wired in Phase 4 (T-P4-2). L2/L3/L4 are stubbed at the
// Ladder interface level: Step(ctx, level) returns
// ErrLadderLevelNotImplemented for every level except L1, so a
// future ticket can drop in implementations without touching the
// callers.
//
// Tokens is the typed value the ladder returns. It is deliberately
// minimal in Phase 4 — CSRF and SessionID are populated only when a
// refresh actually fetched them; the L1 reload path leaves them
// empty because storage_state.json does not persist the CSRF. The
// Sprint-3 fetcher ladder will populate them.
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal;
// it imports stdlib + internal/auth/cookiejar + internal/auth/profile
// only.
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"
)

// Level is the typed ladder rung. The integer values are stable across
// the module (and across the Python original's ladder dictionary);
// callers index by Level, not by ordinal, to keep the wire shape
// inspectable in debug output.
type Level int

// The five ladder levels. Sprint 3 (T-P4-3 / T-P4-4) fills in L2 —
// L4; the names deliberately spell the half-steps ("L2_0", "L2_5")
// the way docs/05-auth.md does, so log lines stay readable.
const (
	// L1 reloads the cookies from on-disk storage and seeds the
	// in-memory jar. This is the rung Phase 4 wires.
	L1 Level = iota + 1
	// L2_0 re-reads the storage_state.json under a bounded
	// bounded-attempt counter — covering the sibling-process
	// case where another CLI invocation refreshed the same
	// profile while we were waiting. Sprint 3.
	L2_0
	// L2_5 runs an operator-supplied
	// NOTEBOOKLM_REFRESH_CMD. Mid-session use is gated behind
	// NOTEBOOKLM_REFRESH_CMD_MIDSESSION=1. Sprint 3.
	L2_5
	// L3 drives a headless browser against the persistent
	// profile to silently re-mint. Opt-in only. Sprint 3.
	L3
	// L4 re-mints from the durable master_token.json beside
	// the profile. Automatic when the master token is
	// present. Sprint 3.
	L4
)

// String returns the canonical "L1", "L2_0", … label. A label never
// carries credential material, so it is safe to surface in log
// output.
func (l Level) String() string {
	switch l {
	case L1:
		return "L1"
	case L2_0:
		return "L2_0"
	case L2_5:
		return "L2_5"
	case L3:
		return "L3"
	case L4:
		return "L4"
	default:
		return fmt.Sprintf("L?(%d)", int(l))
	}
}

// ErrLadderLevelNotImplemented is the sentinel the Ladder.Step
// implementation returns for every level except L1 in Phase 4. A
// caller that reaches it can errors.Is to detect "this ladder rung
// has not been wired yet" and either fall back to a different rung
// or surface a typed "not yet implemented" error to the user.
//
// The sprint-3 implementation removes the sentinel from the
// non-L1 levels; L1 stays wired.
var ErrLadderLevelNotImplemented = errors.New("refresh: ladder level not implemented")

// Tokens is the typed value the L1 rung returns. Phase 4 stores
// only what the L1 reload path can recover without a network call:
// the in-memory cookie snapshot, the in-band account identity
// (authuser, email), the backend descriptor, and — to keep Phase-5
// compatibility with the public Client — a token-fetched timestamp
// describing when the ladder last visited the storage.
//
// CSRF and SessionID are intentionally zero-value: storage_state.json
// does not persist them. A future Sprint (T-P4-3 / T-P4-4) introduces
// the network-fetched Tokens shape that populates them.
//
// Per docs/AGENTS.md rule 4 every Tokens value has a redacting
// String() method that routes through internal/redact's marker so
// a log line never prints the literal cookies.
type Tokens struct {
	// Cookies is the cookie set the L1 reload recovered from
	// the on-disk profile. The slice is freshly allocated and
	// safe for the caller to retain independently of the
	// underlying profile.
	Cookies []CookieView
	// CSRF is the SNlM0e token. Empty after the L1 reload —
	// the disk does not persist this. Populated only after a
	// successful L2.5 / L3 / L4 network fetch (Sprint 3).
	CSRF string
	// SessionID is the FdrFJe value. Same contract as CSRF.
	SessionID string
	// AuthUser is the routing account index from the in-band
	// notebooklm.account metadata. Defaults to 0.
	AuthUser int
	// AccountEmail is the routing email from the in-band
	// notebooklm.account metadata. Empty when unknown.
	AccountEmail string
	// Backend is the typed backend the profile was loaded
	// through.
	Backend Backend
	// FetchedAt is the wall-clock time the ladder last
	// visited the storage. Zero after the constructor; the
	// ladder sets it on every successful Step.
	FetchedAt time.Time
}

// CookieView is the redacted cookie snapshot the ladder carries on
// the Tokens value. It carries Name, Domain, and a redacting
// Accessor for the value — never the literal value via a public
// field — so a Tokens value that reaches a log sink cannot leak the
// underlying credential.
//
// The Cookie type lives in internal/auth/profile and exposes the
// literal Value field for transport code that needs to build a
// Cookie: header. Tokens.Cookies is the LOG-SAFE projection; the
// transport builds its request headers from the underlying profile
// cookies, not from Tokens.
type CookieView struct {
	Name   string
	Domain string
	Path   string
}

// redactedMarker is the byte string substituted for credential
// material in Tokens.String(). It mirrors internal/cookiejar and
// internal/redact so the redact-marker convention is consistent
// across the module.
const redactedMarker = "[REDACTED]"

// String returns the redacted, log-safe representation of Tokens.
// The cookie VALUES never appear; the count and the names are kept
// so a log line remains useful in -vv output. CSRF and SessionID
// collapse to the standard redact marker.
func (t Tokens) String() string {
	fetchedAt := "zero"
	if !t.FetchedAt.IsZero() {
		fetchedAt = t.FetchedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"Tokens(backend=%s csrf=%s session=%s authuser=%d email=%q cookies=%d fetched_at=%s)",
		t.Backend, redactedMarker, redactedMarker, t.AuthUser, t.AccountEmail, len(t.Cookies), fetchedAt,
	)
}

// Backend is the typed backend the Tokens was loaded through. It
// mirrors profile.Backend so a Tokens value carries the same
// backend descriptor as the source profile; Phase-4 only ever
// produces BackendStorageFile because L1 reads disk.
type Backend int

const (
	// BackendUnknown is the zero value; an uninitialized
	// Tokens carries it. The ladder sets Backend on every
	// successful Step.
	BackendUnknown Backend = iota
	// BackendStorageFile is the storage_state.json on-disk
	// path under ~/.notebooklm/profiles/<name>/.
	BackendStorageFile
	// BackendInlineEnv is NOTEBOOKLM_AUTH_JSON — no on-disk
	// profile, read at process start.
	BackendInlineEnv
)

// String returns the canonical label.
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

// Ladder is the read-shape contract the Phase-4 caller consumes.
// Sprint 3 wires the production implementation; T-P4-2 lands a
// minimal L1-only implementation behind this interface so callers
// can be coded against the closed contract today.
//
// Implementations must be safe for concurrent use: the production
// ladder runs as a singleflight.Group keyed by canonical storage
// path, with a per-path success epoch so a follower that arrives
// after a successful refresh returns immediately instead of
// triggering another refresh.
type Ladder interface {
	// Step runs the requested rung and returns the Tokens it
	// produced. The boolean result reports whether the rung
	// fired; a returning false Signals "this rung is not
	// applicable to the current request, try the next one".
	//
	// Errors:
	//   - ErrLadderLevelNotImplemented — the rung has not been
	//     wired yet (errors.Is(err, ErrLadderLevelNotImplemented)).
	//   - any context or IO error if the rung bailed before
	//     success.
	Step(ctx context.Context, level Level) (Tokens, bool, error)
}

// URLHint is an optional, structured hint the L1 reload can carry
// to the caller about where to drive the first retry GET against.
// Phase 4 leaves it zero — the L1 path does not issue a network
// call — but the shape is here so Sprint 3 can populate it without
// changing Ladder.
type URLHint struct {
	// Base is the bare base URL of the request the caller
	// should retry (e.g. "https://notebooklm.google.com").
	Base *url.URL
	// AuthUser, when non-zero, is the routing account the
	// retry should pass via ?authuser=.
	AuthUser int
	// AccountEmail, when non-empty, is the stable routing
	// email the retry should pass via ?authuser=.
	AccountEmail string
}

// String returns the safe, log-friendly description of the hint.
// The URL is rendered through its Host (not the full string) so a
// long query string with embedded at= or authuser= tokens cannot
// leak into a log line.
func (h URLHint) String() string {
	host := "(no base)"
	if h.Base != nil {
		host = h.Base.Host
	}
	return fmt.Sprintf("URLHint(host=%s authuser=%d email=%q)", host, h.AuthUser, h.AccountEmail)
}

// WiredLadder is the Sprint-3 (T-S3-001d) ladder implementation
// that extends DefaultLadder with the L2.0, L2.5, and (when
// provided) L3 / L4 reloaders. It exists as a sibling type rather
// than a modification of DefaultLadder so the Phase-4 contract is
// preserved unchanged for callers that only need L1. The
// underlying L1 reload path is reused via the embedded
// *DefaultLadder — when DefaultLadder.Step is called with L1, the
// WiredLadder.Step method simply delegates, so the cookie-jar
// seeding and in-band account identity surfacing done by ReloadL1
// are identical between the two types.
//
// Dispatch table (T-S3-001d AC):
//
//   - L1   — delegate to DefaultLadder.Step (ReloadL1).
//   - L2_0 — call ReloadL2_0(ctx, w.L2Storage, w.L2Logger).
//     Returns ErrLadderLevelNotImplemented when w.L2Storage is
//     nil so a caller that wires only L1+L2.5 keeps working.
//   - L2_5 — call ReloadL2_5(ctx, w.L2_5Inline, w.L2_5Logger).
//     The rung itself returns its typed sentinels
//     (ErrReloadL2_5NotConfigured / ErrReloadL2_5NotMidSession)
//     when the operator has not configured the env vars, so the
//     dispatcher does not need to pre-check.
//   - L3   — call w.L3Reload when set; otherwise return
//     ErrLadderLevelNotImplemented. T-S3-001c will plug in
//     ReloadL3 by assigning w.L3Reload = ReloadL3 after that
//     ticket merges.
//   - L4   — call w.L4Reload when set; otherwise return
//     ErrLadderLevelNotImplemented. S02's mastertoken ticket will
//     plug in via w.L4Reload when it merges.
//
// The boolean result follows the Ladder contract: true when the
// rung fired and produced Tokens, false when the rung is not
// applicable (sentinel, missing prerequisite, or surface not
// wired yet). Errors that are not the ErrLadderLevelNotImplemented
// sentinel — context.Canceled, ErrReloadL2_0Exhausted,
// ErrReloadL2_5Exhausted, etc. — surface as (zero, false, err)
// so the caller decides whether to escalate to the next rung or
// surface the failure.
type WiredLadder struct {
	// DefaultLadder is the embedded L1 rung. Its Store, Jar,
	// Name, and Now fields are required for the L1 path;
	// anything left zero will surface a ReloadL1 typed error
	// on the first L1 Step call.
	*DefaultLadder

	// L2Storage is the read surface ReloadL2_0 consumes. When
	// nil, the L2.0 rung short-circuits to
	// ErrLadderLevelNotImplemented — the dispatcher never
	// invents a Storage from another field. Production wires a
	// *DiskStorage pointed at the canonical storage_state.json
	// path; tests inject a stub.
	L2Storage Storage
	// L2Logger is the *slog.Logger passed to ReloadL2_0. Nil
	// falls back to slog.Default inside ReloadL2_0 (the rung
	// itself handles a nil logger); WiredLadder.Step forwards
	// nil unchanged.
	L2Logger *slog.Logger

	// L2_5Inline is the minimal inline-reader passed to
	// ReloadL2_5. Optional: when nil, ReloadL2_5 still fires
	// the configured command (the InlineStorage is a
	// future-proofing seam, not a precondition).
	L2_5Inline InlineStorage
	// L2_5Logger is the function-shaped slog receiver passed
	// to ReloadL2_5. Nil falls back to ReloadL2_5's
	// built-in nopLogger; WiredLadder.Step forwards nil
	// unchanged.
	L2_5Logger loggerFunc

	// L3Reload is the forward-compat hook for T-S3-001c (L3
	// headless mint). When nil, Step returns
	// ErrLadderLevelNotImplemented for L3. When set, Step
	// calls w.L3Reload(ctx) and lifts the result into the
	// (Tokens, true, nil) shape. The field is named "Reload"
	// rather than "ReloadL3" so the l3.go implementation
	// file (when it lands) is the only place that needs to
	// own the symbol.
	L3Reload func(ctx context.Context) (Tokens, error)
	// L4Reload is the forward-compat hook for S02's L4
	// master-token work. When nil, Step returns
	// ErrLadderLevelNotImplemented for L4 — this is the ONLY
	// rung whose default surface is the sentinel, per the
	// T-S3-001d AC list.
	L4Reload func(ctx context.Context) (Tokens, error)
}

// WithStorage wires the L2.0 Storage and L2.5 InlineStorage into
// the ladder in a single call. It returns the receiver so
// callers can chain construction with field assignments they
// need to override individually:
//
//	wired := (&refresh.WiredLadder{DefaultLadder: &refresh.DefaultLadder{
//	    Store: store, Jar: jar, Name: name,
//	}}).WithStorage(l2disk, l2_5inline, l2_5logger)
//
// The L2.0 Storage is mandatory for L2.0 to fire — passing a
// nil Storage leaves the L2.0 rung sentinel-stubbed (the AC
// requires callers can opt-out of a rung without re-declaring
// the dispatcher). The InlineStorage is optional; nil is valid
// and matches the L2.5 rung's "no upstream state" contract.
// l25Logger nil leaves the field zero (ReloadL2_5 falls back
// to its internal nopLogger).
func (w *WiredLadder) WithStorage(st Storage, inline InlineStorage, l25Logger loggerFunc) *WiredLadder {
	w.L2Storage = st
	w.L2_5Inline = inline
	if l25Logger != nil {
		w.L2_5Logger = l25Logger
	}
	return w
}

// Step is the WiredLadder Ladder implementation. See the
// WiredLadder doc comment for the per-rung dispatch contract.
//
// Sentinel scope (T-S3-001d AC): ErrLadderLevelNotImplemented
// returns ONLY from the L4 stub AND from a wired-but-not-yet-
// implemented rung (L3 when L3Reload is nil; L2_0 when L2Storage
// is nil — the latter so a partial wiring that omits the Storage
// remains type-safe). The L1 / L2.0 / L2.5 paths return their
// rung-specific typed errors (ErrReloadL2_0Exhausted /
// ErrReloadL2_5Exhausted / ErrReloadL2_5NotConfigured / etc.) on
// failure, NOT the sentinel — the sentinel is the "this rung
// has not been wired yet" signal, not the "this rung tried and
// failed" signal.
func (w *WiredLadder) Step(ctx context.Context, level Level) (Tokens, bool, error) {
	switch level {
	case L1:
		if w.DefaultLadder == nil {
			return Tokens{}, false, fmt.Errorf("%w: %s", ErrLadderLevelNotImplemented, level)
		}
		return w.DefaultLadder.Step(ctx, L1)
	case L2_0:
		if w.L2Storage == nil {
			return Tokens{}, false, fmt.Errorf("%w: %s", ErrLadderLevelNotImplemented, level)
		}
		t, err := ReloadL2_0(ctx, w.L2Storage, w.L2Logger)
		if err != nil {
			return Tokens{}, false, err
		}
		return t, true, nil
	case L2_5:
		t, err := ReloadL2_5(ctx, w.L2_5Inline, w.L2_5Logger)
		if err != nil {
			return Tokens{}, false, err
		}
		return t, true, nil
	case L3:
		if w.L3Reload == nil {
			return Tokens{}, false, fmt.Errorf("%w: %s", ErrLadderLevelNotImplemented, level)
		}
		t, err := w.L3Reload(ctx)
		if err != nil {
			return Tokens{}, false, err
		}
		return t, true, nil
	case L4:
		// L4 is the ONLY rung whose default surface is the
		// sentinel. S02's mastertoken ticket will plug in
		// via w.L4Reload when it merges; until then every
		// L4 Step call returns the sentinel.
		if w.L4Reload == nil {
			return Tokens{}, false, fmt.Errorf("%w: %s", ErrLadderLevelNotImplemented, level)
		}
		t, err := w.L4Reload(ctx)
		if err != nil {
			return Tokens{}, false, err
		}
		return t, true, nil
	default:
		return Tokens{}, false, fmt.Errorf("%w: %s", ErrLadderLevelNotImplemented, level)
	}
}

// Compile-time check: WiredLadder satisfies the Ladder interface
// the Phase-4 callers depend on. A future change that breaks
// the signature trips this assertion before it breaks production.
var _ Ladder = (*WiredLadder)(nil)
