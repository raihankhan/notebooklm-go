// Package logging provides a stderr-only slog handler for notebooklm-go.
//
// This file declares the local redactor seam used by handleAttr (see
// handler.go). It exists so T-P0-2 can land BEFORE internal/redact
// (T-P0-3). When T-P0-3 merges, the stub implementation below is replaced
// with the real import — no other file in this package needs to change.
package logging

import "sync/atomic"

// redactor is the minimal seam between this package and internal/redact.
//
// It is deliberately narrow: a single method that takes the raw attribute
// bytes (already formatted for the text handler) and returns the bytes
// after redaction. The text handler calls ReplaceAttr per attribute, so
// the redaction happens at the attribute boundary where it has access to
// key + value type, not just the on-wire string.
//
// T-P0-3 will replace the local implementation by:
//
//  1. Removing this file's noopRedactor.
//  2. Importing "github.com/raihankhan/notebooklm-go/internal/redact".
//  3. Injecting a *redact.Redactor via WithRedactor(...) at construction.
type redactor interface {
	Apply([]byte) []byte
}

// noopRedactor is the placeholder used until T-P0-3 lands. It returns the
// input unchanged so the handler is fully functional (and the test suite
// green) before the real redactor exists.
type noopRedactor struct{}

// Apply satisfies redactor and is a no-op pass-through.
func (noopRedactor) Apply(b []byte) []byte { return b }

// defaultRedactor returns the package-default redactor. Today this is the
// no-op; tomorrow it will be the real internal/redact implementation.
func defaultRedactor() redactor {
	return noopRedactor{}
}

// hookRedactor holds the package-level redactor used by replaceAttr.
// It is read on every logged attribute, so we use atomic.Pointer for
// lock-free reads. Writes happen only via WithRedactor (tests and main).
var hookRedactor atomic.Pointer[redactor]

func init() {
	r := defaultRedactor()
	hookRedactor.Store(&r)
}

// WithRedactor lets callers (mainly tests; eventually main.go) inject a
// concrete redactor. Returns a cleanup function that restores the previous
// redactor — intended for use with t.Cleanup. We deliberately return a
// no-arg closure rather than the previous redactor itself because the
// caller almost always wants t.Cleanup semantics; passing the previous
// value out invites double-assignment bugs.
func WithRedactor(r redactor) func() {
	if r == nil {
		r = defaultRedactor()
	}
	prev := hookRedactor.Swap(&r)
	return func() {
		hookRedactor.Store(prev)
	}
}

// currentRedactor is the lock-free read used by replaceAttr. Inlined to
// keep the hot path branch-light.
func currentRedactor() redactor {
	if p := hookRedactor.Load(); p != nil {
		return *p
	}
	return defaultRedactor()
}
