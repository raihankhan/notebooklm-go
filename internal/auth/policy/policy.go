// Typed validation entry point for the auth cookie gate.
//
// Validate is the one place the refresh ladder in Phase 4 switches on
// the shape of an auth failure. Callers get back a *TypedError with a
// stable Reason() accessor and never have to scrape human-readable
// text to decide whether to retry, refresh, mint a master token, or
// give up.
//
// Reason values are a closed enum (see the Reason type). The four
// values today cover the gate outcomes docs/05-auth.md §"Required
// cookies" and §"Cookie-domain allowlist" call out:
//
//   - ReasonMissingCookie       — Tier 1 failure: a name from
//     MinimumRequiredCookies is absent. No RotateCookies or OAuth
//     flow can conjure it; recovery declines and the error stands.
//   - ReasonPSIDTSUnroutable   — __Secure-1PSIDTS is present by
//     name but would not be sent to accounts.google.com (wrong
//     domain, expired, or scoped to a host that never reaches
//     accounts). This is the one failure the inline RotateCookies
//     recovery exists to heal (issue #2061).
//   - ReasonNoSecondaryBinding — Tier 2 failure: the secondary
//     binding (OSID, or APISID+SAPISID+LSID) is absent. Warn-only
//     today; raised as a Reason so Phase 4 can branch on it.
//   - ReasonUnrecognizedHost   — extraction saw a host outside
//     the allowlist; the cookie is refused (not just missing).
//
// Two-host diagnostic message constants — both personal hosts must
// be named in the diagnostic for a missing OSID, per docs/05-auth.md
// §"Required cookies". The exact strings live here so the Phase 4
// refresh ladder and the Phase 2 diagnostic commands can quote them
// without re-derivation.
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal
// imports stdlib + internal/redact (cookie names flow through the
// redactor on the diagnostic-message path) + internal/auth/cookiejar
// (the Jar type passed to Validate). No third-party dependencies.
package policy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/auth/cookiejar"
	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// Reason is the closed-enum tag the refresh ladder switches on. The
// underlying int values are stable across releases; callers MUST
// compare against the exported constants, not against a literal.
type Reason int

const (
	// ReasonUnknown is the zero value; returned only by errors
	// constructed outside Validate (none today). The ladder
	// should treat it as a defensive default and refuse to retry.
	ReasonUnknown Reason = iota

	// ReasonMissingCookie — Tier 1 cookie absent by name.
	ReasonMissingCookie

	// ReasonPSIDTSUnroutable — __Secure-1PSIDTS present but not
	// RFC 6265-routable to accounts.google.com/RotateCookies.
	ReasonPSIDTSUnroutable

	// ReasonNoSecondaryBinding — Tier 2 secondary binding
	// absent. Warn-only today (see docs/05-auth.md).
	ReasonNoSecondaryBinding

	// ReasonUnrecognizedHost — cookie host not in the allowlist.
	// Distinct from ReasonMissingCookie because the cookie WAS
	// present; the boundary rejected it.
	ReasonUnrecognizedHost
)

// String returns the canonical machine code for the reason. The
// returned value is stable across releases and is what the public
// error envelope (docs/07-cli-spec.md) uses for the `code` field.
func (r Reason) String() string {
	switch r {
	case ReasonMissingCookie:
		return "missing_cookie"
	case ReasonPSIDTSUnroutable:
		return "psidts_unroutable"
	case ReasonNoSecondaryBinding:
		return "no_secondary_binding"
	case ReasonUnrecognizedHost:
		return "unrecognized_host"
	default:
		return "unknown"
	}
}

// TypedError is the *only* error type Validate returns. The refresh
// ladder in Phase 4 errors.As against it and switches on Reason();
// the message text is for humans and may change between releases.
type TypedError struct {
	reason  Reason
	message string
}

// Error returns the human-readable message. The message routes
// through internal/redact.Apply so any cookie values that ever
// leak into a diagnostic line (notably via the two-host caveat)
// are masked before they reach a log sink or stderr.
//
// The redacted form is for log/error use; this is consistent with
// Cookie.String() and Jar.HeaderFor() in internal/auth/cookiejar.
func (e *TypedError) Error() string {
	if e == nil {
		return ""
	}
	return string(redact.Apply([]byte(e.message)))
}

// Reason returns the closed-enum tag. Stable across releases;
// callers MUST switch on this, not on the message.
func (e *TypedError) Reason() Reason {
	if e == nil {
		return ReasonUnknown
	}
	return e.reason
}

// newTypedError constructs a *TypedError. Internal — callers go
// through Validate. The message is stored verbatim; redaction
// happens on read in Error().
func newTypedError(reason Reason, format string, args ...any) *TypedError {
	return &TypedError{reason: reason, message: fmt.Sprintf(format, args...)}
}

// HostAllowlistNote is appended to diagnostics that touch the
// allowlist. It names every host the allowlist accepts so a user
// running `auth check` after a partial extraction can see what was
// refused. The slice order is: required → optional → regional.
//
// Exported so the `auth doctor` command can render the same list
// without re-deriving it.
func HostAllowlistNote() string {
	var b strings.Builder
	b.WriteString("Allowed cookie hosts:\n")
	for _, h := range requiredHosts {
		b.WriteString("  - ")
		b.WriteString(h)
		b.WriteByte('\n')
	}
	for _, h := range optionalHosts {
		b.WriteString("  - ")
		b.WriteString(h)
		b.WriteString(" (opt-in)\n")
	}
	for _, h := range regionalHosts {
		b.WriteString("  - ")
		b.WriteString(h)
		b.WriteString(" (regional)\n")
	}
	return b.String()
}

// TwoHostScopeNote is the canonical two-host diagnostic message.
//
// Source of truth: docs/05-auth.md §"Required cookies":
//
//	"`OSID` is **app-host-scoped**; `APISID`/`SAPISID` live on
//	`.google.com`. The binding spans hosts because the auth flow
//	crosses them — which is why the diagnostic for a missing
//	binding must name *both* personal hosts."
//
// The exact text below names the two personal hosts verbatim
// (notebook.google.com — the documented default since #2067, and
// notebooklm.google.com — the pre-rebrand legacy host) and the
// two recovery actions the docs prescribe:
//
//  1. Re-run `notebooklm login` so the account-wide
//     APISID+SAPISID+LSID triplet is re-minted (none of which are
//     host-scoped, so both hosts accept it).
//  2. Select the host that has the cookies via
//     NOTEBOOKLM_BASE_URL=https://<other-host>.
//
// The message is exported as a constant string so every caller
// quotes the same words.
const TwoHostScopeNote = "Heads-up: Google serves the personal app from both notebook.google.com " +
	"and notebooklm.google.com and redirects between them, but the OSID binding is " +
	"host-scoped — a cookie set on one host is never sent to the other.\n" +
	"If the binding is still missing afterwards it landed on the other host: " +
	"re-run 'notebooklm login' and complete the sign-in (that re-mints the " +
	"account-wide binding APISID+SAPISID+LSID, none of which are " +
	"host-scoped, so both hosts accept it), or select the host that has " +
	"the cookies with NOTEBOOKLM_BASE_URL=https://<other-host>."

// MissingCookieHint returns the actionable recovery hint for the
// missing-cookies failure mode. The text is the canonical message
// the Python CLI emits (notebooklm._auth.cookie_policy.
// missing_cookies_hint); the constants here keep it byte-stable
// across the two implementations so a user reading both CLIs sees
// the same advice.
//
// The hint branches on which Tier-1 / Tier-2 cookies are actually
// missing, and the OSID-related branches carry the two-host caveat
// so the user is not sent to a host whose host-scoped OSID the
// browser visit cannot reach.
func MissingCookieHint(observed map[string]struct{}) string {
	const extractionHint = "This typically means --browser-cookies extraction was incomplete " +
		"(Chrome 127+ App-Bound Encryption can cause silent partial reads). " +
		"Run 'notebooklm login' to re-authenticate."

	if _, ok := observed[cookieSID]; !ok {
		return "You are not signed in to Google in your browser.\n" +
			"Sign in to a Google account (Gmail, Drive, NotebookLM, ...) " +
			"in your browser and re-run this command."
	}

	_, psidtsMissing := observed[cookiePSIDTS1]
	hasSecondary := HasValidSecondaryBinding(observed)

	if psidtsMissing && !hasSecondary {
		return TwoHostScopeNote + "\n" +
			"Your browser session is signed in to Google but is missing " +
			"the cookies NotebookLM needs (OSID, or APISID+SAPISID+LSID, plus " +
			"__Secure-1PSIDTS).\n" +
			"Open https://notebook.google.com in your browser (sign in if " +
			"prompted), reload the page, then re-run this command."
	}

	if psidtsMissing {
		// No two-host caveat here: __Secure-1PSIDTS lives on
		// .google.com and is therefore sent to both personal
		// hosts. Only the host-scoped OSID binding has a
		// cross-host failure mode.
		return "__Secure-1PSIDTS is missing and the automatic RotateCookies recovery " +
			"did not succeed.\n" +
			"Open https://notebook.google.com in your browser (this triggers " +
			"Google to refresh the cookie), then re-run this command."
	}

	if !hasSecondary {
		return TwoHostScopeNote + "\n" +
			"Your browser cookies are missing the NotebookLM binding " +
			"(OSID, or all of APISID, SAPISID and LSID).\n" +
			"Open https://notebook.google.com in your browser (sign in if " +
			"prompted), reload the page, then re-run this command."
	}

	return extractionHint
}

// Validate inspects jar and returns:
//
//   - nil when the cookie set carries Tier 1 (SID + __Secure-1PSIDTS)
//     and a Tier 2 secondary binding (OSID or APISID+SAPISID+LSID),
//     AND every host on which the cookies are stored is in the
//     allowlist. The PSIDTS-routability check verifies the
//     __Secure-1PSIDTS cookie's Domain attribute domain-matches
//     accounts.google.com.
//
//   - a *TypedError otherwise. Reason() is one of:
//     ReasonMissingCookie, ReasonPSIDTSUnroutable,
//     ReasonNoSecondaryBinding, ReasonUnrecognizedHost.
//
// Validate never returns a non-TypedError. Callers that want a
// generic fallback (e.g. a top-level error envelope) can use
// errors.As(err, &typed) to assert the type and switch on Reason().
//
// The jar is passed as a *cookiejar.Jar (not a value) because the
// cookie jar owns a sync.Mutex; passing it by value copies the
// mutex and is a vet-flagged bug. Validate inspects without
// mutating; the refresh ladder can safely hand the live jar.
func Validate(j *cookiejar.Jar) error {
	// nil-jar is a defensive default; the refresh ladder should
	// never hand us a nil jar, but the gate must not panic if it
	// does.
	if j == nil {
		return newTypedError(ReasonMissingCookie,
			"missing required cookies: SID, __Secure-1PSIDTS\n%s",
			MissingCookieHint(map[string]struct{}{}))
	}
	names := namesFromJar(j)

	// Tier 1: SID + __Secure-1PSIDTS by name. Absent → reject.
	if missing := MissingMinimum(names); len(missing) > 0 {
		hint := MissingCookieHint(names)
		return newTypedError(ReasonMissingCookie,
			"missing required cookies: %s\n%s",
			strings.Join(missing, ", "), hint)
	}

	// PSIDTS-routability: __Secure-1PSIDTS must be present on a
	// domain that domain-matches accounts.google.com (RFC 6265
	// §5.1.3). Absent from the rotate host → refuse.
	if !psidtsRoutable(j) {
		return newTypedError(ReasonPSIDTSUnroutable,
			"__Secure-1PSIDTS is present but not routable to accounts.google.com/RotateCookies; "+
				"the session cannot be kept alive")
	}

	// Allowlist gate: every cookie's Domain attribute must be in
	// the allowlist. Refused here (not silently dropped) because
	// the boundary check is the security chokepoint — the user
	// should know an off-allowlist cookie was present.
	if offHost := findUnrecognizedHost(j); offHost.name != "" {
		return newTypedError(ReasonUnrecognizedHost,
			"cookie %q is on host %q, which is outside the allowlist",
			offHost.name, offHost.domain)
	}

	// Tier 2: secondary binding (warn-only today, but raised as a
	// Reason so Phase 4 can branch). The message carries the
	// two-host caveat so the diagnostic names both personal hosts.
	if !HasValidSecondaryBinding(names) {
		return newTypedError(ReasonNoSecondaryBinding,
			"cookie set lacks a secondary binding (need %s, or all three of %s, %s and %s). "+
				"Google may reject auth on the next call.\n%s",
			cookieOSID, cookieAPISID, cookieSAPISID, cookieLSID, TwoHostScopeNote)
	}

	return nil
}

// psidtsRoutable reports whether jar's __Secure-1PSIDTS cookie is
// RFC 6265-routable to accounts.google.com. The cookie's Domain
// attribute must domain-match accounts.google.com — either an exact
// match on "accounts.google.com" or a dot-prefix match on ".accounts.google.com",
// or it must live on ".google.com" (the parent domain that
// accounts.google.com is a subdomain of).
func psidtsRoutable(j *cookiejar.Jar) bool {
	if j == nil {
		return false
	}
	for _, c := range j.All() {
		if c == nil || c.Name != cookiePSIDTS1 {
			continue
		}
		domain := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		switch domain {
		case "accounts.google.com":
			return true
		case "google.com":
			return true
		}
		// Anything else (notebook.google.com, notebooklm.google.com,
		// or a regional ccTLD variant) is unroutable to the
		// rotate endpoint. This is the failure shape ReasonPSIDTSUnroutable
		// exists to surface.
		return false
	}
	// No PSIDTS cookie at all — Tier 1 has already rejected this
	// case, so the gate here is a defensive "not routable" rather
	// than a true answer. Callers should not rely on the return
	// when the cookie is missing; Validate checks Tier 1 first.
	return false
}

// offHostCookie describes an off-allowlist cookie.
type offHostCookie struct {
	name   string
	domain string
}

// findUnrecognizedHost returns the first cookie whose Domain
// attribute is not in the allowlist. The empty string means every
// cookie is on an allowed host. Iteration order matches jar.All()
// (sorted by name, domain, path) so the diagnostic is deterministic.
func findUnrecognizedHost(j *cookiejar.Jar) offHostCookie {
	if j == nil {
		return offHostCookie{}
	}
	for _, c := range j.All() {
		if c == nil {
			continue
		}
		// Host-only cookies carry the request URL's host stamped
		// on Domain; that is the form we should look up in the
		// allowlist. Domain cookies carry their declared Domain
		// attribute — also the right lookup form.
		if IsAllowedHost(c.Domain) {
			continue
		}
		return offHostCookie{name: c.Name, domain: c.Domain}
	}
	return offHostCookie{}
}

// AsTypedError unwraps err to a *TypedError, reporting its Reason
// and a human-readable Message. The bool return is false when err
// is not a *TypedError (e.g. a non-policy error from elsewhere in
// the auth stack). Callers that want the refresh-ladder switch
// should use the typed return to branch on Reason().
func AsTypedError(err error) (typed *TypedError, message string, reason Reason, ok bool) {
	if err == nil {
		return nil, "", ReasonUnknown, false
	}
	if !errors.As(err, &typed) {
		return nil, "", ReasonUnknown, false
	}
	return typed, typed.Error(), typed.Reason(), true
}

// namesFromJar extracts the cookie-name set from a Jar's All() view.
// Used by Validate. The set is freshly allocated each call; callers
// that need a long-lived snapshot must own their own copy.
func namesFromJar(j *cookiejar.Jar) map[string]struct{} {
	cookies := j.All()
	out := make(map[string]struct{}, len(cookies))
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		out[c.Name] = struct{}{}
	}
	return out
}
