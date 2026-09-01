// Tests for the RFC 6265 cookie jar.
//
// Coverage target: 80% on internal/auth/cookiejar per AGENTIC_LOOP §6.
// The suite is split into four groups:
//  1. Round-trip fuzz over 100k random cookie sets (AC1).
//  2. §5.4 selection table driven from testdata/selections.json (AC2).
//  3. Prefix and public-suffix rejection tables (AC4 + AC5).
//  4. HeaderFor redaction (AC6) plus a few contract tests for the
//     Cookie.String() redaction path and the All() snapshot.
//
// Every cookie value used in a test is a known sentinel ("VAL_…"); the
// assertion that a redacted log line contains "[REDACTED]" (not the
// raw sentinel) is the load-bearing AC6 check.
package cookiejar

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fixture is one row of testdata/selections.json. The shape mirrors
// what the test needs (a URL, a list of cookies to inject, and the
// expected outcome) — no schema field is unused.
type fixture struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	SetURL    string   `json:"set_url,omitempty"`
	Set       []fset   `json:"set"`
	Want      []string `json:"want"`
	WantValue string   `json:"want_value,omitempty"`
}

type fset struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
	Secure bool   `json:"secure"`
}

// loadSelections parses the §5.4 selection matrix from
// testdata/selections.json. Errors fail the test loudly so a typo'd
// fixture never silently passes.
//
// #nosec G304 -- path is operator-controlled test input, not user
// data. The fixture file is committed alongside the test.
func loadSelections(t *testing.T) []fixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "selections.json")) // #nosec G304 -- see comment above.
	if err != nil {
		t.Fatalf("read selections.json: %v", err)
	}
	var rows struct {
		Cases []fixture `json:"cases"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatalf("parse selections.json: %v", err)
	}
	if len(rows.Cases) == 0 {
		t.Fatalf("selections.json has no cases")
	}
	return rows.Cases
}

// runSelectionTable replays a single selection-table row against a
// fresh jar. It is the inner loop used by TestSelectionTable and is
// also a reusable helper for ad-hoc debug runs.
func runSelectionTable(t *testing.T, f fixture) {
	t.Helper()
	jar := New()
	u, err := url.Parse(f.URL)
	if err != nil {
		t.Fatalf("case %q: parse URL: %v", f.Name, err)
	}
	// set_url defaults to url; the override lets a case simulate
	// "cookie set at one URL, looked up at another" — the load-
	// bearing shape for the host-only and subdomain tests.
	setURL := f.URL
	if f.SetURL != "" {
		setURL = f.SetURL
	}
	setu, err := url.Parse(setURL)
	if err != nil {
		t.Fatalf("case %q: parse set_url %q: %v", f.Name, setURL, err)
	}
	host := setu.Hostname()
	stdCookies := make([]*http.Cookie, len(f.Set))
	for i, c := range f.Set {
		// Fixtures use the literal "<host>" to mean "the
		// Set-Cookie's host"; this lets the test express
		// domain-match intent without hard-coding every host. A
		// truly empty Domain stays empty (host-only cookie).
		d := c.Domain
		if d == "<host>" {
			d = host
		}
		stdCookies[i] = &http.Cookie{
			Name:   c.Name,
			Value:  c.Value,
			Domain: d,
			Path:   c.Path,
			Secure: c.Secure,
		}
	}
	jar.SetCookies(setu, stdCookies)
	got := jar.Cookies(u)

	gotNames := make([]string, len(got))
	for i, c := range got {
		gotNames[i] = c.Name
	}
	sort.Strings(gotNames)
	wantSorted := append([]string(nil), f.Want...)
	sort.Strings(wantSorted)

	if !stringSliceEq(gotNames, wantSorted) {
		t.Errorf("case %q: names = %v, want %v (set_url=%q url=%q)",
			f.Name, gotNames, wantSorted, setURL, f.URL)
		return
	}
	if f.WantValue != "" {
		var value string
		for _, c := range got {
			if c.Name == f.Set[0].Name {
				value = c.Value
				break
			}
		}
		if value != f.WantValue {
			t.Errorf("case %q: value = %q, want %q",
				f.Name, value, f.WantValue)
		}
	}
}

func TestSelectionTable(t *testing.T) {
	for _, f := range loadSelections(t) {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			runSelectionTable(t, f)
		})
	}
}

func TestRoundTripFuzz(t *testing.T) {
	const iterations = 100_000
	r := rand.New(rand.NewSource(0xC001E5)) // #nosec G404 -- fixed test seed, not a security primitive.
	jar := New()

	domains := []string{"example.com", "sub.example.com", "api.example.com",
		"notebooklm.google.com", "notebook.google.com", "accounts.google.com"}
	paths := []string{"/", "/a", "/a/b", "/a/b/c", "/api/v1"}
	names := []string{"sid", "osid", "1psid", "1psidts", "session", "pref", "tracking"}

	for i := 0; i < iterations; i++ {
		name := names[r.Intn(len(names))]
		domain := domains[r.Intn(len(domains))]
		path := paths[r.Intn(len(paths))]
		value := fmt.Sprintf("VAL_%d_%d", i, r.Intn(1<<30))

		u := &url.URL{
			Scheme:   "https",
			Host:     domain,
			Path:     path,
			RawQuery: "r=" + fmt.Sprint(i),
		}

		jar.SetCookies(u, []*http.Cookie{{
			Name:   name,
			Value:  value,
			Domain: domain,
			Path:   path,
		}})

		// Probe at a path that uniquely identifies this iteration:
		// append a query string that no earlier cookie can match.
		// We then filter the returned cookies to those whose Path
		// matches the just-set cookie, because a previous iteration
		// may have set a longer-path version that would (correctly)
		// win under RFC 6265 §5.4 — that is not a round-trip bug,
		// it is the documented selection rule.
		probe := &url.URL{
			Scheme: "https", Host: domain, Path: path,
			RawQuery: "r=" + fmt.Sprint(i),
		}
		got := jar.Cookies(probe)

		var actual string
		for _, c := range got {
			if c.Name != name {
				continue
			}
			if c.Path != path {
				continue
			}
			actual = c.Value
			break
		}
		if actual != value {
			t.Fatalf("iter %d: round-trip lost value for %s@%s%s: got %q want %q",
				i, name, domain, path, actual, value)
		}
	}
}

func TestSecureFilteredForHTTP(t *testing.T) {
	jar := New()
	jar.SetCookies(
		mustURL(t, "https://example.com/"),
		[]*http.Cookie{{
			Name: "secureone", Value: "v", Domain: "example.com",
			Path: "/", Secure: true,
		}},
	)
	got := jar.Cookies(mustURL(t, "http://example.com/"))
	if len(got) != 0 {
		t.Errorf("Secure cookie returned over http://: %v", got)
	}
	gotHttps := jar.Cookies(mustURL(t, "https://example.com/"))
	if len(gotHttps) != 1 {
		t.Errorf("Secure cookie missing over https://: %v", gotHttps)
	}
}

func TestPublicSuffixRejection(t *testing.T) {
	jar := New()
	jar.SetCookies(
		mustURL(t, "https://www.example.co.uk/"),
		[]*http.Cookie{{
			Name: "x", Value: "v",
			Domain: "co.uk",
			Path:   "/",
		}},
	)
	if jar.Len() != 0 {
		t.Errorf("public-suffix Domain cookie accepted: %v", jar.All())
	}
	// Direct ValidateHost call returns the typed error.
	c := &Cookie{Name: "x", Value: "v", Domain: "co.uk", Path: "/"}
	err := c.ValidateHost(mustURL(t, "https://www.example.co.uk/"))
	if !errors.Is(err, ErrPublicSuffix) {
		t.Errorf("ValidateHost err = %v, want ErrPublicSuffix", err)
	}
}

func TestPrefixViolations(t *testing.T) {
	cases := []struct {
		name     string
		cookie   *http.Cookie
		url      string
		wantErr  error
		accepted bool
	}{
		{
			name: "__Secure__on_http",
			cookie: &http.Cookie{
				Name: "__Secure-test", Value: "v",
				Domain: "example.com", Path: "/", Secure: true,
			},
			url:     "http://example.com/",
			wantErr: ErrInsecurePrefix,
		},
		{
			name: "__Secure__without_secure_flag",
			cookie: &http.Cookie{
				Name: "__Secure-test", Value: "v",
				Domain: "example.com", Path: "/", Secure: false,
			},
			url:     "https://example.com/",
			wantErr: ErrInsecurePrefix,
		},
		{
			name: "__Secure__valid",
			cookie: &http.Cookie{
				Name: "__Secure-test", Value: "v",
				Domain: "example.com", Path: "/", Secure: true,
			},
			url:      "https://example.com/",
			accepted: true,
		},
		{
			name: "__Host__on_http",
			cookie: &http.Cookie{
				Name: "__Host-test", Value: "v",
				Domain: "example.com", Path: "/", Secure: true,
			},
			url:     "http://example.com/",
			wantErr: ErrInsecurePrefix,
		},
		{
			name: "__Host__with_domain",
			cookie: &http.Cookie{
				Name: "__Host-test", Value: "v",
				Domain: "example.com", Path: "/", Secure: true,
			},
			url:     "https://example.com/",
			wantErr: ErrHostPrefixOnSubpath,
		},
		{
			name: "__Host__with_subpath",
			cookie: &http.Cookie{
				Name: "__Host-test", Value: "v",
				Domain: "", Path: "/foo", Secure: true,
			},
			url:     "https://example.com/",
			wantErr: ErrHostPrefixOnSubpath,
		},
		{
			name: "__Host__valid",
			cookie: &http.Cookie{
				Name: "__Host-test", Value: "v",
				Domain: "", Path: "/", Secure: true,
			},
			url:      "https://example.com/",
			accepted: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			jar := New()
			u := mustURL(t, tc.url)
			ours := fromStdlib(tc.cookie)
			gotErr := ours.ValidateHost(u)
			jar.SetCookies(u, []*http.Cookie{tc.cookie})
			if tc.accepted {
				if gotErr != nil {
					t.Errorf("expected acceptance, got err: %v", gotErr)
				}
				if jar.Len() != 1 {
					t.Errorf("accepted cookie not stored: %d", jar.Len())
				}
				return
			}
			if !errors.Is(gotErr, tc.wantErr) {
				t.Errorf("ValidateHost err = %v, want %v", gotErr, tc.wantErr)
			}
			if jar.Len() != 0 {
				t.Errorf("rejected cookie stored anyway: %d entries", jar.Len())
			}
		})
	}
}

func TestHeaderForRedaction(t *testing.T) {
	jar := New()
	u := mustURL(t, "https://example.com/")
	jar.SetCookies(u, []*http.Cookie{{
		Name:   "session",
		Value:  "SECRET_VALUE_42",
		Domain: "example.com",
		Path:   "/",
		Secure: true,
	}})
	got := jar.HeaderFor(u)
	if strings.Contains(got, "SECRET_VALUE_42") {
		t.Errorf("HeaderFor leaked credential: %q", got)
	}
	if !strings.Contains(got, "session=") {
		t.Errorf("HeaderFor missing cookie name: %q", got)
	}
	// The internal/redact.Apply rewriter substitutes "[REDACTED]";
	// the substring is stable and is the contract HeaderFor exposes.
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("HeaderFor missing redaction marker: %q", got)
	}
}

func TestCookieStringRedaction(t *testing.T) {
	c := &Cookie{Name: "session", Value: "SECRET_VALUE_42"}
	s := c.String()
	if strings.Contains(s, "SECRET_VALUE_42") {
		t.Errorf("Cookie.String leaked credential: %q", s)
	}
	if !strings.Contains(s, "session=") {
		t.Errorf("Cookie.String missing cookie name: %q", s)
	}
}

func TestExpiredCookiesAreNotReturned(t *testing.T) {
	jar := New()
	jar.setClock(func() time.Time {
		return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	})
	jar.SetCookies(
		mustURL(t, "https://example.com/"),
		[]*http.Cookie{{
			Name: "session", Value: "alive",
			Domain: "example.com", Path: "/",
			Expires: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		}, {
			Name: "stale", Value: "dead",
			Domain: "example.com", Path: "/",
			Expires: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}},
	)
	if jar.Len() != 1 {
		t.Errorf("expected 1 stored cookie, got %d", jar.Len())
	}
	got := jar.Cookies(mustURL(t, "https://example.com/"))
	if len(got) != 1 || got[0].Name != "session" {
		t.Errorf("Cookies returned %v, want [session]", got)
	}
}

func TestOSIDTwoHostsIndependent(t *testing.T) {
	jar := New()
	jar.SetCookies(
		mustURL(t, "https://notebooklm.google.com/"),
		[]*http.Cookie{{
			Name: "OSID", Value: "nlm_value",
			Domain: "notebooklm.google.com", Path: "/",
			Secure: true,
		}},
	)
	jar.SetCookies(
		mustURL(t, "https://notebook.google.com/"),
		[]*http.Cookie{{
			Name: "OSID", Value: "nb_value",
			Domain: "notebook.google.com", Path: "/",
			Secure: true,
		}},
	)
	gotNLM := jar.Cookies(mustURL(t, "https://notebooklm.google.com/"))
	gotNB := jar.Cookies(mustURL(t, "https://notebook.google.com/"))
	if len(gotNLM) != 1 || gotNLM[0].Value != "nlm_value" {
		t.Errorf("notebooklm.google.com got %v, want OSID=nlm_value", gotNLM)
	}
	if len(gotNB) != 1 || gotNB[0].Value != "nb_value" {
		t.Errorf("notebook.google.com got %v, want OSID=nb_value", gotNB)
	}
}

func TestAllSnapshotIsIndependent(t *testing.T) {
	jar := New()
	jar.SetCookies(
		mustURL(t, "https://example.com/"),
		[]*http.Cookie{{
			Name: "session", Value: "v1",
			Domain: "example.com", Path: "/",
		}},
	)
	snap := jar.All()
	if len(snap) != 1 {
		t.Fatalf("snapshot length = %d, want 1", len(snap))
	}
	snap[0].Value = "mutated"
	again := jar.All()
	if again[0].Value != "v1" {
		t.Errorf("All returned shared storage; mutation leaked: %v", again)
	}
}

func TestDeduplicationByIdentity(t *testing.T) {
	jar := New()
	u := mustURL(t, "https://example.com/")
	jar.SetCookies(u, []*http.Cookie{{
		Name: "session", Value: "first",
		Domain: "example.com", Path: "/",
	}})
	jar.SetCookies(u, []*http.Cookie{{
		Name: "session", Value: "second",
		Domain: "example.com", Path: "/",
	}})
	got := jar.Cookies(u)
	if len(got) != 1 {
		t.Fatalf("expected 1 cookie after dedup, got %d: %v", len(got), got)
	}
	if got[0].Value != "second" {
		t.Errorf("dedup winner = %q, want %q (last-wins)", got[0].Value, "second")
	}
}

// -- helpers -----------------------------------------------------------

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
