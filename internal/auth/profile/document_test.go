// Package profile: tests for the typed Profile value, the Name
// validator, the Backend enum, and the redacting String methods on
// Cookies and Tokens.
//
// Boundary: per docs/AGENTS.md rule 5 this test file is part of the
// mode=internal package; it imports stdlib only.
package profile

import (
	"strings"
	"testing"
	"time"
)

// AC5: Name validator — accepts the canonical profile names, rejects
// the unsafe ones. Mirrors the Python
// notebooklm._auth.paths.resolve_profile contract.
func TestNewName(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"default", false},
		{"work", false},
		{"alice", false},
		{"profile-1", false},
		{"", true},
		{".", true},
		{"..", true},
		{".hidden", true},
		{"../escape", true},
		{"with/slash", true},
		{"with\\backslash", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			n, err := NewName(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewName(%q) expected error, got %q", tc.in, n)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewName(%q) unexpected error: %v", tc.in, err)
			}
			if n.String() != tc.in {
				t.Fatalf("NewName(%q).String() = %q, want %q", tc.in, n.String(), tc.in)
			}
		})
	}
}

// AC5: Backend.String returns the canonical label so a log line can
// surface the backend without leaking credential material.
func TestBackendString(t *testing.T) {
	cases := []struct {
		b    Backend
		want string
	}{
		{BackendUnknown, "unknown"},
		{BackendStorageFile, "storage_file"},
		{BackendInlineEnv, "inline_env"},
		{Backend(99), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.b.String(); got != tc.want {
				t.Fatalf("Backend(%d).String() = %q, want %q", tc.b, got, tc.want)
			}
		})
	}
}

// AC5: Cookie.String() must redact the value. Docs/AGENTS.md rule 4
// forbids a literal cookie value reaching any log/error sink.
func TestCookieStringRedacts(t *testing.T) {
	c := Cookie{Name: "SID", Value: "leaked-secret-value"}
	got := c.String()
	if !strings.Contains(got, "SID=") {
		t.Fatalf("Cookie.String() = %q, want it to begin with SID=", got)
	}
	if strings.Contains(got, "leaked-secret-value") {
		t.Fatalf("Cookie.String() leaked the value: %q", got)
	}
	// An empty-named cookie collapses to the redact marker so a
	// malformed row never accidentally surfaces a name it does not
	// own.
	if (Cookie{}).String() != redactedToken {
		t.Fatalf("Cookie{}.String() = %q, want %q", (Cookie{}).String(), redactedToken)
	}
}

// AC5: Tokens.String() redacts CSRF and SessionID; keeps the
// non-secret identity fields verbatim.
func TestTokensStringRedacts(t *testing.T) {
	tok := Tokens{
		CSRF:         "leaked-csrf",
		SessionID:    "leaked-session",
		AuthUser:     2,
		AccountEmail: "alice@example.invalid",
		FetchedAt:    time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
	}
	got := tok.String()
	if strings.Contains(got, "leaked-csrf") || strings.Contains(got, "leaked-session") {
		t.Fatalf("Tokens.String() leaked a secret: %q", got)
	}
	if !strings.Contains(got, "authuser=2") {
		t.Fatalf("Tokens.String() = %q, want authuser=2 visible", got)
	}
	if !strings.Contains(got, "alice@example.invalid") {
		t.Fatalf("Tokens.String() = %q, want account email visible", got)
	}
	// Zero FetchedAt renders as "zero" so a Profile that never
	// fetched is distinguishable from one that fetched at the
	// epoch.
	var zero Tokens
	if !strings.Contains(zero.String(), "fetched_at=zero") {
		t.Fatalf("Tokens{}.String() = %q, want fetched_at=zero", zero.String())
	}
}

// AC5: Profile.String() redacts cookies via the count, keeps the
// profile identity.
func TestProfileStringRedacts(t *testing.T) {
	p := Profile{
		Name:      Name("work"),
		Backend:   BackendStorageFile,
		Cookies:   make([]Cookie, 3),
		Account:   Account{AuthUser: 0, Email: "alice@example.invalid"},
		Tokens:    Tokens{CSRF: "leaked", SessionID: "leaked"},
		Options:   Options{IncludeOptional: true},
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got := p.String()
	for _, secret := range []string{"leaked", "[REDACTED]"} {
		// we want a redact marker for secrets, but no literal
		// "leaked" string in the output.
		_ = secret
	}
	if strings.Contains(got, "leaked") {
		t.Fatalf("Profile.String() leaked a secret: %q", got)
	}
	if !strings.Contains(got, `name="work"`) {
		t.Fatalf("Profile.String() = %q, want name=\"work\"", got)
	}
	if !strings.Contains(got, "backend=storage_file") {
		t.Fatalf("Profile.String() = %q, want backend=storage_file", got)
	}
	if !strings.Contains(got, "cookies=3") {
		t.Fatalf("Profile.String() = %q, want cookies=3", got)
	}
	if !strings.Contains(got, "alice@example.invalid") {
		t.Fatalf("Profile.String() = %q, want account email visible", got)
	}
}
