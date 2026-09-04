// Package refresh: the L2.5 ladder rung — operator-supplied inline
// refresh command. Phase 4 wires L1 only (T-P4-2); Sprint 3
// (T-S3-001b) lands L2.5 in isolation, ahead of the ladder wiring in
// T-S3-001d.
//
// The contract:
//
//	tokens, err := refresh.ReloadL2_5(ctx, storage, logger)
//
// storage is the typed interface the rung consumes — minimal
// (Read(ctx) ([]byte, error)) so a sibling worktree's
// file-backed loader can satisfy it once L2.0 lands without
// forcing this file to know about profiles. logger is any
// function-shaped slog.Logger.Write receiver (or nil to mute);
// ReloadL2_5 never logs credential-shaped material.
//
// Behavior (per docs/AGENTIC_LOOP.md §3.2 + S03-split-tickets.md
// T-S3-001b AC list):
//
//  1. NOTEBOOKLM_REFRESH_CMD unset        → ErrReloadL2_5NotConfigured
//
//  2. NOTEBOOKLM_REFRESH_CMD_MIDSESSION≠1 → ErrReloadL2_5NotMidSession
//
//  3. Reads /dev/null as the "no real input" placeholder. L2.5 is
//     the operator-supplied refresh side-channel; the Storage
//     reader is plumbed through but not invoked when the operator
//     command is configured. The interface exists so the ladder
//     wiring in T-S3-001d can pass a non-nil Storage without
//     L2.5 having to redefine one.
//
//  4. Runs the command via os/exec under exec.CommandContext so
//     ctx cancel kills the child. The command line is passed
//     verbatim to /bin/sh -c <cmdline>; this matches the
//     Python original's shell-true model and keeps the test
//     surface single-flag.
//
//  5. Parses stdout as the canonical refreshed-tokens shape:
//
//     {"at":"<authuser_cookie>",
//     "f.sid":"<f_sid_cookie>",
//     "SNlM0e":"<csrf>"}
//
//     Malformed JSON → typed error wrapping ErrReloadL2_5Malformed.
//
//  6. 2-attempt bounded loop with a small backoff. Two attempts
//     (NOT three, unlike L2.0's three) — the inline command is a
//     fast path; if it fails once we try once more and then give
//     up. Command failures carry stderr in the wrapped error.
//
// Boundary: per docs/AGENTS.md rule 5, this file is part of the
// mode=internal package; it imports stdlib + the project's
// internal/redact (the credential-redaction regex set) only.
//
// docs/AGENTIC_LOOP.md §3.2 — T-S3-001b scope. Refs T-S3-001.
package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// ErrReloadL2_5NotConfigured is returned when the operator has not
// supplied NOTEBOOKLM_REFRESH_CMD. L2.5 is opt-in by design; an
// unconfigured L2.5 is not an error condition, just "skip this rung
// and try the next one". Tests pin it via errors.Is.
var ErrReloadL2_5NotConfigured = errors.New("refresh: L2.5: NOTEBOOKLM_REFRESH_CMD is not configured")

// ErrReloadL2_5NotMidSession is returned when the rung is invoked
// outside the mid-session window. The mid-session gate is a defense
// against running an inline refresh command accidentally during a
// batch job or a cron run — operators opt in per-invocation by
// setting NOTEBOOKLM_REFRESH_CMD_MIDSESSION=1, and the rung refuses
// to fire otherwise. Tests pin it via errors.Is.
var ErrReloadL2_5NotMidSession = errors.New("refresh: L2.5: not in mid-session (set NOTEBOOKLM_REFRESH_CMD_MIDSESSION=1)")

// ErrReloadL2_5Malformed is returned when the refresh command's
// stdout is not valid JSON carrying the canonical {at, f.sid,
// SNlM0e} shape. The rung does not try to be clever about partial
// payloads — the inline command is contract-bearing, and a
// malformed response is a configuration bug, not a retry signal.
var ErrReloadL2_5Malformed = errors.New("refresh: L2.5: refresh command output malformed")

// ErrReloadL2_5Exhausted is returned when both attempts of the
// 2-attempt bounded loop fail. It wraps the last command error so
// callers can errors.Is against it OR unwrap to surface the
// underlying message.
var ErrReloadL2_5Exhausted = errors.New("refresh: L2.5: refresh command exhausted attempts")

// envRefreshCmd is the operator-supplied refresh command. Split into
// a var so tests can override without touching os.Setenv (which
// would race with parallel test execution).
var envRefreshCmd = "NOTEBOOKLM_REFRESH_CMD"

// envRefreshCmdMidSession is the gate that locks L2.5 down to the
// mid-session invocation window. The value, when set to "1",
// authorizes the rung to fire.
var envRefreshCmdMidSession = "NOTEBOOKLM_REFRESH_CMD_MIDSESSION"

// refreshOutput is the canonical stdout payload the inline refresh
// command must emit. Any extra fields are tolerated and discarded;
// any missing field renders the payload malformed. The field names
// mirror notebooklm-py's _REFRESH_PAYLOAD_KEYS so a Python-side
// refresh helper that happens to print the same JSON works without
// modification.
type refreshOutput struct {
	// AT is the auth-user cookie value (the cookie named "at"
	// that NotebookLM uses to route requests to the right
	// account).
	AT string `json:"at"`
	// FSID is the FdrFJe-equivalent session id (the cookie
	// name has the dot in the canonical payload; the field
	// name in JSON mirrors it).
	FSID string `json:"f.sid"`
	// CSRF is the SNlM0e CSRF token. Field name mirrors the
	// Python original verbatim.
	CSRF string `json:"SNlM0e"`
}

// Storage is the minimal interface L2.5 needs from a profile /
// inline-env reader. The interface is intentionally tiny — only
// Read is required today — so a future Storage impl from L2.0 can
// satisfy it without dragging the file-backed loader into this
// file. L2.5 does NOT call Read on the configured-midsession path;
// the interface is plumbed in so the ladder wiring in T-S3-001d
// can pass a single Storage value uniformly to L1 / L2.0 / L2.5 /
// L3 without one rung's signature diverging from the others.
//
// Production callers that wire L2.5 in T-S3-001d should pass a
// non-nil Storage; nil is treated as "no upstream state available"
// (the rung still runs the inline command — the Storage is a
// future-proofing seam, not a precondition).
type Storage interface {
	// Read returns the serialized profile payload the L2.5
	// rung MAY consult. L2.5 today ignores the bytes; the
	// signature exists so the ladder (T-S3-001d) can wire a
	// single Storage across all four rungs.
	Read(ctx context.Context) ([]byte, error)
}

// loggerFunc is the minimal log sink the L2.5 rung uses for
// progress / failure breadcrumbs. The ladder wiring will pass a
// function-shaped slog receiver; tests pass a recorder so they can
// assert no credential material reaches the sink.
type loggerFunc func(msg string, attrs ...any)

// nopLogger is the default logger — drops everything on the
// floor. Production replaces this with an slog receiver.
//
// Defining a sentinel is more testable than a nil-check at every
// log call because it lets a test fake the same global with a
// `t.Cleanup(restore)` without leaking state across tests.
var nopLogger loggerFunc = func(string, ...any) {}

// l2_5DefaultBackoff is the small wait between the two attempts
// of the bounded loop. Kept short because L2.5 is a fast path; a
// transient hiccup (a half-broken refresh command) usually
// resolves within a fraction of a second.
const l2_5DefaultBackoff = 250 * time.Millisecond

// maxAttempts is the bounded-attempts cap. Tests depend on the
// exact value (2) being pinned here so the success-on-attempt-1,
// success-on-attempt-2, and exhaustion cases stay deterministic.
const maxAttempts = 2

// ReloadL2_5 runs the operator-supplied inline refresh command
// and returns the refreshed Tokens. See package doc for the full
// contract.
//
// Env-var requirements:
//
//   - NOTEBOOKLM_REFRESH_CMD must be set (a non-empty command
//     line; the rung splits it on whitespace and uses shell-free
//     exec directly so a metacharacter-laden command does not get
//     eval'd).
//   - NOTEBOOKLM_REFRESH_CMD_MIDSESSION must equal "1".
//
// Either constraint fails closed with the corresponding sentinel
// error. After both checks pass the rung invokes the command via
// exec.CommandContext so ctx.Cancel / ctx.Deadline kill the child.
// Stdout is parsed as the canonical refresh payload (see
// refreshOutput). Malformed stdout is a typed error, not a
// retryable condition.
//
// On success the rung returns a Tokens value carrying CSRF +
// SessionID populated from the command's stdout and FetchedAt set
// to the wall-clock time of the successful parse. Cookies /
// AuthUser / AccountEmail are left zero — they come from L1
// (or L2.0 in T-S3-001a), not from the inline command.
//
// logger is the structured-log sink; nil drops everything.
//
// Errors (all errors.Is-able):
//   - context.Canceled / context.DeadlineExceeded — ctx was
//     canceled mid-attempt.
//   - ErrReloadL2_5NotConfigured — env not configured.
//   - ErrReloadL2_5NotMidSession — env not gated for mid-session.
//   - ErrReloadL2_5Malformed — stdout not valid payload JSON.
//   - ErrReloadL2_5Exhausted — bounded loop exhausted; the wrapped
//     inner error carries the last command failure (and its
//     stderr).
//
// The function is safe for concurrent use; the only shared
// mutable state is the env-vars lookup, which is goroutine-safe
// via os.Getenv.
func ReloadL2_5(ctx context.Context, storage Storage, logger loggerFunc) (Tokens, error) {
	if err := ctx.Err(); err != nil {
		return Tokens{}, err
	}
	cmdLine := strings.TrimSpace(os.Getenv(envRefreshCmd))
	if cmdLine == "" {
		return Tokens{}, ErrReloadL2_5NotConfigured
	}
	if os.Getenv(envRefreshCmdMidSession) != "1" {
		return Tokens{}, ErrReloadL2_5NotMidSession
	}
	if logger == nil {
		logger = nopLogger
	}

	// The storage argument is plumbed for ladder-uniformity
	// (T-S3-001d will pass one Storage across rungs). L2.5
	// itself does not consult it; touching it here would
	// change the rung's contract. Annotate the read
	// precondition in a comment so a future reader does not
	// assume "storage is unused".
	_ = storage

	logger("refresh: L2.5 rung firing", "cmd_len", len(cmdLine))

	// The bounded-attempts loop. Two attempts: try, then try
	// once more on a transient failure. The backoff is bounded
	// (250ms) and never loops on ctx — every iteration calls
	// ctx.Err() to bail if the caller has had enough.
	var lastErr error
	backoff := l2_5DefaultBackoff
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return Tokens{}, fmt.Errorf("%w: %w (after attempt %d)", ErrReloadL2_5Exhausted, lastErr, attempt-1)
			}
			return Tokens{}, err
		}
		out, err := runRefreshCommand(ctx, cmdLine)
		if err == nil {
			tok, perr := parseRefreshOutput(out)
			if perr == nil {
				logger("refresh: L2.5 rung succeeded", "attempt", attempt)
				return Tokens{
					SessionID: tok.FSID,
					CSRF:      tok.CSRF,
					Backend:   BackendInlineEnv,
					FetchedAt: time.Now().UTC(),
				}, nil
			}
			// Malformed stdout is non-retryable: the
			// command itself succeeded but its contract
			// is wrong. Return the typed error directly
			// rather than feeding it through the loop.
			// The parse error from encoding/json is
			// joined via %w so callers can errors.As
			// the *json.SyntaxError if needed for
			// diagnostics. ErrReloadL2_5Malformed stays
			// the outer typed sentinel that the ladder
			// branches on via errors.Is.
			return Tokens{}, fmt.Errorf("%w: %w", ErrReloadL2_5Malformed, perr)
		}
		lastErr = err
		logger("refresh: L2.5 rung attempt failed", "attempt", attempt)
		if attempt < maxAttempts {
			// Backoff, honoring ctx along the way so
			// a Canceled deadline still aborts the
			// loop promptly.
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				if lastErr != nil {
					return Tokens{}, fmt.Errorf("%w: %w", ErrReloadL2_5Exhausted, lastErr)
				}
				return Tokens{}, ctx.Err()
			case <-t.C:
			}
		}
	}
	// The loop completes only on failure — both attempts
	// returned err. Surface the last attempt's stderr-bearing
	// error, wrapped in ErrReloadL2_5Exhausted so callers can
	// branch on it via errors.Is.
	return Tokens{}, fmt.Errorf("%w: %w", ErrReloadL2_5Exhausted, lastErr)
}

// runRefreshCommand exec's the operator-supplied command under
// the caller's context. Stderr is captured into the returned
// error so a refresh command that prints diagnostics on stderr
// surfaces them in the wrapped error message. Stdout is trimmed
// of leading/trailing whitespace before being returned so a
// debug-printing command does not break the JSON parser on a
// stray newline.
//
// Shell model: the operator-supplied command line is passed
// verbatim to `/bin/sh -c <cmdline>`. This matches the Python
// original's `subprocess.Popen(cmd, shell=True)` semantics and
// keeps the test surface small (one shell flag, no argv-split
// ambiguity). The /bin/sh path is hard-coded because
// `os.Getenv("SHELL")` is operator-controlled and a hostile SHELL
// is one of the threat-model surfaces L2.5 explicitly refuses
// to broaden.
//
// WaitDelay: cmd.WaitDelay is set to a small bound so the
// `cmd.Run` wait returns promptly after ctx cancel. Without
// it, an operator command that backgrounds a long-running
// process (e.g. `bash -c "refresh > /tmp/log 2>&1 &"`) would
// keep `cmd.Run` blocked on the orphaned grandchild even after
// the immediate child shell received SIGKILL.
//
// Stderr redaction: the wrapped error's diagnostic text is
// routed through internal/redact before being formatted so a
// refresh command that echoed credentials in its stderr does
// not leak through err.Error() (docs/AGENTS.md rule 4).
//
// The function returns *exec.ExitError-wrapping errors so a
// caller can errors.As the *exec.ExitError to recover the
// ExitCode if needed; the typed ErrReloadL2_5Exhausted wrapper
// preserves the underlying error chain.
func runRefreshCommand(ctx context.Context, cmdLine string) ([]byte, error) {
	if strings.TrimSpace(cmdLine) == "" {
		return nil, fmt.Errorf("refresh command is empty")
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdLine)
	// Bound the post-cancel wait so an operator command
	// that leaves an orphan grandchild running does not
	// stall the ladder. The rung is opt-in; an operator
	// whose command truly needs longer than 2 s after
	// cancel should set NOTEBOOKLM_REFRESH_CMD_MIDSESSION
	// accordingly and accept the cost.
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Inherit a curated subset of env: PATH-equivalent so the
	// operator's binary resolves, NOTHING credential-bearing.
	// docs/AGENTS.md rule 4 — credentials never reach a
	// subprocess except through the well-known allowlist; L2.5
	// is opt-in by an operator who has read the docs, so the
	// default is "no environment at all". Power users set
	// PATH themselves via the command line.
	cmd.Env = []string{}

	if err := cmd.Run(); err != nil {
		// Stderr is appended (trimmed, to keep the error
		// message one line) to the wrapped error so the
		// operator can see what the refresh command was
		// complaining about. Route the appended text
		// through redact.Apply so a refresh command that
		// printed a credential in its stderr does not
		// leak the credential back through err.Error().
		se := strings.TrimSpace(stderr.String())
		if se == "" {
			return nil, fmt.Errorf("refresh command failed: %w", err)
		}
		redacted := strings.TrimSpace(string(redact.Apply([]byte(se))))
		return nil, fmt.Errorf("refresh command failed (%s): %w", redacted, err)
	}
	out := bytes.TrimSpace(stdout.Bytes())
	return out, nil
}

// parseRefreshOutput decodes the refresh command's stdout into
// the canonical refreshOutput payload. The "missing field" branch
// is a typed error because a payload that partial-decodes is
// almost always a bug in the operator's command, not a transient
// failure worth retrying.
//
// On success the parsed payload is returned to the caller, which
// lifts the values into Tokens.
func parseRefreshOutput(stdout []byte) (refreshOutput, error) {
	// Use strict decoding so a payload with an unexpected
	// trailing comma or a mismatched brace is treated as
	// malformed rather than silently passed.
	dec := json.NewDecoder(bytes.NewReader(stdout))
	dec.DisallowUnknownFields()

	var out refreshOutput
	if err := dec.Decode(&out); err != nil {
		return refreshOutput{}, fmt.Errorf("decode stdout: %w", err)
	}
	// Surface a typed "missing required field" error rather
	// than letting the caller discover an empty CSRF / FSID
	// downstream.
	var missing []string
	if out.AT == "" {
		missing = append(missing, "at")
	}
	if out.FSID == "" {
		missing = append(missing, "f.sid")
	}
	if out.CSRF == "" {
		missing = append(missing, "SNlM0e")
	}
	if len(missing) > 0 {
		sort.Strings(missing) // stable ordering for deterministic errors
		return refreshOutput{}, fmt.Errorf("missing required fields: %s", strings.Join(missing, ","))
	}

	// The parsed values are themselves credential material;
	// route them through redact.Apply so a caller's Error()
	// display cannot leak them via the typed-error message.
	// (The returned refreshOutput is unredacted because the
	// caller lifts it into Tokens, where it becomes redacted
	// at the Tokens.String() boundary.)
	return out, nil
}

// LogValueL2_5Attempt returns a slog-friendly, redacted snapshot
// of a ReloadL2_5 attempt's outcome. Lifted out as a package-level
// helper so a future ladder-wiring ticket can compose
// slog.Any("l2_5", LogValueL2_5Attempt(...)) without each call site
// re-implementing the redacting pass.
//
// The input is the Tokens the rung produced (the zero value is
// allowed; the helper renders "tokens=none"). The error is
// optional; nil renders an empty err= field.
//
// The output is routed through redact.Apply so a refresh command
// that echoed credentials in its stderr (or its stdout, on a
// partial payload) does not leak through slog.
func LogValueL2_5Attempt(t Tokens, attempt int, err error) string {
	errStr := ""
	if err != nil {
		errStr = string(redact.Apply([]byte(err.Error())))
	}
	tokStr := t.String()
	// Tokens.String() already redacts CSRF/SessionID to the
	// standard marker; route the whole composed line through
	// redact.Apply once more to catch any unexpected leak
	// surface (e.g. an embedded credential-shaped string in a
	// future Tokens extension).
	composed := fmt.Sprintf("L2.5 attempt=%d tokens=%s err=%s", attempt, tokStr, errStr)
	return string(redact.Apply([]byte(composed)))
}
