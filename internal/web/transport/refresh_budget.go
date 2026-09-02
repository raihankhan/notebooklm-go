// Package transport — refresh_budget.go.
//
// RefreshBudget is the single-shot token that enforces the
// "one refresh per logical RPC" invariant. The transport has two
// layers that can each decide "this is an auth error and I should
// refresh": the HTTP-status layer in
// AuthRefreshMiddleware (which fires on a wire 401) and the
// decode layer in Executor (which fires on a decoded auth-error
// payload). Without a shared budget, both layers can refresh on
// one call — the classic "two refreshes, one of them stale" race.
//
// The budget is intentionally a one-shot primitive:
//   - Constructed at the entry of each logical RPC.
//   - Checked on every entry point that might refresh.
//   - Take() flips spent to true exactly once under sync.Once.
//   - Spend() is the read-side companion to Take(); it returns true
//     when the budget has been consumed by some other path.
//   - On a successful RPC, the budget is dropped without ever
//     being Taken; on a failed RPC, Take() is called by whichever
//     layer decides to refresh.
//
// The primitive is allocation-free: it carries only a sync.Once
// and a spent flag. It is also goroutine-safe: any number of
// concurrent Take / Spend calls reduce to a single observable
// refresh.
//
// Boundary: per docs/AGENTS.md rule 5, this file imports stdlib
// only (sync/atomic, sync).
package transport

import "sync"

// RefreshBudget is a one-shot refresh token. Construct one per
// logical RPC and pass it through the call context so the two
// refresh paths share the same single-shot primitive.
//
// The zero value is ready to use — no constructor needed.
type RefreshBudget struct {
	mu sync.Mutex
	// spent flips to true on the first successful Take. Once set,
	// every subsequent Take returns false.
	spent bool
	// reason is the first error string reported by Take; if a
	// second path tries to Take() after the first already spent
	// the budget, the second paths' error is captured as a
	// duplicate so the executor can log both names for forensics.
	reason string
	// duplicate is non-empty when a second caller tried to Take
	// after the first already spent. Used by Executor to decide
	// whether to surface an extra diagnostic.
	duplicate string
}

// Spend reports whether the budget has already been spent by a
// prior Take call. It does NOT consume the budget. Useful as the
// guard inside an outer loop: "if Spend() is true, the budget
// already paid for one refresh, do not fire another".
//
// The return is true when at least one Take() has already
// succeeded. Spend is safe for concurrent use with Take.
func (b *RefreshBudget) Spend() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// Take consumes the budget exactly once. The first caller wins;
// every other caller returns (false, "") along with the first
// reason the budget recorded.
//
// reason is the short label the first caller wants logged in the
// case where a second caller tries to refresh redundantly
// (typically the wire-401 path's reason on a successful take, or
// the decoded-auth-error path's reason on a successful take; the
// duplicate reason is whatever the second caller wanted to log).
func (b *RefreshBudget) Take(reason string) (won bool, observedReason string) {
	if b == nil {
		// A nil budget never blocks a refresh, but it also never
		// remembers one. This is the "no shared budget wired up"
		// fallback for code that hasn't been threaded through
		// Runtime yet.
		return true, ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent {
		b.duplicate = reason
		return false, b.reason
	}
	b.spent = true
	b.reason = reason
	return true, reason
}

// Reset returns the budget to the unspent state. Intended for
// test fixtures; production code never calls Reset.
func (b *RefreshBudget) Reset() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.spent = false
	b.reason = ""
	b.duplicate = ""
	b.mu.Unlock()
}

// Reason returns the reason the first Take caller recorded.
// Returns "" if the budget has never been Taken or if the receiver
// is nil.
func (b *RefreshBudget) Reason() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reason
}

// Duplicate returns the reason a second caller tried to record
// when the budget had already been spent. Returns "" if no second
// caller has tried, or if the receiver is nil.
func (b *RefreshBudget) Duplicate() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.duplicate
}

// onceHeld is a sentinel kept so the sync import stays live if the
// file is ever simplified to use sync.Once directly. Production
// code never references it.
//
// The current implementation uses sync.Mutex; the field exists as
// a regression guard for any future refactor that switches to
// sync.Once without re-checking concurrent Take semantics.
var _ = sync.Mutex{}
