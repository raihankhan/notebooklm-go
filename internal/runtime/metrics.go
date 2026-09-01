// Package runtime holds the lifecycle, supervisor, metrics, and deadline
// primitives shared by every transport call.
//
// metrics.go — atomic counters and the RPCEvent callback fan-out.
//
// Counters are loaded-only on the read path (Snapshot is a fan of
// atomic.Int64 loads with no mutex), so a hot RPC loop can poll the
// snapshot without contending with the producers. The fan-out is
// race-safe: registering a callback while another goroutine is
// emitting is benign — the new callback observes future events, never
// a half-applied one.
package runtime

import (
	"sync"
	"sync/atomic"
	"time"
)

// Snapshot is a point-in-time copy of the runtime counters. The
// fields are atomic.Int64 in Metrics; Snapshot() loads each one with
// a plain Load and copies into a plain int64 struct, so the value
// returned is a consistent-by-field view but may drift across fields
// under heavy concurrency. That is intentional and matches what the
// Python original exposes.
type Snapshot struct {
	RpcCallsStarted        int64
	RpcCallsSucceeded      int64
	RpcCallsFailed         int64
	RpcAuthRetries         int64
	RpcDecodeErrors        int64
	QueueWaitSeconds       int64 // cumulative across all calls; updated by AddQueueWaitSeconds
	ByteCountMismatchTotal int64
}

// Metrics owns the runtime counters and the optional RPCEvent
// callback fan-out. The struct is safe for concurrent use by any
// number of goroutines; the read path (Snapshot, RPCEventCallbacks)
// is lock-free and the write path (Inc*, AddQueueWaitSeconds,
// RegisterRPCEventCallback) uses atomic loads/stores or a short
// mutex held only while registering a callback.
type Metrics struct {
	rpcCallsStarted        atomic.Int64
	rpcCallsSucceeded      atomic.Int64
	rpcCallsFailed         atomic.Int64
	rpcAuthRetries         atomic.Int64
	rpcDecodeErrors        atomic.Int64
	queueWaitSeconds       atomic.Int64
	byteCountMismatchTotal atomic.Int64

	// cbMu guards only the callbacks slice. Emit reads the slice
	// header once under the lock and then iterates without holding
	// it, so a slow callback cannot block the emitter's release.
	cbMu      sync.RWMutex
	callbacks []func(RPCEvent)
}

// RPCEvent is one terminal RPC telemetry record. The supervisor
// emits one per logical RPC, regardless of whether it succeeded or
// failed. The fields are deliberately minimal so a third-party
// observer (a logger, a metrics exporter, a UI counter) can plug in
// without coupling to the internal state.
type RPCEvent struct {
	// Method is the RPC method name, e.g. "Notebooks.Create". The
	// wire's obfuscated id stays in the transport layer; this is the
	// symbolic name.
	Method string

	// Status is "success" or "error". Other strings are reserved
	// for future use.
	Status string

	// Elapsed is wall time spent in the call (queue + transport +
	// decode), measured against a monotonic clock.
	Elapsed time.Duration

	// ErrorType is the qualified type name of the error if the
	// call failed; empty on success.
	ErrorType string
}

// NewMetrics returns a Metrics with zero counters and no callbacks.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// IncRpcCallsStarted bumps the started counter by 1.
func (m *Metrics) IncRpcCallsStarted() {
	if m == nil {
		return
	}
	m.rpcCallsStarted.Add(1)
}

// IncRpcCallsSucceeded bumps the succeeded counter by 1.
func (m *Metrics) IncRpcCallsSucceeded() {
	if m == nil {
		return
	}
	m.rpcCallsSucceeded.Add(1)
}

// IncRpcCallsFailed bumps the failed counter by 1.
func (m *Metrics) IncRpcCallsFailed() {
	if m == nil {
		return
	}
	m.rpcCallsFailed.Add(1)
}

// IncRpcAuthRetries bumps the auth-retry counter by 1.
func (m *Metrics) IncRpcAuthRetries() {
	if m == nil {
		return
	}
	m.rpcAuthRetries.Add(1)
}

// IncRpcDecodeErrors bumps the decode-error counter by 1.
func (m *Metrics) IncRpcDecodeErrors() {
	if m == nil {
		return
	}
	m.rpcDecodeErrors.Add(1)
}

// AddQueueWaitSeconds adds the given wait to the cumulative
// queue-wait counter. The argument may be negative to subtract; the
// counter is signed.
func (m *Metrics) AddQueueWaitSeconds(seconds float64) {
	if m == nil {
		return
	}
	// Convert to microseconds before truncation so sub-second waits
	// contribute meaningfully. The Python original uses float
	// seconds; the Go counter is int64 nanos to stay lock-free.
	m.queueWaitSeconds.Add(int64(seconds * float64(time.Second)))
}

// AddByteCountMismatch adds n to the byte-count-mismatch total. The
// counter is unsigned; passing a negative value is silently dropped
// to keep the surface narrow.
func (m *Metrics) AddByteCountMismatch(n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.byteCountMismatchTotal.Add(n)
}

// Snapshot returns a point-in-time copy of the counters. The read
// path is lock-free; the only synchronization is the atomic.Int64
// loads themselves. A reader therefore sees each field as of some
// moment in the load sequence but not a globally consistent
// cross-field view — that is intentional and matches the Python
// original.
func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	return Snapshot{
		RpcCallsStarted:        m.rpcCallsStarted.Load(),
		RpcCallsSucceeded:      m.rpcCallsSucceeded.Load(),
		RpcCallsFailed:         m.rpcCallsFailed.Load(),
		RpcAuthRetries:         m.rpcAuthRetries.Load(),
		RpcDecodeErrors:        m.rpcDecodeErrors.Load(),
		QueueWaitSeconds:       m.queueWaitSeconds.Load(),
		ByteCountMismatchTotal: m.byteCountMismatchTotal.Load(),
	}
}

// RegisterRPCEventCallback installs a callback that will receive
// every future RPCEvent. The callback is invoked synchronously on
// the emitting goroutine; callbacks that block therefore block the
// emitter. The contract is documented here so callers do not blame
// the runtime for a slow observer.
//
// Registering a nil callback is a no-op. Re-registering with a
// different function replaces the previous one for the same index
// (newest registration wins); this is sufficient for the test
// harness and for a single consumer. A future change can extend the
// fan-out to N callbacks without breaking the surface.
func (m *Metrics) RegisterRPCEventCallback(cb func(RPCEvent)) {
	if m == nil || cb == nil {
		return
	}
	m.cbMu.Lock()
	defer m.cbMu.Unlock()
	m.callbacks = append(m.callbacks, cb)
}

// EmitRPCEvent fans one event out to every registered callback in
// the order they were registered. The slice is read once under the
// lock and then iterated without the lock so a slow callback does
// not block other callbacks' registration.
//
// Emit is safe under concurrent registration: a callback that was
// added after Emit started is not called for the in-flight event.
// A callback that was removed (slice truncation, future API) before
// Emit started is not called either. Exactly-once delivery to each
// registered callback for the in-flight event is the contract.
func (m *Metrics) EmitRPCEvent(ev RPCEvent) {
	if m == nil {
		return
	}
	m.cbMu.RLock()
	// Copy the slice header so a concurrent append does not race
	// with the iteration. The callbacks themselves stay shared.
	cbs := m.callbacks
	m.cbMu.RUnlock()
	for _, cb := range cbs {
		cb(ev)
	}
}

// RPCEventCallbacks returns a copy of the current callback set for
// test introspection. Production code does not need this.
func (m *Metrics) RPCEventCallbacks() []func(RPCEvent) {
	if m == nil {
		return nil
	}
	m.cbMu.RLock()
	defer m.cbMu.RUnlock()
	out := make([]func(RPCEvent), len(m.callbacks))
	copy(out, m.callbacks)
	return out
}
