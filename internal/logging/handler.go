package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// reqIDKey is the unexported context key under which request ids are
// stored. Using an empty struct type makes lookups collision-proof even if
// another package happens to use the string "request_id" as a key.
type reqIDKey struct{}

// WithRequestID returns a derived context carrying the given request id.
// Callers usually do `logging.WithRequestID(ctx, rid)` at the top of an
// HTTP handler, Cobra command, or batchexecute RPC so every downstream
// log line picks up the same correlation id automatically.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, reqIDKey{}, id)
}

// RequestID extracts the request id from ctx, or "" when none is set.
// Never returns a sentinel — empty string is the documented "no id" signal.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(reqIDKey{}).(string); ok {
		return v
	}
	return ""
}

// levelFromEnv parses NOTEBOOKLM_LOG_LEVEL into a slog.Level. The supported
// tokens are case-insensitive: "debug", "info", "warn"/"warning", "error".
// Unrecognized or empty values default to slog.LevelInfo so a typo never
// causes a silent switch to a higher level (we prefer noise over silence).
//
// The -v counter, when supplied via OptionLevel, is applied AFTER the env
// hint and can only RAISE verbosity (counter > 0 maps to LevelDebug).
func levelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NOTEBOOKLM_LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info", "":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}

// stderrOnly is the writer passed to slog.NewTextHandler in New() when
// the caller passes nil. It guarantees the handler never writes to
// os.Stdout — see TestStderrOnly in logger_test.go.
//
// We deliberately do NOT wrap os.Stderr in a sync.Mutex wrapper; the
// underlying *os.File writes are themselves safe for concurrent use, and
// adding a layer would interfere with line-buffered tooling like `tee`.
type stderrOnly struct{}

// Write satisfies io.Writer; always writes to os.Stderr.
func (stderrOnly) Write(p []byte) (int, error) { return os.Stderr.Write(p) }

var _ io.Writer = stderrOnly{}

// redactHandler wraps a slog.Handler and applies two transformations:
//
//  1. If the record was emitted with a context that carries a request id
//     (see WithRequestID), the handler attaches it as a "request_id"
//     attribute. This is the only place request id injection happens —
//     callers don't pass it explicitly.
//
//  2. Every attribute — built-in or user-supplied — is run through
//     hookRedactor via ReplaceAttr so secrets embedded in attribute values
//     are masked before they hit the wire.
//
// The handler is safe for concurrent use (it delegates to slog.Handler
// internally, which is documented as safe).
type redactHandler struct {
	inner  slog.Handler
	writer io.Writer // captured for level/verbosity Option rebuilds
}

// newHandler builds the default handler: a text handler writing to w at the
// given level. The redactor is whatever WithRedactor / defaultRedactor
// currently points at; it does NOT take a redactor argument because the
// package-private hook is the seam T-P0-3 will swap.
func newHandler(w io.Writer, level slog.Level) *redactHandler {
	if w == nil {
		w = stderrOnly{}
	}
	return &redactHandler{
		writer: w,
		inner: slog.NewTextHandler(w, &slog.HandlerOptions{
			Level:       level,
			ReplaceAttr: replaceAttr,
		}),
	}
}

// replaceAttr is the slog.ReplaceAttr hook. It runs once per attribute
// (including "time", "level", "msg") and gives us a chance to mask
// secrets. We do the masking here rather than in Handle so attribute
// values are still typed — slog passes us the concrete value so we can
// choose to redact or not per-type.
//
// request_id injection happens in Handle (not here) so user-supplied
// request_id attrs are not masked/redacted-twice.
func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	// Skip request_id — Handle() will add it AFTER ReplaceAttr.
	if a.Key == "request_id" {
		return a
	}

	red := currentRedactor()
	switch a.Value.Kind() {
	case slog.KindString:
		s := a.Value.String()
		masked := string(red.Apply([]byte(s)))
		if masked != s {
			return slog.String(a.Key, masked)
		}
	case slog.KindAny:
		// slog.KindAny covers structs, maps, errors, fmt.Stringers.
		// slog.Value.String() already does the right per-Kind formatting,
		// so we route its output through the redactor. This is the path
		// the on-wire test exercises: a struct that holds a redacted
		// field whose String() exposes the secret.
		raw := []byte(a.Value.String())
		masked := string(red.Apply(raw))
		if masked != string(raw) {
			return slog.String(a.Key, masked)
		}
	}
	return a
}

// Enabled delegates to the inner handler.
func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle injects request_id (if present on ctx) BEFORE delegating to the
// inner handler. We mutate the record rather than building a new one to
// stay within slog's allocation budget on the hot path.
func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	if rid := RequestID(ctx); rid != "" {
		r.AddAttrs(slog.String("request_id", rid))
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs / WithGroup are pass-throughs — context-based request_id
// injection does not depend on them. We propagate `writer` so chained
// rebuilds (OptionLevel / OptionVerbosity) keep the same destination.
func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &redactHandler{inner: h.inner.WithAttrs(attrs), writer: h.writer}
}

// WithGroup is a pass-through.
func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name), writer: h.writer}
}

var _ slog.Handler = (*redactHandler)(nil)
