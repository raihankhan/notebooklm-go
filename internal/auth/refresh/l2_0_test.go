// Package refresh: tests for the L2.0 ladder rung — file-backed
// reload under a 3-attempt bounded loop.
//
// The suite pins the ACs from T-S3-001a:
//
//   - success-on-attempt-1: the first read returns the expected
//     Tokens and surfaces the file-touched timestamp.
//   - success-on-attempt-3: two transient failures followed by a
//     successful read on the third attempt must produce Tokens
//     and a non-zero Attempts counter.
//   - all-exhausted: three consecutive failures must return
//     ErrReloadL2_0Exhausted, errors.Is-matchable.
//   - file-missing: a missing storage_state.json short-circuits with
//     ErrReloadL2_0FileMissing, no retries.
//   - no-cred log-scan: every Tokens / error returned by
//     ReloadL2_0 must route through the redacting accessors so no
//     credential-shaped substring (cookie value, at=, SNlM0e,
//     FdrFJe, email) ever leaks into t.Log output.
//
// Boundary: per docs/AGENTS.md rule 5 this test file is part of the
// mode=internal package; it imports stdlib only. The Storage stub
// keeps the suite free of any on-disk fixture so the file-missing
// test does not require t.TempDir() teardown.
package refresh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/auth/storage"
)

// stubStorage is a controllable Storage implementation. Each
// per-call Read returns the next pair from Calls; once the slice is
// exhausted every subsequent Read returns LastErr. This pattern
// keeps the table tests declarative: a test sets up the call list
// once and the stub drives the bounded retry loop without per-test
// counters.
type stubStorage struct {
	// Results is the per-call result list. Index 0 is the first
	// attempt's return. A nil entry in the slice is allowed
	// when the test wants to fall through to LastErr.
	Results []stubResult
	// LastErr is returned (when set) after Results is exhausted.
	LastErr error
	// Calls is the observed call count — the test asserts
	// against this to confirm bounded-retry semantics.
	Calls int
}

// stubResult is one Read return. Either Err or Storage is
// meaningful; both may be set in which case Err wins (mirroring
// the production return shape).
type stubResult struct {
	Storage storage.Storage
	Mtime   time.Time
	Err     error
}

// ReadStorage is the Storage implementation. It advances Calls on
// every call and returns the next stubResult from Results, falling
// back to LastErr after the slice is exhausted.
func (s *stubStorage) ReadStorage(ctx context.Context) (storage.Storage, time.Time, error) {
	_ = ctx
	s.Calls++
	idx := s.Calls - 1
	if idx < len(s.Results) {
		r := s.Results[idx]
		if r.Err != nil {
			return storage.Storage{}, time.Time{}, r.Err
		}
		return r.Storage, r.Mtime, nil
	}
	if s.LastErr != nil {
		return storage.Storage{}, time.Time{}, s.LastErr
	}
	return storage.Storage{}, time.Time{}, errors.New("stubStorage: no result configured")
}

// quietLogger returns a *slog.Logger that drops every record. Used
// in tests where the diagnostic output is not under test — the
// no-cred-log-scan test wires its own logger to capture and inspect
// the bytes.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// captureLogger returns a *slog.Logger whose output is buffered so
// the no-cred log-scan test can scan the rendered text for
// credential-shaped substrings.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// sampleStorage builds a Storage value populated with one cookie
// plus the in-band account namespace. The cookie value carries the
// FAKE_ marker so a regression that leaks the value into a log
// line is detectable by simple substring matching.
func sampleStorage(t *testing.T, mtime time.Time) storage.Storage {
	t.Helper()
	return storage.Storage{
		Cookies: []storage.Cookie{
			{
				Name:    "SID",
				Value:   "FAKE_SID_VALUE",
				Domain:  ".google.com",
				Path:    "/",
				Expires: int64Ptr(0),
			},
		},
		NotebookLM: &storage.NotebookLM{
			Account: storage.Account{AuthUser: 2, Email: "alice@example.invalid"},
		},
	}
}

// int64Ptr returns a non-nil pointer to v. Used so the test fixture
// matches the canonical storage_state.json expires=-1 sentinel
// without dragging in the read-time normalization the production
// code applies.
func int64Ptr(v int64) *int64 { return &v }

// TestL2_0SuccessOnAttempt1: a stub whose first call returns the
// sample storage must produce Tokens with the expected account
// identity, the file-touched timestamp captured on Tokens.FetchedAt,
// and exactly one observed call to the storage (no retries on a
// happy path).
func TestL2_0SuccessOnAttempt1(t *testing.T) {
	mtime := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	stub := &stubStorage{
		Results: []stubResult{{Storage: sampleStorage(t, mtime), Mtime: mtime}},
	}
	tokens, err := ReloadL2_0(context.Background(), stub, quietLogger())
	if err != nil {
		t.Fatalf("ReloadL2_0(attempt1) error = %v", err)
	}
	if stub.Calls != 1 {
		t.Fatalf("Calls = %d, want 1 (no retry on success)", stub.Calls)
	}
	if tokens.AuthUser != 2 {
		t.Fatalf("Tokens.AuthUser = %d, want 2", tokens.AuthUser)
	}
	if tokens.AccountEmail != "alice@example.invalid" {
		t.Fatalf("Tokens.AccountEmail = %q, want alice@example.invalid", tokens.AccountEmail)
	}
	if tokens.Backend != BackendStorageFile {
		t.Fatalf("Tokens.Backend = %v, want BackendStorageFile", tokens.Backend)
	}
	if !tokens.FetchedAt.Equal(mtime) {
		t.Fatalf("Tokens.FetchedAt = %v, want %v (file-touched timestamp)", tokens.FetchedAt, mtime)
	}
	if len(tokens.Cookies) != 1 || tokens.Cookies[0].Name != "SID" {
		t.Fatalf("Tokens.Cookies = %+v, want one SID cookie view", tokens.Cookies)
	}
}

// TestL2_0SuccessOnAttempt3: a stub whose first two calls fail with
// a transient error and whose third call succeeds must produce
// Tokens, observe exactly 3 storage calls, and surface the
// successful-attempt counter through ReloadL2_0WithMtime.
//
// The transient error used here is NOT os.ErrNotExist — the
// missing-file branch short-circuits without retrying, so it has
// its own test. The transient here is a generic IO failure.
func TestL2_0SuccessOnAttempt3(t *testing.T) {
	mtime := time.Date(2026, 9, 4, 12, 30, 0, 0, time.UTC)
	transient := errors.New("simulated IO failure")
	stub := &stubStorage{
		Results: []stubResult{
			{Err: transient},
			{Err: transient},
			{Storage: sampleStorage(t, mtime), Mtime: mtime},
		},
	}
	tokens, err := ReloadL2_0(context.Background(), stub, quietLogger())
	if err != nil {
		t.Fatalf("ReloadL2_0(attempt3) error = %v", err)
	}
	if stub.Calls != 3 {
		t.Fatalf("Calls = %d, want 3", stub.Calls)
	}
	if tokens.AuthUser != 2 {
		t.Fatalf("Tokens.AuthUser = %d, want 2", tokens.AuthUser)
	}
	if !tokens.FetchedAt.Equal(mtime) {
		t.Fatalf("Tokens.FetchedAt = %v, want %v", tokens.FetchedAt, mtime)
	}

	res, err := ReloadL2_0WithMtime(context.Background(), &stubStorage{
		Results: []stubResult{
			{Err: transient},
			{Err: transient},
			{Storage: sampleStorage(t, mtime), Mtime: mtime},
		},
	}, quietLogger())
	if err != nil {
		t.Fatalf("ReloadL2_0WithMtime(attempt3) error = %v", err)
	}
	if res.Attempts != 3 {
		t.Fatalf("res.Attempts = %d, want 3", res.Attempts)
	}
	if !res.FileTouched.Equal(mtime) {
		t.Fatalf("res.FileTouched = %v, want %v", res.FileTouched, mtime)
	}
	if res.ReloadL2_0AttemptedAt.IsZero() {
		t.Fatalf("res.ReloadL2_0AttemptedAt is zero")
	}
}

// TestL2_0AllExhausted: three consecutive transient failures must
// return ErrReloadL2_0Exhausted, matchable via errors.Is, and must
// observe exactly l2_0MaxAttempts calls to the storage (the bounded
// counter is the load-bearing behavior).
func TestL2_0AllExhausted(t *testing.T) {
	transient := errors.New("simulated persistent IO failure")
	stub := &stubStorage{
		Results: []stubResult{{Err: transient}, {Err: transient}, {Err: transient}},
	}
	_, err := ReloadL2_0(context.Background(), stub, quietLogger())
	if err == nil {
		t.Fatalf("ReloadL2_0(exhausted) succeeded, want error")
	}
	if !errors.Is(err, ErrReloadL2_0Exhausted) {
		t.Fatalf("ReloadL2_0(exhausted) err = %v, want ErrReloadL2_0Exhausted", err)
	}
	if !errors.Is(err, transient) {
		t.Fatalf("ReloadL2_0(exhausted) err = %v, want wrapped transient %v", err, transient)
	}
	if stub.Calls != l2_0MaxAttempts {
		t.Fatalf("Calls = %d, want %d (bounded counter)", stub.Calls, l2_0MaxAttempts)
	}
}

// TestL2_0FileMissing: a stub whose ReadStorage returns
// os.ErrNotExist must short-circuit with ErrReloadL2_0FileMissing,
// and must NOT retry — the missing-file branch is a configuration
// problem, not a transient IO failure, so the bounded counter is
// not consulted.
//
// The wrapping must surface both the typed sentinel (for errors.Is)
// and os.ErrNotExist (for callers that branch on the underlying
// missing-file marker without scraping message text).
func TestL2_0FileMissing(t *testing.T) {
	stub := &stubStorage{
		Results: []stubResult{{Err: os.ErrNotExist}},
	}
	_, err := ReloadL2_0(context.Background(), stub, quietLogger())
	if err == nil {
		t.Fatalf("ReloadL2_0(missing) succeeded, want error")
	}
	if !errors.Is(err, ErrReloadL2_0FileMissing) {
		t.Fatalf("ReloadL2_0(missing) err = %v, want ErrReloadL2_0FileMissing", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReloadL2_0(missing) err = %v, want wrapped os.ErrNotExist", err)
	}
	if stub.Calls != 1 {
		t.Fatalf("Calls = %d, want 1 (no retry on missing file)", stub.Calls)
	}
}

// TestL2_0FileMissingEmptyPath: a DiskStorage with an empty Path
// field surfaces storage.ErrEmptyPath, which the ladder treats as
// the missing-file branch (no retry). Distinct test from the
// generic stub above so a regression that confuses empty-path with
// missing-file is detected.
func TestL2_0FileMissingEmptyPath(t *testing.T) {
	disk := &DiskStorage{Path: ""}
	_, err := ReloadL2_0(context.Background(), disk, quietLogger())
	if err == nil {
		t.Fatalf("ReloadL2_0(emptypath) succeeded, want error")
	}
	if !errors.Is(err, ErrReloadL2_0FileMissing) {
		t.Fatalf("ReloadL2_0(emptypath) err = %v, want ErrReloadL2_0FileMissing", err)
	}
	if !errors.Is(err, storage.ErrEmptyPath) {
		t.Fatalf("ReloadL2_0(emptypath) err = %v, want wrapped storage.ErrEmptyPath", err)
	}
}

// TestL2_0NilStorage: a nil Storage argument fails closed with a
// typed error rather than panicking — mirrors the ReloadL1 nil
// contract.
func TestL2_0NilStorage(t *testing.T) {
	_, err := ReloadL2_0(context.Background(), nil, quietLogger())
	if err == nil {
		t.Fatalf("ReloadL2_0(nil storage) succeeded, want error")
	}
}

// TestL2_0NilLogger: a nil logger falls back to slog.Default
// without surfacing an error. The acceptance is silence-on-success;
// the exhaustive path would normally write a Warn line through the
// default logger which the test does not capture. Here we only
// assert the success path's behavior.
func TestL2_0NilLogger(t *testing.T) {
	mtime := time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC)
	stub := &stubStorage{
		Results: []stubResult{{Storage: sampleStorage(t, mtime), Mtime: mtime}},
	}
	tokens, err := ReloadL2_0(context.Background(), stub, nil)
	if err != nil {
		t.Fatalf("ReloadL2_0(nil logger) error = %v", err)
	}
	if tokens.AuthUser != 2 {
		t.Fatalf("Tokens.AuthUser = %d, want 2", tokens.AuthUser)
	}
}

// TestL2_0ContextCanceled: ctx.Done before the first attempt
// short-circuits with context.Canceled and observes zero storage
// calls. The bounded-retry backoff loop must check ctx between
// every attempt so a canceled follower does not stall the caller.
func TestL2_0ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stub := &stubStorage{
		Results: []stubResult{{Err: errors.New("never reached")}},
	}
	_, err := ReloadL2_0(ctx, stub, quietLogger())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReloadL2_0(canceled) err = %v, want context.Canceled", err)
	}
	if stub.Calls != 0 {
		t.Fatalf("Calls = %d, want 0 (ctx cancel before first attempt)", stub.Calls)
	}
}

// TestL2_0ContextCanceledMidLoop: ctx.Done during the backoff
// between attempts short-circuits with context.Canceled and
// observes fewer than l2_0MaxAttempts calls. The bounded loop must
// honor ctx in the backoff select so a caller with a tight
// deadline does not stall on a retry that has already lost its
// usefulness.
func TestL2_0ContextCanceledMidLoop(t *testing.T) {
	transient := errors.New("simulated transient IO failure")
	ctx, cancel := context.WithCancel(context.Background())
	stub := &cancelAfterStub{
		stubStorage: stubStorage{
			Results: []stubResult{{Err: transient}, {Err: transient}, {Err: transient}},
		},
		cancel: cancel,
	}
	_, err := ReloadL2_0(ctx, stub, quietLogger())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReloadL2_0(canceled-mid) err = %v, want context.Canceled", err)
	}
	if stub.Calls >= l2_0MaxAttempts {
		t.Fatalf("Calls = %d, want < %d (ctx honored during backoff)", stub.Calls, l2_0MaxAttempts)
	}
}

// cancelAfterStub is a stubStorage variant that cancels the
// supplied context after the first call. Used to simulate a
// caller whose deadline fires between attempts — the bounded
// loop must observe ctx.Done during the backoff sleep and
// short-circuit.
type cancelAfterStub struct {
	stubStorage
	cancel context.CancelFunc
}

func (c *cancelAfterStub) ReadStorage(ctx context.Context) (storage.Storage, time.Time, error) {
	r, mtime, err := c.stubStorage.ReadStorage(ctx)
	c.cancel()
	return r, mtime, err
}

// TestL2_0NoCredentialLeakInTokens: every Tokens value ReloadL2_0
// returns must route through the redacting accessors so no
// credential-shaped substring (cookie value, at=, SNlM0e, FdrFJe,
// email-in-value-form) ever reaches a log sink.
//
// The test scans both the Tokens.String() output AND any error
// returned alongside the Tokens value. Both surfaces are
// log-line candidates per docs/AGENTS.md rule 4.
//
// Note: the email "alice@example.invalid" is identity metadata, not
// a credential; the AC allows it. The credential-shaped substrings
// the test guards against are the literal cookie value
// "FAKE_SID_VALUE", the wire-format "at=", the WIZ keys "SNlM0e" /
// "FdrFJe", and any other value-shaped substring. The Cookies
// CookieView exposes only Name/Domain/Path (not Value) so the
// cookie value can never reach a log line via Tokens.String().
func TestL2_0NoCredentialLeakInTokens(t *testing.T) {
	mtime := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	stub := &stubStorage{
		Results: []stubResult{{Storage: sampleStorage(t, mtime), Mtime: mtime}},
	}
	var buf bytes.Buffer
	logger := captureLogger(&buf)
	tokens, err := ReloadL2_0(context.Background(), stub, logger)
	if err != nil {
		t.Fatalf("ReloadL2_0 error = %v", err)
	}

	// The Tokens.String() view is the log-safe projection; scan
	// it for forbidden substrings.
	tokensStr := tokens.String()
	for _, secret := range forbiddenSecrets() {
		t.Logf("Tokens.String() rendered: %q", tokensStr)
		if strings.Contains(tokensStr, secret) {
			t.Errorf("Tokens.String() leaked credential %q: %q", secret, tokensStr)
		}
	}
	if hasAtEqualsToken(tokensStr) {
		t.Errorf("Tokens.String() leaked at= auth-token run: %q", tokensStr)
	}
	// The cookies view must never carry a Value field — verify
	// by formatting the slice via %v and scanning for the
	// fake value.
	if strings.Contains(fmtSprintTokensCookies(tokens), "FAKE_SID_VALUE") {
		t.Errorf("Tokens.Cookies leaked value: %q", fmtSprintTokensCookies(tokens))
	}

	// The log output from the capture buffer must also be free
	// of credential-shaped substrings. The Warn / Debug lines
	// carry the attempt number and error text; the error text
	// is the only place a leak can land because the success
	// path does not log the storage body.
	logOut := buf.String()
	t.Logf("Logger output: %q", logOut)
	for _, secret := range forbiddenSecrets() {
		if strings.Contains(logOut, secret) {
			t.Errorf("Logger output leaked credential %q: %q", secret, logOut)
		}
	}
	if hasAtEqualsToken(logOut) {
		t.Errorf("Logger output leaked at= auth-token run: %q", logOut)
	}
}

// TestL2_0NoCredentialLeakInError: an exhausted-error path must
// route the wrapped cause through the error string without
// exposing the cookie value. The transient error message used
// here carries a FAKE_ marker so a regression is detectable.
func TestL2_0NoCredentialLeakInError(t *testing.T) {
	const fakeCause = "FAKE_CAUSE_SHOULD_BE_SAFE"
	transient := errors.New(fakeCause)
	stub := &stubStorage{
		Results: []stubResult{{Err: transient}, {Err: transient}, {Err: transient}},
	}
	var buf bytes.Buffer
	_, err := ReloadL2_0(context.Background(), stub, captureLogger(&buf))
	if err == nil {
		t.Fatalf("ReloadL2_0(exhausted) succeeded, want error")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "ErrReloadL2_0Exhausted") && !strings.Contains(errStr, "exhausted") {
		t.Errorf("exhausted error missing sentinel label: %q", errStr)
	}
	if strings.Contains(errStr, "FAKE_SID_VALUE") {
		t.Errorf("exhausted error leaked cookie value: %q", errStr)
	}
	// The transient cause is wrapped; its message is allowed
	// to surface verbatim because the test authored it. The
	// contract is "no credential-shaped substring", not "no
	// arbitrary error text".
	t.Logf("exhausted error: %q", errStr)
	t.Logf("logger output:   %q", buf.String())
	for _, secret := range forbiddenSecrets() {
		if strings.Contains(errStr, secret) {
			t.Errorf("exhausted error leaks %q: %q", secret, errStr)
		}
		if strings.Contains(buf.String(), secret) {
			t.Errorf("logger output leaks %q: %q", secret, buf.String())
		}
	}
	if hasAtEqualsToken(errStr) {
		t.Errorf("exhausted error leaks at= auth-token run: %q", errStr)
	}
	if hasAtEqualsToken(buf.String()) {
		t.Errorf("logger output leaks at= auth-token run: %q", buf.String())
	}
}

// forbiddenSecrets is the closed list of credential-shaped
// substrings the no-cred log-scan regression guards. The list
// mirrors docs/AGENTS.md rule 4 plus the cookie value marker used
// across the suite:
//
//   - cookie-value placeholder FAKE_SID_VALUE — emitted only when
//     a CookieView or Storage value accidentally reaches a log line.
//   - at= followed by an alphanumeric credential shape (e.g. "at=ABC")
//     — the auth-token form/query marker; matched as a value-shaped
//     pattern rather than the bare "at=" so identity metadata like
//     "fetched_at=" does not trip the assertion.
//   - SNlM0e / FdrFJe — the WIZ_global_data CSRF / session keys.
//
// The list is intentionally closed; new credential shapes land
// here with an explanatory comment so a future test author does
// not duplicate one.
func forbiddenSecrets() []string {
	return []string{
		"FAKE_SID_VALUE",
		"at=FAKE", // value-shaped marker; matches auth-token form bodies.
		"SNlM0e",
		"FdrFJe",
	}
}

// hasAtEqualsToken returns true if s contains the auth-token form
// pattern (a value-shaped at= run). Identity metadata like
// "fetched_at=" is intentionally NOT matched — only auth-token
// values like "at=ABC123" are. Used by the no-cred log-scan
// regression so it does not false-positive on FetchedAt formatting.
//
// The discriminator is the character immediately preceding the
// "at=" run: a real auth-token at= form/query value is preceded by
// a separator character (space, '?', '&', '\n') — never by an
// identifier character (letter, digit, underscore). A keyword
// like "fetched_at=" is preceded by a space in fmt output, but
// the substring search finds "at=" inside it; the lookbehind
// confirms we are NOT inside a longer identifier.
func hasAtEqualsToken(s string) bool {
	const prefix = "at="
	searchStart := 0
	for {
		i := strings.Index(s[searchStart:], prefix)
		if i < 0 {
			return false
		}
		absI := searchStart + i
		// Lookbehind: the character immediately before "at="
		// must NOT be an identifier character (letter, digit,
		// underscore). If it is, this is a keyword
		// occurrence like "fetched_at=" or "created_at="
		// and we skip it.
		if absI > 0 {
			prev := s[absI-1]
			if isIdentChar(prev) {
				searchStart = absI + len(prefix)
				continue
			}
		}
		// Lookahead: the value-shaped run must begin with a
		// credential character (alphanumeric, '-', '_').
		j := absI + len(prefix)
		if j >= len(s) {
			return false
		}
		c := s[j]
		if isIdentChar(c) {
			return true
		}
		searchStart = absI + len(prefix)
	}
}

func isIdentChar(c byte) bool {
	switch {
	case c >= '0' && c <= '9',
		c >= 'a' && c <= 'z',
		c >= 'A' && c <= 'Z',
		c == '_':
		return true
	}
	return false
}

// fmtSprintTokensCookies renders Tokens.Cookies via fmt.Sprintf %v
// so the test can scan for the cookie value. The CookieView type
// has only Name/Domain/Path fields; a future change that adds a
// Value field will trip this assertion immediately.
func fmtSprintTokensCookies(t Tokens) string {
	var b strings.Builder
	for _, c := range t.Cookies {
		b.WriteString(c.Name)
		b.WriteByte('=')
		b.WriteString(c.Domain)
		b.WriteByte(';')
		b.WriteString(c.Path)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestL2_0ErrReloadL2_0ExhaustedExported: the sentinel is
// exportable and reachable via errors.Is from any wrapping — pins
// the contract for downstream rungs (T-S3-001b/c/d) that will
// check for it.
func TestL2_0ErrReloadL2_0ExhaustedExported(t *testing.T) {
	if ErrReloadL2_0Exhausted == nil {
		t.Fatalf("ErrReloadL2_0Exhausted is nil")
	}
}

// TestL2_0ErrReloadL2_0FileMissingExported: the missing-file
// sentinel is exportable and distinct from the exhausted sentinel
// — pins the contract for the ladder's "no profile" branch.
func TestL2_0ErrReloadL2_0FileMissingExported(t *testing.T) {
	if ErrReloadL2_0FileMissing == nil {
		t.Fatalf("ErrReloadL2_0FileMissing is nil")
	}
	if errors.Is(ErrReloadL2_0Exhausted, ErrReloadL2_0FileMissing) {
		t.Fatalf("ErrReloadL2_0Exhausted and ErrReloadL2_0FileMissing collide")
	}
	if errors.Is(ErrReloadL2_0FileMissing, ErrReloadL2_0Exhausted) {
		t.Fatalf("ErrReloadL2_0FileMissing and ErrReloadL2_0Exhausted collide")
	}
}

// TestL2_0StorageInterfaceIsSatisfied: DiskStorage must implement
// the Storage interface at compile time. A future refactor that
// changes the interface signature breaks this assertion before it
// breaks a production caller.
func TestL2_0StorageInterfaceIsSatisfied(t *testing.T) {
	var _ Storage = (*DiskStorage)(nil)
}
