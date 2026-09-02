// Package extract contains the WIZ_global_data extractor and the four-branch
// failure taxonomy used by the refresh ladder.
//
// Port of notebooklm._auth.extraction. The two public surfaces are:
//
//   - ExtractWIZ — the SNlM0e/FdrFJe field extractor. Covers all three
//     quoting variants NotebookLM's page shell has been observed to emit
//     (canonical double-quoted, single-quoted, HTML-escaped).
//   - ClassifyFailure — the single source of truth for branch order, shared
//     between the extractor and the refresh ladder (Phase 4). The branch
//     order is load-bearing: region/anti-abuse gate -> cookie-mismatch hop
//     -> auth redirect -> token missing. Per docs/05-auth.md and
//     notebooklm._auth.extraction._extraction_failure, a single function
//     owns the ordering so the two callers cannot drift.
//
// Boundary: per docs/AGENTS.md rule 5, this package is mode=internal; it
// imports stdlib + internal/redact (for error-message scrubbing) +
// internal/auth/cookiejar (for the Jar the classifier inspects). No
// third-party dependencies.
package extract

import (
	"errors"
	"fmt"
	"regexp"
)

// Sentinel errors for the four branches. Each branch in
// ClassifyFailure wraps one of these with %w so callers can switch on
// errors.Is without scraping message text.
//
// The order in which ClassifyFailure checks them is load-bearing and
// pinned by TestFailureBranchOrder in failure_test.go.
var (
	// ErrRegionBlocked — the request landed on Google's region / anti-abuse
	// access gate (notebooklm.google with no .com). The credential model
	// is fine; the network environment is the problem.
	ErrRegionBlocked = errors.New("region/anti-abuse gate")

	// ErrCookieMismatch — the chain hit Google's /CookieMismatch
	// interstitial. The cookies are valid; their scoping is not.
	ErrCookieMismatch = errors.New("cookie mismatch hop")

	// ErrAuthRedirect — the final URL is a Google sign-in surface. The
	// session expired or the storage_state belongs to a different account.
	ErrAuthRedirect = errors.New("auth redirect")

	// ErrTokenMissing — the response came from a NotebookLM app host but
	// did not carry the expected WIZ_global_data field. Page-structure
	// drift worth filing a bug about.
	ErrTokenMissing = errors.New("token missing")
)

// csrfField is the WIZ_global_data key that carries the CSRF token.
const csrfField = "SNlM0e"

// sessionField is the WIZ_global_data key that carries the session id.
const sessionField = "FdrFJe"

// wizFieldPatterns returns the ordered list of regex patterns tried
// for one WIZ_global_data key. The order is load-bearing: canonical
// double-quoted first (the common case), then single-quoted (rare,
// observed in some debug renders), then HTML-escaped (when the script
// block is rendered inside an attribute or escaped fragment).
//
// All three patterns tolerate backslash-escaped delimiters inside the
// value so JSON-style escapes parse correctly. The HTML-escaped
// variant captures everything between &quot; delimiters; because Go's
// RE2 engine does not support the (?!…) negative-lookahead the Python
// version uses, the Go variant guards the terminator at the &
// character and accepts any non-&quot; prefix at that position. This
// is slightly more permissive than the Python version (a stray
// ampersand inside the value would stop the capture early) but the
// canonical NotebookLM app shell never puts a bare & in token values.
//
// Source: notebooklm._auth.extraction._build_wiz_field_patterns.
func wizFieldPatterns(key string) []*regexp.Regexp {
	escaped := regexp.QuoteMeta(key)
	return []*regexp.Regexp{
		// 1. Canonical double-quoted: "key":"value"
		regexp.MustCompile(`"` + escaped + `"\s*:\s*"([^"\\]*(?:\\.[^"\\]*)*)"`),
		// 2. Single-quoted variant: 'key':'value'
		regexp.MustCompile(`'` + escaped + `'\s*:\s*'([^'\\]*(?:\\.[^'\\]*)*)'`),
		// 3. HTML-escaped: &quot;key&quot;:&quot;value&quot;
		//    The value run: characters that are not '&' or a
		//    leading-quote pair. Go's RE2 lacks negative
		//    lookahead, so we stop on the first '&' inside the
		//    value; a real NotebookLM token never contains one.
		regexp.MustCompile(`&quot;` + escaped + `&quot;\s*:\s*&quot;([^&]*?)&quot;`),
	}
}

// csrfPatterns and sessionPatterns are the pre-compiled variants used
// by ExtractWIZ. Built once at init time.
var (
	csrfPatterns    = wizFieldPatterns(csrfField)
	sessionPatterns = wizFieldPatterns(sessionField)
)

// ExtractWIZ returns (csrf, session, nil) on a clean extraction. Both
// fields are extracted independently: a page carrying SNlM0e but not
// FdrFJe still returns the CSRF token and a nil error so the caller
// can decide whether the missing field is worth surfacing.
//
// On a missing field ExtractWIZ invokes ClassifyFailure with the
// supplied finalURL (and an optional jar for future expansion) so
// the branch order is shared with the refresh ladder. The returned
// error wraps one of the four sentinels; callers switch on errors.Is.
//
// Source: notebooklm._auth.extraction.extract_csrf_from_html +
// extract_session_id_from_html. The Go version combines the two
// convenience wrappers into one call to keep the per-attempt boundary
// cheap.
//
// signals "from WIZ_global_data" while ExtractWIZ names the action.
//
//nolint:revive // stutter is intentional: the package name "extract"
func ExtractWIZ(html string) (csrf, session string, err error) {
	return ExtractWIZWithURL(html, "", nil)
}

// ExtractWIZWithURL is ExtractWIZ with an explicit final URL and
// cookie jar — used by the refresh ladder when it has the response
// chain in hand. Pass jar=nil if not available; the classifier is
// URL-only today.
//
//nolint:revive // see ExtractWIZ.
func ExtractWIZWithURL(html string, finalURL string, jar jarView) (csrf, session string, err error) {
	csrfFound := false
	sessionFound := false
	if csrf, csrfFound = extractField(html, csrfPatterns); !csrfFound {
		csrf = ""
	}
	if session, sessionFound = extractField(html, sessionPatterns); !sessionFound {
		session = ""
	}
	if csrfFound && sessionFound {
		return csrf, session, nil
	}
	// At least one field was missing — classify so the branch order
	// is shared with the refresh ladder.
	return "", "", fmt.Errorf("extract: %w", ClassifyFailure(html, jar, finalURL))
}

// extractField tries the ordered pattern list against html and
// returns the first match. The boolean is false when no pattern
// matched; the value is the empty string in that case. An empty
// capture group is a legitimate answer (some Google endpoints emit
// empty tokens), so the boolean is the only signal.
func extractField(html string, patterns []*regexp.Regexp) (string, bool) {
	for _, re := range patterns {
		m := re.FindStringSubmatch(html)
		if m == nil {
			continue
		}
		return m[1], true
	}
	return "", false
}
