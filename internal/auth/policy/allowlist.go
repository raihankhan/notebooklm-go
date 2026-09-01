// Package policy holds the auth-side cookie-domain allowlist, the
// minimum-required-cookie set, the Tier 2 secondary-binding matrix,
// and the typed validation entry point the refresh ladder in Phase 4
// switches on.
//
// The package is mode=internal per docs/AGENTS.md rule 5: it imports
// stdlib + internal/redact (for the diagnostic messages that name
// cookie names, see Validate) + internal/auth/cookiejar (for the Jar
// type passed to Validate). No third-party dependencies and no
// module-internal imports beyond that triple.
//
// The allowlist is a security boundary: cookies outside it are not
// requested, not stored, and not sent. Per docs/05-auth.md §"Cookie-
// domain allowlist", extraction and persistence both filter through
// this list. Two personal hosts (notebook.google.com and
// notebooklm.google.com) carry the per-product OSID binding cookie,
// and the diagnostic for a missing binding must name *both* — hence
// the dedicated two-host message in policy.go.
//
// All host strings are sourced verbatim from docs/05-auth.md and the
// canonical Python implementation
// notebooklm._auth.cookie_policy.{REQUIRED_COOKIE_DOMAINS,
// OPTIONAL_COOKIE_DOMAINS_BY_LABEL, GOOGLE_REGIONAL_CCTLDS}. Bump
// schema_version in testdata/hosts.txt when adding a row.
package policy

import (
	"strings"
)

// personalBaseHost is the default NotebookLM app host (issue #2067,
// ADR-0028). The CLI's RPCs land here and Google sets the per-product
// OSID / __Secure-OSID binding cookies here. Both personal hosts are
// required because the auth flow crosses them.
const personalBaseHost = "notebook.google.com"

// personalLegacyHost is the pre-rebrand NotebookLM app host. Still
// served, still selectable via NOTEBOOKLM_BASE_URL, and sets the
// same per-product binding cookies. Both must stay accepted.
const personalLegacyHost = "notebooklm.google.com"

// PersonalAppHosts is the closed set of hosts Google serves the
// personal app from. The order is not load-bearing; downstream code
// that wants a stable iteration should sort.
var PersonalAppHosts = []string{personalBaseHost, personalLegacyHost}

// requiredHosts is the canonical list of hosts whose cookies the
// extraction + persistence paths accept by default. Per
// docs/05-auth.md §"Cookie-domain allowlist", this is the chokepoint
// for the security boundary — everything outside it is silently
// dropped. Both dotted and undotted variants are listed deliberately:
// cookie-jar normalization can add or drop a leading dot, and a
// dropped variant means a silently missing cookie next extraction.
//
// Source of truth: docs/05-auth.md §"Cookie-domain allowlist" and
// notebooklm._auth.cookie_policy.REQUIRED_COOKIE_DOMAINS.
var requiredHosts = []string{
	".google.com",
	"google.com",
	"." + personalBaseHost,
	personalBaseHost,
	"." + personalLegacyHost,
	personalLegacyHost,
	".notebooklm.cloud.google.com",
	"notebooklm.cloud.google.com",
	".googleusercontent.com",
	"accounts.google.com",
	".accounts.google.com",
	"drive.google.com",
	".drive.google.com",
}

// optionalHosts is the closed set of sibling Google product hosts
// that are NOT exercised by any current code path but exist because
// early versions requested them "for symmetry with a logged-in
// browser" (issue #360). They are opt-in only via --include-domains.
//
// Source of truth: docs/05-auth.md §"Cookie-domain allowlist" and
// notebooklm._auth.cookie_policy.OPTIONAL_COOKIE_DOMAINS_BY_LABEL.
var optionalHosts = []string{
	// youtube
	".youtube.com",
	"youtube.com",
	"accounts.youtube.com",
	".accounts.youtube.com",
	// docs
	"docs.google.com",
	".docs.google.com",
	// myaccount
	"myaccount.google.com",
	".myaccount.google.com",
	// mail
	"mail.google.com",
	".mail.google.com",
}

// regionalCCTLDS is the closed set of regional Google ccTLDs where
// Google may set auth cookies. Users in these regions may carry SID
// on a regional domain instead of .google.com. The exact suffix is
// what follows ".google." — for example "com.sg" for
// ".google.com.sg" and "co.uk" for ".google.co.uk".
//
// Source of truth: docs/05-auth.md §"Cookie-domain allowlist" and
// notebooklm._auth.cookie_policy.GOOGLE_REGIONAL_CCTLDS.
var regionalCCTLDS = []string{
	// .google.com.XX pattern (country-code second-level domains)
	"com.sg", // Singapore
	"com.au", // Australia
	"com.br", // Brazil
	"com.mx", // Mexico
	"com.ar", // Argentina
	"com.hk", // Hong Kong
	"com.tw", // Taiwan
	"com.my", // Malaysia
	"com.ph", // Philippines
	"com.vn", // Vietnam
	"com.pk", // Pakistan
	"com.bd", // Bangladesh
	"com.ng", // Nigeria
	"com.eg", // Egypt
	"com.tr", // Turkey
	"com.ua", // Ukraine
	"com.co", // Colombia
	"com.pe", // Peru
	"com.sa", // Saudi Arabia
	"com.ae", // UAE
	// .google.co.XX pattern (countries using .co second-level)
	"co.uk", // United Kingdom
	"co.jp", // Japan
	"co.in", // India
	"co.kr", // South Korea
	"co.za", // South Africa
	"co.nz", // New Zealand
	"co.id", // Indonesia
	"co.th", // Thailand
	"co.il", // Israel
	"co.ve", // Venezuela
	"co.cr", // Costa Rica
	"co.ke", // Kenya
	"co.ug", // Uganda
	"co.tz", // Tanzania
	"co.ma", // Morocco
	"co.ao", // Angola
	"co.mz", // Mozambique
	"co.zw", // Zimbabwe
	"co.bw", // Botswana
	// .google.XX pattern (single ccTLD countries)
	"cn",  // China
	"de",  // Germany
	"fr",  // France
	"it",  // Italy
	"es",  // Spain
	"nl",  // Netherlands
	"pl",  // Poland
	"ru",  // Russia
	"ca",  // Canada
	"be",  // Belgium
	"at",  // Austria
	"ch",  // Switzerland
	"se",  // Sweden
	"no",  // Norway
	"dk",  // Denmark
	"fi",  // Finland
	"pt",  // Portugal
	"gr",  // Greece
	"cz",  // Czech Republic
	"ro",  // Romania
	"hu",  // Hungary
	"ie",  // Ireland
	"sk",  // Slovakia
	"bg",  // Bulgaria
	"hr",  // Croatia
	"si",  // Slovenia
	"lt",  // Lithuania
	"lv",  // Latvia
	"ee",  // Estonia
	"lu",  // Luxembourg
	"cl",  // Chile
	"cat", // Catalonia (special case - 3 letter)
}

// regionalHosts is derived from regionalCCTLDS: each ccTLD yields one
// ".google.<ccTLD>" entry (the form cookies carry when Google sets
// them in a regional domain). Built once at init time so IsAllowedHost
// stays a single map lookup.
var regionalHosts = func() []string {
	out := make([]string, len(regionalCCTLDS))
	for i, cc := range regionalCCTLDS {
		out[i] = ".google." + cc
	}
	return out
}()

// allowedHosts is the closed set the public IsAllowedHost consults.
// Built once at init time by unioning required + optional + regional,
// then de-duplicating so a host that appears in two lists is still
// represented exactly once.
//
// The set is deliberately conservative: a host not in the map is
// refused, no substring match, no DNS-walking fallback. A typo in a
// caller or a hostile cookie store row cannot slip through.
var allowedHosts = func() map[string]struct{} {
	m := make(map[string]struct{}, len(requiredHosts)+len(optionalHosts)+len(regionalHosts))
	for _, h := range requiredHosts {
		m[strings.ToLower(h)] = struct{}{}
	}
	for _, h := range optionalHosts {
		m[strings.ToLower(h)] = struct{}{}
	}
	for _, h := range regionalHosts {
		m[strings.ToLower(h)] = struct{}{}
	}
	return m
}()

// RequiredHosts returns a copy of the required cookie-domain list.
// Exported for callers that want to enumerate the default allowlist
// without going through IsAllowedHost (e.g. the build_cookie_domain_
// allowlist builder in the Python source).
func RequiredHosts() []string {
	out := make([]string, len(requiredHosts))
	copy(out, requiredHosts)
	return out
}

// OptionalHosts returns a copy of the optional cookie-domain list.
func OptionalHosts() []string {
	out := make([]string, len(optionalHosts))
	copy(out, optionalHosts)
	return out
}

// RegionalHosts returns a copy of the regional .google.<ccTLD> list.
func RegionalHosts() []string {
	out := make([]string, len(regionalHosts))
	copy(out, regionalHosts)
	return out
}

// IsAllowedHost reports whether host is in the cookie-domain
// allowlist. The check is set-membership only; the caller is responsible
// for any leading-dot normalization (the canonical entries already
// include both forms so a normalized or unnormalized input both match).
//
// Comparison is case-insensitive (ASCII-only). Cookie Domain attributes
// are ASCII per RFC 6265 §4.1.2.3; a Unicode-equivalent like "GOOGLE.COM"
// is accepted because the canonical ".google.com" form is in the map.
//
// Returns false on the empty string — the security boundary must refuse
// a degenerate input rather than collapse it to the catch-all.
func IsAllowedHost(host string) bool {
	if host == "" {
		return false
	}
	_, ok := allowedHosts[strings.ToLower(host)]
	return ok
}
