// Package logging provides a stderr-only slog handler for notebooklm-go.
//
// The redaction hook (see handler.go) routes every attribute value
// through internal/redact.Apply so secrets embedded in attribute
// values are masked before they hit the wire. The seam lives in this
// file so the rest of the package never imports internal/redact
// directly — easier to swap out for testing.
package logging

import (
	"sync/atomic"

	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// redactor is the minimal seam between this package and internal/redact.
// It is deliberately narrow: a single method that takes the raw
// attribute bytes (already formatted for the text handler) and returns
// the bytes after redaction. internal/redact.Apply satisfies it
// directly; tests can inject their own implementation via WithRedactor.
type redactor interface {
	Apply([]byte) []byte
}

// hookRedactor holds the package-level redactor used by replaceAttr.
// It is read on every logged attribute, so we use atomic.Pointer for
// lock-free reads. Writes happen only via WithRedactor (tests and main).
var hookRedactor atomic.Pointer[redactor]

func init() {
	r := defaultRedactor()
	hookRedactor.Store(&r)
}

// defaultRedactor returns the package-default redactor: the real
// internal/redact.Apply implementation.
func defaultRedactor() redactor {
	return redactorFunc(redact.Apply)
}

// redactorFunc adapts a free function to the redactor interface so we
// do not need to declare a local wrapper struct just to satisfy the
// seam.
type redactorFunc func([]byte) []byte

// Apply satisfies redactor by delegating to the underlying function.
func (f redactorFunc) Apply(b []byte) []byte { return f(b) }

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
