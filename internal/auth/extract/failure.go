// failure.go: the four-branch failure taxonomy.
//
// Single source of truth for the classifier, shared between the
// extractor (ExtractWIZWithURL) and the refresh ladder (Phase 4). The
// branch order is load-bearing and pinned by TestFailureBranchOrder —
// see docs/05-auth.md §"Authentication" and
// notebooklm._auth.extraction._extraction_failure.

package extract

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// authPathRedactHosts is the allowlist of hosts whose path component
// can carry an opaque auth token (`/o/oauth2/auth/<token>`). The
// safe-URL formatter drops the path only for these; non-auth hosts
// keep their path because the path is operator signal, not credential.
//
// Source: notebooklm._url_utils._AUTH_PATH_REDACT_HOSTS.
var authPathRedactHosts = []string{
	"accounts.google.com",
	"oauth2.googleapis.com",
	"oauth2.googleusercontent.com",
}

// appHosts is the closed set of hosts that legitimately serve a
// NotebookLM app shell (a page carrying WIZ_global_data). Defined as
// a var so future rebrand hosts land here without further code churn.
//
// Source: notebooklm._url_utils._NOTEBOOKLM_APP_HOSTS, which delegates
// to PERSONAL_APP_HOSTS + ENTERPRISE_BASE_HOST.
var appHosts = []string{
	"notebook.google.com",
	"notebooklm.google.com",
	"notebooklm.cloud.google.com",
}

// unavailableHost is the bare marketing/landing host (no .com) that
// serves the region/anti-abuse access gate. Distinguished from the
// app hosts purely by the absent .com suffix.
const unavailableHost = "notebooklm.google"

// cookieMismatchPath is the path component of Google's
// /CookieMismatch interstitial.
const cookieMismatchPath = "cookiemismatch"

// jarView is the minimal interface the classifier needs from a
// cookie jar. The production Jar implements it via Snapshot(); tests
// pass a nil jar without ceremony.
type jarView interface {
	// Snapshot returns the cookie list, in stable order. Reserved for
	// future expansion (today the classifier is URL-only).
	Snapshot() []*cookieSnapshot
}

// cookieSnapshot is the read-only projection of a jar cookie that the
// classifier exposes. Defined here so the jarView interface stays
// self-contained (no dependency on internal/auth/cookiejar in the
// public signature).
type cookieSnapshot struct {
	Name   string
	Value  string
	Domain string
	Path   string
}

// Failure is the typed classifier result. Callers unwrap the embedded
// sentinel to switch on ErrRegionBlocked / ErrCookieMismatch /
// ErrAuthRedirect / ErrTokenMissing via errors.Is.
//
// The classifier owns the branch order. The extractor and the refresh
// ladder both route through here so they cannot disagree.
type Failure struct {
	// Sentinel is one of the four exported errRegionBlocked-style
	// values. Stable across releases.
	Sentinel error
	// Message is the human-readable diagnostic. Routes through
	// internal/redact before reaching a log sink.
	Message string
	// FinalURL is the URL the classifier inspected, scrubbed to
	// scheme://host[/path-or-redacted].
	FinalURL string
}

// Error implements the error interface. The message runs through
// internal/redact.Apply so any cookie-shaped substring that ever
// leaks into the diagnostic (notably via the final URL) is masked
// before reaching a log sink or stderr.
func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	return string(redact.Apply([]byte(f.Message)))
}

// Unwrap exposes the sentinel so errors.Is(err, ErrRegionBlocked)
// etc. work for callers that wrap a *Failure in another error type.
func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Sentinel
}

// ClassifyFailure is the shared entry point. It is called from both
// the extract path (ExtractWIZWithURL) and the refresh-ladder path
// (Phase 4) so the branch order lives in exactly one place.
//
// The html argument is currently unused: the page body is never
// consulted at all. The old contains_google_auth_redirect(html)
// fallback matched any accounts.google.com URL anywhere in the body,
// which nearly every Google-served page contains, so it converted
// "wrong page" into "Authentication expired" and discarded the final
// URL — the one piece of evidence that would have identified the
// real fault. Every branch below carries the URL.
//
// jar is the live cookie jar passed through for future use (the
// cookie-mismatch branch is URL-only today, but the refresh ladder
// will pass it in so a future expansion can correlate cookie
// presence with the mismatch). nil is allowed.
//
// finalURL is the URL the response landed on (typically response.URL
// after redirects). Empty string is allowed; the classifier treats it
// as "no URL signal" and falls through to the token-missing branch.
//
// Branch order (load-bearing, pinned by TestFailureBranchOrder):
//
//  1. Region / anti-abuse gate (ErrRegionBlocked). Checked first
//     because that gate page carries an accounts.google.com sign-in
//     link that older body-scans misclassified as expired auth.
//  2. Cookie mismatch hop (ErrCookieMismatch). Checked before the
//     auth branch because the interstitial lives on accounts.google.com
//     and would otherwise be reported as an expiry (#2038).
//  3. Auth redirect (ErrAuthRedirect). Driven by the final URL alone.
//  4. Token missing (ErrTokenMissing). The fallback when the URL
//     evidence is inconclusive.
//
// Source: notebooklm._auth.extraction._extraction_failure +
// _url_only_extraction_failure.
func ClassifyFailure(_ string, _ jarView, finalURL string) *Failure {
	chain := []string{finalURL}

	if isUnavailableRedirect(finalURL) {
		return regionFailure(finalURL)
	}
	if hop := findCookieMismatchHop(chain); hop != "" {
		return cookieMismatchFailure(hop, finalURL)
	}
	if isGoogleAuthRedirect(finalURL) {
		return authRedirectFailure(finalURL)
	}
	return tokenMissingFailure(finalURL)
}

// regionFailure builds the region/anti-abuse diagnostic. The
// location= query value is surfaced explicitly because safeURL drops
// the query.
//
// Source: notebooklm._auth.extraction._unavailable_redirect_message.
func regionFailure(finalURL string) *Failure {
	loc := unavailableLocation(finalURL)
	target := safeURL(finalURL)
	if loc != "" {
		target = target + " (location=" + loc + ")"
	}
	return &Failure{
		Sentinel: ErrRegionBlocked,
		Message: fmt.Sprintf(
			"NotebookLM redirected this request to its region / anti-abuse access gate: "+
				"%s. This is not a library bug or an expired login. Likely a VPN/proxy "+
				"or datacenter IP, or an IP/timezone/language mismatch. "+
				"Verify by opening the configured app host in a normal browser on the same network; "+
				"if it redirects there too, use a residential connection in a supported region.",
			target),
		FinalURL: safeURL(finalURL),
	}
}

// cookieMismatchFailure builds the diagnostic for a /CookieMismatch
// hop. The hop is rendered with its literal path because safeURL
// would redact the very marker being reported.
//
// Source: notebooklm._auth.extraction._cookie_mismatch_message.
func cookieMismatchFailure(hop, finalURL string) *Failure {
	hu, err := url.Parse(hop)
	hopDisplay := hop
	if err == nil && hu != nil {
		scheme := hu.Scheme
		if scheme == "" {
			scheme = "https"
		}
		hopDisplay = scheme + "://" + strings.ToLower(hu.Hostname()) + "/CookieMismatch"
	}
	landed := safeURL(finalURL)
	chain := hopDisplay
	if landed != "" {
		chain = hopDisplay + ", and the chain ended at " + landed
	}
	return &Failure{
		Sentinel: ErrCookieMismatch,
		Message: fmt.Sprintf(
			"Google's CookieMismatch page was reached during this request: %s. "+
				"Google rejected the cookies as not matching the host they were sent to. "+
				"This is a cookie-scoping problem and NOT necessarily an expired session — "+
				"the credentials may be perfectly valid. Common causes: a storage_state.json "+
				"whose per-cookie domains were flattened to a single host, cookies belonging "+
				"to a different Google account or session, or a stale __Secure-1PSIDTS. "+
				"Run 'notebooklm login' to re-extract cookies; if it recurs, verify that "+
				"storage_state.json preserves each cookie's original domain.",
			chain),
		FinalURL: safeURL(finalURL),
	}
}

// authRedirectFailure builds the "Authentication expired" diagnostic.
// Driven by the final URL alone.
//
// Source: notebooklm._auth.extraction._url_only_extraction_failure (auth branch).
func authRedirectFailure(finalURL string) *Failure {
	return &Failure{
		Sentinel: ErrAuthRedirect,
		Message: fmt.Sprintf(
			"Authentication expired or invalid. Final URL: %s\n"+
				"Run 'notebooklm login' to re-authenticate.",
			safeURL(finalURL)),
		FinalURL: safeURL(finalURL),
	}
}

// tokenMissingFailure splits the "token missing" diagnostic by
// whether the response landed on an app host. The body is never
// consulted.
//
// Source: notebooklm._auth.extraction._token_not_found_message.
func tokenMissingFailure(finalURL string) *Failure {
	var detail string
	if isAppHost(finalURL) {
		detail = "This may indicate the page structure has changed."
	} else {
		detail = "The response did not come from a NotebookLM app host, so the request never " +
			"reached the app — this is a redirect/environment problem, not a page-structure " +
			"change. Note that most Google-served pages carry accounts.google.com links, so " +
			"their presence in the body is not evidence that the session expired."
	}
	return &Failure{
		Sentinel: ErrTokenMissing,
		Message: fmt.Sprintf(
			"Token not found in HTML. Final URL: %s\n%s",
			safeURL(finalURL), detail),
		FinalURL: safeURL(finalURL),
	}
}

// safeURL strips credential-shaped parts of a URL for error display.
// Returns the empty string on empty input so the default final_url=""
// renders cleanly instead of degenerating to "://".
//
// Source: notebooklm._auth.extraction._safe_url.
func safeURL(raw string) string {
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
	if path != "" && path != "/" && isAuthHost(host) {
		path = "/<redacted>"
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + netloc + path
}

// isAuthHost returns true when host is one of the auth-path-redaction
// allowlist entries, exactly or as a subdomain.
func isAuthHost(host string) bool {
	for _, h := range authPathRedactHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// isAppHost returns true when the URL is one of the hosts that can
// legitimately serve a NotebookLM app shell. Used to split the
// "token missing" diagnostic into "on app host" (page-structure
// change) vs "off app host" (redirect/environment problem).
//
// Source: notebooklm._url_utils.is_notebooklm_app_host.
func isAppHost(raw string) bool {
	if raw == "" {
		return true // mirror Python: empty URL treated as on-app-host
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range appHosts {
		if host == h {
			return true
		}
	}
	return false
}

// isUnavailableRedirect returns true when raw is the bare
// notebooklm.google landing host (with or without a subdomain).
//
// Source: notebooklm._url_utils.is_notebooklm_unavailable_redirect.
func isUnavailableRedirect(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == unavailableHost || strings.HasSuffix(host, "."+unavailableHost)
}

// isGoogleAuthRedirect returns true when raw is a Google sign-in
// surface. Hostname-based, exact or subdomain.
//
// Source: notebooklm._url_utils.is_google_auth_redirect.
func isGoogleAuthRedirect(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "accounts.google.com" || strings.HasSuffix(host, ".accounts.google.com")
}

// isCookieMismatchRedirect returns true when raw is Google's
// accounts.google.com/CookieMismatch interstitial.
//
// Source: notebooklm._url_utils.is_cookie_mismatch_redirect.
func isCookieMismatchRedirect(raw string) bool {
	if !isGoogleAuthRedirect(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return false
	}
	return strings.EqualFold(strings.Trim(u.EscapedPath(), "/"), cookieMismatchPath)
}

// findCookieMismatchHop returns the first URL in chain that is a
// /CookieMismatch hop. The final URL alone cannot see the
// interstitial because it 302s onward to a support.google.com help
// article, so callers pass the redirect history followed by the
// final URL.
//
// Source: notebooklm._url_utils.find_cookie_mismatch_hop.
func findCookieMismatchHop(chain []string) string {
	for _, u := range chain {
		if isCookieMismatchRedirect(u) {
			return u
		}
	}
	return ""
}

// unavailableLocation returns the location= query value from an
// access-gate URL, or "" when absent. Sanitized: only ASCII
// alphanumerics, dashes, underscores, capped at 64 chars.
//
// Source: notebooklm._url_utils.notebooklm_unavailable_location.
func unavailableLocation(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return ""
	}
	q := u.Query()
	vals := q["location"]
	if len(vals) == 0 {
		return ""
	}
	first := vals[0]
	var b strings.Builder
	for _, r := range first {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
			if b.Len() >= 64 {
				break
			}
		}
	}
	return b.String()
}
