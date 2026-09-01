package wire

import (
	"strings"
	"testing"
)

// TestIsAllowedHost_TableDriven covers every allowed host and a battery
// of disallowed variants: wrong scheme, port, userinfo, path, query,
// fragment, an empty host, a host we explicitly do not serve from, and
// the canonical credential-bearing form.
func TestIsAllowedHost_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		allowed bool
	}{
		// Allowlist positives — the three hosts from doc 03.
		{"personal", "https://notebook.google.com", true},
		{"personal_legacy", "https://notebooklm.google.com", true},
		{"enterprise", "https://notebooklm.cloud.google.com", true},
		// Trailing slash should be tolerated.
		{"personal_trailing_slash", "https://notebook.google.com/", true},

		// Allowlist negatives — scheme.
		{"http_scheme", "http://notebook.google.com", false},
		{"ftp_scheme", "ftp://notebook.google.com", false},
		{"empty_scheme", "//notebook.google.com", false},

		// Allowlist negatives — port / userinfo.
		{"with_port", "https://notebook.google.com:443", false},
		{"with_userinfo", "https://user:pw@notebook.google.com", false},

		// Allowlist negatives — path / query / fragment.
		{"with_path", "https://notebook.google.com/foo", false},
		{"with_query", "https://notebook.google.com?x=1", false},
		{"with_fragment", "https://notebook.google.com#frag", false},

		// Allowlist negatives — unknown host.
		{"unknown_host", "https://example.com", false},
		{"looks_like_google", "https://notebook.google.com.evil.tld", false},
		{"empty_host", "https://", false},

		// Allowlist negatives — malformed URL.
		{"garbage", "not a url", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAllowedHost(tc.url)
			if got != tc.allowed {
				t.Fatalf("IsAllowedHost(%q) = %v, want %v", tc.url, got, tc.allowed)
			}
		})
	}
}

// TestAllowedHosts_ExactMatch asserts the published list is exactly the
// three hosts doc 03 enumerates, in any order. Adding a fourth host
// without updating this test is a deliberate "fix the test" signal.
func TestAllowedHosts_ExactMatch(t *testing.T) {
	want := map[Host]struct{}{
		HostPersonal:       {},
		HostPersonalLegacy: {},
		HostEnterprise:     {},
	}
	got := AllowedHosts()
	if len(got) != len(want) {
		t.Fatalf("AllowedHosts returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for _, h := range got {
		if _, ok := want[h]; !ok {
			t.Errorf("unexpected host %q in allowlist", h)
		}
	}
}

// TestBuilders_AllowlistedHosts asserts the three builders produce
// exactly the URLs doc 03 enumerates (path suffixes only; the host
// portion comes from the Host constant).
func TestBuilders_AllowlistedHosts(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"personal_batch", BatchexecuteURL(HostPersonal), "https://notebook.google.com" + BatchexecutePath},
		{"legacy_batch", BatchexecuteURL(HostPersonalLegacy), "https://notebooklm.google.com" + BatchexecutePath},
		{"enterprise_batch", BatchexecuteURL(HostEnterprise), "https://notebooklm.cloud.google.com" + BatchexecutePath},
		{"personal_stream", StreamedChatURL(HostPersonal), "https://notebook.google.com" + StreamedChatPath},
		{"personal_upload", UploadURL(HostPersonal), "https://notebook.google.com" + UploadPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("builder returned %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestBuilders_RejectNonAllowlistedHost confirms a typo in the host
// constant returns an empty string rather than building a URL we would
// then send credentials to.
func TestBuilders_RejectNonAllowlistedHost(t *testing.T) {
	bogus := Host("https://not-a-real-host.example.com")
	if got := BatchexecuteURL(bogus); got != "" {
		t.Errorf("BatchexecuteURL returned %q for non-allowlisted host, want empty", got)
	}
	if got := StreamedChatURL(bogus); got != "" {
		t.Errorf("StreamedChatURL returned %q for non-allowlisted host, want empty", got)
	}
	if got := UploadURL(bogus); got != "" {
		t.Errorf("UploadURL returned %q for non-allowlisted host, want empty", got)
	}
}

// TestEndpointPaths_MatchDoc03 protects the path constants from a
// silent "let's modernize the URL" edit — Google's batchexecute
// endpoint is a load-bearing string.
func TestEndpointPaths_MatchDoc03(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{BatchexecutePath, "/_/LabsTailwindUi/data/batchexecute"},
		{StreamedChatPath, "/_/LabsTailwindUi/data/google.internal.labs.tailwind.orchestration.v1.LabsTailwindOrchestrationService/GenerateFreeFormStreamed"},
		{UploadPath, "/upload/_/"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("path drift: got %q, want %q", tc.got, tc.want)
		}
	}
	// Sanity: every path starts with a single slash and contains no
	// scheme. The host portion is added by the builders.
	for _, p := range []string{BatchexecutePath, StreamedChatPath, UploadPath} {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("path %q does not start with /", p)
		}
		if strings.Contains(p, "://") {
			t.Errorf("path %q contains a scheme separator", p)
		}
	}
}
