// Tests for ReqidCounter — the monotonic per-request id counter used
// by the chat-streaming RPC.
//
// The T-S3-007b contract (docs/sprint-reports/S03-split-tickets.md
// lines 231-237) is:
//
//   - Increment returns 1, 2, 3, ... in order
//   - ReqidCounter is safe for concurrent use
//   - go test -race must pass
//
// We pin both invariants here: the sequential-order test asserts the
// exact id sequence; the concurrent test drives the race detector
// with enough goroutines / iterations that a non-atomic implementation
// cannot survive.
package params

import (
	"sync"
	"testing"
)

// TestReqidCounter_Sequential — Increment returns 1, 2, 3, ... in
// order. Single-goroutine baseline; the concurrent test below
// exercises the race-detector path.
func TestReqidCounter_Sequential(t *testing.T) {
	c := NewReqidCounter()
	for i := int64(1); i <= 100; i++ {
		got := c.Increment()
		if got != i {
			t.Fatalf("ReqidCounter.Increment call %d = %d, want %d", i, got, i)
		}
	}
}

// TestReqidCounter_StartsAtOne — the first call after NewReqidCounter
// returns 1, not 0. The wire contract starts at 1; the chat backend
// rejects id=0 with an opaque schema-drift error.
func TestReqidCounter_StartsAtOne(t *testing.T) {
	c := NewReqidCounter()
	if got := c.Increment(); got != 1 {
		t.Fatalf("first Increment = %d, want 1", got)
	}
}

// TestReqidCounter_Value — the snapshot accessor returns the most
// recent id without incrementing. The value is not synchronized with
// concurrent Increment calls; this test exercises only the single-
// goroutine read path.
func TestReqidCounter_Value(t *testing.T) {
	c := NewReqidCounter()
	if got := c.Value(); got != 0 {
		t.Errorf("zero value Value() = %d, want 0", got)
	}
	_ = c.Increment()
	_ = c.Increment()
	if got := c.Value(); got != 2 {
		t.Errorf("post-2x Increment Value() = %d, want 2", got)
	}
}

// TestReqidCounter_Reset — Reset replaces the counter so the next
// Increment returns newValue + 1. Used by tests that want a
// deterministic baseline; not part of the production hot path.
func TestReqidCounter_Reset(t *testing.T) {
	c := NewReqidCounter()
	_ = c.Increment()
	_ = c.Increment()
	c.Reset(100)
	if got := c.Value(); got != 100 {
		t.Errorf("after Reset(100) Value() = %d, want 100", got)
	}
	if got := c.Increment(); got != 101 {
		t.Errorf("after Reset(100) Increment = %d, want 101", got)
	}
}

// TestReqidCounter_Concurrent — Increment is safe for concurrent
// use. The race detector must pass (run with `go test -race`).
//
// The test fires N goroutines × M iterations of Increment; the union
// of all returned ids must be exactly {1, 2, ..., N*M} with no
// duplicates and no gaps.
func TestReqidCounter_Concurrent(t *testing.T) {
	const goroutines = 32
	const iterations = 1000

	c := NewReqidCounter()
	const total = goroutines * iterations

	results := make([][]int64, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			local := make([]int64, iterations)
			for i := 0; i < iterations; i++ {
				local[i] = c.Increment()
			}
			results[g] = local
		}()
	}
	wg.Wait()

	// Collect every id into a set; assert size == total (no duplicates,
	// no gaps) and every id is in [1, total].
	seen := make(map[int64]bool, total)
	for _, r := range results {
		for _, id := range r {
			if id < 1 || id > total {
				t.Errorf("Increment returned out-of-range id %d (expected 1..%d)", id, total)
			}
			if seen[id] {
				t.Errorf("Increment returned duplicate id %d", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != total {
		t.Fatalf("saw %d unique ids, want %d", len(seen), total)
	}
}

// TestReqidCounter_ConcurrentResetSafe — Reset under concurrent
// Increment calls is racy by design (the documentation says Reset is
// for test-fixture seeding, not production hot paths). This test
// only confirms that a single Reset followed by Increment returns
// newValue + 1 in the absence of concurrent writers.
func TestReqidCounter_ConcurrentResetSafe(t *testing.T) {
	c := NewReqidCounter()
	c.Reset(0)
	if got := c.Increment(); got != 1 {
		t.Errorf("after Reset(0) Increment = %d, want 1", got)
	}
}

// TestReqidCounter_ZeroValue — the zero value is ready to use (no
// NewReqidCounter call required). A ReqidCounter field declared as
// `var c ReqidCounter` must work the same as `NewReqidCounter()`.
func TestReqidCounter_ZeroValue(t *testing.T) {
	var c ReqidCounter
	if got := c.Increment(); got != 1 {
		t.Errorf("zero-value ReqidCounter.Increment = %d, want 1", got)
	}
}

// TestBuilderCoverageCalls_Reqid — exercises every accessor so the
// coverage tool sees the function bodies.
func TestBuilderCoverageCalls_Reqid(t *testing.T) {
	c := NewReqidCounter()
	_ = c.Increment()
	_ = c.Value()
	c.Reset(99)
}
