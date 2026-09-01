// Minimum-required-cookie set for the Tier 1 preflight.
//
// Tier 1 is a hard validator failure: callers that reach Validate
// without a recovery wrapper must not proceed with an unusable
// cookie set. Two cookies are required by name, both verified to be
// RFC 6265-routable to accounts.google.com/RotateCookies so the
// keepalive can re-mint __Secure-1PSIDTS on the rotate path.
//
// Source of truth: docs/05-auth.md §"Required cookies" and
// notebooklm._auth.cookie_policy.MINIMUM_REQUIRED_COOKIES. The
// constants are deliberately the same in both implementations so a
// Python-imported storage_state passes the same gate the Python CLI
// applies (F2 round-trip in docs/AGENTS.md).

package policy

// cookieSID is the individually-required singleton: a session without
// SID is unrecoverable through any Google endpoint, because no
// RotateCookies or OAuth flow mints it. It is also the most commonly
// missing cookie in partial browser extractions (issue #990).
const cookieSID = "SID"

// cookiePSIDTS1 is the second Tier 1 entry. It is a __Secure- prefixed
// cookie (forces Secure + https), live, and RFC 6265-routable to
// accounts.google.com so the keepalive can POST RotateCookies to
// mint a fresh value. Present-by-name but unroutable is its own
// failure reason (ReasonPSIDTSUnroutable) because rotation is what
// keeps the session alive.
const cookiePSIDTS1 = "__Secure-1PSIDTS"

// MinimumRequiredCookies is the closed set a session MUST carry to be
// considered valid by Validate. Missing either name raises a
// *TypedError with Reason() == ReasonMissingCookie.
//
// The set is exported as a frozen slice (not a map) so callers cannot
// mutate it at runtime; the only mutating verb is HasMinimum, which
// takes a set of observed names and answers the gating question
// without exposing the slice.
var MinimumRequiredCookies = []string{cookieSID, cookiePSIDTS1}

// minimumSet is the lookup form of MinimumRequiredCookies. Built once
// at init so the Tier 1 gate is a constant-time set check, not a
// linear scan.
var minimumSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(MinimumRequiredCookies))
	for _, n := range MinimumRequiredCookies {
		m[n] = struct{}{}
	}
	return m
}()

// HasMinimum reports whether observed carries every name in
// MinimumRequiredCookies. Used by Validate and by call sites that
// need a yes/no answer without the typed-error machinery (e.g. a
// preflight probe that does not want to allocate an error).
//
// observed is treated as a set: only membership matters. An empty
// set returns false.
func HasMinimum(observed map[string]struct{}) bool {
	if len(observed) < len(minimumSet) {
		return false
	}
	for name := range minimumSet {
		if _, ok := observed[name]; !ok {
			return false
		}
	}
	return true
}

// MissingMinimum returns the names from MinimumRequiredCookies that
// are absent from observed. The slice is sorted (SID before
// __Secure-1PSIDTS) so error messages and log lines stay
// deterministic across runs and across the Python/Go implementations.
func MissingMinimum(observed map[string]struct{}) []string {
	var missing []string
	for _, name := range MinimumRequiredCookies {
		if _, ok := observed[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}
