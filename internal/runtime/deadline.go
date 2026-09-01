// Package runtime holds the lifecycle, supervisor, metrics, and deadline
// primitives shared by every transport call.
//
// deadline.go — T0-anchored aggregate deadline used by retry and
// polling loops. The T0 anchor is captured at New and is never
// extended on retry; decode-time refresh-and-retry still respects
// the original budget.
package runtime

import (
	"context"
	"sync"
	"time"
)

// Budget tracks an aggregate timeout against a monotonic clock. The
// anchor (T0) is captured at construction and is intentionally
// immutable: a decode-time retry that constructs a fresh Budget from
// the same parent context with the same duration does not extend the
// original anchor. This is the property the retry middleware relies on
// to clamp its post-refresh sleep.
//
// The Budget is safe for concurrent use; the embedded once guards
// the done channel that flips exactly once.
type Budget struct {
	timeout   time.Duration
	startedAt time.Time // monotonic anchor (T0)
	doneCh    chan struct{}
	doneOnce  sync.Once
	// timer drives the close of doneCh. Nil when the Budget was
	// constructed with a non-positive duration (no timer needed)
	// or after the timer fires and the channel has been closed.
	timer *time.Timer
}

// New returns a Budget with T0 captured at the moment of the call.
// The parent context is accepted for forward-compatibility (so a
// later phase can derive Done() cancellation from context.Context)
// but is not observed today. The Budget's anchor does not depend on
// the parent context: cancellation of the parent does not extend the
// deadline.
//
// A non-positive duration yields an already-expired Budget whose
// Expired() returns true immediately and whose Done() channel is
// closed.
func New(parent context.Context, d time.Duration) *Budget {
	_ = parent // accepted for forward-compatibility; no current use.
	if d < 0 {
		d = 0
	}
	b := &Budget{
		timeout:   d,
		startedAt: time.Now(),
		doneCh:    make(chan struct{}),
	}
	if d <= 0 {
		b.doneOnce.Do(func() { close(b.doneCh) })
		return b
	}
	// Use AfterFunc so the close runs on the runtime's timer
	// goroutine rather than a per-Budget goroutine. Under heavy
	// load the per-Budget goroutine can be starved and the timer
	// can fire arbitrarily late; AfterFunc schedules on the
	// shared timer pool and is not vulnerable to that starvation.
	b.timer = time.AfterFunc(d, func() {
		b.doneOnce.Do(func() { close(b.doneCh) })
	})
	return b
}

// Timeout returns the original duration the Budget was started with.
// It does not change as the budget elapses.
func (b *Budget) Timeout() time.Duration {
	if b == nil {
		return 0
	}
	return b.timeout
}

// Remaining returns the time left until T0+timeout, never negative.
// A Budget that has elapsed returns 0.
func (b *Budget) Remaining() time.Duration {
	if b == nil {
		return 0
	}
	if b.Expired() {
		return 0
	}
	elapsed := time.Since(b.startedAt)
	// time.Since is monotonic-positive; subtraction cannot underflow.
	if elapsed >= b.timeout {
		return 0
	}
	return b.timeout - elapsed
}

// Expired reports whether the Budget has reached or passed its
// timeout. It flips true at exactly the zero mark and never returns
// false again.
func (b *Budget) Expired() bool {
	if b == nil {
		return true
	}
	return time.Since(b.startedAt) >= b.timeout
}

// Done returns a channel that is closed when the Budget expires.
// It is a single-shot channel: exactly one close, no value, no error.
func (b *Budget) Done() <-chan struct{} {
	if b == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return b.doneCh
}

// Clamp returns d capped to the Budget's remaining time, never
// negative. A common use is to bound a sleep duration before
// retrying a decode-time refresh: the budget does not extend, so a
// long retry sequence ends up polling Done() instead of stacking
// unbounded sleeps.
func (b *Budget) Clamp(d time.Duration) time.Duration {
	if b == nil {
		return 0
	}
	if d <= 0 {
		return 0
	}
	r := b.Remaining()
	if d > r {
		return r
	}
	return d
}
