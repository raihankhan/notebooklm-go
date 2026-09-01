// Package logging is the stderr-only slog logger for notebooklm-go.
//
// Public API surface:
//
//	logging.New(os.Stderr)        // explicit writer (tests use bytes.Buffer)
//	logging.New(nil)              // defaults to os.Stderr via stderrOnly{}
//	logging.Default()             // the process-wide *slog.Logger
//	logging.WithRequestID(ctx,id) // attach a request id to ctx
//	logging.RequestID(ctx)        // extract the request id from ctx
//
// Level precedence (highest priority first):
//
//  1. explicit OptionLevel(...) / OptionVerbosity(n) — overrides everything
//  2. NOTEBOOKLM_LOG_LEVEL env var
//  3. slog.LevelInfo (default)
//
// The redaction seam (see redact_hook.go) is deliberately package-private;
// callers cannot inject a redactor without using the test-only WithRedactor.
// When T-P0-3 lands the seam will be wired to internal/redact in main.go.
package logging

import (
	"io"
	"log/slog"
	"sync"
)

// Option mutates the *slog.Logger returned by New.
type Option func(*slog.Logger) *slog.Logger

// OptionLevel sets the minimum level the handler emits. Use this when
// NOTEBOOKLM_LOG_LEVEL is too coarse (e.g. tests that need LevelError).
func OptionLevel(l slog.Level) Option {
	return func(in *slog.Logger) *slog.Logger {
		h := in.Handler()
		// We can't lower the inner handler's level post-construction,
		// so we rebuild. This is the documented escape hatch.
		newH := newHandler(currentWriter(h), l)
		return slog.New(newH)
	}
}

// OptionVerbosity is the -v-style counter. Counts above zero raise the
// level to Debug; counts below zero lower it to Error. The counter is
// additive on top of NOTEBOOKLM_LOG_LEVEL — callers can still be
// constrained downward by OptionLevel.
//
// Behavior matches kubectl / docker / gh: -v is debug, -vv is debug
// (counter is informational; we don't differentiate v=2 from v=1 yet).
func OptionVerbosity(count int) Option {
	return func(in *slog.Logger) *slog.Logger {
		var lvl slog.Level
		switch {
		case count <= -2:
			lvl = slog.LevelError
		case count <= -1:
			lvl = slog.LevelWarn
		case count >= 1:
			lvl = slog.LevelDebug
		default:
			return in // count == 0 — leave level alone
		}
		h := in.Handler()
		newH := newHandler(currentWriter(h), lvl)
		return slog.New(newH)
	}
}

// currentWriter extracts the captured io.Writer from a redactHandler so
// Option rebuilds don't accidentally lose the user's destination. When
// the handler isn't ours (e.g. a chained .Handler().WithGroup returned
// something exotic), we fall back to stderrOnly{}.
func currentWriter(h slog.Handler) io.Writer {
	if rh, ok := h.(*redactHandler); ok && rh.writer != nil {
		return rh.writer
	}
	return stderrOnly{}
}

// New builds a *slog.Logger that writes to w (or os.Stderr when w is nil)
// using the level derived from NOTEBOOKLM_LOG_LEVEL / opts.
//
// Callers must not assume any default format — today it's text; later
// phases may add a JSON option for the REST server. All current CLI
// consumers want text (greppable).
func New(w io.Writer, opts ...Option) *slog.Logger {
	base := slog.New(newHandler(w, levelFromEnv()))
	for _, opt := range opts {
		base = opt(base)
	}
	return base
}

var (
	defaultOnce sync.Once
	defaultLog  *slog.Logger
)

// Default returns the process-wide *slog.Logger. It is created lazily on
// first call so test binaries that swap WithRedactor() before logging
// still see their injected redactor.
//
// Use Default() from main.go and from library callers that don't want to
// plumb a *slog.Logger through every function. Tests should construct
// their own logger with New() to keep output deterministic.
func Default() *slog.Logger {
	defaultOnce.Do(func() {
		defaultLog = New(nil)
	})
	return defaultLog
}

// SetDefault atomically replaces the process-wide logger. Reserved for
// cmd/notebooklm/main.go so the CLI can wire in a level from flag
// parsing before any subcommand logs.
//
// Passing nil is treated as "reset to a fresh default" — useful in tests
// when restoring after a test-scoped SetDefault(custom). If you need to
// restore a previous logger exactly, capture it before calling
// SetDefault and pass it back in.
func SetDefault(l *slog.Logger) {
	if l == nil {
		defaultLog = New(nil)
		slog.SetDefault(defaultLog)
		return
	}
	defaultLog = l
	slog.SetDefault(l)
}
