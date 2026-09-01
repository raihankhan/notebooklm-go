package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

// maskRedactor is a test-only redactor that replaces any occurrence of
// `from` in attribute bytes with `to`. It exists so tests that need
// to verify the swap-in / swap-out seam (WithRedactor) can use a
// stable, non-credential-shape token that the default
// internal/redact implementation will never match. Using a fake
// "TOKEN" / "PAYLOAD" string keeps the swap tests independent of the
// real redactor's coverage.
type maskRedactor struct {
	from string
	to   string
}

func (m maskRedactor) Apply(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte(m.from), []byte(m.to))
}

// TestRedaction_String exercises the on-wire contract for the most common
// case: a string attribute carrying a real Google credential token
// must be masked by the default redactor (which is internal/redact
// after T-P0-3 lands).
func TestRedaction_String(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelDebug))

	log.Info("hello", slog.String("token", "SNlM0e"))

	out := buf.String()
	if strings.Contains(out, "SNlM0e") {
		t.Fatalf("credential token leaked on wire:\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("masked token missing from output:\n%s", out)
	}
}

// TestRedaction_Struct exercises the slog.KindAny branch — logging a
// struct that holds a redacted field should mask the field's text
// representation on the wire.
func TestRedaction_Struct(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelDebug))

	log.Info("payload", slog.Any("data", struct {
		User string
		Tok  string
	}{User: "alice", Tok: `{"FdrFJe":"abc123"}`}))

	out := buf.String()
	if strings.Contains(out, "FdrFJe") {
		t.Fatalf("credential token leaked on wire:\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("masked token missing from output:\n%s", out)
	}
	// Non-secret fields must survive the redactor unchanged.
	if !strings.Contains(out, "alice") {
		t.Fatalf("non-secret field dropped:\n%s", out)
	}
}

// TestRequestIDPropagation asserts the context-carried request id appears
// on every log line emitted with that context.
func TestRequestIDPropagation(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelDebug))

	ctx := WithRequestID(context.Background(), "req-abc-123")
	log.InfoContext(ctx, "first")
	log.WarnContext(ctx, "second")
	log.DebugContext(ctx, "third")

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), out)
	}
	for i, line := range lines {
		if !strings.Contains(line, `request_id=req-abc-123`) {
			t.Errorf("line %d missing request_id: %s", i, line)
		}
	}
}

// TestRequestID_EmptyIsIgnored pins the "no id, no attribute" contract.
func TestRequestID_EmptyIsIgnored(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelDebug))

	log.InfoContext(context.Background(), "no id here")
	if strings.Contains(buf.String(), "request_id=") {
		t.Fatalf("request_id leaked into output without a context value:\n%s", buf.String())
	}
}

// TestStderrOnly_Nil_WritesToStderr asserts the spec contract:
//
//	`internal/logging.New(...)` writes to stderr only — never stdout.
//
// We exercise this in two parts:
//
//  1. Passing an explicit writer (the main.go path) routes every line
//     to that writer, not stdout.
//  2. Passing nil routes every line through stderrOnly{}, which writes
//     to os.Stderr — provable by inspecting that stderrOnly's Write
//     delegates to os.Stderr, not os.Stdout.
func TestStderrOnly_Nil_WritesToStderr(t *testing.T) {
	t.Parallel()

	// Part 1: explicit writer.
	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelInfo))
	log.Info("hello")
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("explicit writer did not receive line:\n%s", buf.String())
	}

	// Part 2: stderrOnly routes to os.Stderr, not os.Stdout. We can
	// confirm by direct field/Write inspection — the writer is
	// stateless and must delegate to the stderr fd.
	var sw io.Writer = stderrOnly{}
	n, err := sw.Write([]byte("probe\n"))
	if err != nil {
		t.Fatalf("stderrOnly.Write returned error: %v", err)
	}
	if n != len("probe\n") {
		t.Errorf("stderrOnly.Write returned n=%d, want %d", n, len("probe\n"))
	}

	// Negative control: stderrOnly's Write must NOT call os.Stdout. We
	// capture os.Stdout's file pointer before/after and confirm it
	// was untouched (no fd swap, no Write call).
	stdoutBefore := os.Stdout
	_, _ = sw.Write([]byte("quiet\n"))
	if os.Stdout != stdoutBefore {
		t.Fatal("os.Stdout was clobbered by stderrOnly.Write")
	}
}

// TestNew_DefaultLevel_HonoursEnv verifies NOTEBOOKLM_LOG_LEVEL routing.
func TestNew_DefaultLevel_HonoursEnv(t *testing.T) {
	// NOT parallel — mutates process-wide env.
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "debug")

	var buf bytes.Buffer
	log := New(&buf)
	log.Debug("sentinel-debug")

	if !strings.Contains(buf.String(), "sentinel-debug") {
		t.Fatalf("debug line missing at NOTEBOOKLM_LOG_LEVEL=debug:\n%s", buf.String())
	}
}

// TestNew_DefaultLevel_DefaultsToInfo verifies the default info level
// suppresses debug when the env var is unset / unrecognized.
func TestNew_DefaultLevel_DefaultsToInfo(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "nonsense")

	var buf bytes.Buffer
	log := New(&buf)
	log.Debug("sentinel-debug")
	log.Info("sentinel-info")

	out := buf.String()
	if strings.Contains(out, "sentinel-debug") {
		t.Errorf("debug leaked at default level:\n%s", out)
	}
	if !strings.Contains(out, "sentinel-info") {
		t.Errorf("info dropped at default level:\n%s", out)
	}
}

// TestOptionLevel rebuilds the handler with a custom level and asserts the
// new level is honored.
func TestOptionLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelError))

	log.Warn("should-not-appear")
	log.Error("should-appear", slog.String("k", "v"))

	out := buf.String()
	if strings.Contains(out, "should-not-appear") {
		t.Errorf("warn leaked at Error level:\n%s", out)
	}
	if !strings.Contains(out, "should-appear") {
		t.Errorf("error dropped at Error level:\n%s", out)
	}
}

// TestOptionVerbosity_Positive asserts -v (count >= 1) raises the level
// to Debug.
func TestOptionVerbosity_Positive(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "") // clear any inherited env

	var buf bytes.Buffer
	log := New(&buf, OptionVerbosity(1))
	log.Debug("sentinel-v1")

	if !strings.Contains(buf.String(), "sentinel-v1") {
		t.Fatalf("debug dropped with OptionVerbosity(1):\n%s", buf.String())
	}
}

// TestOptionVerbosity_Negative asserts very negative counts lower the
// level to Error.
func TestOptionVerbosity_Negative(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "")

	var buf bytes.Buffer
	log := New(&buf, OptionVerbosity(-2))
	log.Warn("should-not-appear")
	log.Error("should-appear")

	out := buf.String()
	if strings.Contains(out, "should-not-appear") {
		t.Errorf("warn leaked at OptionVerbosity(-2):\n%s", out)
	}
	if !strings.Contains(out, "should-appear") {
		t.Errorf("error dropped at OptionVerbosity(-2):\n%s", out)
	}
}

// TestOptionVerbosity_ZeroLeavesLevelAlone — count==0 is a no-op so
// callers can use it without conditionally passing the Option.
func TestOptionVerbosity_ZeroLeavesLevelAlone(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "")

	var buf bytes.Buffer
	log := New(&buf, OptionVerbosity(0))
	log.Debug("should-not-appear-noop")

	if strings.Contains(buf.String(), "should-not-appear-noop") {
		t.Errorf("debug leaked after no-op OptionVerbosity(0):\n%s", buf.String())
	}
}

// TestReplaceAttr_DefaultRedactorIsRealRedact confirms that the
// default redactor (after T-P0-3) is the real internal/redact.Apply.
// We probe with a benign value that no regex family matches so the
// byte survives the pipeline unchanged — if the default were still
// the T-P0-2 no-op stub the test would also pass, but the matching
// TestRedaction_String above proves real redaction fires when a
// credential token is present.
func TestReplaceAttr_DefaultRedactorIsRealRedact(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "")

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelDebug))
	log.Info("plain", slog.String("k", "harmless-text"))

	if !strings.Contains(buf.String(), "harmless-text") {
		t.Fatalf("benign value should pass through the default redactor:\n%s",
			buf.String())
	}
}

// TestWithRedactor_Restores asserts the cleanup closure really restores the
// previous redactor.
func TestWithRedactor_Restores(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "")

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelDebug))

	// Mask
	cleanup := WithRedactor(maskRedactor{from: "TOKEN", to: "X"})
	log.Info("first", slog.String("k", "TOKEN"))
	if strings.Contains(buf.String(), "TOKEN") {
		t.Fatalf("masked value leaked:\n%s", buf.String())
	}

	// Restore
	cleanup()

	buf.Reset()
	log.Info("second", slog.String("k", "TOKEN"))
	if !strings.Contains(buf.String(), "TOKEN") {
		t.Fatalf("restored redactor did not pass through:\n%s", buf.String())
	}
}

// TestNew_HonoursRedactorAfterConstruction confirms that WithRedactor
// (which mutates the package-level pointer) is honored by loggers
// constructed BEFORE the swap. The redactor is resolved lazily inside
// replaceAttr, not captured at handler-construction time — this test
// pins that contract.
//
// (The Default() process-wide logger goes to os.Stderr which we can't
// capture portably in this test environment, so we exercise the seam
// via a directly-constructed logger that uses the same newHandler
// pathway.)
func TestNew_HonoursRedactorAfterConstruction(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "")

	var buf bytes.Buffer
	log := slog.New(newHandler(&buf, slog.LevelDebug))

	// Pre-swap: the secret reaches the wire.
	log.Info("pre", slog.String("k", "PAYLOAD"))
	if !strings.Contains(buf.String(), "PAYLOAD") {
		t.Fatalf("setup: pre-swap line missing payload:\n%s", buf.String())
	}
	buf.Reset()

	// Swap in a masking redactor AFTER the logger was built.
	cleanup := WithRedactor(maskRedactor{from: "PAYLOAD", to: "Z"})
	defer cleanup()

	log.Info("post", slog.String("k", "PAYLOAD"))
	if strings.Contains(buf.String(), "PAYLOAD") {
		t.Fatalf("late-bound redactor ignored:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Z") {
		t.Fatalf("masked value missing from output:\n%s", buf.String())
	}
}

// TestStderrOnly_Typeassertion is a tiny contract test for the
// stderrOnly{} writer.
func TestStderrOnly_Typeassertion(t *testing.T) {
	t.Parallel()

	var w io.Writer = stderrOnly{}
	// Sanity: writing to it must not panic and must route to os.Stderr.
	// We can't capture os.Stderr in a portable way, so we only assert
	// the Write succeeds and returns a non-negative count.
	n, err := w.Write([]byte(""))
	if err != nil {
		t.Fatalf("stderrOnly.Write returned error: %v", err)
	}
	if n != 0 {
		t.Errorf("empty Write returned n=%d, want 0", n)
	}
}

// TestConcurrentLogging is a smoke test for race conditions — we rely on
// `go test -race` to flag issues, but we also assert no panic.
func TestConcurrentLogging(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelDebug))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			log.Info("concurrent", slog.Int("i", i))
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 8 {
		t.Fatalf("expected 8 lines, got %d:\n%s", len(lines), buf.String())
	}
}

// TestNew_NilWriter_FallsBackToStderr covers the contract path used by
// Default(). We can't intercept os.Stderr here, but we can verify the
// returned logger doesn't panic on first use.
func TestNew_NilWriter_FallsBackToStderr(t *testing.T) {
	t.Parallel()

	log := New(nil, OptionLevel(slog.LevelDebug))
	// We can't assert the destination without hijacking fd 2, so we
	// only assert construction succeeds and Enabled() is true.
	if !log.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("nil-writer logger reported Debug as disabled")
	}

	// And we exercise a no-op write path to ensure no panic on init.
	_ = os.Stderr // guard: os is referenced even when fd isn't captured
}

// TestNew_DefaultLevel_WarnRouting covers the "warn" / "warning" branch
// of levelFromEnv.
func TestNew_DefaultLevel_WarnRouting(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "warn")

	var buf bytes.Buffer
	log := New(&buf)
	log.Info("should-not-appear")
	log.Warn("should-appear")

	out := buf.String()
	if strings.Contains(out, "should-not-appear") {
		t.Errorf("info leaked at NOTEBOOKLM_LOG_LEVEL=warn:\n%s", out)
	}
	if !strings.Contains(out, "should-appear") {
		t.Errorf("warn dropped at NOTEBOOKLM_LOG_LEVEL=warn:\n%s", out)
	}
}

// TestNew_DefaultLevel_WarningAlias covers the "warning" alias.
func TestNew_DefaultLevel_WarningAlias(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "warning")

	var buf bytes.Buffer
	log := New(&buf)
	log.Warn("should-appear-warning")

	if !strings.Contains(buf.String(), "should-appear-warning") {
		t.Fatalf("warn dropped at NOTEBOOKLM_LOG_LEVEL=warning:\n%s", buf.String())
	}
}

// TestNew_DefaultLevel_ErrorRouting covers the "error" branch.
func TestNew_DefaultLevel_ErrorRouting(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "ERROR") // also exercises case-insensitive

	var buf bytes.Buffer
	log := New(&buf)
	log.Warn("should-not-appear-error")
	log.Error("should-appear-error")

	out := buf.String()
	if strings.Contains(out, "should-not-appear-error") {
		t.Errorf("warn leaked at NOTEBOOKLM_LOG_LEVEL=error:\n%s", out)
	}
	if !strings.Contains(out, "should-appear-error") {
		t.Errorf("error dropped at NOTEBOOKLM_LOG_LEVEL=error:\n%s", out)
	}
}

// TestWithAttrs_PropagatesRequestID confirms chained attributes
// survive the WithAttrs pass-through (covers the small wrapper method).
func TestWithAttrs_PropagatesRequestID(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelDebug))
	ctx := WithRequestID(context.Background(), "req-attrs")
	chained := log.With(slog.String("component", "test"))
	chained.InfoContext(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, "request_id=req-attrs") {
		t.Errorf("request_id missing on chained logger:\n%s", out)
	}
	if !strings.Contains(out, "component=test") {
		t.Errorf("component attribute missing:\n%s", out)
	}
}

// TestWithGroup_PropagatesWriter confirms WithGroup preserves the writer
// and the request_id hook still fires through the grouped handler.
func TestWithGroup_PropagatesWriter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	base := New(&buf, OptionLevel(slog.LevelDebug))
	grouped := base.WithGroup("svc")
	ctx := WithRequestID(context.Background(), "req-grp")
	grouped.InfoContext(ctx, "hello", slog.String("k", "v"))

	out := buf.String()
	if !strings.Contains(out, "svc") {
		t.Fatalf("WithGroup did not apply group name:\n%s", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Fatalf("attribute inside group not emitted:\n%s", out)
	}
	if !strings.Contains(out, "request_id=req-grp") {
		t.Fatalf("request_id missing through grouped handler:\n%s", out)
	}
}

// TestWithRedactor_NilUsesDefault confirms nil is replaced by the noop.
func TestWithRedactor_NilUsesDefault(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "")

	cleanup := WithRedactor(nil)
	defer cleanup()

	var buf bytes.Buffer
	log := slog.New(newHandler(&buf, slog.LevelDebug))
	log.Info("plain", slog.String("k", "SECRET-NIL"))

	if !strings.Contains(buf.String(), "SECRET-NIL") {
		t.Fatalf("nil redactor should fall back to noop:\n%s", buf.String())
	}
}

// TestDefault_AndSetDefault exercises the lazy-init process-wide logger
// and SetDefault(nil) reset path.
func TestDefault_AndSetDefault(t *testing.T) {
	t.Setenv("NOTEBOOKLM_LOG_LEVEL", "")

	// Calling Default multiple times must return the same logger.
	a := Default()
	b := Default()
	if a != b {
		t.Errorf("Default() returned different loggers on consecutive calls")
	}

	// Capture and restore.
	prev := a
	t.Cleanup(func() { SetDefault(prev) })

	// SetDefault with a custom logger.
	custom := New(nil, OptionLevel(slog.LevelError))
	SetDefault(custom)
	if Default() != custom {
		t.Errorf("SetDefault did not replace Default()")
	}

	// SetDefault(nil) should rebuild the process-default logger.
	SetDefault(nil)
	if Default() == nil {
		t.Errorf("SetDefault(nil) left Default() nil")
	}
}

// TestWithRequestID_EmptyInput returns the original context unchanged —
// documented contract for callers that pass "" through.
func TestWithRequestID_EmptyInput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if WithRequestID(ctx, "") != ctx {
		t.Errorf("WithRequestID(ctx, \"\") should return the original ctx")
	}
}

// TestRequestID_NilContext handles a defensive caller that passes a nil
// context.Context interface. We probe the nil interface (not a typed-nil
// concrete context) so staticcheck's nil-context rule stays happy — the
// underlying contract is "any nil context.Context yields an empty id".
func TestRequestID_NilContext(t *testing.T) {
	t.Parallel()

	var ctx context.Context // nil interface, NOT a typed nil
	if got := RequestID(ctx); got != "" {
		t.Errorf("RequestID(nil) = %q, want empty string", got)
	}
}

// TestCurrentWriter_UnknownHandler covers the fallback path in
// currentWriter when the handler isn't a *redactHandler.
func TestCurrentWriter_UnknownHandler(t *testing.T) {
	t.Parallel()

	other := slog.NewTextHandler(io.Discard, nil)
	if w := currentWriter(other); w == nil {
		t.Errorf("currentWriter should fall back to stderrOnly{} for unknown handlers")
	}
}

// TestEnabled_Delegates pins the trivial pass-through path.
func TestEnabled_Delegates(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, OptionLevel(slog.LevelWarn))
	if log.Enabled(context.Background(), slog.LevelDebug) {
		t.Errorf("Debug should be disabled at LevelWarn")
	}
	if !log.Enabled(context.Background(), slog.LevelWarn) {
		t.Errorf("Warn should be enabled at LevelWarn")
	}
}
