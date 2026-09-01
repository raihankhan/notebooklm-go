package atomicio

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flockChildBin builds the testdata/flockchild program with the
// testflockchild build tag and returns its path. It is built lazily on
// first call so a single test invocation pays the build cost exactly once.
var (
	flockChildBinOnce sync.Once
	flockChildBinPath string
	flockChildBinErr  error
)

func buildFlockChild(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("flock is not implemented on Windows in this build")
	}
	flockChildBinOnce.Do(func() {
		binDir, err := os.MkdirTemp("", "atomicio-flockchild-*")
		if err != nil {
			flockChildBinErr = err
			return
		}
		bin := filepath.Join(binDir, "flockchild")
		cmd := exec.Command("go", "build",
			"-tags", "testflockchild",
			"-o", bin,
			"./testdata/flockchild") // #nosec G204 -- test-controlled.
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			flockChildBinErr = err
			return
		}
		flockChildBinPath = bin
	})
	if flockChildBinErr != nil {
		t.Fatalf("build flockchild: %v", flockChildBinErr)
	}
	return flockChildBinPath
}

// skipOnWindows is set as a guard at the top of tests that touch flock.
// The Windows implementation panics on use (see flock_windows.go), and the
// ticket explicitly skip-gates Windows-only code in non-Windows CI.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("flock is not implemented on Windows in this build")
	}
}

// TestFlock_TwoGoroutines is the AC4 goroutine case: one goroutine takes
// Exclusive; the other goroutine takes Shared and must block until the
// Exclusive holder releases. We use Try* to verify the blocking semantics
// rather than racing on a real timer.
func TestFlock_TwoGoroutines(t *testing.T) {
	skipOnWindows(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	releaseA, err := Exclusive(path)
	if err != nil {
		t.Fatalf("Exclusive first: %v", err)
	}
	defer func() { _ = releaseA() }()

	// While A holds Exclusive, TryShared must return (nil, false, nil).
	tryR, ok, err := TryShared(path)
	if err != nil {
		t.Fatalf("TryShared under Exclusive: %v", err)
	}
	if ok {
		_ = tryR()
		t.Fatalf("TryShared under Exclusive returned ok=true, want false")
	}

	// TryExclusive must also fail.
	tryE, ok, err := TryExclusive(path)
	if err != nil {
		t.Fatalf("TryExclusive under Exclusive: %v", err)
	}
	if ok {
		_ = tryE()
		t.Fatalf("TryExclusive under Exclusive returned ok=true, want false")
	}

	// A second Shared-from-another-goroutine attempt blocks until A
	// releases. We spawn a goroutine, give it time to attempt the lock,
	// then release A and assert the goroutine gets the lock within a
	// bounded window.
	gotLock := make(chan Release, 1)
	gotErr := make(chan error, 1)
	go func() {
		r, err := Shared(path)
		if err != nil {
			gotErr <- err
			return
		}
		gotLock <- r
	}()

	// Give the goroutine time to attempt and block.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-gotLock:
		t.Fatalf("goroutine acquired lock while Exclusive was still held")
	case <-gotErr:
		t.Fatalf("goroutine failed unexpectedly")
	default:
	}

	// Release A.
	if err := releaseA(); err != nil {
		t.Fatalf("releaseA: %v", err)
	}
	// The deferred releaseA() will be a no-op now (released==true in the closure).

	// Now the goroutine should acquire Shared within a bounded window.
	select {
	case r := <-gotLock:
		_ = r()
	case err := <-gotErr:
		t.Fatalf("goroutine failed after release: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("goroutine did not acquire Shared after Exclusive was released")
	}
}

// TestFlock_TwoProcesses is the AC4 process case: two os.Exec-launched
// children both try to lock the same path. The parent holds Exclusive;
// the child tries TryExclusive and must fail to acquire. The child's
// exit code distinguishes "ok=true (bug)" from "ok=false (correct)".
func TestFlock_TwoProcesses(t *testing.T) {
	bin := buildFlockChild(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	// Make the lock file present so the helper has something to flock on.
	if err := WriteFile(path, []byte("seed"), 0o600); err != nil {
		t.Fatalf("WriteFile seed: %v", err)
	}

	// Hold Exclusive from the parent.
	parentRelease, err := Exclusive(path)
	if err != nil {
		t.Fatalf("Exclusive: %v", err)
	}
	defer func() { _ = parentRelease() }()

	// Launch the child with op=tryexclusive.
	run := exec.Command(bin, "tryexclusive", path) // #nosec G204 -- test-controlled.
	out, err := run.CombinedOutput()
	if err == nil {
		t.Fatalf("child exited 0 (it acquired the lock) — should have failed; output=%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child error is not ExitError: %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("child exit code = %d, want 1 (TryExclusive returned ok=false). output=%s",
			exitErr.ExitCode(), out)
	}
}

// TestFlock_SharedConcurrent asserts multiple Shared holders may coexist.
func TestFlock_SharedConcurrent(t *testing.T) {
	skipOnWindows(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := WriteFile(path, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const n = 5
	releases := make([]Release, 0, n)
	defer func() {
		for _, r := range releases {
			_ = r()
		}
	}()

	for i := 0; i < n; i++ {
		r, err := Shared(path)
		if err != nil {
			t.Fatalf("Shared %d: %v", i, err)
		}
		releases = append(releases, r)
	}

	// TryExclusive must fail while shared locks are held.
	_, ok, err := TryExclusive(path)
	if err != nil {
		t.Fatalf("TryExclusive under shared: %v", err)
	}
	if ok {
		t.Fatalf("TryExclusive under shared returned ok=true, want false")
	}
}

// TestFlock_PerPathMutex asserts that two goroutines acquiring the same
// path from the same process serialize on the in-process mutex, even if
// the OS-level flock permits them to overlap.
func TestFlock_PerPathMutex(t *testing.T) {
	skipOnWindows(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := WriteFile(path, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const n = 4
	var counter int64
	var wg sync.WaitGroup
	var maxConcurrent int64
	var current int64

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := Exclusive(path)
			if err != nil {
				t.Errorf("Exclusive: %v", err)
				return
			}
			// Hold for a measurable window while updating the counter.
			now := atomic.AddInt64(&current, 1)
			for {
				cur := atomic.LoadInt64(&maxConcurrent)
				if now <= cur || atomic.CompareAndSwapInt64(&maxConcurrent, cur, now) {
					break
				}
			}
			atomic.AddInt64(&counter, 1)
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&current, -1)
			if err := r(); err != nil {
				t.Errorf("release: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&counter); got != n {
		t.Fatalf("counter = %d, want %d", got, n)
	}
	if got := atomic.LoadInt64(&maxConcurrent); got > 1 {
		t.Fatalf("maxConcurrent = %d, want 1 (per-path mutex must serialize)", got)
	}
}

// TestDeriveLockPath covers the centralized derivation.
func TestDeriveLockPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/tmp/foo/config.json", "/tmp/foo/config.json.lock"},
		{"config.json", "config.json.lock"},
		{"./storage_state.json", "storage_state.json.lock"},
	}
	for _, c := range cases {
		got, err := deriveLockPath(c.in)
		if err != nil {
			t.Fatalf("deriveLockPath(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("deriveLockPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	_, err := deriveLockPath("")
	if err == nil {
		t.Fatalf("deriveLockPath(\"\") returned nil error")
	}
}
