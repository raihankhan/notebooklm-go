// Package refresh: tests for L2.5 — the inline-command ladder rung
// landed in T-S3-001b. The suite pins every AC from
// docs/sprint-reports/S03-split-tickets.md §"T-S3-001b" +
// docs/sprint-reports/S03-tickets.md §"T-S3-001" (L2.5 row).
//
// AC list under test:
//
//  1. Reads NOTEBOOKLM_REFRESH_CMD env var.
//  2. Env unset → ErrReloadL2_5NotConfigured.
//  3. NOTEBOOKLM_REFRESH_CMD_MIDSESSION unset → ErrReloadL2_5NotMidSession.
//  4. exec.CommandContext so ctx cancel kills the child.
//  5. 2-attempt bounded loop.
//  6. Stdout parsed as JSON; malformed → typed error.
//  7. Command-failure error includes stderr.
//
// Plus the regression guard:
//
//   - no-cred log-scan: error.Error() and Tokens.LogValue() do not
//     leak a credential-shaped substring.
//
// Boundary: per docs/AGENTS.md rule 5 this test file is part of the
// mode=internal package; it imports stdlib only. Environment
// manipulation goes through t.Setenv which is race-safe.
package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeStorage satisfies Storage with a no-op Read so tests can
// pass a non-nil Storage through ReloadL2_5 without ceremony.
type fakeStorage struct {
	bytes []byte
	err   error
}

func (f *fakeStorage) Read(ctx context.Context) ([]byte, error) {
	return f.bytes, f.err
}

// shellQuote wraps s in single quotes for /bin/sh -c so a payload
// containing whitespace, quotes, or braces does not break the
// shell parser. Single-quoted strings are literal except for the
// closing single quote, which we escape as four characters:
// close-quote, backslash, open-quote, apostrophe, open-quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeScriptTemp writes s as an executable file in a tempdir
// and returns its path. Used by tests that want a script (rather
// than `sh -c '...'`) so the rung's arg-split path is exercised
// end-to-end.
func writeScriptTemp(t *testing.T, s string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "refresh.sh")
	// #nosec G306 -- the script must be executable for the
	// rung to invoke it directly; a 0600 permission would
	// leave the test file non-executable.
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+s+"\n"), 0o755); err != nil { // #nosec G306
		t.Fatalf("write script: %v", err)
	}
	return p
}

// TestL2_5NotConfigured: NOTEBOOKLM_REFRESH_CMD unset →
// ErrReloadL2_5NotConfigured; the rung refuses to fire and the
// mid-session gate is never reached (the config gate is checked
// first per docs/S03-split-tickets.md AC2).
func TestL2_5NotConfigured(t *testing.T) {
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", "")
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")
	_, err := ReloadL2_5(context.Background(), nil, nil)
	if !errors.Is(err, ErrReloadL2_5NotConfigured) {
		t.Fatalf("ReloadL2_5(unset) error = %v, want ErrReloadL2_5NotConfigured", err)
	}
}

// TestL2_5NotMidSession: NOTEBOOKLM_REFRESH_CMD set but the
// mid-session gate unset → ErrReloadL2_5NotMidSession. The rung
// refuses to fire even though the command is configured.
func TestL2_5NotMidSession(t *testing.T) {
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", "true")
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "")
	_, err := ReloadL2_5(context.Background(), nil, nil)
	if !errors.Is(err, ErrReloadL2_5NotMidSession) {
		t.Fatalf("ReloadL2_5(no midsession) error = %v, want ErrReloadL2_5NotMidSession", err)
	}
}

// TestL2_5NotMidSessionWrongValue: the mid-session gate only
// opens for "1"; "true", "yes", "on" all close it.
func TestL2_5NotMidSessionWrongValue(t *testing.T) {
	for _, v := range []string{"true", "yes", "on", "2", "enabled"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("NOTEBOOKLM_REFRESH_CMD", "true")
			t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", v)
			_, err := ReloadL2_5(context.Background(), nil, nil)
			if !errors.Is(err, ErrReloadL2_5NotMidSession) {
				t.Fatalf("ReloadL2_5(midsession=%q) error = %v, want ErrReloadL2_5NotMidSession", v, err)
			}
		})
	}
}

// TestL2_5SuccessFirstAttempt: a well-behaved command that prints
// valid payload JSON on attempt 1 → Tokens with CSRF / SessionID
// populated.
func TestL2_5SuccessFirstAttempt(t *testing.T) {
	wantCSRF := "FRESH_CSRF_TOKEN_AAA"
	wantFSID := "FRESH_FSID_BBB"
	payload := refreshOutput{
		AT:   "AUTHUSER_COOKIE_CCC",
		FSID: wantFSID,
		CSRF: wantCSRF,
	}
	payloadJSON, _ := json.Marshal(payload)
	// The env-var command line is a shell script that
	// echoes the payload JSON on stdout. /bin/sh -c
	// executes it under exec.CommandContext.
	cmdLine := "echo " + shellQuote(string(payloadJSON))
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	tokens, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
	if err != nil {
		t.Fatalf("ReloadL2_5(success) error = %v", err)
	}
	if tokens.CSRF != wantCSRF {
		t.Errorf("tokens.CSRF = %q, want %q", tokens.CSRF, wantCSRF)
	}
	if tokens.SessionID != wantFSID {
		t.Errorf("tokens.SessionID = %q, want %q", tokens.SessionID, wantFSID)
	}
	if tokens.Backend != BackendInlineEnv {
		t.Errorf("tokens.Backend = %v, want BackendInlineEnv", tokens.Backend)
	}
	if tokens.FetchedAt.IsZero() {
		t.Errorf("tokens.FetchedAt is zero")
	}
}

// TestL2_5SuccessSecondAttempt: a command that fails the first
// invocation then succeeds on retry → Tokens populated. The
// retry mechanism is the 2-attempt bounded loop; the test
// uses a sentinel-file trick to count invocations and flip
// behavior between attempts.
func TestL2_5SuccessSecondAttempt(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "attempts")
	wantCSRF := "RETRY_CSRF_DDD"
	wantFSID := "RETRY_FSID_EEE"
	payload := refreshOutput{
		AT:   "AUTHUSER_COOKIE_FFF",
		FSID: wantFSID,
		CSRF: wantCSRF,
	}
	payloadJSON, _ := json.Marshal(payload)
	// On first invocation: write counter=1 and exit 1. On
	// second invocation: write counter=2 and print the
	// payload.
	cmdLine := "if [ ! -f " + shellQuote(counter) + " ]; then " +
		"echo 1 > " + shellQuote(counter) + "; exit 1; " +
		"else echo 2 >> " + shellQuote(counter) + "; " +
		"echo " + shellQuote(string(payloadJSON)) + "; fi"
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	tokens, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
	if err != nil {
		t.Fatalf("ReloadL2_5(retry-success) error = %v", err)
	}
	if tokens.CSRF != wantCSRF {
		t.Errorf("tokens.CSRF = %q, want %q", tokens.CSRF, wantCSRF)
	}
	if tokens.SessionID != wantFSID {
		t.Errorf("tokens.SessionID = %q, want %q", tokens.SessionID, wantFSID)
	}
	// The counter file should have both 1 and 2 recorded.
	// #nosec G304 -- counter is a t.TempDir()-rooted path
	// written by the test's own subprocess; no external
	// input.
	b, rerr := os.ReadFile(counter) // #nosec G304
	if rerr != nil {
		t.Fatalf("read counter: %v", rerr)
	}
	if !bytes.Contains(b, []byte("1\n2")) {
		t.Errorf("counter file = %q, want both attempt-1 and attempt-2 lines", string(b))
	}
}

// TestL2_5AllAttemptsFailed: a command that always exits 1 →
// ErrReloadL2_5Exhausted wrapping the last attempt's error, and
// the stderr is surfaced in the message.
func TestL2_5AllAttemptsFailed(t *testing.T) {
	cmdLine := "echo 'SIMULATED REFRESH FAILURE' 1>&2; exit 1"
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	_, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
	if err == nil {
		t.Fatalf("ReloadL2_5(all-failed) error = nil, want ErrReloadL2_5Exhausted")
	}
	if !errors.Is(err, ErrReloadL2_5Exhausted) {
		t.Fatalf("ReloadL2_5(all-failed) error = %v, want ErrReloadL2_5Exhausted", err)
	}
	if !strings.Contains(err.Error(), "SIMULATED REFRESH FAILURE") {
		t.Errorf("ReloadL2_5(all-failed) message missing stderr: %q", err.Error())
	}
}

// TestL2_5MalformedJSON: a command that exits 0 but prints
// non-JSON → ErrReloadL2_5Malformed. The rung does NOT retry on
// malformed output (it's a config bug, not a transient).
func TestL2_5MalformedJSON(t *testing.T) {
	cmdLine := "echo 'this is not JSON'"
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	_, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
	if err == nil {
		t.Fatalf("ReloadL2_5(malformed) error = nil, want ErrReloadL2_5Malformed")
	}
	if !errors.Is(err, ErrReloadL2_5Malformed) {
		t.Fatalf("ReloadL2_5(malformed) error = %v, want ErrReloadL2_5Malformed", err)
	}
}

// TestL2_5MalformedJSONMissingField: a payload that decodes but
// is missing a required field → ErrReloadL2_5Malformed with a
// typed "missing required fields" suffix.
func TestL2_5MalformedJSONMissingField(t *testing.T) {
	// Missing SNlM0e — the most common partial payload.
	cmdLine := `printf '{"at":"x","f.sid":"y"}'`
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	_, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
	if !errors.Is(err, ErrReloadL2_5Malformed) {
		t.Fatalf("ReloadL2_5(missing-field) error = %v, want ErrReloadL2_5Malformed", err)
	}
	if !strings.Contains(err.Error(), "SNlM0e") {
		t.Errorf("ReloadL2_5(missing-field) message missing field name: %q", err.Error())
	}
}

// TestL2_5ContextCancelKillsChild: a long-running command is
// killed when the caller's context is canceled mid-flight. The
// child writes a sentinel file as it starts so the test can
// confirm the child was actually exec'd before being killed.
func TestL2_5ContextCancelKillsChild(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "child_started")
	cmdLine := "echo x > " + shellQuote(sentinel) + "; sleep 30"
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := ReloadL2_5(ctx, &fakeStorage{}, nil)
		errCh <- err
	}()
	// Give the child a moment to exec and write the sentinel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sentinel); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		// Two acceptable outcomes: a wrapped
		// ErrReloadL2_5Exhausted (if the cancel landed
		// after attempt 1 started running) or a direct
		// ctx error (if the cancel landed before any
		// attempt ran). Either is fine; the invariant
		// is "the rung returned within the cancel
		// window".
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatalf("ReloadL2_5 did not return after ctx cancel")
	}
	// Verify the child actually started (so we know we
	// exercised the kill-child path, not the
	// never-exec'd path).
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("child sentinel %q not written; test did not exercise exec: %v", sentinel, err)
	}
}

// TestL2_5NoCredentialLeak: the regression guard. A failed run
// whose command's stderr or stdout carries credential-shaped
// substrings MUST NOT propagate them through err.Error() OR
// through the Tokens.LogValue() helper.
//
// This is the AC item: "no credential-shaped substring leaks
// through error.Error() or Tokens.LogValue()" — the same shape
// docs/AGENTS.md rule 4 enforces for every other auth component.
func TestL2_5NoCredentialLeak(t *testing.T) {
	// Seeded credential-shaped strings. These are deliberately
	// high-entropy and shaped like real SNlM0e / FdrFJe values
	// so a regex match is non-coincidental.
	const (
		seedCSRF    = "LEAKED_CSRF_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
		seedSession = "LEAKED_FSID_ZyXwVuTsRqPoNmLkJiHgFeDcBa9876543210"
	)

	// Case 1: command exits non-zero with credential in stderr.
	// err.Error() must redact the credential.
	t.Run("stderr_credential", func(t *testing.T) {
		cmdLine := "echo SNlM0e=" + seedCSRF + " 1>&2; exit 1"
		t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
		t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

		_, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
		if err == nil {
			t.Fatalf("expected error from failing command")
		}
		// The err.Error() must NOT contain the literal
		// credential value. The redact regex catches
		// `SNlM0e=...` shapes; the surrounding
		// ErrReloadL2_5Exhausted wrapper does not
		// pre-empt that.
		if strings.Contains(err.Error(), seedCSRF) {
			t.Errorf("err.Error() leaked CSRF: %q", err.Error())
		}
	})

	// Case 2: a successful command whose JSON payload
	// contains the credential — the Tokens.LogValue()
	// helper must redact, AND the Tokens.String() must
	// already redact (it does — see TestTokensStringRedacts
	// in l1_test.go). Here we test LogValueL2_5Attempt
	// composition.
	t.Run("tokens_logvalue_credential", func(t *testing.T) {
		tokens := Tokens{
			Cookies:      []CookieView{{Name: "SID", Domain: ".google.com", Path: "/"}},
			CSRF:         seedCSRF,
			SessionID:    seedSession,
			AuthUser:     7,
			AccountEmail: "alice@example.invalid",
			Backend:      BackendInlineEnv,
			FetchedAt:    time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC),
		}
		got := LogValueL2_5Attempt(tokens, 1, nil)
		for _, secret := range []string{seedCSRF, seedSession} {
			if strings.Contains(got, secret) {
				t.Errorf("LogValueL2_5Attempt leaked %q: %q", secret, got)
			}
		}
	})

	// Case 3: a successful command whose err-on-exhaust
	// path's message includes a credential — the helper
	// must redact via the err-string branch.
	t.Run("attempt_helper_err_credential", func(t *testing.T) {
		// Build a synthetic err whose message embeds the
		// credential. Run it through LogValueL2_5Attempt
		// and assert the credential is masked.
		err := &errWithCred{msg: "refresh command failed (stderr=SNlM0e=" + seedCSRF + ")"}
		tokens := Tokens{
			CSRF:    seedCSRF,
			Backend: BackendInlineEnv,
		}
		got := LogValueL2_5Attempt(tokens, 2, err)
		if strings.Contains(got, seedCSRF) {
			t.Errorf("LogValueL2_5Attempt leaked CSRF via err branch: %q", got)
		}
	})
}

// errWithCred is a tiny error type whose Error() returns a string
// containing a seeded credential. Used to drive the
// LogValueL2_5Attempt err-branch redaction path deterministically.
type errWithCred struct{ msg string }

func (e *errWithCred) Error() string { return e.msg }

// TestL2_5SentinelsDistinct: every exported sentinel must be
// errors.Is-distinct from the others. A future refactor that
// silently merges two sentinels would cause this test to fail.
func TestL2_5SentinelsDistinct(t *testing.T) {
	all := []error{
		ErrReloadL2_5NotConfigured,
		ErrReloadL2_5NotMidSession,
		ErrReloadL2_5Malformed,
		ErrReloadL2_5Exhausted,
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if errors.Is(all[i], all[j]) {
				t.Errorf("sentinel %q collides with %q",
					all[i].Error(), all[j].Error())
			}
		}
	}
	// The sentinel must also be distinct from the L1
	// not-implemented sentinel that lives in ladder.go.
	if errors.Is(ErrReloadL2_5NotConfigured, ErrLadderLevelNotImplemented) {
		t.Errorf("L2.5 NotConfigured collides with ErrLadderLevelNotImplemented")
	}
}

// TestL2_5RespectsMaxAttempts: the bounded loop is exactly 2
// attempts; a counter only ever reaches 2. This pins the AC
// item "2-attempt bounded loop".
func TestL2_5RespectsMaxAttempts(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "n")
	cmdLine := "echo x >> " + shellQuote(counter) + "; exit 1"
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	_, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
	if !errors.Is(err, ErrReloadL2_5Exhausted) {
		t.Fatalf("err = %v, want ErrReloadL2_5Exhausted", err)
	}
	// #nosec G304 -- counter is a t.TempDir()-rooted path
	// written by the test's own subprocess; no external
	// input.
	b, rerr := os.ReadFile(counter) // #nosec G304
	if rerr != nil {
		t.Fatalf("read counter: %v", rerr)
	}
	got := bytes.Count(b, []byte{'x'})
	if want := 2; got != want {
		t.Errorf("invocation count = %d, want %d (2-attempt bound); lines=%q", got, want, string(b))
	}
}

// TestL2_5CommandContextKill: a deliberately long-running
// command is killed by exec.CommandContext when ctx is canceled;
// the test exercises the kill path by bounding how long the
// rung takes to return. This is the AC item "Uses
// exec.CommandContext so ctx cancel kills the child".
func TestL2_5CommandContextKill(t *testing.T) {
	cmdLine := "sleep 30"
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := ReloadL2_5(ctx, &fakeStorage{}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("ReloadL2_5(killed) error = nil, want non-nil")
	}
	if elapsed > 3*time.Second {
		t.Errorf("ReloadL2_5 took %s to return after ctx cancel, want <3s", elapsed)
	}
}

// TestL2_5StoragePlumbing: a nil Storage is allowed; the rung
// does not call Read on the configured-midsession path. The
// interface is plumbed for ladder-uniformity (T-S3-001d), not
// for the rung to consult. A non-nil Storage whose Read
// returns an error is also tolerated — L2.5 ignores it.
func TestL2_5StoragePlumbing(t *testing.T) {
	cmdLine := `printf '{"at":"x","f.sid":"y","SNlM0e":"z"}'`
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	// nil storage: success.
	if _, err := ReloadL2_5(context.Background(), nil, nil); err != nil {
		t.Errorf("ReloadL2_5(nil storage) error = %v, want nil", err)
	}

	// erroring storage: ignored, success.
	st := &fakeStorage{err: errors.New("simulated upstream failure")}
	if _, err := ReloadL2_5(context.Background(), st, nil); err != nil {
		t.Errorf("ReloadL2_5(erring storage) error = %v, want nil (L2.5 ignores Storage)", err)
	}
}

// TestL2_5ExitErrorAsPin: a command that exits non-zero can be
// introspected via errors.As to recover the *exec.ExitError.
// This pins the integration with os/exec so a future refactor
// that swaps exec.CommandContext for something else fails here.
func TestL2_5ExitErrorAsPin(t *testing.T) {
	cmdLine := "exit 42"
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", cmdLine)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	_, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
	if err == nil {
		t.Fatalf("ReloadL2_5(exit42) error = nil, want non-nil")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		// We fail loud to flag the regression but keep
		// the test resilient to the message shape.
		t.Errorf("ReloadL2_5(exit42) error chain does not contain *exec.ExitError: %v", err)
	}
}

// TestL2_5ScriptFilePath: a non-shell command path (an
// executable script that does its own refresh) is split into
// argv via strings.Fields — exercises the script-arg path end
// to end. This is the production case: a script written and
// chmodded by the operator.
func TestL2_5ScriptFilePath(t *testing.T) {
	script := writeScriptTemp(t,
		`printf '{"at":"x","f.sid":"script_fsid","SNlM0e":"script_csrf"}'`)

	t.Setenv("NOTEBOOKLM_REFRESH_CMD", script)
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "1")

	tokens, err := ReloadL2_5(context.Background(), &fakeStorage{}, nil)
	if err != nil {
		t.Fatalf("ReloadL2_5(script) error = %v", err)
	}
	if tokens.CSRF != "script_csrf" {
		t.Errorf("tokens.CSRF = %q, want script_csrf", tokens.CSRF)
	}
	if tokens.SessionID != "script_fsid" {
		t.Errorf("tokens.SessionID = %q, want script_fsid", tokens.SessionID)
	}
}
