package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBudget_NewAnchorsT0 asserts the anchor (T0) is captured at
// New, not deferred. The test verifies that Remaining() decreases
// over time without re-creating the Budget.
func TestBudget_NewAnchorsT0(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	b := New(bg, time.Hour)
	first := b.Remaining()
	if first <= 0 {
		t.Fatalf("Remaining at start = %v, want > 0", first)
	}
	time.Sleep(20 * time.Millisecond)
	second := b.Remaining()
	if second >= first {
		t.Fatalf("Remaining did not decrease: first=%v second=%v", first, second)
	}
}

// TestBudget_NonExtensionOnRetry is the AC4 property: a fresh Budget
// from the same parent context and same duration does NOT share an
// anchor with the original. Each instance anchors at its own T0.
//
// The retry-loop interpretation is that a decode-time retry that
// constructs a new Budget does not extend the original; both
// Budgets decrement on their own clocks. The original's Remaining
// keeps going down regardless of any second instance.
//
// The test uses long (10s) budgets and short gaps. The pre-budget
// check uses an absolute threshold instead of a "mid < now"
// comparison so a flaky timer on a heavily-loaded CI host cannot
// drive Remaining() to zero before the test gets to read it.
func TestBudget_NonExtensionOnRetry(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	original := New(bg, 10*time.Second)
	time.Sleep(50 * time.Millisecond)

	// Capture the budget as a fraction; if the original Remaining
	// is anywhere near the original timeout we know the anchor is
	// fresh and the test continues.
	mid := original.Remaining()
	if mid < 9*time.Second {
		t.Fatalf("original Remaining = %v after 50ms, want ~9.95s (anchor drifted)", mid)
	}

	// A retry constructs a fresh Budget with the same duration.
	retry := New(bg, 10*time.Second)

	// The retry starts fresh; the original keeps decreasing.
	if retry.Remaining() < 9*time.Second {
		t.Fatalf("retry Remaining = %v, want ~10s (anchor should be fresh)", retry.Remaining())
	}
	if cur := original.Remaining(); cur >= mid {
		t.Fatalf("original Remaining did not decrease: mid=%v now=%v", mid, cur)
	}

	// Wait for the original to elapse is overkill here — just
	// confirm the retry's anchor is its own.
	if retry.startedAt.Equal(original.startedAt) {
		t.Fatalf("retry shares anchor with original")
	}
}

// TestBudget_Monotonic verifies Remaining() decreases monotonically
// over successive samples. Sub-millisecond fluctuations on coarse
// timers can cause two consecutive samples to be equal, so the
// test takes the floor across many samples.
func TestBudget_Monotonic(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	b := New(bg, time.Second)
	var last = b.Remaining()
	for i := 0; i < 20; i++ {
		time.Sleep(time.Millisecond)
		cur := b.Remaining()
		if cur > last {
			t.Fatalf("Remaining grew: last=%v cur=%v", last, cur)
		}
		last = cur
	}
}

// TestBudget_ExpiredFlipsAtZero asserts Expired() flips to true
// right around T0+timeout. We use a 50ms budget and probe Expired()
// after 150ms — comfortably past the flip on any loaded host.
func TestBudget_ExpiredFlipsAtZero(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	b := New(bg, 50*time.Millisecond)
	if b.Expired() {
		t.Fatalf("fresh budget reports expired")
	}
	time.Sleep(150 * time.Millisecond)
	if !b.Expired() {
		t.Fatalf("budget did not expire after %v", 150*time.Millisecond)
	}
	if b.Remaining() != 0 {
		t.Fatalf("Remaining after expiry = %v, want 0", b.Remaining())
	}
}

// TestBudget_NegativeDuration treats negative durations as
// immediately expired and the done channel as closed.
func TestBudget_NegativeDuration(t *testing.T) {
	t.Parallel()
	b := New(context.Background(), -time.Second)
	if !b.Expired() {
		t.Fatalf("negative-duration budget should be expired")
	}
	if b.Remaining() != 0 {
		t.Fatalf("Remaining = %v, want 0", b.Remaining())
	}
	select {
	case <-b.Done():
		// closed channel reports ready immediately.
	default:
		t.Fatalf("Done() channel not closed for negative duration")
	}
}

// TestBudget_DoneChannelClosed ensures the Done() channel fires
// once and only once when the budget elapses.
//
// Both probes (pre-fire and post-fire) are sized to survive a
// heavily-loaded CI host scheduling the timer goroutine late. The
// post-fire probe waits up to a full second past the budget, which
// is the max practical window before a deadline primitive is
// useless.
func TestBudget_DoneChannelClosed(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	b := New(bg, 100*time.Millisecond)

	select {
	case <-b.Done():
		t.Fatalf("Done() fired before timeout")
	case <-time.After(20 * time.Millisecond):
	}

	select {
	case <-b.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatalf("Done() did not fire after timeout")
	}

	// A second receive must also succeed; the channel is one-shot
	// closed, not consumed.
	select {
	case <-b.Done():
		// expected
	default:
		t.Fatalf("Done() channel was drained")
	}
}

// TestBudget_ClampBoundsSleep asserts Clamp() returns the lesser of
// the requested duration and the remaining budget, and never goes
// negative.
func TestBudget_ClampBoundsSleep(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	b := New(bg, 100*time.Millisecond)

	if got := b.Clamp(time.Second); got > 100*time.Millisecond {
		t.Fatalf("Clamp returned %v, want <= remaining", got)
	}
	if got := b.Clamp(-time.Second); got != 0 {
		t.Fatalf("Clamp(negative) = %v, want 0", got)
	}
	if got := b.Clamp(0); got != 0 {
		t.Fatalf("Clamp(0) = %v, want 0", got)
	}

	// After expiry, clamp returns 0.
	time.Sleep(150 * time.Millisecond)
	if got := b.Clamp(time.Second); got != 0 {
		t.Fatalf("Clamp after expiry = %v, want 0", got)
	}
}

// TestBudget_NilSafety guards against panics on a nil Budget; every
// public method must be safe to call on a zero-value.
func TestBudget_NilSafety(t *testing.T) {
	t.Parallel()
	var b *Budget
	if !b.Expired() {
		t.Fatalf("nil.Expired() should be true")
	}
	if b.Remaining() != 0 {
		t.Fatalf("nil.Remaining() should be 0")
	}
	if b.Timeout() != 0 {
		t.Fatalf("nil.Timeout() should be 0")
	}
	if b.Clamp(time.Second) != 0 {
		t.Fatalf("nil.Clamp() should be 0")
	}
	ch := b.Done()
	select {
	case <-ch:
	default:
		t.Fatalf("nil.Done() should be closed")
	}
}

// TestBudget_ConcurrentAccess proves Remaining, Expired, and Done
// are race-free under -race.
func TestBudget_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	b := New(bg, time.Second)
	var wg sync.WaitGroup
	var stops atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = b.Remaining()
				_ = b.Expired()
				select {
				case <-b.Done():
					stops.Add(1)
					return
				default:
				}
			}
		}()
	}
	wg.Wait()
}
