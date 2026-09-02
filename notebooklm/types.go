// Package notebooklm is the public Go SDK root for NotebookLM.
//
// This file holds the cross-namespace type aliases the SDK
// surfaces today; namespace-specific types (Notebook, Source,
// Artifact, …) land in later phase tickets alongside the
// namespace APIs that consume them.
//
// Port of notebooklm.client.py::NotebookLMClient attribute
// surface. The Python original uses `Literal["web", "android"]`
// for the backend; the Go port mirrors the literal with a typed
// string so the boundarycheck rule against a magic-string API
// stays a compile-time error.
package notebooklm

import (
	"github.com/raihankhan/notebooklm-go/internal/runtime"
)

// BackendName names a namespace backend. The Python original
// supports "web" and "android"; the Go port carries the same
// two values and rejects any other string at compile time when
// callers use the Backend constant below.
//
// See notebooklm.client.py::NotebookLMClient.__init__'s
// `backend: Literal["web", "android"]` parameter.
type BackendName string

// Known backend names. These are the values
// internal/web/wire/urls.go validates against the three-host
// allowlist — every Backend value the SDK surfaces must resolve
// to one of them.
const (
	// BackendWeb is the canonical Web backend. The Python
	// original preserves the established implementation.
	// See notebooklm.client.py::NotebookLMClient's default
	// `backend=None` resolution which falls through to "web".
	BackendWeb BackendName = "web"

	// BackendAndroid installs the Android adapter for every
	// public namespace. A few operations retain documented
	// Web compatibility collaborators where the recovered
	// mobile route has no usable admitted contract.
	// See notebooklm.client.py::NotebookLMClient.__init__.
	BackendAndroid BackendName = "android"
)

// MetricsSnapshot is the public SDK view of the cumulative
// runtime counters. The fields are read-only and copied by
// value; the underlying counters live in internal/runtime/metrics
// and are loaded atomically on the read path.
//
// See notebooklm.client.py::ClientMetricsSnapshot (the Python
// `types` module exports the same field set).
type MetricsSnapshot struct {
	// RPCStarted is the count of logical RPCs admitted through
	// the supervisor. Bumped before the semaphore acquires a
	// slot, so a queued-but-not-running call is already
	// counted.
	RPCStarted int64

	// RPCSucceeded is the count of logical RPCs that returned
	// a successful (decoded, typed) result.
	RPCSucceeded int64

	// RPCFailed is the count of logical RPCs that returned a
	// typed error (transport failure, decode failure, or
	// auth failure after the budget was spent).
	RPCFailed int64

	// AuthRetries is the count of mid-RPC refresh-and-retry
	// attempts that fired. A refresh that lands a 200 wins;
	// a refresh that loses the budget surfaces as an RPCFailed
	// without bumping this counter again.
	AuthRetries int64

	// DecodeErrors is the count of wire frames whose payload
	// could not be decoded (the wire/decode layer's typed
	// decode failures).
	DecodeErrors int64

	// QueueWaitNanos is the cumulative queue-wait time across
	// every call, in nanoseconds. The Python original exposes
	// float seconds; the Go SDK keeps nanoseconds as int64 so
	// the snapshot stays serialization-stable across 32-bit
	// and 64-bit platforms.
	QueueWaitNanos int64

	// ByteCountMismatches is the cumulative count of response
	// frames whose declared byte length did not match the body
	// the server actually shipped. This is the count that
	// guards against a fragmented-encoding regression in the
	// chunked parser.
	ByteCountMismatches int64
}

// fromRuntimeSnapshot maps the internal/runtime Snapshot into the
// public SDK type. Kept private so callers cannot accidentally
// depend on the internal field naming.
func fromRuntimeSnapshot(s runtime.Snapshot) MetricsSnapshot {
	return MetricsSnapshot{
		RPCStarted:          s.RpcCallsStarted,
		RPCSucceeded:        s.RpcCallsSucceeded,
		RPCFailed:           s.RpcCallsFailed,
		AuthRetries:         s.RpcAuthRetries,
		DecodeErrors:        s.RpcDecodeErrors,
		QueueWaitNanos:      s.QueueWaitSeconds,
		ByteCountMismatches: s.ByteCountMismatchTotal,
	}
}
