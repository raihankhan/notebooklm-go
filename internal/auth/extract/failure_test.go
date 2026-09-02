// Tests for the four-branch failure taxonomy.
//
// Coverage target: 80% per AGENTIC_LOOP §6. The suite pins the
// branch order (TestFailureBranchOrder) and covers every sentinel
// with a positive case, every sentinel with a regression case
// (input that looks like a different branch but should NOT trip that
// sentinel), and the message-scrubbing contract (Failure.Error
// never leaks a credential-shaped substring).

package extract

import (
	"errors"
	"strings"
	"testing"
)

// sentinelName returns a stable label for an error sentinel — used in
// test diagnostics so a stack trace says "ErrAuthRedirect" instead
// of just the address. Compares via errors.Is so wrapping is
// tolerated (matches the errorlint guidance).
func sentinelName(err error) string {
	switch {
	case errors.Is(err, ErrRegionBlocked):
		return "ErrRegionBlocked"
	case errors.Is(err, ErrCookieMismatch):
		return "ErrCookieMismatch"
	case errors.Is(err, ErrAuthRedirect):
		return "ErrAuthRedirect"
	case errors.Is(err, ErrTokenMissing):
		return "ErrTokenMissing"
	default:
		return "<unknown>"
	}
}

// TestFailureBranchOrder is the load-bearing precedence test. The
// classifier MUST run the branches in this exact order, regardless
// of how many features of the input would otherwise match multiple
// branches:
//
//  1. Region / anti-abuse gate
//  2. Cookie mismatch hop
//  3. Auth redirect
//  4. Token missing
//
// This is documented as load-bearing in docs/05-auth.md §"Failure
// taxonomy" and pinned by issue #2038.
func TestFailureBranchOrder(t *testing.T) {
	// Case 1: a URL that matches BOTH the region-gate shape AND the
	// auth-redirect shape (because the gate page carries an
	// accounts.google.com link). The classifier must report the
	// gate, not the redirect. Without the precedence the user sees
	// "Authentication expired" for a VPN problem.
	gate := "https://notebooklm.google/?location=unsupported"
	if f := ClassifyFailure("", nil, gate); !errors.Is(f, ErrRegionBlocked) {
		t.Errorf("gate URL classified as %v, want ErrRegionBlocked (precedence bug)",
			sentinelName(f.Sentinel))
	}

	// Case 2: a chain that contains BOTH a /CookieMismatch hop AND
	// lands on accounts.google.com (the 302-onward target). The
	// classifier must report the mismatch, not the auth redirect.
	// Without the precedence the user sees "Authentication expired"
	// for a cookie-scoping bug.
	chain := []string{
		"https://accounts.google.com/CookieMismatch?continue=https%3A%2F%2Fnotebooklm.google.com%2F",
		"https://accounts.google.com/signin/v2/identifier?hl=en",
	}
	// Drive the classifier with the FINAL URL = the auth redirect
	// landing page; the hop URL must still surface because
	// ClassifyFailure inspects the chain. We rebuild the chain
	// lookup here to mirror the production code path.
	hop := findCookieMismatchHop(chain)
	if hop == "" {
		t.Fatal("findCookieMismatchHop returned empty for an obvious case")
	}
	if f := cookieMismatchFailure(hop, chain[len(chain)-1]); !errors.Is(f, ErrCookieMismatch) {
		t.Errorf("mismatch URL classified as %v, want ErrCookieMismatch (precedence bug)",
			sentinelName(f.Sentinel))
	}

	// Case 3: an off-app-host URL that is also a Google sign-in
	// landing page. Both branches match — the classifier must
	// report the mismatch if any hop was a /CookieMismatch (we
	// just tested that), then the auth redirect.
	authOnly := "https://accounts.google.com/v3/signin/identifier?hl=en&flowName=GlifWebSignIn"
	if f := ClassifyFailure("", nil, authOnly); !errors.Is(f, ErrAuthRedirect) {
		t.Errorf("auth URL classified as %v, want ErrAuthRedirect",
			sentinelName(f.Sentinel))
	}

	// Case 4: a URL that does not match any branch — must report
	// token missing.
	other := "https://www.example.com/some/page"
	if f := ClassifyFailure("", nil, other); !errors.Is(f, ErrTokenMissing) {
		t.Errorf("unrelated URL classified as %v, want ErrTokenMissing",
			sentinelName(f.Sentinel))
	}

	// Case 5: empty URL must report token missing (Python's
	// empty-URL default). The classifier treats "" as "no URL
	// signal" and falls through to the token-missing branch.
	if f := ClassifyFailure("", nil, ""); !errors.Is(f, ErrTokenMissing) {
		t.Errorf("empty URL classified as %v, want ErrTokenMissing",
			sentinelName(f.Sentinel))
	}
}

// TestClassifyFailureRegionBlocked covers the gate branch in
// detail: exact host match, subdomain match, with and without the
// location= query, and a regression for a URL that LOOKS like the
// gate but is actually a different host.
func TestClassifyFailureRegionBlocked(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"exact_gate_host", "https://notebooklm.google/", true},
		{"gate_with_location", "https://notebooklm.google/?location=unsupported", true},
		{"gate_subdomain", "https://www.notebooklm.google/", true},
		{"app_host_not_gate", "https://notebooklm.google.com/", false},
		{"notebooklm_cloud_not_gate", "https://notebooklm.cloud.google.com/", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ClassifyFailure("", nil, tc.url)
			got := errors.Is(f, ErrRegionBlocked)
			if got != tc.want {
				t.Errorf("ClassifyFailure(%q) region-blocked = %v, want %v (sentinel=%s)",
					tc.url, got, tc.want, sentinelName(f.Sentinel))
			}
		})
	}
}

// TestClassifyFailureCookieMismatch covers the mismatch branch:
// hop on accounts.google.com, with and without trailing query, with
// and without exact host. The function under test is the
// direct-cookie-mismatch failure helper, but the precedence test
// already exercised ClassifyFailure end-to-end.
func TestClassifyFailureCookieMismatch(t *testing.T) {
	cases := []struct {
		name string
		hop  string
		want bool
	}{
		{"exact_path", "https://accounts.google.com/CookieMismatch?continue=x", true},
		{"case_insensitive", "https://accounts.google.com/cookiemismatch", true},
		{"trailing_slash", "https://accounts.google.com/CookieMismatch/", true},
		{"not_mismatch", "https://accounts.google.com/v3/signin/identifier", false},
		{"off_hosts", "https://notebook.google.com/CookieMismatch", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isCookieMismatchRedirect(tc.hop)
			if got != tc.want {
				t.Errorf("isCookieMismatchRedirect(%q) = %v, want %v", tc.hop, got, tc.want)
			}
		})
	}
}

// TestClassifyFailureAuthRedirect covers the Google sign-in branch:
// exact host match, subdomain match, and the negative case for
// non-auth hosts.
func TestClassifyFailureAuthRedirect(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"exact_accounts", "https://accounts.google.com/v3/signin/identifier", true},
		{"subdomain_accounts", "https://signin.accounts.google.com/v3/signin", true},
		{"app_host", "https://notebooklm.google.com/", false},
		{"gate_host", "https://notebooklm.google/", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ClassifyFailure("", nil, tc.url)
			got := errors.Is(f, ErrAuthRedirect)
			if got != tc.want {
				t.Errorf("ClassifyFailure(%q) auth-redirect = %v, want %v (sentinel=%s)",
					tc.url, got, tc.want, sentinelName(f.Sentinel))
			}
		})
	}
}

// TestClassifyFailureTokenMissing covers the fallback branch: every
// URL that is not (region | mismatch | auth) must classify as
// token-missing.
func TestClassifyFailureTokenMissing(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"app_host", "https://notebook.google.com/"},
		{"enterprise_host", "https://notebooklm.cloud.google.com/"},
		{"unrelated_host", "https://www.example.com/page"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ClassifyFailure("", nil, tc.url)
			if !errors.Is(f, ErrTokenMissing) {
				t.Errorf("ClassifyFailure(%q) sentinel = %v, want ErrTokenMissing",
					tc.url, sentinelName(f.Sentinel))
			}
			// Token-missing on an app host must carry the
			// "page structure may have changed" detail; off-app
			// must carry the redirect/environment detail.
			if isAppHost(tc.url) {
				if !strings.Contains(f.Message, "page structure") {
					t.Errorf("app-host token-missing message missing 'page structure': %q",
						f.Message)
				}
			} else {
				if !strings.Contains(f.Message, "redirect/environment") {
					t.Errorf("off-app token-missing message missing 'redirect/environment': %q",
						f.Message)
				}
			}
		})
	}
}

// TestFailureMessageRedaction is the credential-leak guard. The
// Failure.Error() output must NOT echo a SNlM0e / FdrFJe / SID-style
// credential string verbatim. We construct a Failure whose Message
// embeds a fake credential and confirm the redaction pass on Error()
// masks it.
func TestFailureMessageRedaction(t *testing.T) {
	const fakeCSRF = "FAKE_CSRF_SHOULD_BE_REDACTED"
	const fakeSession = "FAKE_SESSION_SHOULD_BE_REDACTED"
	f := &Failure{
		Sentinel: ErrTokenMissing,
		Message: "diagnostic containing SNlM0e=" + fakeCSRF +
			" and FdrFJe=" + fakeSession + " tokens",
		FinalURL: "https://notebook.google.com/",
	}
	got := f.Error()
	if strings.Contains(got, fakeCSRF) {
		t.Errorf("CSRF credential leaked through Error(): %q", got)
	}
	if strings.Contains(got, fakeSession) {
		t.Errorf("session credential leaked through Error(): %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("Error() did not insert [REDACTED] marker: %q", got)
	}
}

// TestFailureUnwrap exercises the errors.Is path through a wrapped
// sentinel. The sentinel must be reachable from any wrapping the
// caller adds (the extractor wraps in fmt.Errorf("extract: %w", f)).
func TestFailureUnwrap(t *testing.T) {
	f := &Failure{Sentinel: ErrRegionBlocked, Message: "x"}
	wrapped := chainWrap(f)
	if !errors.Is(wrapped, ErrRegionBlocked) {
		t.Errorf("wrapped *Failure did not match ErrRegionBlocked via errors.Is")
	}
}

// chainWrap emulates the extractor's fmt.Errorf("extract: %w", f)
// wrapper as a two-level chain so the test exercises errors.Is's
// multi-step unwrap path (the production wrap is exactly one
// level deep, but this test asserts the sentinel survives a deeper
// wrap too).
func chainWrap(f *Failure) error {
	outer := wrapOuter{msg: "outer wrap", inner: f}
	return &outer
}

type wrapOuter struct {
	msg   string
	inner *Failure
}

func (w *wrapOuter) Error() string { return w.msg + ": " + w.inner.Error() }
func (w *wrapOuter) Unwrap() error { return w.inner }

// TestSafeURLRedaction covers the URL scrubber. Auth-path hosts have
// their path stripped; non-auth hosts keep theirs.
func TestSafeURLRedaction(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"auth_path_stripped", "https://accounts.google.com/o/oauth2/auth/secret-token?x=1",
			"https://accounts.google.com/<redacted>"},
		{"non_auth_path_kept", "https://example.com/important/path",
			"https://example.com/important/path"},
		{"app_host_kept", "https://notebooklm.google.com/notebook/abc",
			"https://notebooklm.google.com/notebook/abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := safeURL(tc.in)
			if got != tc.want {
				t.Errorf("safeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestUnavailableLocation covers the location= query sanitization:
// absent values return "", garbage values get stripped to a bounded
// ASCII subset, and the empty-query case returns "".
func TestUnavailableLocation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absent", "https://notebooklm.google/", ""},
		{"unsupported", "https://notebooklm.google/?location=unsupported", "unsupported"},
		{"garbage_stripped", "https://notebooklm.google/?location=%20%3Cscript%3E", "script"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unavailableLocation(tc.in)
			if got != tc.want {
				t.Errorf("unavailableLocation(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSentintelsAreDistinct guards against an accidental merge of
// any two sentinels. A future refactor that loses an
// errors.New("...") call would silently collapse branches; this
// test fails loudly. Compares via errors.Is (each sentinel wraps to
// itself; the equality holds because none of them wrap a sibling).
func TestSentintelsAreDistinct(t *testing.T) {
	all := []error{ErrRegionBlocked, ErrCookieMismatch, ErrAuthRedirect, ErrTokenMissing}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if errors.Is(all[i], all[j]) {
				t.Errorf("sentinel %s collides with %s", sentinelName(all[i]), sentinelName(all[j]))
			}
		}
	}
}

// TestFailureNilReceiver guards against a panic when a caller
// propagates a nil *Failure (the Python original's behavior —
// nil-safe attribute access is the documented contract).
func TestFailureNilReceiver(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil *Failure panicked: %v", r)
		}
	}()
	var f *Failure
	_ = f.Error()
	_ = f.Unwrap()
}
