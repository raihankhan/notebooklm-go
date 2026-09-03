// Package notebooklm — options.go.
//
// Functional options for Client.New. The Python original takes
// 21 keyword arguments through __init__ (timeout, storage_path,
// keepalive, rate_limit_max_retries, …); the Go port converts
// that surface to the functional-options idiom so the call site
// reads as a declarative spec without breaking the existing
// behavior of the positional constructor.
//
// Per docs/AGENTS.md rule: every Option is a public,
// export-tested seam. Each With* constructor returns an
// OptionFunc that mutates the internal config struct held by
// Client.New before the wiring step. Adding a new Option is
// always a backwards-compatible change — callers who do not pass
// it keep the documented default.
//
// Port of notebooklm.client.py::NotebookLMClient.__init__ —
// every default below matches the Python source verbatim.
package notebooklm

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/runtime"
)

// Option is the functional-options type for Client.New.
//
// Implementations are produced by the With* constructors in this
// file. An Option must be safe to apply exactly once and must
// not retain references to mutable caller state past the New
// call — see each With* constructor for the contract.
type Option interface {
	// apply mutates cfg in place. The method is unexported so
	// external packages cannot synthesize their own Options
	// outside the With* constructors; the public surface is
	// the With* set only.
	apply(*clientConfig)
}

// OptionFunc adapts a plain function to the Option interface.
// It is the building block the With* constructors use; external
// callers should not construct an OptionFunc directly.
type OptionFunc func(*clientConfig)

// apply implements Option.
func (f OptionFunc) apply(cfg *clientConfig) {
	if f == nil {
		return
	}
	f(cfg)
}

// clientConfig is the bag of values the With* constructors
// mutate before Client.New wires the transport. It is private
// so the public API surface stays the Option set; it lives in
// this file (rather than client.go) so every Option constructor
// can read and write it without forcing client.go to grow.
type clientConfig struct {
	// storagePath is the path to storage_state.json. An empty
	// path means "do not load; the caller already wired
	// credentials through another mechanism (Phase 4 will add
	// this; today it is a Phase 5 placeholder)."
	storagePath string

	// backend is the requested namespace backend. Empty means
	// "no preference; honor NOTEBOOKLM_BACKEND / fall back to
	// Web". BackendWeb / BackendAndroid pin a specific backend.
	backend BackendName

	// logger is the structured logger the Client will use for
	// lifecycle events (open, close, refresh). nil falls back
	// to an io.Discard-bound slog.Logger so the Client is
	// quiet by default — the CLI / MCP / REST adapters inject
	// their own logger in Phase 5+.
	logger *slog.Logger

	// metrics is the runtime.Metrics the Client will own.
	// nil means "create a fresh one internally". A non-nil
	// value lets a caller share a Metrics handle across
	// multiple Clients, which is useful for aggregating
	// production telemetry from a fleet of worker goroutines.
	metrics *runtime.Metrics

	// epoch is the optional starting epoch the Kernel will
	// use. Zero means "use the default (1)". A non-zero value
	// is intended for tests that want to assert epoch-fencing
	// arithmetic without spinning a real Client.
	epoch uint64

	// maxInFlight is the supervisor semaphore cap. Zero means
	// "use the Phase 5 default (16)". A negative value is
	// rejected at New time.
	maxInFlight int

	// httpClient is the *http.Client the transport kernel will
	// mount. Nil means "build the documented default
	// (&http.Client{Timeout: 30s})". The seam exists so tests
	// can inject a cassette-backed http.Client whose Transport
	// is a go-vcr recorder.
	httpClient *http.Client
}

// Default values mirrored from the Python source. See
// notebooklm.client.py::NotebookLMClient.__init__ defaults.
const (
	defaultMaxInFlight = 16
	defaultEpoch       = uint64(1)
)

// newConfig returns a clientConfig seeded with the documented
// defaults. The function is the single source of truth for
// "what does the Python default look like in Go?"; every Option
// constructor reads from here so a future default change is a
// one-line edit.
func newConfig() clientConfig {
	return clientConfig{
		storagePath: "",
		backend:     "",
		logger:      nil,
		metrics:     nil,
		epoch:       defaultEpoch,
		maxInFlight: defaultMaxInFlight,
	}
}

// resolveLogger returns a non-nil *slog.Logger. When the caller
// passed nil through WithLogger, this function returns a logger
// bound to io.Discard so the Client is silent by default — the
// Python original uses logging.getLogger(__name__) at module
// scope, which honors the root logger's level; the Go port
// mirrors that by being silent unless the caller opts in.
func resolveLogger(in *slog.Logger) *slog.Logger {
	if in != nil {
		return in
	}
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
}

// WithStoragePath pins the storage_state.json path the Client
// will read on construction. An empty path is rejected at
// apply-time so the caller learns about the typo immediately
// rather than at FromStorage time.
//
// Port of notebooklm.client.py::NotebookLMClient.__init__'s
// `storage_path` parameter, which is also the argument the
// Python `from_storage` classmethod forwards.
func WithStoragePath(path string) Option {
	return OptionFunc(func(cfg *clientConfig) {
		if strings.TrimSpace(path) == "" {
			// Keep the documented default rather than
			// overriding with an explicitly-bad path.
			return
		}
		cfg.storagePath = path
	})
}

// WithBackend pins the namespace backend. The Go port keeps the
// values aligned with internal/web/wire's three-host allowlist:
//
//   - BackendWeb    -> notebooklm.google.com / notebook.google.com
//   - BackendAndroid -> the Android mobile RPC surface
//
// An empty string is the documented "no preference" value and
// triggers the env-var + default-fallback resolution in
// Client.New. Any other value is preserved verbatim so a future
// backend (e.g. a preview namespace) can be added without a
// package-level edit; the allowlist rejects unknown values at
// first RPC.
//
// Port of notebooklm.client.py::NotebookLMClient.__init__'s
// `backend: Literal["web", "android"]` parameter.
func WithBackend(b BackendName) Option {
	return OptionFunc(func(cfg *clientConfig) {
		cfg.backend = b
	})
}

// WithLogger installs a structured logger the Client will use
// for lifecycle events. Passing nil is equivalent to omitting
// the option: the Client is silent by default (mirrors the
// Python original, which inherits the root logger).
//
// The logger MUST NOT be reused across goroutines that share
// other state in non-thread-safe ways. *slog.Logger is safe for
// concurrent use by design; the contract here is that the
// caller does not add hooks that break that invariant.
func WithLogger(l *slog.Logger) Option {
	return OptionFunc(func(cfg *clientConfig) {
		cfg.logger = l
	})
}

// WithMetrics injects a runtime.Metrics handle. A non-nil value
// is shared between the Client and any other owners — every
// Inc* / Add* / Snapshot operation is safe for concurrent use.
// Passing nil is equivalent to omitting the option: the Client
// constructs a fresh Metrics internally.
//
// This is the seam that lets a long-running service aggregate
// RPC counters across many Client instances without a fan-in
// listener. See internal/runtime/metrics.go::Metrics for the
// thread-safety contract.
func WithMetrics(m *runtime.Metrics) Option {
	return OptionFunc(func(cfg *clientConfig) {
		cfg.metrics = m
	})
}

// WithEpoch pins the starting epoch the Kernel uses. A non-zero
// value is intended for tests that want to assert epoch-fencing
// arithmetic; production code should omit this option (the
// default is 1, matching the Python original).
//
// Passing zero is equivalent to omitting the option. A negative
// value is rejected at apply-time.
func WithEpoch(epoch uint64) Option {
	return OptionFunc(func(cfg *clientConfig) {
		if epoch == 0 {
			return
		}
		cfg.epoch = epoch
	})
}

// withMaxInFlight is an internal Option used by tests to
// shrink the supervisor semaphore without touching the public
// surface. Production callers should not pass this; the
// Python original exposes the equivalent as the
// `max_concurrent_rpcs` constructor parameter, which will land
// as a public WithMaxConcurrentRPCs option in Phase 5+ once
// the auth-tier defaults are stable.
func withMaxInFlight(n int) Option {
	return OptionFunc(func(cfg *clientConfig) {
		cfg.maxInFlight = n
	})
}

// WithHTTPClient installs a custom *http.Client the transport
// kernel should mount. When the option is omitted, New builds a
// default *http.Client with a 30-second timeout (the Python
// original's _transport/transport.py default).
//
// The seam exists so tests can inject a cassette-backed
// *http.Client whose Transport is a go-vcr recorder; production
// callers should omit this option. A nil argument is treated as
// "use the default" so a caller that passes a nil OptionFunc
// output never silently leaves the Client with no http.Client.
//
// Because the kernel owns the http.Client, the kernel's epoch
// fencing, cookie jar mounting, and 64 MiB response-size cap all
// apply unchanged to a Client injected here; the option only
// replaces the http.Client the kernel wraps.
func WithHTTPClient(httpClient *http.Client) Option {
	return OptionFunc(func(cfg *clientConfig) {
		if httpClient == nil {
			return
		}
		cfg.httpClient = httpClient
	})
}
