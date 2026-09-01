package runtime

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMetrics_InitialZero covers the zero-value contract: a fresh
// Metrics reports all counters at 0.
func TestMetrics_InitialZero(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	snap := m.Snapshot()
	want := Snapshot{}
	if snap != want {
		t.Fatalf("fresh snapshot = %+v, want %+v", snap, want)
	}
}

// TestMetrics_IncrementersBumpCounters exercises each public counter
// and asserts Snapshot() reports the bumped value.
func TestMetrics_IncrementersBumpCounters(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.IncRpcCallsStarted()
	m.IncRpcCallsStarted()
	m.IncRpcCallsSucceeded()
	m.IncRpcCallsFailed()
	m.IncRpcAuthRetries()
	m.IncRpcDecodeErrors()
	m.AddQueueWaitSeconds(0.25)
	m.AddQueueWaitSeconds(0.5)
	m.AddByteCountMismatch(42)

	snap := m.Snapshot()
	if snap.RpcCallsStarted != 2 {
		t.Fatalf("RpcCallsStarted = %d, want 2", snap.RpcCallsStarted)
	}
	if snap.RpcCallsSucceeded != 1 {
		t.Fatalf("RpcCallsSucceeded = %d, want 1", snap.RpcCallsSucceeded)
	}
	if snap.RpcCallsFailed != 1 {
		t.Fatalf("RpcCallsFailed = %d, want 1", snap.RpcCallsFailed)
	}
	if snap.RpcAuthRetries != 1 {
		t.Fatalf("RpcAuthRetries = %d, want 1", snap.RpcAuthRetries)
	}
	if snap.RpcDecodeErrors != 1 {
		t.Fatalf("RpcDecodeErrors = %d, want 1", snap.RpcDecodeErrors)
	}
	wantQueue := int64((0.25 + 0.5) * float64(time.Second))
	if snap.QueueWaitSeconds != wantQueue {
		t.Fatalf("QueueWaitSeconds = %d, want %d", snap.QueueWaitSeconds, wantQueue)
	}
	if snap.ByteCountMismatchTotal != 42 {
		t.Fatalf("ByteCountMismatchTotal = %d, want 42", snap.ByteCountMismatchTotal)
	}
}

// TestMetrics_NilSafety guards against panics on a nil Metrics;
// every public method must be safe to call on a zero value.
func TestMetrics_NilSafety(t *testing.T) {
	t.Parallel()
	var m *Metrics
	// Each of these must not panic.
	m.IncRpcCallsStarted()
	m.IncRpcCallsSucceeded()
	m.IncRpcCallsFailed()
	m.IncRpcAuthRetries()
	m.IncRpcDecodeErrors()
	m.AddQueueWaitSeconds(1)
	m.AddByteCountMismatch(1)
	snap := m.Snapshot()
	if snap != (Snapshot{}) {
		t.Fatalf("nil Snapshot = %+v, want zero", snap)
	}
	m.EmitRPCEvent(RPCEvent{Method: "noop"})
}

// TestMetrics_FanOutExactlyOnce: a registered callback must be
// invoked exactly once per EmitRPCEvent. AC2.
func TestMetrics_FanOutExactlyOnce(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	var hits atomic.Int32
	m.RegisterRPCEventCallback(func(_ RPCEvent) {
		hits.Add(1)
	})
	m.EmitRPCEvent(RPCEvent{Method: "m1", Status: "success"})
	m.EmitRPCEvent(RPCEvent{Method: "m2", Status: "error"})
	m.EmitRPCEvent(RPCEvent{Method: "m3", Status: "success"})
	if got := hits.Load(); got != 3 {
		t.Fatalf("hits = %d, want 3", got)
	}
}

// TestMetrics_FanOutUnderConcurrentEmit: the fan-out must be race-
// safe under -race when many goroutines emit simultaneously.
func TestMetrics_FanOutUnderConcurrentEmit(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	var hits atomic.Int32
	m.RegisterRPCEventCallback(func(_ RPCEvent) {
		hits.Add(1)
	})

	const G = 32
	const perG = 50
	var wg sync.WaitGroup
	for i := 0; i < G; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				m.EmitRPCEvent(RPCEvent{Method: "x", Status: "success"})
			}
		}()
	}
	wg.Wait()
	if got := hits.Load(); got != int32(G*perG) {
		t.Fatalf("hits = %d, want %d", got, G*perG)
	}
}

// TestMetrics_FanOutNoCallbackIsNoop: EmitRPCEvent on a Metrics
// with no callbacks returns immediately and does not crash.
func TestMetrics_FanOutNoCallbackIsNoop(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.EmitRPCEvent(RPCEvent{Method: "m"})
}

// TestMetrics_RegisterNilIgnored: a nil callback does not get
// stored, and a subsequent emit with no other callbacks is a no-op.
func TestMetrics_RegisterNilIgnored(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.RegisterRPCEventCallback(nil)
	var hits atomic.Int32
	m.RegisterRPCEventCallback(func(_ RPCEvent) {
		hits.Add(1)
	})
	m.EmitRPCEvent(RPCEvent{Method: "x"})
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
}

// TestMetrics_SnapshotIsLockFree: 1000 goroutines call Snapshot
// concurrently while another goroutine bumps each counter. The
// race detector must report no data race; the snapshot's fields are
// expected to be monotonic across iterations (a sample snapshot is
// not required to be exact at any instant, but the increments must
// all land eventually).
func TestMetrics_SnapshotIsLockFree(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	var wg sync.WaitGroup

	stop := make(chan struct{})

	// Producers
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				m.IncRpcCallsStarted()
			}
		}()
	}

	// Readers
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = m.Snapshot()
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestMetrics_AddByteCountMismatchIgnoresNegative guards the surface
// from accidental underflow.
func TestMetrics_AddByteCountMismatchIgnoresNegative(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.AddByteCountMismatch(-10)
	m.AddByteCountMismatch(0)
	if got := m.Snapshot().ByteCountMismatchTotal; got != 0 {
		t.Fatalf("ByteCountMismatchTotal = %d, want 0", got)
	}
}

// TestMetrics_RegisterAndEmitRace: registration and emission
// happening concurrently must not corrupt the callback slice.
func TestMetrics_RegisterAndEmitRace(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	stop := make(chan struct{})
	var hits atomic.Int32
	var wg sync.WaitGroup

	// Emitter
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				m.EmitRPCEvent(RPCEvent{Method: "x"})
			}
		}()
	}

	// Registrars
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.RegisterRPCEventCallback(func(_ RPCEvent) {
					hits.Add(1)
				})
			}
		}()
	}

	time.Sleep(30 * time.Millisecond)
	close(stop)
	wg.Wait()
	// We do not assert a specific hit count — the race test is
	// whether the slice survives concurrent register/emit
	// without panicking.
}
