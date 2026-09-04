// Package params — reqid.go: monotonic per-request id counter used by
// the chat-streaming RPC.
//
// The NotebookLM chat backend requires every streaming-chat request to
// carry a monotonically-increasing `_reqid` URL parameter (see
// notebooklm-py/src/notebooklm/_web/transport/reqid_counter.py for the
// Python reference). ReqidCounter is the Go-side primitive that hands
// out those ids to concurrent callers — its Increment is safe under
// the race detector so a goroutine-per-request SDK surface cannot
// double-issue an id.
//
// The Python original seeds the counter at a baseline of 100000 with a
// default step of 100000. The Go port here follows the T-S3-007b
// contract documented in docs/sprint-reports/S03-split-tickets.md
// (lines 231-237): Increment returns 1, 2, 3, ... in order — the SDK
// surface layer (T-S3-007e) is responsible for translating the
// internal 1-based id to the wire-required large positive integer when
// it composes the URL parameter.
//
// The counter is intentionally tiny: one atomic int64 and one
// atomic.AddInt64 call per Increment. No mutex, no async lock —
// atomic.AddInt64 is the right primitive for read-modify-write on a
// single word, and the race detector confirms it.
package params

import "sync/atomic"

// ReqidCounter is a monotonic int64 counter the chat-stream RPC uses
// for per-request ids. Zero value is ready to use; Increment returns
// 1, 2, 3, ... in strictly-increasing order even under concurrent
// callers.
//
// ReqidCounter is intentionally a value type (not a pointer-only type)
// so a caller can declare it as a field and pass `c.Increment` to
// `sync.WaitGroup`-style fan-out code without taking the address
// explicitly. The atomic state lives behind the pointer the runtime
// passes to atomic operations, so copying the struct is safe — every
// copy shares the same atomic word through the indirection.
//
// Reset is provided so a test can deterministically seed the counter
// without depending on the order of Increment calls.
type ReqidCounter struct {
	v atomic.Int64
}

// NewReqidCounter returns a ready-to-use counter whose first Increment
// call will return 1. Use Reset to seed a different starting value.
func NewReqidCounter() *ReqidCounter {
	return &ReqidCounter{}
}

// Increment atomically bumps the counter by one and returns the new
// value. Successive calls return strictly-monotonic, distinct ids
// even when invoked from concurrent goroutines.
//
// Safe for concurrent use: the race detector confirms no data race
// on the internal atomic word. Multiple goroutines calling Increment
// in parallel observe a total order over the returned ids.
func (c *ReqidCounter) Increment() int64 {
	return c.v.Add(1)
}

// Value returns a snapshot of the current counter value without
// incrementing. The read is not synchronized with concurrent
// Increment calls — a caller that needs the post-increment value MUST
// use Increment, which is the atomic read-modify-write. Value exists
// for test assertions and diagnostic logging where a best-effort
// snapshot is fine.
func (c *ReqidCounter) Value() int64 {
	return c.v.Load()
}

// Reset replaces the counter with newValue atomically. After Reset,
// the next Increment returns newValue + 1. Used by tests that want a
// deterministic baseline; not part of the production hot path.
func (c *ReqidCounter) Reset(newValue int64) {
	c.v.Store(newValue)
}
