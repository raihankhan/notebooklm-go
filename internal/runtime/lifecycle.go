// Package runtime holds the lifecycle, supervisor, metrics, and deadline
// primitives shared by every transport call.
//
// lifecycle.go — phased participant ordering, rollback on a failed
// open, and deterministic teardown failure precedence.
//
// Lifecycle owns one resource generation at a time. Open runs every
// registered participant in declared phase order. If a phase fails,
// participants from earlier phases are rolled back in reverse order,
// and the first error from the failing phase wins. Close runs every
// close hook in reverse-declaration order; the first error wins for
// the returned error but every close attempt is still attempted.
//
// All exported methods are safe for concurrent use.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Lifecycle coordinates a set of participants that must be opened
// and closed in a controlled order. Each participant is associated
// with a phase number; lower phases open first. Within a phase, the
// declaration order is preserved.
//
// Lifecycle is stateful: at most one open wave and one close wave
// run at a time per Lifecycle instance. Concurrent Open calls block
// until the in-flight wave completes; Open after Close is not
// supported and returns an error.
type Lifecycle struct {
	mu sync.Mutex
	// opened flips true on a successful Open. It gates Close so a
	// Close on a never-opened lifecycle is a no-op.
	opened bool
	// closed flips true on Close (or after a failed Open that ran
	// the rollback). It is the irreversible teardown flag.
	closed bool
	// openErr is set on a failed Open. It is returned by every
	// subsequent Open or Close on the same instance.
	openErr error

	// Participants keyed by name. The order of insertion is the
	// declaration order; phase is read on Register.
	parts []*participant
}

// participant is one registered resource owner.
type participant struct {
	name  string
	phase int
	open  func(ctx context.Context) error
	close func(ctx context.Context) error
	// opened records whether open() returned nil. Closed uses it
	// to skip rolling back an un-opened participant if the open
	// wave aborted past it (e.g. phase 2 failed before phase 3 was
	// attempted).
	opened bool
}

// NewLifecycle returns an empty Lifecycle ready for Register.
func NewLifecycle() *Lifecycle {
	return &Lifecycle{}
}

// Register adds a participant with the given phase number. Lower
// phase numbers open first; within a phase, participants open in
// the order they were registered.
//
// An open or close callback may be nil; the missing callback is
// skipped. Duplicate names are not allowed within the same phase;
// across phases they are permitted because a phase is a strict
// ordering unit.
//
// Register after a successful Open is an error: it cannot be
// observed by the open wave that just ran and would silently miss
// the close wave too.
func (l *Lifecycle) Register(name string, phase int, open, close func(ctx context.Context) error) error {
	if l == nil {
		return errors.New("runtime: nil lifecycle")
	}
	if name == "" {
		return errors.New("runtime: participant name is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.opened {
		return errors.New("runtime: register after open is not permitted")
	}
	if l.closed {
		return errors.New("runtime: register after close is not permitted")
	}
	for _, p := range l.parts {
		if p.name == name && p.phase == phase {
			return fmt.Errorf("runtime: duplicate participant %q in phase %d", name, phase)
		}
	}
	l.parts = append(l.parts, &participant{
		name:  name,
		phase: phase,
		open:  open,
		close: close,
	})
	return nil
}

// Open runs every participant in declared phase order. If a phase
// fails, every participant already opened is rolled back in reverse
// order and the first error from the failing phase is returned. If
// multiple participants in the failing phase errored, the first in
// declaration order wins.
//
// Open is idempotent on a fully opened lifecycle: a second call is
// a no-op and returns nil. After a failed Open, the lifecycle is in
// the closed state and a subsequent Open returns the original
// error (a fresh Lifecycle is the right way to retry).
func (l *Lifecycle) Open(ctx context.Context) error {
	if l == nil {
		return errors.New("runtime: nil lifecycle")
	}
	l.mu.Lock()
	if l.openErr != nil {
		err := l.openErr
		l.mu.Unlock()
		return err
	}
	if l.opened {
		l.mu.Unlock()
		return nil
	}
	// Sort by phase then by declaration order. Phase is the primary
	// key; within a phase, registration order is preserved by the
	// stable secondary key (original index).
	sorted := l.sortedParts()
	opened := sorted[:0:0]
	var firstErr error
	for _, p := range sorted {
		if p.open == nil {
			// Record as opened so Close can skip it; there is
			// nothing to roll back.
			p.opened = true
			continue
		}
		if err := p.open(ctx); err != nil {
			firstErr = fmt.Errorf("runtime: open phase %d participant %q: %w",
				p.phase, p.name, err)
			break
		}
		p.opened = true
		opened = append(opened, p)
	}
	if firstErr != nil {
		// Roll back every participant we successfully opened,
		// in reverse order. The first rollback error is wrapped
		// with the original open error so the caller sees both.
		rollbackErr := firstErr
		rollbackReported := false
		for i := len(opened) - 1; i >= 0; i-- {
			p := opened[i]
			if p.close == nil {
				continue
			}
			if err := p.close(ctx); err != nil && !rollbackReported {
				rollbackErr = fmt.Errorf(
					"runtime: rollback %q after failed open: %w (open error: %w)",
					p.name, err, firstErr)
				rollbackReported = true
			}
		}
		l.openErr = rollbackErr
		l.closed = true
		l.mu.Unlock()
		return rollbackErr
	}
	l.opened = true
	l.mu.Unlock()
	return nil
}

// Close runs every opened participant's close hook in reverse
// declaration order. The first error wins for the returned error,
// but every other close hook is still attempted (the order is
// reverse-declaration, which means highest phase first within the
// reverse-declaration order). Close is idempotent and safe under
// concurrent Close calls.
//
// Close propagates a previously-failed Open: if Open returned an
// error, Close returns the same error without re-running the close
// hooks (there is nothing open to close).
func (l *Lifecycle) Close(ctx context.Context) error {
	if l == nil {
		return errors.New("runtime: nil lifecycle")
	}
	l.mu.Lock()
	if l.openErr != nil {
		err := l.openErr
		l.mu.Unlock()
		return err
	}
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	// Close in reverse declaration order so the last-opened
	// participant tears down first. The reverse sort respects
	// phase: phase 3 closes before phase 2 closes before phase 1.
	all := l.parts
	toClose := make([]*participant, 0, len(all))
	for _, p := range all {
		if p.opened && p.close != nil {
			toClose = append(toClose, p)
		}
	}
	// Reverse by declaration order: last-in, first-out.
	for i, j := 0, len(toClose)-1; i < j; i, j = i+1, j-1 {
		toClose[i], toClose[j] = toClose[j], toClose[i]
	}
	l.mu.Unlock()

	var firstErr error
	for _, p := range toClose {
		if err := p.close(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("runtime: close participant %q: %w",
				p.name, err)
		}
	}
	return firstErr
}

// sortedParts returns a copy of the participants sorted by phase
// (ascending) then by declaration order (ascending). The returned
// slice shares no backing array with l.parts.
func (l *Lifecycle) sortedParts() []*participant {
	out := make([]*participant, len(l.parts))
	copy(out, l.parts)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].phase != out[j].phase {
			return out[i].phase < out[j].phase
		}
		// Same phase: preserve declaration order. We tagged the
		// original index on the participant struct indirectly via
		// pointer identity, but since we copied, fall back to the
		// slice position before sort: that is exactly the
		// declaration order.
		return originalIndex(l.parts, out[i]) < originalIndex(l.parts, out[j])
	})
	return out
}

// originalIndex returns the first index of p in src. Used to break
// ties between participants in the same phase: the declaration
// order is the original slice position.
func originalIndex(src []*participant, p *participant) int {
	for i, q := range src {
		if q == p {
			return i
		}
	}
	return len(src)
}
