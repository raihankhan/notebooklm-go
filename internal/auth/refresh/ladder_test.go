// Package refresh: tests for the Ladder interface, the Level enum,
// and the ErrLadderLevelNotImplemented sentinel. AC2 + AC3 of
// T-P4-2.
//
// Boundary: per docs/AGENTS.md rule 5 this test file is part of the
// mode=internal package; it imports stdlib only.
package refresh

import (
	"context"
	"errors"
	"testing"
)

// TestLevelString: the Level enum maps each rung to its canonical
// label.
func TestLevelString(t *testing.T) {
	cases := []struct {
		l    Level
		want string
	}{
		{L1, "L1"},
		{L2_0, "L2_0"},
		{L2_5, "L2_5"},
		{L3, "L3"},
		{L4, "L4"},
		{Level(0), "L?(0)"},
		{Level(99), "L?(99)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.l.String(); got != tc.want {
				t.Fatalf("Level(%d).String() = %q, want %q", int(tc.l), got, tc.want)
			}
		})
	}
}

// TestLevelsDistinct: every Level has a unique integer value.
// Catches any "two enums share a constant" bug before it reaches
// the dispatcher.
func TestLevelsDistinct(t *testing.T) {
	seen := map[Level]bool{}
	for _, l := range []Level{L1, L2_0, L2_5, L3, L4} {
		if seen[l] {
			t.Fatalf("Level %s duplicated", l)
		}
		seen[l] = true
	}
}

// TestLadderStepNotImplemented: AC3 — every non-L1 level returns
// ErrLadderLevelNotImplemented wrapped with the level name. The
// default Ladder MUST NOT silently return zero values; a
// "not-implemented" rung is the only safe Phase-4 surface.
func TestLadderStepNotImplemented(t *testing.T) {
	ladder := &DefaultLadder{}
	for _, l := range []Level{L2_0, L2_5, L3, L4} {
		t.Run(l.String(), func(t *testing.T) {
			tokens, ok, err := ladder.Step(context.Background(), l)
			if err == nil {
				t.Fatalf("DefaultLadder.Step(%s) err = nil, want ErrLadderLevelNotImplemented", l)
			}
			if !errors.Is(err, ErrLadderLevelNotImplemented) {
				t.Fatalf("DefaultLadder.Step(%s) err = %v, want ErrLadderLevelNotImplemented", l, err)
			}
			if ok {
				t.Fatalf("DefaultLadder.Step(%s) ok = true, want false", l)
			}
			if tokens.FetchedAt.IsZero() == false {
				// Tokens value must be the zero value (no
				// half-populated state on a not-implemented
				// rung).
				t.Fatalf("DefaultLadder.Step(%s) tokens = %+v, want zero Tokens", l, tokens)
			}
		})
	}
}

// TestDefaultLadderUnknownLevel: an out-of-range Level returns
// the same sentinel — never a panic, never a literal "level not
// implemented: L?(99)" without the sentinel wrap.
func TestDefaultLadderUnknownLevel(t *testing.T) {
	ladder := &DefaultLadder{}
	_, _, err := ladder.Step(context.Background(), Level(99))
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("DefaultLadder.Step(unknown) err = %v, want ErrLadderLevelNotImplemented", err)
	}
	if err == nil {
		t.Fatalf("DefaultLadder.Step(unknown) err = nil, want non-nil")
	}
}

// TestErrLadderLevelNotImplementedExported: the sentinel is
// exportable and reusable by Sprint-3 callers.
func TestErrLadderLevelNotImplementedExported(t *testing.T) {
	if ErrLadderLevelNotImplemented == nil {
		t.Fatalf("ErrLadderLevelNotImplemented is nil")
	}
}
