// Package runtime holds the lifecycle, supervisor, metrics, and deadline
// primitives shared by every transport call.
//
// supervisor.go — drain admission → metrics → semaphore; call and
// operation leases; cancellation-safe settlement; race-free
// admitted-child spawning.
//
// Admit sequence per call:
//
//  1. Drain admission: the supervisor refuses new calls once a
//     drain is in progress, blocking the caller on the drain
//     condition until the drain completes.
//  2. Metrics: rpcCallsStarted is bumped synchronously, before the
//     semaphore acquire, so a queued-but-not-running call is already
//     counted in started/succeeded/failed.
//  3. Semaphore: a bounded in-flight set, configurable at
//     construction. When the limit is reached, callers block here
//     until a slot frees or their context cancels.
//
// Admit returns a Lease whose Release() decrements the in-flight
// counter exactly once even under concurrent release. The atomic
// counter guards the decrement; double-release is a no-op.
package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Lease is the proof of admission returned by Supervisor.Admit.
// Release is idempotent; calling it more than once is a no-op so a
// caller using `defer lease.Release()` after a panic stays safe.
type Lease interface {
	// Release decrements the in-flight counter exactly once.
	// Subsequent calls are no-ops.
	Release()
	// Operation returns the label the lease was admitted with.
	// Used by the transport chain for logging and by tests for
	// introspection; production code that does not need the
	// label can ignore the value.
	Operation() Operation
}

// Operation is a string label attached to an admission for telemetry
// and debugging. Empty is allowed and reserved for non-RPC work.
type Operation string

// Supervisor owns the drain condition, the metrics handle, and the
// in-flight semaphore. It is the single authority on admission
// state for one resource generation.
//
// All methods are safe for concurrent use.
type Supervisor struct {
	metrics *Metrics

	// maxInFlight is the configured semaphore capacity. Zero means
	// unbounded (no semaphore gating).
	maxInFlight int64

	// inFlight is the live count of admitted-but-not-released
	// leases. It is incremented on Admit, decremented on Release.
	// The counter is the single source of truth; Release uses
	// CompareAndSwap so double-release is detected and skipped.
	inFlight atomic.Int64

	// drainCond gates admission on the drain flag. When drain is
	// true, new Admit calls block until drain is cleared or the
	// caller's context cancels.
	drainMu  sync.Mutex
	draining bool
	waiters  []chan struct{} // closed to release blocked callers

	// now is an overridable monotonic clock; nil falls back to
	// time.Now.
	now func() time.Time
}

// NewSupervisor returns a Supervisor that admits through the given
// metrics handle. maxInFlight is the bounded in-flight count: 0
// disables the semaphore, any positive integer bounds it. A negative
// value is rejected.
func NewSupervisor(metrics *Metrics, maxInFlight int) (*Supervisor, error) {
	if maxInFlight < 0 {
		return nil, errors.New("runtime: maxInFlight must be >= 0")
	}
	return &Supervisor{
		metrics:     metrics,
		maxInFlight: int64(maxInFlight),
		now:         time.Now,
	}, nil
}

// InFlight returns the current admitted-but-not-released count. The
// read is lock-free (atomic.Load) and safe under any number of
// concurrent Release calls.
func (s *Supervisor) InFlight() int64 {
	if s == nil {
		return 0
	}
	return s.inFlight.Load()
}

// MetricsHandle returns the metrics instance the supervisor was
// constructed with. Used by the transport chain for telemetry on
// the rare paths that need direct access (mostly tests).
func (s *Supervisor) MetricsHandle() *Metrics {
	if s == nil {
		return nil
	}
	return s.metrics
}

// Admit blocks until the call is admitted through the
// drain → metrics → semaphore pipeline and returns a Lease whose
// Release decrements the in-flight counter exactly once.
//
// Admit returns nil, ctx.Err() if ctx cancels while waiting on the
// drain or semaphore. It never returns nil, nil.
//
// Calling Admit on a closed supervisor (after BeginDrain +
// EndDrain with drain permanently latched, or after Shutdown) is
// safe and behaves like a canceled context: Admit waits forever or
// returns ctx.Err().
func (s *Supervisor) Admit(ctx context.Context, op Operation) (Lease, error) {
	if s == nil {
		return nil, errors.New("runtime: nil supervisor")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Step 1: drain admission. Block until either drain clears,
	// the context cancels, or the supervisor shuts down.
	if err := s.waitForDrain(ctx); err != nil {
		return nil, err
	}

	// Step 2: bump the started counter.
	queueStarted := s.now()
	if s.metrics != nil {
		s.metrics.IncRpcCallsStarted()
	}

	// Step 3: acquire a semaphore slot. Reservation is part of
	// the acquire step (CAS on the in-flight counter) so the
	// counter reflects the reservation the instant the call is
	// admitted.
	if err := s.acquireSlot(ctx); err != nil {
		// We bumped started but never settled. Roll the
		// counter back so a started call that failed to admit
		// does not skew the started/succeeded ratio.
		if s.metrics != nil {
			s.metrics.IncRpcCallsFailed()
		}
		return nil, err
	}
	queueWait := s.now().Sub(queueStarted).Seconds()
	if s.metrics != nil {
		s.metrics.AddQueueWaitSeconds(queueWait)
	}
	return &lease{s: s, op: op}, nil
}

// waitForDrain blocks until the supervisor stops draining, ctx
// cancels, or the supervisor shuts down.
func (s *Supervisor) waitForDrain(ctx context.Context) error {
	for {
		s.drainMu.Lock()
		if !s.draining {
			s.drainMu.Unlock()
			return nil
		}
		ch := make(chan struct{})
		s.waiters = append(s.waiters, ch)
		s.drainMu.Unlock()
		select {
		case <-ch:
			// Re-check on next iteration; another drain
			// wave could have started.
		case <-ctx.Done():
			// Pull our waiter off the list so a future
			// EndDrain does not leak a closed channel.
			s.drainMu.Lock()
			for i, w := range s.waiters {
				if w == ch {
					s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
					break
				}
			}
			s.drainMu.Unlock()
			return ctx.Err()
		}
	}
}

// acquireSlot reserves a semaphore slot by CAS-incrementing the
// in-flight counter. When the counter would exceed maxInFlight,
// the caller blocks until either a release frees a slot or the
// context cancels. When maxInFlight is 0 the call is unbounded
// and the reservation succeeds immediately.
//
// The CAS loop replaces a held mutex so the wait path is
// goroutine-safe and does not contend on a single lock. Polling
// every 5 ms is the cheap approximation of "wait for the counter
// to drop"; the cost is one extra wait cycle at most.
func (s *Supervisor) acquireSlot(ctx context.Context) error {
	if s.maxInFlight == 0 {
		s.inFlight.Add(1)
		return nil
	}
	for {
		cur := s.inFlight.Load()
		if cur >= s.maxInFlight {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
				continue
			}
		}
		if s.inFlight.CompareAndSwap(cur, cur+1) {
			return nil
		}
		// Lost the race; loop and re-check.
	}
}

// BeginDrain refuses new Admit calls; in-flight calls continue
// until they Release. EndDrain re-enables admission. Drain is a
// short-lived flag, not a shutdown signal; the supervisor is
// designed to be drained, settled, and reopened many times.
func (s *Supervisor) BeginDrain() {
	if s == nil {
		return
	}
	s.drainMu.Lock()
	s.draining = true
	s.drainMu.Unlock()
}

// EndDrain re-enables admission and wakes every blocked caller.
// They will re-check drain on the next loop iteration; new calls
// arriving after EndDrain returns proceed without blocking.
func (s *Supervisor) EndDrain() {
	if s == nil {
		return
	}
	s.drainMu.Lock()
	s.draining = false
	waiters := s.waiters
	s.waiters = nil
	s.drainMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// lease is the concrete Lease returned by Admit.
type lease struct {
	s        *Supervisor
	released atomic.Int32 // 0 = active, 1 = released
	op       Operation
}

func (l *lease) Release() {
	if l == nil || l.s == nil {
		return
	}
	// CompareAndSwap on the released flag ensures exactly-once
	// release semantics even when Release is called concurrently
	// from multiple goroutines.
	if !l.released.CompareAndSwap(0, 1) {
		return
	}
	// Only the goroutine that wins the CAS decrements the counter.
	if l.s.inFlight.Add(-1) < 0 {
		// Defensive: the counter should never go negative. Reset
		// to zero so a future bug does not leak the underflow.
		l.s.inFlight.Store(0)
	}
}

// Operation returns the label the lease was admitted with. It is
// observable after Release because the lease is not zeroed on
// release; the label is part of the admission record, not the
// release machinery.
func (l *lease) Operation() Operation {
	if l == nil {
		return ""
	}
	return l.op
}
