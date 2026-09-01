package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestLifecycle_OpenPhaseOrder verifies participants are opened in
// declared phase order. The test records the order in a shared log.
func TestLifecycle_OpenPhaseOrder(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	var mu sync.Mutex
	var log []string

	open := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			log = append(log, "open:"+name)
			mu.Unlock()
			return nil
		}
	}
	close := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			log = append(log, "close:"+name)
			mu.Unlock()
			return nil
		}
	}

	// Register in non-monotonic order to assert the phase sort
	// actually re-orders them.
	mustRegister(t, l, "p1", 1, open("p1"), close("p1"))
	mustRegister(t, l, "p3", 3, open("p3"), close("p3"))
	mustRegister(t, l, "p2a", 2, open("p2a"), close("p2a"))
	mustRegister(t, l, "p2b", 2, open("p2b"), close("p2b"))

	if err := l.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []string{"open:p1", "open:p2a", "open:p2b", "open:p3"}
	if !sliceEqual(log, want) {
		t.Fatalf("open order = %v, want %v", log, want)
	}
}

// TestLifecycle_OpenRollbackOnPhase2Failure covers AC1: when phase 2
// fails, phase 1 is rolled back in reverse order. Phase 3 is not
// touched.
func TestLifecycle_OpenRollbackOnPhase2Failure(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	var mu sync.Mutex
	var log []string

	open := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			log = append(log, "open:"+name)
			mu.Unlock()
			return nil
		}
	}
	closeFn := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			log = append(log, "close:"+name)
			mu.Unlock()
			return nil
		}
	}
	failing := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			log = append(log, "open:"+name)
			mu.Unlock()
			return errors.New("phase2 boom")
		}
	}

	mustRegister(t, l, "p1", 1, open("p1"), closeFn("p1"))
	mustRegister(t, l, "p2", 2, failing("p2"), closeFn("p2"))
	mustRegister(t, l, "p3", 3, open("p3"), closeFn("p3"))

	err := l.Open(context.Background())
	if err == nil {
		t.Fatalf("Open returned nil, want error")
	}
	if !strings.Contains(err.Error(), "phase2 boom") {
		t.Fatalf("error = %v, want it to mention phase2 boom", err)
	}

	// Rollback order: p1 (the only opened participant before
	// failure). p3 is never opened. p2 is the failing phase so its
	// close hook is NOT called during rollback (it never
	// succeeded).
	want := []string{
		"open:p1",
		"open:p2",
		"close:p1",
	}
	if !sliceEqual(log, want) {
		t.Fatalf("rollback log = %v, want %v", log, want)
	}

	// A second Open on the same lifecycle surfaces the original
	// error; a fresh lifecycle is the way to retry.
	if err2 := l.Open(context.Background()); err2 == nil {
		t.Fatalf("second Open returned nil, want prior error")
	}
}

// TestLifecycle_OpenRollbackMultipleOpenedInPhase covers a multi-
// participant failing phase: phase 2 has two participants; the first
// succeeds, the second fails. The first is rolled back; phase 1 is
// also rolled back; phase 3 is not touched.
func TestLifecycle_OpenRollbackMultipleOpenedInPhase(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	var mu sync.Mutex
	var log []string

	mustRegister(t, l, "p1", 1,
		func(context.Context) error { mu.Lock(); log = append(log, "open:p1"); mu.Unlock(); return nil },
		func(context.Context) error { mu.Lock(); log = append(log, "close:p1"); mu.Unlock(); return nil },
	)
	mustRegister(t, l, "p2a", 2,
		func(context.Context) error { mu.Lock(); log = append(log, "open:p2a"); mu.Unlock(); return nil },
		func(context.Context) error { mu.Lock(); log = append(log, "close:p2a"); mu.Unlock(); return nil },
	)
	mustRegister(t, l, "p2b", 2,
		func(context.Context) error {
			mu.Lock()
			log = append(log, "open:p2b")
			mu.Unlock()
			return errors.New("p2b fail")
		},
		func(context.Context) error { mu.Lock(); log = append(log, "close:p2b"); mu.Unlock(); return nil },
	)
	mustRegister(t, l, "p3", 3,
		func(context.Context) error { mu.Lock(); log = append(log, "open:p3"); mu.Unlock(); return nil },
		func(context.Context) error { mu.Lock(); log = append(log, "close:p3"); mu.Unlock(); return nil },
	)

	err := l.Open(context.Background())
	if err == nil {
		t.Fatalf("Open returned nil, want error")
	}
	want := []string{
		"open:p1",
		"open:p2a",
		"open:p2b",
		"close:p2a",
		"close:p1",
	}
	if !sliceEqual(log, want) {
		t.Fatalf("rollback log = %v, want %v", log, want)
	}
}

// TestLifecycle_CloseDeterministicPrecedence: the FIRST close
// failure wins for the returned error; every other close hook is
// still attempted. This is the teardown-failure-precedence
// contract.
func TestLifecycle_CloseDeterministicPrecedence(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	var mu sync.Mutex
	var log []string
	var ran atomic.Int32

	mustRegister(t, l, "a", 1,
		func(context.Context) error { mu.Lock(); log = append(log, "open:a"); mu.Unlock(); return nil },
		func(context.Context) error {
			mu.Lock()
			log = append(log, "close:a")
			mu.Unlock()
			ran.Add(1)
			return errors.New("close-a failed")
		},
	)
	mustRegister(t, l, "b", 1,
		func(context.Context) error { mu.Lock(); log = append(log, "open:b"); mu.Unlock(); return nil },
		func(context.Context) error {
			mu.Lock()
			log = append(log, "close:b")
			mu.Unlock()
			ran.Add(1)
			return errors.New("close-b failed")
		},
	)

	if err := l.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	err := l.Close(context.Background())
	if err == nil {
		t.Fatalf("Close returned nil, want error")
	}
	if !strings.Contains(err.Error(), "close-b") {
		t.Fatalf("err = %v, want it to name the first close failure (close-b)", err)
	}
	// Both hooks must run even though the first one errored.
	if ran.Load() != 2 {
		t.Fatalf("only %d close hooks ran, want 2", ran.Load())
	}
	// Close must be in reverse-declaration order: b then a (b was
	// registered after a).
	want := []string{"open:a", "open:b", "close:b", "close:a"}
	if !sliceEqual(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}

	// A second Close is a no-op (idempotent).
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

// TestLifecycle_RegisterAfterOpenFails guards the API: Register
// after a successful Open is rejected because the new participant
// cannot be observed by the open wave that just ran.
func TestLifecycle_RegisterAfterOpenFails(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	mustRegister(t, l, "a", 1, noopOpen, noopClose)
	if err := l.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	err := l.Register("late", 2, noopOpen, noopClose)
	if err == nil {
		t.Fatalf("Register after Open succeeded, want error")
	}
}

// TestLifecycle_RegisterDuplicateInPhaseRejected: a duplicate name
// in the same phase is a configuration error caught at register
// time.
func TestLifecycle_RegisterDuplicateInPhaseRejected(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	mustRegister(t, l, "dup", 1, noopOpen, noopClose)
	err := l.Register("dup", 1, noopOpen, noopClose)
	if err == nil {
		t.Fatalf("Register duplicate succeeded, want error")
	}
}

// TestLifecycle_OpenIdempotent verifies a second Open after a
// successful first Open is a no-op.
func TestLifecycle_OpenIdempotent(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	var opened atomic.Int32
	mustRegister(t, l, "a", 1,
		func(context.Context) error { opened.Add(1); return nil },
		noopClose,
	)
	if err := l.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Open(context.Background()); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if opened.Load() != 1 {
		t.Fatalf("open hook ran %d times, want 1", opened.Load())
	}
}

// TestLifecycle_CloseOnNeverOpened is a no-op.
func TestLifecycle_CloseOnNeverOpened(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	mustRegister(t, l, "a", 1, noopOpen, noopClose)
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close on never-opened lifecycle: %v", err)
	}
}

// TestLifecycle_RollbackWithCloseFailure surfaces both errors: the
// open error wins for the returned error, but the close rollback
// failure is wrapped alongside so neither is lost.
func TestLifecycle_RollbackWithCloseFailure(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	mustRegister(t, l, "p1", 1,
		noopOpen,
		func(context.Context) error { return errors.New("rollback-boom") },
	)
	mustRegister(t, l, "p2", 2,
		func(context.Context) error { return errors.New("open-boom") },
		noopClose,
	)
	err := l.Open(context.Background())
	if err == nil {
		t.Fatalf("Open returned nil")
	}
	if !strings.Contains(err.Error(), "open-boom") {
		t.Fatalf("err = %v, want open-boom mentioned", err)
	}
	if !strings.Contains(err.Error(), "rollback-boom") {
		t.Fatalf("err = %v, want rollback-boom mentioned", err)
	}
}

// TestLifecycle_ConcurrentOpenClose attempts a stress run under the
// race detector to flush out any data race the lifecycle participates
// in.
func TestLifecycle_ConcurrentOpenClose(t *testing.T) {
	t.Parallel()
	l := NewLifecycle()
	for i := 0; i < 4; i++ {
		name := string(rune('a' + i))
		mustRegister(t, l, name, 1, noopOpen, noopClose)
	}
	if err := l.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Close(context.Background())
		}()
	}
	wg.Wait()
}

func noopOpen(context.Context) error  { return nil }
func noopClose(context.Context) error { return nil }

func mustRegister(t *testing.T, l *Lifecycle, name string, phase int, open, close func(context.Context) error) {
	t.Helper()
	if err := l.Register(name, phase, open, close); err != nil {
		t.Fatalf("Register %q phase %d: %v", name, phase, err)
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
