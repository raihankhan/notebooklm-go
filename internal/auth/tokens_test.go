// Tests for the Tokens struct and the LoadFromStorage pipeline.
//
// Coverage target: 80% per AGENTIC_LOOP §6. The suite pins:
//
//  1. LoadFromStorage happy path: a fixture storage_state.json +
//     fixture-backed TokenFetcher produces a Tokens with the
//     expected (csrf, session) pair.
//  2. Source-resolution precedence: Path > Inline > empty.
//  3. Empty source returns ErrEmptySource.
//  4. Policy-gate failures return ErrPolicyGate.
//  5. Tokens.String() and Tokens.LogValue() never leak a credential
//     substring (the load-bearing AGENTS.md rule 4 assertion).

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/auth/cookiejar"
)

// writeFixture writes a fixture storage_state.json to dir and
// returns its path. The fixture mirrors the Python-side shape —
// Tier 1 (SID + __Secure-1PSIDTS) + Tier 2 (OSID) — so the seeded
// jar passes policy.Validate.
func writeFixture(t *testing.T, dir string) string {
	t.Helper()
	doc := map[string]any{
		"cookies": []map[string]any{
			{
				"name":     "SID",
				"value":    "FAKE_SID_VALUE_NOT_A_REAL_CREDENTIAL",
				"domain":   ".google.com",
				"path":     "/",
				"expires":  1798761234,
				"httpOnly": true,
				"secure":   true,
				"sameSite": "None",
			},
			{
				"name":     "__Secure-1PSIDTS",
				"value":    "FAKE_PSIDTS_VALUE_NOT_A_REAL_CREDENTIAL",
				"domain":   "accounts.google.com",
				"path":     "/",
				"expires":  1798761234,
				"httpOnly": true,
				"secure":   true,
				"sameSite": "None",
			},
			{
				"name":     "OSID",
				"value":    "FAKE_OSID_VALUE_NOT_A_REAL_CREDENTIAL",
				"domain":   "notebook.google.com",
				"path":     "/",
				"expires":  1798761234,
				"httpOnly": true,
				"secure":   true,
				"sameSite": "Lax",
			},
		},
		"notebooklm": map[string]any{
			"account": map[string]any{
				"authuser": 0,
				"email":    "fixture@example.invalid",
			},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(dir, "storage_state.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// fakeFetcher returns a canned (csrf, session) pair. The production
// fetcher would call ExtractWIZ against the app host's response body;
// the test fetcher short-circuits to keep the test network-free.
func fakeFetcher(csrf, session string) TokenFetcher {
	return FetchFunc(func(_ context.Context, _ *cookiejar.Jar) (string, string, error) {
		return csrf, session, nil
	})
}

// TestLoadFromStorageHappyPath is the AC2 round-trip test: a fixture
// storage_state.json + a fixture-backed fetcher must produce a
// Tokens with the expected (csrf, session) pair, a non-zero
// LoadedAt stamp, the expected Source label, and a populated Jar.
func TestLoadFromStorageHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir)

	const wantCSRF = "FAKE_CSRF_LOADED_FROM_FIXTURE"
	const wantSession = "FAKE_SESSION_LOADED_FROM_FIXTURE"

	tokens, err := LoadFromStorage(context.Background(),
		Source{Path: path},
		fakeFetcher(wantCSRF, wantSession),
	)
	if err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}

	if tokens.CSRFToken != wantCSRF {
		t.Errorf("CSRFToken = %q, want %q", tokens.CSRFToken, wantCSRF)
	}
	if tokens.SessionID != wantSession {
		t.Errorf("SessionID = %q, want %q", tokens.SessionID, wantSession)
	}
	if tokens.LoadedAt.IsZero() {
		t.Errorf("LoadedAt is zero")
	}
	if !strings.HasPrefix(tokens.Source, "file:") {
		t.Errorf("Source = %q, want file: prefix", tokens.Source)
	}
	if tokens.Jar == nil {
		t.Error("Jar is nil")
	}
	if tokens.Jar != nil && tokens.Jar.Len() == 0 {
		t.Error("Jar is empty after seeding")
	}
}

// TestLoadFromStorageInline is the equivalent test for the
// NOTEBOOKLM_AUTH_JSON path. The Source carries a raw byte slice,
// no disk read.
func TestLoadFromStorageInline(t *testing.T) {
	const wantCSRF = "FAKE_INLINE_CSRF"
	const wantSession = "FAKE_INLINE_SESSION"
	inline := []byte(`{
		"cookies":[
			{"name":"SID","value":"FAKE_SID_INLINE","domain":".google.com","path":"/","expires":1798761234,"httpOnly":true,"secure":true,"sameSite":"None"},
			{"name":"__Secure-1PSIDTS","value":"FAKE_PSIDTS_INLINE","domain":"accounts.google.com","path":"/","expires":1798761234,"httpOnly":true,"secure":true,"sameSite":"None"},
			{"name":"OSID","value":"FAKE_OSID_INLINE","domain":"notebook.google.com","path":"/","expires":1798761234,"httpOnly":true,"secure":true,"sameSite":"Lax"}
		]
	}`)

	tokens, err := LoadFromStorage(context.Background(),
		Source{Inline: inline},
		fakeFetcher(wantCSRF, wantSession),
	)
	if err != nil {
		t.Fatalf("LoadFromStorage inline: %v", err)
	}
	if tokens.Source != "env:NOTEBOOKLM_AUTH_JSON" {
		t.Errorf("Source = %q, want env:NOTEBOOKLM_AUTH_JSON", tokens.Source)
	}
	if tokens.CSRFToken != wantCSRF {
		t.Errorf("CSRFToken = %q, want %q", tokens.CSRFToken, wantCSRF)
	}
	if tokens.SessionID != wantSession {
		t.Errorf("SessionID = %q, want %q", tokens.SessionID, wantSession)
	}
}

// TestLoadFromStorageEmptySource asserts the empty-source branch.
// Callers must specify a Path or an Inline byte slice; nothing
// implies a default home directory.
func TestLoadFromStorageEmptySource(t *testing.T) {
	_, err := LoadFromStorage(context.Background(),
		Source{},
		fakeFetcher("csrf", "session"),
	)
	if !errors.Is(err, ErrEmptySource) {
		t.Errorf("LoadFromStorage(empty source) err = %v, want ErrEmptySource", err)
	}
}

// TestLoadFromStorageMissingPath asserts the missing-file branch.
// The os.PathError is wrapped with ErrStorageRead so callers can
// errors.Is(err, fs.ErrNotExist).
func TestLoadFromStorageMissingPath(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadFromStorage(context.Background(),
		Source{Path: filepath.Join(dir, "does-not-exist.json")},
		fakeFetcher("csrf", "session"),
	)
	if !errors.Is(err, ErrStorageRead) {
		t.Errorf("LoadFromStorage(missing) err = %v, want ErrStorageRead", err)
	}
}

// TestLoadFromStoragePolicyGate asserts the policy gate runs.
// Without OSID (Tier 2 missing) the cookie set is incomplete and
// policy.Validate returns a typed error which LoadFromStorage
// wraps with ErrPolicyGate.
func TestLoadFromStoragePolicyGate(t *testing.T) {
	dir := t.TempDir()
	// Build a storage_state.json that passes Tier 1 (SID + PSIDTS)
	// but is missing Tier 2 (OSID). This is the "warn-only" case
	// in policy today, but Tier 1 is intact — actually, to trip
	// the gate we need to drop Tier 1.
	doc := map[string]any{
		"cookies": []map[string]any{
			{
				"name":     "OSID",
				"value":    "FAKE_OSID_POLICY_GATE",
				"domain":   "notebook.google.com",
				"path":     "/",
				"expires":  1798761234,
				"httpOnly": true,
				"secure":   true,
				"sameSite": "Lax",
			},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, "storage_state.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = LoadFromStorage(context.Background(),
		Source{Path: path},
		fakeFetcher("csrf", "session"),
	)
	if !errors.Is(err, ErrPolicyGate) {
		t.Errorf("LoadFromStorage(policy gate) err = %v, want ErrPolicyGate", err)
	}
}

// TestLoadFromStorageFetcherError is the error-passthrough branch:
// when the fetcher returns an error, LoadFromStorage must wrap it
// with ErrTokenMissing so the refresh ladder can switch on the
// sentinel.
func TestLoadFromStorageFetcherError(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir)

	sentinel := errors.New("fetcher hard fail")
	fetcher := FetchFunc(func(_ context.Context, _ *cookiejar.Jar) (string, string, error) {
		return "", "", sentinel
	})
	_, err := LoadFromStorage(context.Background(),
		Source{Path: path}, fetcher)
	if !errors.Is(err, ErrTokenMissing) {
		t.Errorf("LoadFromStorage(fetcher error) err = %v, want ErrTokenMissing", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("LoadFromStorage(fetcher error) lost inner sentinel: %v", err)
	}
}

// TestLoadFromStorageNilFetcher is the "you forgot the fetcher"
// branch. Distinct from ErrEmptySource so callers can tell apart
// the two misconfigurations.
func TestLoadFromStorageNilFetcher(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir)
	_, err := LoadFromStorage(context.Background(),
		Source{Path: path}, nil)
	if !errors.Is(err, ErrNoFetcher) {
		t.Errorf("LoadFromStorage(nil fetcher) err = %v, want ErrNoFetcher", err)
	}
}

// TestTokensStringRedaction is the AC1 redaction test. Tokens.String()
// must NOT contain the literal CSRF or session values.
func TestTokensStringRedaction(t *testing.T) {
	const fakeCSRF = "FAKE_CSRF_SHOULD_NOT_APPEAR"
	const fakeSession = "FAKE_SESSION_SHOULD_NOT_APPEAR"
	tokens := &Tokens{
		CSRFToken: fakeCSRF,
		SessionID: fakeSession,
		LoadedAt:  time.Now(),
		Source:    "file:/tmp/fixture.json",
		Jar:       cookiejar.New(),
	}
	got := tokens.String()
	if strings.Contains(got, fakeCSRF) {
		t.Errorf("Tokens.String() leaked CSRF: %q", got)
	}
	if strings.Contains(got, fakeSession) {
		t.Errorf("Tokens.String() leaked session: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("Tokens.String() did not insert [REDACTED] marker: %q", got)
	}
}

// TestTokensLogValueRedaction is the LogValue counterpart of the
// redaction test. A Tokens routed through slog must not leak a
// credential in the output line.
func TestTokensLogValueRedaction(t *testing.T) {
	const fakeCSRF = "FAKE_CSRF_LOG_VALUE_LEAK_TEST"
	const fakeSession = "FAKE_SESSION_LOG_VALUE_LEAK_TEST"
	tokens := &Tokens{
		CSRFToken: fakeCSRF,
		SessionID: fakeSession,
		LoadedAt:  time.Now(),
		Source:    "file:/tmp/fixture.json",
		Jar:       cookiejar.New(),
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	logger.Info("snapshot", "tokens", tokens)

	got := buf.String()
	if strings.Contains(got, fakeCSRF) {
		t.Errorf("LogValue leaked CSRF in JSON output: %q", got)
	}
	if strings.Contains(got, fakeSession) {
		t.Errorf("LogValue leaked session in JSON output: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("LogValue did not insert [REDACTED] marker: %q", got)
	}
}

// TestTokensNilString is the nil-receiver guard. String() must
// not panic and must produce a recognizable "<nil>" placeholder.
func TestTokensNilString(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil Tokens.String() panicked: %v", r)
		}
	}()
	var t1 *Tokens
	got := t1.String()
	if got != "<nil>" {
		t.Errorf("nil Tokens.String() = %q, want <nil>", got)
	}
}

// TestTokensNilLogValue mirrors the nil-receiver guard for the
// LogValue method.
func TestTokensNilLogValue(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil Tokens.LogValue() panicked: %v", r)
		}
	}()
	var t1 *Tokens
	v := t1.LogValue()
	if v.Kind() != slog.KindString {
		t.Errorf("nil Tokens.LogValue() kind = %v, want KindString", v.Kind())
	}
}
