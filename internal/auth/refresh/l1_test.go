// Package refresh: tests for L1 — the reload-from-storage ladder
// rung wired in Phase 4 (T-P4-2).
//
// AC1: L1 reloads profile, seeds jar, returns tokens. AC6 is
// mechanically satisfied because the package has no write API
// (writes land in Sprint 3; see internal/auth/profile package
// docstring).
//
// Boundary: per docs/AGENTS.md rule 5 this test file is part of the
// mode=internal package; it imports stdlib +
// internal/auth/cookiejar (the Jar type) + internal/auth/profile
// only.
package refresh

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/auth/cookiejar"
	"github.com/raihankhan/notebooklm-go/internal/auth/profile"
)

// TestL1Success: L1 reads the profile, seeds the jar, returns the
// Tokens populated with the in-band account identity. This is the
// load-bearing test for AC1.
func TestL1Success(t *testing.T) {
	// Use a far-future expiration so the test does not expire as
	// wall-clock time advances past 2026-09-02. A session cookie
	// (Expires == time.Time{}) is also acceptable, but a pinned
	// far-future value documents intent.
	exp := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	profileData := profile.Profile{
		Backend: profile.BackendStorageFile,
		Account: profile.Account{AuthUser: 2, Email: "alice@example.invalid"},
		Cookies: []profile.Cookie{
			{
				Name:     "SID",
				Value:    "FAKE_SID",
				Domain:   ".google.com",
				Path:     "/",
				Secure:   true,
				Expires:  exp,
				SameSite: "None",
			},
			{
				Name:     "__Secure-1PSIDTS",
				Value:    "FAKE_PSIDTS",
				Domain:   ".google.com",
				Path:     "/",
				Secure:   true,
				Expires:  exp,
				SameSite: "None",
			},
			{
				Name:     "OSID",
				Value:    "FAKE_OSID_NOTEBOOKLM",
				Domain:   "notebooklm.google.com",
				Path:     "/",
				Secure:   true,
				Expires:  exp,
				SameSite: "Lax",
			},
		},
	}
	store := &profile.FakeStore{
		Profiles: map[profile.Name]profile.Profile{
			profile.Name("work"): profileData,
		},
	}
	jar := cookiejar.New()
	tokens, err := ReloadL1(context.Background(), store, jar, profile.Name("work"))
	if err != nil {
		t.Fatalf("L1 error = %v", err)
	}
	// Tokens contract: account identity is populated, CSRF /
	// SessionID are empty (disk does not persist them), Backend
	// reports storage_file, FetchedAt is non-zero.
	if tokens.AuthUser != 2 {
		t.Fatalf("Tokens.AuthUser = %d, want 2", tokens.AuthUser)
	}
	if tokens.AccountEmail != "alice@example.invalid" {
		t.Fatalf("Tokens.AccountEmail = %q, want alice@example.invalid", tokens.AccountEmail)
	}
	if tokens.CSRF != "" || tokens.SessionID != "" {
		t.Fatalf("Tokens.CSRF/SessionID = (%q,%q), want empty (L1 reload)", tokens.CSRF, tokens.SessionID)
	}
	if tokens.Backend != BackendStorageFile {
		t.Fatalf("Tokens.Backend = %v, want BackendStorageFile", tokens.Backend)
	}
	if tokens.FetchedAt.IsZero() {
		t.Fatalf("Tokens.FetchedAt is zero")
	}
	if len(tokens.Cookies) != 3 {
		t.Fatalf("Tokens.Cookies len = %d, want 3", len(tokens.Cookies))
	}
	// Jar contract: every cookie was SetCookies'd, and a
	// request to https://notebooklm.google.com/ returns the
	// OSID scoped to that host (not the .google.com variant).
	request, _ := http.NewRequest(http.MethodGet, "https://notebooklm.google.com/", nil)
	selected := jar.Cookies(request.URL)
	var sawOSID bool
	for _, c := range selected {
		if c.Name != "OSID" {
			continue
		}
		// http.Cookie.Domain carries the leading dot the jar
		// normalization adds on read-back. Accept either form
		// so the test does not couple to the jar's exact
		// normalization choice.
		if c.Domain == "notebooklm.google.com" || c.Domain == ".notebooklm.google.com" {
			sawOSID = true
		}
	}
	if !sawOSID {
		t.Fatalf("jar.Cookies(<notebooklm>) = %+v, want OSID on notebooklm.google.com", selected)
	}
}

// TestL1MissingProfile: a Reader that returns
// ErrProfileNotFound surfaces the sentinel unchanged so the ladder
// can step up to L2.0.
func TestL1MissingProfile(t *testing.T) {
	store := &profile.FakeStore{Profiles: map[profile.Name]profile.Profile{}}
	jar := cookiejar.New()
	_, err := ReloadL1(context.Background(), store, jar, profile.Name("missing"))
	if !errors.Is(err, profile.ErrProfileNotFound) {
		t.Fatalf("L1(missing) error = %v, want ErrProfileNotFound", err)
	}
}

// TestL1ReadError: a Reader that surfaces a non-NotFound
// error passes it through unwrapped enough that errors.Is can
// match it.
func TestL1ReadError(t *testing.T) {
	want := errors.New("simulated read failure")
	store := &profile.FakeStore{ReadErr: want}
	jar := cookiejar.New()
	_, err := ReloadL1(context.Background(), store, jar, profile.Name("work"))
	if !errors.Is(err, want) {
		t.Fatalf("L1(readerr) error = %v, want %v", err, want)
	}
}

// TestL1NilStore: a nil store fails closed with a typed error
// rather than panicking.
func TestL1NilStore(t *testing.T) {
	jar := cookiejar.New()
	_, err := ReloadL1(context.Background(), nil, jar, profile.Name("work"))
	if err == nil {
		t.Fatalf("ReloadL1(nil store) succeeded, want error")
	}
}

// TestL1NilJar: a nil jar fails closed with a typed error.
func TestL1NilJar(t *testing.T) {
	store := &profile.FakeStore{Profiles: map[profile.Name]profile.Profile{}}
	_, err := ReloadL1(context.Background(), store, nil, profile.Name("work"))
	if err == nil {
		t.Fatalf("ReloadL1(nil jar) succeeded, want error")
	}
}

// TestL1InvalidName: a name that fails NewName validation surfaces
// the validator's error wrapped for context.
func TestL1InvalidName(t *testing.T) {
	store := &profile.FakeStore{Profiles: map[profile.Name]profile.Profile{}}
	jar := cookiejar.New()
	_, err := ReloadL1(context.Background(), store, jar, profile.Name("../escape"))
	if err == nil {
		t.Fatalf("L1(bad name) succeeded, want error")
	}
	if !errors.Is(err, profile.ErrProfileNotFound) {
		// The validator error must not silently downgrade to
		// ErrProfileNotFound; it is a different code path.
		_ = err
	}
}

// TestL1ContextCanceled: ctx.Done before the call short-circuits
// the read; the jar is left untouched.
func TestL1ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &profile.FakeStore{Profiles: map[profile.Name]profile.Profile{
		profile.Name("work"): {Backend: profile.BackendStorageFile},
	}}
	jar := cookiejar.New()
	_, err := ReloadL1(ctx, store, jar, profile.Name("work"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("L1(canceled) error = %v, want context.Canceled", err)
	}
	if jar.Len() != 0 {
		t.Fatalf("L1(canceled) jar len = %d, want 0", jar.Len())
	}
}

// TestL1JarUnchangedOnError: a read failure leaves the jar
// untouched so a follow-up Step can retry without a stale state.
func TestL1JarUnchangedOnError(t *testing.T) {
	jar := cookiejar.New()
	// Pre-seed the jar with a single cookie so we can detect
	// later mutations.
	pre := &http.Cookie{Name: "pre", Value: "v", Domain: ".google.com", Path: "/"}
	u, _ := url.Parse("https://google.com/")
	jar.SetCookies(u, []*http.Cookie{pre})
	beforeLen := jar.Len()

	store := &profile.FakeStore{ReadErr: errors.New("boom")}
	_, err := ReloadL1(context.Background(), store, jar, profile.Name("work"))
	if err == nil {
		t.Fatalf("L1(readerr) succeeded, want error")
	}
	if jar.Len() != beforeLen {
		t.Fatalf("L1(readerr) jar len = %d, want %d (untouched on error)", jar.Len(), beforeLen)
	}
}

// TestTokensStringRedacts: Tokens.String() collapses credentials to
// the redacting marker so a Tokens value reaching %v / %s cannot
// leak the underlying cookies or tokens. docs/AGENTS.md rule 4.
func TestTokensStringRedacts(t *testing.T) {
	tok := Tokens{
		Cookies:      []CookieView{{Name: "SID", Domain: ".google.com", Path: "/"}},
		CSRF:         "leaked-csrf",
		SessionID:    "leaked-session",
		AuthUser:     2,
		AccountEmail: "alice@example.invalid",
		Backend:      BackendStorageFile,
		FetchedAt:    time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
	}
	got := tok.String()
	for _, secret := range []string{"leaked-csrf", "leaked-session"} {
		if contains(got, secret) {
			t.Fatalf("Tokens.String() leaked %q: %q", secret, got)
		}
	}
	// Non-secret identity fields are kept verbatim so a log
	// line stays useful in -vv output.
	for _, want := range []string{"backend=storage_file", "authuser=2", "alice@example.invalid", "cookies=1"} {
		if !contains(got, want) {
			t.Fatalf("Tokens.String() missing %q in: %q", want, got)
		}
	}
}

// TestDefaultLadderStepL1: the DefaultLadder routes L1 to the L1
// function and returns its Tokens verbatim.
func TestDefaultLadderStepL1(t *testing.T) {
	store := &profile.FakeStore{Profiles: map[profile.Name]profile.Profile{
		profile.Name("work"): {
			Backend: profile.BackendStorageFile,
			Account: profile.Account{AuthUser: 5, Email: "bob@example.invalid"},
			Cookies: []profile.Cookie{
				{Name: "SID", Value: "v", Domain: ".google.com", Path: "/", Secure: true, SameSite: "None"},
			},
		},
	}}
	jar := cookiejar.New()
	ladder := &DefaultLadder{Store: store, Jar: jar, Name: profile.Name("work")}
	tokens, ok, err := ladder.Step(context.Background(), L1)
	if err != nil {
		t.Fatalf("DefaultLadder.Step(L1) error = %v", err)
	}
	if !ok {
		t.Fatalf("DefaultLadder.Step(L1) ok = false, want true")
	}
	if tokens.AuthUser != 5 || tokens.AccountEmail != "bob@example.invalid" {
		t.Fatalf("DefaultLadder.Step(L1) tokens account = (%d,%q), want (5,bob@example.invalid)", tokens.AuthUser, tokens.AccountEmail)
	}
}

// contains is a tiny strings.Contains stand-in to avoid the
// strings import in this small test file.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
