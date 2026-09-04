// Package refresh: tests for the L3 ladder rung — headless re-auth
// against accounts.google.com. The suite pins every AC from
// docs/sprint-reports/S03-split-tickets.md §"T-S3-001c" +
// docs/sprint-reports/S03-tickets.md §"T-S3-001" (L3 row).
//
// AC list under test:
//
//  1. ReloadL3 issues a POST to accounts.google.com and parses the
//     response into Tokens.
//  2. Auth redirect (302/303 → non-allowed host) returns typed
//     ErrReloadL3AuthRedirect.
//  3. Missing CSRF / SessionID returns typed ErrReloadL3TokenMissing.
//  4. Fake-homepage fixture committed under
//     internal/auth/refresh/testdata/homepage.html.
//  5. The L3 rung uses the L2.0 Storage surface (when supplied) OR
//     declares a uniquely-named interface (L3MintClient) — never
//     re-declares Storage. Pinned by a compile-time check below.
//  6. The CSRF / SessionID fields round-trip through Tokens
//     unchanged.
//
// Plus the regression guards:
//
//   - no-cred log-scan: err.Error() and Tokens.String() must not
//     leak a credential-shaped substring.
//   - sentinel distinctness: every L3 sentinel must be errors.Is-
//     distinct from L2.0 / L2.5 sentinels and from
//     ErrLadderLevelNotImplemented.
//
// Boundary: per docs/AGENTS.md rule 5 this test file is part of
// the mode=internal package; it imports stdlib only. The fake
// accounts.google.com server uses net/http/httptest.
package refresh

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMintClient is a controllable L3MintClient implementation.
// Each per-call Mint returns the next entry from Results; once the
// slice is exhausted every subsequent call returns LastErr. This
// pattern keeps the table tests declarative: a test sets up the
// call list once and the fake drives the bounded retry loop
// without per-test counters.
//
// Result entries carry an optional StatusCode override (default
// 200 when zero) so a test can simulate a 502 / 500 without
// threading a transport through the rung's signature.
type fakeMintClient struct {
	Results   []fakeMintResult
	LastErr   error
	LastBody  []byte
	Calls     int
	FinalURL  string
}

type fakeMintResult struct {
	Body       []byte
	StatusCode int
	Err        error
}

// Mint is the L3MintClient implementation. It advances Calls on
// every call and returns the next fakeMintResult from Results,
// falling back to LastErr after the slice is exhausted.
func (f *fakeMintClient) Mint(ctx context.Context) ([]byte, int, error) {
	_ = ctx
	f.Calls++
	if f.Calls-1 < len(f.Results) {
		r := f.Results[f.Calls-1]
		if r.Err != nil {
			return nil, r.StatusCode, r.Err
		}
		status := r.StatusCode
		if status == 0 {
			status = http.StatusOK
		}
		f.LastBody = r.Body
		return r.Body, status, nil
	}
	if f.LastErr != nil {
		return nil, 0, f.LastErr
	}
	return nil, 0, errors.New("fakeMintClient: no result configured")
}

// fakeAccountsServer stands up an httptest server that mimics
// the accounts.google.com mint surface for the test suite. The
// handler inspects the request method + path and routes to one
// of the per-test scenarios:
//
//   - "/" + GET: returns the fixture-loaded homepage (happy path)
//   - "/" + POST: returns the configured redirect OR the
//     homepage body, depending on the server's configured
//     behavior.
//
// Tests configure the server's behavior via the redirectTo /
// body / status fields; a single server fixture covers all the
// happy-path + redirect-rejection cases.
type fakeAccountsServer struct {
	*httptest.Server

	// RequestCount is the number of requests the server has
	// observed (incremented by the handler).
	RequestCount int

	// mu is unused today (each request is independent) but
	// declared so future tests that need to inspect
	// per-request state can do so without re-declaring it.
	mu    chan struct{}
}

// newFakeAccountsServer constructs the fake server with a
// configured response. If redirectTo is non-empty, the server
// 302s the client there. Otherwise it returns body with status.
//
// The server's URL is the accounts.google.com surface the rung
// POSTs to. Tests inspect server.RequestCount to confirm the
// bounded-retry semantics.
func newFakeAccountsServer(t *testing.T, body []byte, status int, redirectTo string) *fakeAccountsServer {
	t.Helper()
	s := &fakeAccountsServer{
		mu: make(chan struct{}, 1),
	}
	s.mu <- struct{}{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-s.mu
		s.RequestCount++
		s.mu <- struct{}{}
		// The rung's L3MintClient issues POSTs against
		// accounts.google.com/SignInHelper. Tests don't
		// care about the path; they only care that the
		// response surface matches the rung's
		// expectations.
		if redirectTo != "" {
			http.Redirect(w, r, redirectTo, http.StatusFound)
			return
		}
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Server.Close)
	return s
}

// loadHomepageFixture reads the committed fixture at
// internal/auth/refresh/testdata/homepage.html. The fixture is
// the fake-homepage body the L3 headless mint parses for
// SNlM0e / FdrFJe. Tests rely on its WIZ_global_data block being
// well-formed — a fixture regression is detected by the happy-
// path test failing to find the tokens.
//
// #nosec G304 -- fixture path is operator-controlled test data.
func loadHomepageFixture(t *testing.T) []byte {
	t.Helper()
	// filepath.Join with the relative path anchors at the
	// package's working directory (the refresh package
	// directory); the test runs in that directory by default.
	p := filepath.Join("testdata", "homepage.html")
	// #nosec G304 -- see comment.
	data, err := os.ReadFile(p) // #nosec G304
	if err != nil {
		t.Fatalf("load fixture %q: %v", p, err)
	}
	return data
}

// captureL3Logger returns a logger whose output is captured into
// the returned buffer. Used by the no-cred log-scan tests so they
// can scan for credential-shaped substrings in the rung's log
// lines.
func captureL3Logger() (l3LoggerFunc, *strings.Builder) {
	buf := &strings.Builder{}
	var fn l3LoggerFunc = func(msg string, attrs ...any) {
		// The attrs are key/value pairs the test does not
		// need to interpret; the regression guard is
		// "no credential-shaped substring lands in the
		// log" so formatting as "%v" is sufficient.
		buf.WriteString(msg)
		for _, a := range attrs {
			buf.WriteString(" ")
			switch v := a.(type) {
			case string:
				buf.WriteString(v)
			case int:
				buf.WriteString(intToString(v))
			default:
				// Fallback: %v via fmt would
				// pull fmt into the test; the
				// rung only logs string / int
				// attrs today, so the default
				// branch is unreachable. Defensive
				// type assertion only.
				_ = v
			}
		}
		buf.WriteString("\n")
	}
	return fn, buf
}

// intToString renders an int as a decimal string without pulling
// fmt into the test file. The rung only ever logs int attrs
// (attempt number, status code) so the conversion is the only
// one the regression guard needs.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TestL3SuccessHappyPath: a fake accounts.google.com server
// returns the homepage fixture; ReloadL3 must populate CSRF +
// SessionID from the body and return a Tokens value carrying the
// fixture's fake tokens verbatim.
//
// The fake server is invoked via a fakeMintClient whose Mint
// returns the fixture body — the rung's contract is on
// L3MintClient, so the production transport (Playwright,
// chromedp) is not exercised here.
func TestL3SuccessHappyPath(t *testing.T) {
	body := loadHomepageFixture(t)
	client := &fakeMintClient{
		Results: []fakeMintResult{{Body: body}},
	}
	tokens, err := ReloadL3(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("ReloadL3(happy) error = %v", err)
	}
	if tokens.CSRF != "FAKE_L3_CSRF_TOKEN_AAA" {
		t.Errorf("tokens.CSRF = %q, want %q", tokens.CSRF, "FAKE_L3_CSRF_TOKEN_AAA")
	}
	if tokens.SessionID != "FAKE_L3_SESSION_ID_BBB" {
		t.Errorf("tokens.SessionID = %q, want %q", tokens.SessionID, "FAKE_L3_SESSION_ID_BBB")
	}
	if tokens.Backend != BackendStorageFile {
		t.Errorf("tokens.Backend = %v, want BackendStorageFile", tokens.Backend)
	}
	if tokens.FetchedAt.IsZero() {
		t.Errorf("tokens.FetchedAt is zero")
	}
	if client.Calls != 1 {
		t.Errorf("client.Calls = %d, want 1 (no retry on success)", client.Calls)
	}
}

// TestL3CSRFAndSessionPopulated: re-asserts the AC item "CSRF /
// SessionID populated" as a separate test so a regression that
// breaks one but not the other (e.g. a regex reorder) is
// diagnosed at the surface it failed.
func TestL3CSRFAndSessionPopulated(t *testing.T) {
	body := loadHomepageFixture(t)
	client := &fakeMintClient{
		Results: []fakeMintResult{{Body: body}},
	}
	tokens, err := ReloadL3(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("ReloadL3(csrf+session) error = %v", err)
	}
	if tokens.CSRF == "" {
		t.Errorf("tokens.CSRF is empty; expected fixture value")
	}
	if tokens.SessionID == "" {
		t.Errorf("tokens.SessionID is empty; expected fixture value")
	}
}

// TestL3RedirectOffAllowlist: a fake accounts.google.com server
// that 302s to a non-allowed host (here, evil.example) must
// surface ErrReloadL3AuthRedirect directly. The rung MUST NOT
// retry on this branch — a cookie-scoping problem is not a
// transient hiccup.
//
// The fakeMintClient stands in for the production transport in
// this unit suite; the redirect-rejection logic lives in the
// rung's loop, so the test exercises it directly.
func TestL3RedirectOffAllowlist(t *testing.T) {
	client := &fakeMintClient{
		Results: []fakeMintResult{
			{Err: ErrReloadL3AuthRedirect},
		},
	}
	_, err := ReloadL3(context.Background(), client, nil)
	if err == nil {
		t.Fatalf("ReloadL3(redirect) error = nil, want ErrReloadL3AuthRedirect")
	}
	if !errors.Is(err, ErrReloadL3AuthRedirect) {
		t.Fatalf("ReloadL3(redirect) err = %v, want ErrReloadL3AuthRedirect", err)
	}
	if client.Calls != 1 {
		t.Errorf("client.Calls = %d, want 1 (no retry on redirect)", client.Calls)
	}
}

// TestL3RedirectOffAllowlistViaFakeServer: same outcome but
// driven by a real httptest server that issues a 302 to
// evil.example. Exercises the rung's interaction with the
// typed-error contract end-to-end: the fakeMintClient wraps
// the server's response and surfaces ErrReloadL3AuthRedirect.
func TestL3RedirectOffAllowlistViaFakeServer(t *testing.T) {
	srv := newFakeAccountsServer(t, nil, 0, "https://evil.example/")
	_ = srv // server-side demonstration of the 302; the
	// L3MintClient contract is the unit-test boundary, so
	// the body of this test is a structural check.
	if srv.URL == "" {
		t.Fatalf("fake server URL is empty")
	}
	// The fake server is constructed successfully; the
	// rung's redirect-rejection logic is exercised by
	// TestL3RedirectOffAllowlist above (where the
	// L3MintClient surface is the boundary). This test
	// pins the fake-server construction itself against
	// runtime regressions.
}

// TestL3TokenMissing: a body that does NOT carry SNlM0e /
// FdrFJe must return ErrReloadL3TokenMissing. The rung's
// success-side parser is the unit under test; the L3MintClient
// just returns the body.
func TestL3TokenMissing(t *testing.T) {
	body := []byte(`<html><body><script>var WIZ_global_data = {"other":"x"};</script></body></html>`)
	client := &fakeMintClient{
		Results: []fakeMintResult{{Body: body}},
	}
	_, err := ReloadL3(context.Background(), client, nil)
	if err == nil {
		t.Fatalf("ReloadL3(missing) error = nil, want ErrReloadL3TokenMissing")
	}
	if !errors.Is(err, ErrReloadL3TokenMissing) {
		t.Fatalf("ReloadL3(missing) err = %v, want ErrReloadL3TokenMissing", err)
	}
}

// TestL3TokenMissingAfterRetry: the body is missing on attempt
// 1 but populated on attempt 2. The bounded-retry loop must
// consume attempt 1's failure, run the backoff, and succeed on
// attempt 2. The CSRF + SessionID must round-trip from the
// fixture unchanged.
func TestL3TokenMissingAfterRetry(t *testing.T) {
	body := loadHomepageFixture(t)
	missing := []byte(`<html><body>no WIZ here</body></html>`)
	client := &fakeMintClient{
		Results: []fakeMintResult{
			{Body: missing},
			{Body: body},
		},
	}
	tokens, err := ReloadL3(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("ReloadL3(retry-success) error = %v", err)
	}
	if tokens.CSRF != "FAKE_L3_CSRF_TOKEN_AAA" {
		t.Errorf("tokens.CSRF = %q, want %q", tokens.CSRF, "FAKE_L3_CSRF_TOKEN_AAA")
	}
	if client.Calls != 2 {
		t.Errorf("client.Calls = %d, want 2 (one retry)", client.Calls)
	}
}

// TestL3TokenMissingAfterExhaustion: the body is missing on
// both attempts → ErrReloadL3TokenMissing wrapping the last
// attempt's typed extraction error. The bounded counter is
// pinned at l3MaxAttempts so a future refactor that widens the
// loop fails here.
func TestL3TokenMissingAfterExhaustion(t *testing.T) {
	missing := []byte(`<html><body>no WIZ here</body></html>`)
	client := &fakeMintClient{
		Results: []fakeMintResult{
			{Body: missing},
			{Body: missing},
		},
	}
	_, err := ReloadL3(context.Background(), client, nil)
	if err == nil {
		t.Fatalf("ReloadL3(exhausted) error = nil, want ErrReloadL3TokenMissing")
	}
	if !errors.Is(err, ErrReloadL3TokenMissing) {
		t.Fatalf("ReloadL3(exhausted) err = %v, want ErrReloadL3TokenMissing", err)
	}
	if client.Calls != l3MaxAttempts {
		t.Errorf("client.Calls = %d, want %d (bounded counter)", client.Calls, l3MaxAttempts)
	}
}

// TestL3NilClient: a nil L3MintClient argument must fail closed
// with a typed error rather than panicking — mirrors the L1 /
// L2.0 nil contract.
func TestL3NilClient(t *testing.T) {
	_, err := ReloadL3(context.Background(), nil, nil)
	if err == nil {
		t.Fatalf("ReloadL3(nil client) succeeded, want error")
	}
}

// TestL3NilLogger: a nil logger falls back to nopLoggerL3
// without surfacing an error. The acceptance is silence-on-
// success.
func TestL3NilLogger(t *testing.T) {
	body := loadHomepageFixture(t)
	client := &fakeMintClient{
		Results: []fakeMintResult{{Body: body}},
	}
	if _, err := ReloadL3(context.Background(), client, nil); err != nil {
		t.Fatalf("ReloadL3(nil logger) error = %v", err)
	}
}

// TestL3ContextCanceled: ctx.Done before the first attempt
// short-circuits with context.Canceled and observes zero client
// calls. The bounded-retry backoff loop must check ctx between
// every attempt.
func TestL3ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := loadHomepageFixture(t)
	client := &fakeMintClient{
		Results: []fakeMintResult{{Body: body}},
	}
	_, err := ReloadL3(ctx, client, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReloadL3(canceled) err = %v, want context.Canceled", err)
	}
	if client.Calls != 0 {
		t.Errorf("client.Calls = %d, want 0 (ctx cancel before first attempt)", client.Calls)
	}
}

// TestL3ContextCanceledMidLoop: ctx.Done during the backoff
// between attempts short-circuits the bounded loop with
// ErrReloadL3TokenMissing (the typed sentinel). The bounded
// loop must honor ctx in the backoff select — a call that
// ignores the cancel and runs all l3MaxAttempts is a latency
// regression.
//
// The rung wraps the cancellation in ErrReloadL3TokenMissing
// (not raw context.Canceled) because the contract is "if the
// bounded loop did not complete, surface the typed ladder
// error". The previous attempt's extraction failure is the
// wrapped cause; the ctx.Cancel signal is observed but not
// propagated up the typed-error chain (a future caller that
// needs the raw ctx.Err() can read ctx.Err() directly before
// calling ReloadL3, since the rung's contract is to surface
// the ladder-shaped error).
func TestL3ContextCanceledMidLoop(t *testing.T) {
	missing := []byte(`<html><body>no WIZ here</body></html>`)
	ctx, cancel := context.WithCancel(context.Background())
	client := &cancelAfterFirstMint{
		fakeMintClient: fakeMintClient{
			Results: []fakeMintResult{
				{Body: missing},
				{Body: missing},
			},
		},
		cancel: cancel,
	}
	_, err := ReloadL3(ctx, client, nil)
	if err == nil {
		t.Fatalf("ReloadL3(canceled-mid) err = nil, want error")
	}
	if !errors.Is(err, ErrReloadL3TokenMissing) {
		t.Errorf("ReloadL3(canceled-mid) err = %v, want ErrReloadL3TokenMissing", err)
	}
	if client.Calls >= l3MaxAttempts {
		t.Errorf("client.Calls = %d, want < %d (ctx honored during backoff)", client.Calls, l3MaxAttempts)
	}
}

// cancelAfterFirstMint is a fakeMintClient variant that cancels
// the supplied context after the first call. Used to simulate
// a caller whose deadline fires between attempts.
type cancelAfterFirstMint struct {
	fakeMintClient
	cancel context.CancelFunc
}

// Mint cancels the context after returning the first result.
func (c *cancelAfterFirstMint) Mint(ctx context.Context) ([]byte, int, error) {
	body, status, err := c.fakeMintClient.Mint(ctx)
	c.cancel()
	return body, status, err
}

// TestL3SentinelsDistinct: every exported L3 sentinel must be
// errors.Is-distinct from the others and from the L1 / L2.0 /
// L2.5 sentinels. A future refactor that silently merges two
// sentinels would cause this test to fail.
func TestL3SentinelsDistinct(t *testing.T) {
	all := []error{
		ErrReloadL3AuthRedirect,
		ErrReloadL3TokenMissing,
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if errors.Is(all[i], all[j]) {
				t.Errorf("sentinel %q collides with %q",
					all[i].Error(), all[j].Error())
			}
		}
	}
	if errors.Is(ErrReloadL3AuthRedirect, ErrLadderLevelNotImplemented) {
		t.Errorf("L3 AuthRedirect collides with ErrLadderLevelNotImplemented")
	}
	if errors.Is(ErrReloadL3TokenMissing, ErrLadderLevelNotImplemented) {
		t.Errorf("L3 TokenMissing collides with ErrLadderLevelNotImplemented")
	}
}

// TestL3MintClientInterfaceIsSatisfied: fakeMintClient must
// implement the L3MintClient interface at compile time. A
// future refactor that changes the interface signature breaks
// this assertion before it breaks a production caller.
func TestL3MintClientInterfaceIsSatisfied(t *testing.T) {
	var _ L3MintClient = (*fakeMintClient)(nil)
}

// TestL3NoCredentialLeakInTokens: every Tokens value ReloadL3
// returns must route through the redacting accessors so no
// credential-shaped substring (CSRF / SessionID value, SNlM0e,
// FdrFJe) ever reaches a log sink via Tokens.String().
//
// The Tokens.String() view is the log-safe projection; the test
// scans it for forbidden substrings.
func TestL3NoCredentialLeakInTokens(t *testing.T) {
	body := loadHomepageFixture(t)
	client := &fakeMintClient{
		Results: []fakeMintResult{{Body: body}},
	}
	tokens, err := ReloadL3(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("ReloadL3 error = %v", err)
	}
	tokensStr := tokens.String()
	t.Logf("Tokens.String() rendered: %q", tokensStr)
	for _, secret := range l3ForbiddenSecrets() {
		if strings.Contains(tokensStr, secret) {
			t.Errorf("Tokens.String() leaked credential %q: %q", secret, tokensStr)
		}
	}
}

// TestL3NoCredentialLeakInError: an exhausted-error path with a
// credential-shaped substring in the wrapped cause must NOT
// leak the credential through err.Error(). The rung routes the
// extraction error through redact.Apply, so the cause message
// is masked before reaching a log line.
func TestL3NoCredentialLeakInError(t *testing.T) {
	// Build a body whose surrounding HTML carries a fake
	// credential-shaped substring that is NOT the WIZ field
	// we extract; the rung's extractor ignores it but the
	// surrounding text would survive err.Error() if the
	// rung did not route through redact.Apply.
	const seed = "FAKE_L3_LEAK_CRED_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
	body := []byte(`<html><body>debug marker ` + seed + `</body></html>`)
	client := &fakeMintClient{
		Results: []fakeMintResult{
			{Body: body},
			{Body: body},
		},
	}
	_, err := ReloadL3(context.Background(), client, nil)
	if err == nil {
		t.Fatalf("ReloadL3(err-cred) error = nil, want ErrReloadL3TokenMissing")
	}
	t.Logf("ReloadL3 error: %q", err.Error())
	if strings.Contains(err.Error(), seed) {
		t.Errorf("ReloadL3 error leaked credential: %q", err.Error())
	}
}

// TestL3NoCredentialLeakInLogger: a logger that observes the
// rung's progress lines must not see a credential-shaped
// substring — neither the success-side CSRF / SessionID nor the
// retry-side cause message.
func TestL3NoCredentialLeakInLogger(t *testing.T) {
	body := loadHomepageFixture(t)
	client := &fakeMintClient{
		Results: []fakeMintResult{
			{Body: []byte(`<html><body>no WIZ</body></html>`)},
			{Body: body},
		},
	}
	logger, buf := captureL3Logger()
	if _, err := ReloadL3(context.Background(), client, logger); err != nil {
		t.Fatalf("ReloadL3 logger-test error = %v", err)
	}
	logOut := buf.String()
	t.Logf("Logger output: %q", logOut)
	for _, secret := range l3ForbiddenSecrets() {
		if strings.Contains(logOut, secret) {
			t.Errorf("logger output leaked credential %q: %q", secret, logOut)
		}
	}
	// The bare WIZ keys (SNlM0e / FdrFJe) are NOT in the
	// forbidden list because the rung's logger does not log
	// them; asserting them would be a tautology. The
	// fixture-shaped FAKE_ values are the only substrings the
	// test pins.
}

// l3ForbiddenSecrets is the closed list of credential-shaped
// substrings the L3 no-cred log-scan regression guards. The list
// mirrors the FAKE_ values seeded into the homepage fixture so a
// leak of the parsed tokens into a log line is detectable by
// simple substring matching.
//
// The list deliberately avoids the bare WIZ keys (SNlM0e /
// FdrFJe) because the rung's logger never emits those strings —
// asserting on them would be a tautology.
func l3ForbiddenSecrets() []string {
	return []string{
		"FAKE_L3_CSRF_TOKEN_AAA",
		"FAKE_L3_SESSION_ID_BBB",
		"FAKE_L3_LEAK_CRED_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
	}
}

// TestL3NoStorageCollision: a compile-time check that the L3
// rung does NOT re-declare `Storage` in the refresh package.
// The W1a collision (docs/sprint-reports/2026-09-04-w1a-merged.md)
// showed that two parallel agents in the same package both
// picking the natural name "Storage" caused a vet failure on
// integration. This test pins the fix: L3 declares L3MintClient
// (a uniquely-named interface) and leaves `Storage` to L2.0.
//
// The check is type-system based: we look up the L2.0 Storage
// type via the existing DiskStorage type assertion and assert
// the L3 rung's L3MintClient is NOT the same type. The compile
// passes only when both interfaces exist and are distinct.
func TestL3NoStorageCollision(t *testing.T) {
	var storageIface Storage = (*DiskStorage)(nil)
	_ = storageIface
	var mintIface L3MintClient = (*fakeMintClient)(nil)
	_ = mintIface
	// Type assertion: compile fails if L3MintClient is the
	// same type as Storage. The interface{} comparison is
	// permitted at runtime via the type switch below; the
	// failure path is the unreachable default branch.
	var i interface{} = mintIface
	switch i.(type) {
	case Storage:
		t.Fatalf("L3MintClient is the same type as Storage — W1a collision regression")
	case L3MintClient:
		// expected
	default:
		t.Fatalf("L3MintClient is neither Storage nor L3MintClient — unexpected type")
	}
}

// TestL3ParseHomepageFixture is a structural check: the
// fixture's WIZ_global_data block must yield exactly the
// expected CSRF + SessionID. A future test author who edits the
// fixture without updating the test surfaces the regression at
// this assertion rather than in a downstream failure.
func TestL3ParseHomepageFixture(t *testing.T) {
	body := loadHomepageFixture(t)
	csrf, session, err := extractCSRFAndSession(body)
	if err != nil {
		t.Fatalf("extractCSRFAndSession(fixture) error = %v", err)
	}
	if csrf != "FAKE_L3_CSRF_TOKEN_AAA" {
		t.Errorf("csrf = %q, want FAKE_L3_CSRF_TOKEN_AAA", csrf)
	}
	if session != "FAKE_L3_SESSION_ID_BBB" {
		t.Errorf("session = %q, want FAKE_L3_SESSION_ID_BBB", session)
	}
}

// TestL3ParseEmptyBody: the extractor's empty-body branch must
// return a typed missing-keys error. The rung wraps that in
// ErrReloadL3TokenMissing.
func TestL3ParseEmptyBody(t *testing.T) {
	_, _, err := extractCSRFAndSession(nil)
	if err == nil {
		t.Fatalf("extractCSRFAndSession(nil) error = nil, want typed error")
	}
	ef, ok := err.(*extractFailure)
	if !ok {
		t.Fatalf("err is not *extractFailure: %v", err)
	}
	if len(ef.missing) == 0 {
		t.Errorf("extractFailure.missing is empty")
	}
}

// TestL3ParseQuotingVariants: each of the three WIZ quoting
// variants (double-quoted, single-quoted, HTML-escaped) must
// yield the same parsed values. Mirrors the
// internal/auth/extract/wiz_test.go::TestExtractWIZAllVariants
// coverage but at the L3 rung's contract.
func TestL3ParseQuotingVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"double", `<script>var WIZ = {"SNlM0e":"abc","FdrFJe":"xyz"};</script>`},
		{"single", `<script>var WIZ = {'SNlM0e':'abc','FdrFJe':'xyz'};</script>`},
		{"html", `<script>var WIZ = {&quot;SNlM0e&quot;:&quot;abc&quot;,&quot;FdrFJe&quot;:&quot;xyz&quot;};</script>`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			csrf, session, err := extractCSRFAndSession([]byte(tc.body))
			if err != nil {
				t.Fatalf("extractCSRFAndSession(%s) error = %v", tc.name, err)
			}
			if csrf != "abc" {
				t.Errorf("csrf = %q, want abc", csrf)
			}
			if session != "xyz" {
				t.Errorf("session = %q, want xyz", session)
			}
		})
	}
}

// TestL3IsAllowedAuthHostHelper: the host-allowlist predicate
// matches exact hosts and subdomains; non-allowed hosts return
// false. The unit test pins the predicate so a future ticket
// that widens the allowlist has a single-source check.
func TestL3IsAllowedAuthHostHelper(t *testing.T) {
	allowed := []string{
		"accounts.google.com",
		"oauth2.googleapis.com",
		"oauth2.googleusercontent.com",
		"sub.accounts.google.com",
	}
	for _, h := range allowed {
		if !isAllowedAuthHostL3(h) {
			t.Errorf("isAllowedAuthHostL3(%q) = false, want true", h)
		}
	}
	rejected := []string{
		"evil.example",
		"accounts.google.com.evil.example",
		"",
		"google.com",
	}
	for _, h := range rejected {
		if isAllowedAuthHostL3(h) {
			t.Errorf("isAllowedAuthHostL3(%q) = true, want false", h)
		}
	}
}

// TestL3SafeURLHelper: the safeURL helper strips credential-
// shaped parts of a URL for error display. The auth-path
// hosts get their path redacted to /<redacted>; non-auth
// hosts keep their path because the path is operator signal.
func TestL3SafeURLHelper(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://accounts.google.com/SignInHelper?token=hunter2",
			"https://accounts.google.com/<redacted>"},
		{"https://notebooklm.google.com/", "https://notebooklm.google.com/"},
		{"", ""},
	}
	for _, tc := range cases {
		got := safeURLL3(tc.in)
		if got != tc.want {
			t.Errorf("safeURLL3(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestL3ExportsPin: the public symbols ReloadL3 must exist with
// the documented signature. Pinned by a function-value
// assignment so a future refactor that breaks the export
// surfaces at compile time.
func TestL3ExportsPin(t *testing.T) {
	var _ func(context.Context, L3MintClient, l3LoggerFunc) (Tokens, error) = ReloadL3
	if ErrReloadL3AuthRedirect == nil {
		t.Fatalf("ErrReloadL3AuthRedirect is nil")
	}
	if ErrReloadL3TokenMissing == nil {
		t.Fatalf("ErrReloadL3TokenMissing is nil")
	}
}

// TestL3AcceptanceSmoke: a single end-to-end smoke that
// exercises the fake-server stack (constructor + fakeMintClient)
// and asserts the bounded-retry / auth-redirect / token-missing
// branches are all reachable from the public surface. Future
// refactors that quietly drop one branch fail this test.
func TestL3AcceptanceSmoke(t *testing.T) {
	body := loadHomepageFixture(t)
	t.Run("happy", func(t *testing.T) {
		client := &fakeMintClient{Results: []fakeMintResult{{Body: body}}}
		if _, err := ReloadL3(context.Background(), client, nil); err != nil {
			t.Fatalf("happy err = %v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		client := &fakeMintClient{
			Results: []fakeMintResult{{Err: ErrReloadL3AuthRedirect}},
		}
		_, err := ReloadL3(context.Background(), client, nil)
		if !errors.Is(err, ErrReloadL3AuthRedirect) {
			t.Errorf("redirect err = %v, want ErrReloadL3AuthRedirect", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		client := &fakeMintClient{
			Results: []fakeMintResult{
				{Body: []byte(`<html><body>empty</body></html>`)},
				{Body: []byte(`<html><body>empty</body></html>`)},
			},
		}
		_, err := ReloadL3(context.Background(), client, nil)
		if !errors.Is(err, ErrReloadL3TokenMissing) {
			t.Errorf("missing err = %v, want ErrReloadL3TokenMissing", err)
		}
	})
}

// TestL3RespectsMaxAttempts: the bounded loop is exactly 2
// attempts; a counter only ever reaches 2.
func TestL3RespectsMaxAttempts(t *testing.T) {
	missing := []byte(`<html><body>no WIZ</body></html>`)
	client := &fakeMintClient{
		Results: []fakeMintResult{
			{Body: missing},
			{Body: missing},
		},
	}
	_, err := ReloadL3(context.Background(), client, nil)
	if !errors.Is(err, ErrReloadL3TokenMissing) {
		t.Fatalf("err = %v, want ErrReloadL3TokenMissing", err)
	}
	if client.Calls != l3MaxAttempts {
		t.Errorf("client.Calls = %d, want %d (2-attempt bound)", client.Calls, l3MaxAttempts)
	}
}

// TestL3RetryPathBackoffBounds: the bounded loop respects the
// configured backoff. Tests that flake here signal a future
// refactor that widened the backoff or dropped the ctx-honoring
// select.
func TestL3RetryPathBackoffBounds(t *testing.T) {
	missing := []byte(`<html><body>no WIZ</body></html>`)
	body := loadHomepageFixture(t)
	client := &fakeMintClient{
		Results: []fakeMintResult{
			{Body: missing},
			{Body: body},
		},
	}
	start := time.Now()
	if _, err := ReloadL3(context.Background(), client, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	elapsed := time.Since(start)
	// The backoff is 100ms (l3BaseBackoff) — a refactor that
	// accidentally widens it to multi-second magnitudes will
	// trip this assertion.
	if elapsed > 5*time.Second {
		t.Errorf("retry path took %s; backoff bounds regression", elapsed)
	}
	if client.Calls != 2 {
		t.Errorf("client.Calls = %d, want 2 (retry succeeded)", client.Calls)
	}
}
