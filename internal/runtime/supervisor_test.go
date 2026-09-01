package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSupervisor_NewRejectsNegativeCapacity covers the constructor's
// input validation.
func TestSupervisor_NewRejectsNegativeCapacity(t *testing.T) {
	t.Parallel()
	if _, err := NewSupervisor(NewMetrics(), -1); err == nil {
		t.Fatalf("NewSupervisor accepted negative capacity")
	}
}

// TestSupervisor_AdmitReturnsLease verifies the basic happy path:
// Admit returns a non-nil lease and the in-flight counter rises by
// one.
func TestSupervisor_AdmitReturnsLease(t *testing.T) {
	t.Parallel()
	s, err := NewSupervisor(NewMetrics(), 0)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	lease, err := s.Admit(context.Background(), "test")
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if lease == nil {
		t.Fatalf("Admit returned nil lease")
	}
	if got := s.InFlight(); got != 1 {
		t.Fatalf("InFlight = %d, want 1", got)
	}
	lease.Release()
	if got := s.InFlight(); got != 0 {
		t.Fatalf("InFlight after Release = %d, want 0", got)
	}
}

// TestSupervisor_ReleaseExactlyOnce is the AC3 property: even under
// concurrent Release calls, the in-flight counter decrements
// exactly once.
func TestSupervisor_ReleaseExactlyOnce(t *testing.T) {
	t.Parallel()
	s, err := NewSupervisor(NewMetrics(), 0)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	lease, err := s.Admit(context.Background(), "race")
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease.Release()
		}()
	}
	wg.Wait()
	if got := s.InFlight(); got != 0 {
		t.Fatalf("InFlight after %d concurrent Releases = %d, want 0", N, got)
	}
}

// TestSupervisor_AdmitReleaseRace is the AC5 harness: hundreds of
// goroutines admit, hold for a moment, then release. The final
// in-flight count must be zero.
func TestSupervisor_AdmitReleaseRace(t *testing.T) {
	t.Parallel()
	s, err := NewSupervisor(NewMetrics(), 0)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	const G = 100
	var wg sync.WaitGroup
	var admitted atomic.Int32
	for i := 0; i < G; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := s.Admit(context.Background(), "race")
			if err != nil {
				t.Errorf("Admit: %v", err)
				return
			}
			admitted.Add(1)
			// Yield a few times so other goroutines can also
			// Admit before any of us Release.
			for j := 0; j < 10; j++ {
				time.Sleep(time.Microsecond)
			}
			lease.Release()
		}()
	}
	wg.Wait()
	if admitted.Load() != G {
		t.Fatalf("admitted = %d, want %d", admitted.Load(), G)
	}
	if got := s.InFlight(); got != 0 {
		t.Fatalf("InFlight = %d after race, want 0", got)
	}
}

// TestSupervisor_BoundedBlocksThenReleases covers the semaphore
// step: with capacity N, M>N admits cause the surplus to wait. A
// release unblocks exactly one waiter.
func TestSupervisor_BoundedBlocksThenReleases(t *testing.T) {
	t.Parallel()
	const cap = 2
	s, err := NewSupervisor(NewMetrics(), cap)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	leases := make([]Lease, 0, 4)
	for i := 0; i < cap; i++ {
		l, err := s.Admit(context.Background(), "fill")
		if err != nil {
			t.Fatalf("Admit %d: %v", i, err)
		}
		leases = append(leases, l)
	}

	// The next Admit must block. Use a short timeout context.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	l, err := s.Admit(ctx, "block")
	if !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			l.Release()
			t.Fatalf("Admit blocked call returned lease, want context error")
		}
		t.Fatalf("Admit blocked err = %v, want DeadlineExceeded", err)
	}

	// Release one; the next Admit must succeed.
	leases[0].Release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	l2, err := s.Admit(ctx2, "after-release")
	if err != nil {
		t.Fatalf("Admit after release: %v", err)
	}
	l2.Release()
	for _, lease := range leases[1:] {
		lease.Release()
	}
}

// TestSupervisor_DrainBlocksUntilEnded asserts the drain phase
// blocks new admits until EndDrain.
func TestSupervisor_DrainBlocksUntilEnded(t *testing.T) {
	t.Parallel()
	s, err := NewSupervisor(NewMetrics(), 0)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	s.BeginDrain()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Admit(ctx, "during-drain"); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			t.Fatalf("Admit during drain returned lease, want context error")
		}
		t.Fatalf("Admit during drain err = %v, want DeadlineExceeded", err)
	}

	// EndDrain and a fresh admit goes through.
	s.EndDrain()
	lease, err := s.Admit(context.Background(), "after-drain")
	if err != nil {
		t.Fatalf("Admit after EndDrain: %v", err)
	}
	lease.Release()
}

// TestSupervisor_DrainMultipleWaitersAllWoken: many goroutines
// waiting on drain are all released when EndDrain fires.
func TestSupervisor_DrainMultipleWaitersAllWoken(t *testing.T) {
	t.Parallel()
	s, err := NewSupervisor(NewMetrics(), 0)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	s.BeginDrain()

	const G = 20
	var wg sync.WaitGroup
	results := make([]error, G)
	for i := 0; i < G; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			lease, err := s.Admit(ctx, "wait")
			if err != nil {
				results[idx] = err
				return
			}
			lease.Release()
		}(i)
	}

	// Give every goroutine a chance to enter waitForDrain.
	time.Sleep(20 * time.Millisecond)
	s.EndDrain()

	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Errorf("waiter %d err = %v", i, err)
		}
	}
}

// TestSupervisor_CanceledContextReturnsErr ensures Admit honors a
// pre-canceled context without bumping counters or returning a
// lease.
func TestSupervisor_CanceledContextReturnsErr(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	s, err := NewSupervisor(m, 0)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lease, err := s.Admit(ctx, "canceled")
	if !errors.Is(err, context.Canceled) {
		if err == nil {
			lease.Release()
			t.Fatalf("Admit with canceled context returned lease")
		}
		t.Fatalf("Admit err = %v, want Canceled", err)
	}
	snap := m.Snapshot()
	if snap.RpcCallsStarted != 0 || snap.RpcCallsFailed != 0 {
		t.Fatalf("metrics after canceled admit = %+v, want zeros", snap)
	}
}

// TestSupervisor_MetricsStartedIncrements asserts the metrics bump
// happens during Admit and the queue-wait counter advances.
func TestSupervisor_MetricsStartedIncrements(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	s, err := NewSupervisor(m, 0)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	lease, err := s.Admit(context.Background(), "metrics")
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	lease.Release()
	snap := m.Snapshot()
	if snap.RpcCallsStarted != 1 {
		t.Fatalf("RpcCallsStarted = %d, want 1", snap.RpcCallsStarted)
	}
	if snap.QueueWaitSeconds <= 0 {
		t.Fatalf("QueueWaitSeconds = %d, want > 0", snap.QueueWaitSeconds)
	}
}

// TestSupervisor_NilSafety: a nil supervisor's exported methods
// must not panic.
func TestSupervisor_NilSafety(t *testing.T) {
	t.Parallel()
	var s *Supervisor
	if s.InFlight() != 0 {
		t.Fatalf("nil.InFlight != 0")
	}
	if s.MetricsHandle() != nil {
		t.Fatalf("nil.MetricsHandle != nil")
	}
	// Methods that return errors on nil should not panic.
	if _, err := s.Admit(context.Background(), "x"); err == nil {
		t.Fatalf("nil.Admit returned no error")
	}
	s.BeginDrain() // no-op, must not panic
	s.EndDrain()   // no-op, must not panic
}

// TestSupervisor_LeaseDoubleRelease verifies Release is idempotent:
// calling it twice does not double-decrement.
func TestSupervisor_LeaseDoubleRelease(t *testing.T) {
	t.Parallel()
	s, err := NewSupervisor(NewMetrics(), 0)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	lease, err := s.Admit(context.Background(), "x")
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	lease.Release()
	lease.Release() // idempotent
	if got := s.InFlight(); got != 0 {
		t.Fatalf("InFlight after double release = %d, want 0", got)
	}
}

// TestSupervisor_BoundedUnderRace is the AC5 harness at scale:
// hundreds of goroutines admit against a tightly-bounded supervisor
// and the in-flight counter never exceeds the capacity and ends at
// zero.
func TestSupervisor_BoundedUnderRace(t *testing.T) {
	t.Parallel()
	const cap = 4
	s, err := NewSupervisor(NewMetrics(), cap)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	const G = 200
	var wg sync.WaitGroup
	var overshoot atomic.Int32
	var peak atomic.Int64
	for i := 0; i < G; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := s.Admit(context.Background(), "bounded")
			if err != nil {
				overshoot.Add(1)
				return
			}
			cur := s.InFlight()
			for {
				p := peak.Load()
				if cur <= p {
					break
				}
				if peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(time.Microsecond)
			lease.Release()
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > cap {
		t.Fatalf("peak in-flight = %d, want <= capacity %d", got, cap)
	}
	if overshoot.Load() != 0 {
		t.Fatalf("overshoot errors = %d, want 0", overshoot.Load())
	}
	if got := s.InFlight(); got != 0 {
		t.Fatalf("final InFlight = %d, want 0", got)
	}
}
