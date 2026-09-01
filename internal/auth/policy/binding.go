// Tier 2 secondary-binding matrix.
//
// Per docs/05-auth.md §"Required cookies", the homepage GET requires
// *at least one* of two secondary-binding paths in addition to Tier 1:
//
//   - OSID (recent-sign-in binding), OR
//   - APISID + SAPISID (legacy XSSI pair) AND bare LSID.
//
// Without one of those, Google 302s to accounts.google.com/v3/signin
// even when SID and __Secure-1PSIDTS are present and otherwise valid.
//
// The binding spans hosts: OSID is app-host-scoped (one of the two
// personal hosts) while APISID/SAPISID live on .google.com and LSID
// on accounts.google.com. The auth flow crosses the hosts, and a
// missing per-product OSID on the host the request is actually going
// to is the diagnostic the two-host message exists to surface.
//
// This file ports the canonical matrix from
// notebooklm._auth.cookie_policy._has_valid_secondary_binding. The
// matrix is deliberately domain-blind: a check restricted to what
// routes to the *target* URL would reject working profiles (#2054).

package policy

// Tier 2 secondary-binding cookie names.
const (
	// cookieOSID is the per-product binding cookie Google sets on
	// the personal app host. Present → sufficient on its own
	// (verified with every accounts.google.com cookie stripped,
	// docs/05-auth.md §"Required cookies" row 1).
	cookieOSID = "OSID"

	// cookieAPISID is the legacy XSSI-pair entry. Lives on
	// .google.com and survives every domain filter; the XSSI
	// branch was never tested without it before the three-way
	// ablation in issue #1977.
	cookieAPISID = "APISID"

	// cookieSAPISID is the second half of the legacy XSSI pair.
	cookieSAPISID = "SAPISID"

	// cookieLSID is the account-wide session id. Lives on
	// accounts.google.com. Required *only* when OSID is absent;
	// the row-4 ablation shows APISID+SAPISID+LSID works but
	// APISID+SAPISID-without-LSID fails.
	cookieLSID = "LSID"
)

// secondaryBindingMatrix is the closed mapping from a primary cookie
// (the "anchor" of a binding) to the secondary (domain, name) pair
// the cookie store must carry to authenticate against that primary.
// Used by LookupBinding and by Validate when reporting
// ReasonNoSecondaryBinding.
//
// The matrix is intentionally narrow: only the primary names whose
// absence is a documented failure shape appear here. Adding a row
// without a corresponding ablation is a regression risk — the
// three-way ablation in issue #1977 is what made the LSID
// requirement visible, and the next ablation may surface a new
// dependency we do not yet model.
var secondaryBindingMatrix = map[string]bindingRow{
	// OSID present → sufficient on its own. The matrix row still
	// exists so the diagnostic can name what was checked; the
	// binding domain is the personal app host (one of the two),
	// and the name is the same as the primary, so no extra
	// secondary is required.
	cookieOSID: {
		domain: "any",
		name:   "", // primary satisfies the binding on its own
	},
	// LSID required when OSID is absent. The three-way ablation
	// showed that APISID+SAPISID-without-LSID fails; the binding
	// domain is accounts.google.com, the name is LSID.
	cookieLSID: {
		domain: "accounts.google.com",
		name:   cookieLSID,
	},
}

// bindingRow is one row of the secondary-binding matrix.
type bindingRow struct {
	domain string // the host the secondary must be routable to; "any" means the secondary does not depend on the primary
	name   string // the cookie name that must be present; "" means the primary satisfies the binding on its own
}

// LookupBinding returns the secondary (domain, name) pair required
// when primaryName anchors a binding. ok is false when no row exists
// — a primary that does not appear in the matrix means no secondary
// is required and the caller should treat the gate as satisfied for
// that primary.
//
// The returned domain is "any" for rows where the primary satisfies
// the binding on its own (OSID today); callers that want to enforce
// a routable-to-host check should treat "any" as a pass.
func LookupBinding(primaryName string) (domain, name string, ok bool) {
	row, ok := secondaryBindingMatrix[primaryName]
	if !ok {
		return "", "", false
	}
	return row.domain, row.name, true
}

// HasValidSecondaryBinding is the canonical Tier 2 acceptance check.
// Returns true iff at least one of:
//
//   - OSID is present in observed, OR
//   - APISID, SAPISID, and LSID are all present in observed.
//
// The check is domain-blind by design (see file comment); routing is
// the job of the cookie jar, not the policy gate. The function is
// exported because the PSIDTS recovery path (T-P2-5) and the refresh
// ladder (Phase 4) both need the same answer.
func HasValidSecondaryBinding(observed map[string]struct{}) bool {
	if _, ok := observed[cookieOSID]; ok {
		return true
	}
	if _, ok := observed[cookieAPISID]; !ok {
		return false
	}
	if _, ok := observed[cookieSAPISID]; !ok {
		return false
	}
	if _, ok := observed[cookieLSID]; !ok {
		return false
	}
	return true
}

// SecondaryBindingPath returns a label describing which secondary
// binding path is in use, for diagnostics. The returned value is one
// of:
//
//   - "osid"   — OSID is present (recent-sign-in path)
//   - "xssi"   — APISID+SAPISID+LSID are all present (legacy path)
//   - "none"   — neither path is satisfied; the caller should warn
//     (Tier 2 is warn-only by design, see docs/05-auth.md)
//
// Used by the auth/doctor and auth/check commands to report the
// binding form. The label is stable across releases; downstream
// parsers key on it.
func SecondaryBindingPath(observed map[string]struct{}) string {
	if _, ok := observed[cookieOSID]; ok {
		return "osid"
	}
	if _, ok := observed[cookieAPISID]; !ok {
		return "none"
	}
	if _, ok := observed[cookieSAPISID]; !ok {
		return "none"
	}
	if _, ok := observed[cookieLSID]; !ok {
		return "none"
	}
	return "xssi"
}
