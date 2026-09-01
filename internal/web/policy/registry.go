// IdempotencyRegistry — the gate the transport consults before replaying
// a batchexecute RPC after a lost response.
//
// See doc.go for the package rationale. This file is the data structure
// plus the public registration / lookup API. Validation lives in
// (Registry).Validate and is invoked from NewRegistry; the constructor
// also asserts every Method has at least one entry, per the package
// invariant.
package policy

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Class is the idempotency class for a (method, variant) entry.
//
// The numeric ordering is significant: it is the strictness ordering,
// so a future "at least" helper could pick the strictest class from a
// set. Today no such helper exists; the constant values are still
// stable for log/CLI output.
type Class int

const (
	// ClassSafe — never mutates server state; safe to retry forever.
	ClassSafe Class = iota
	// ClassReadOnly — semantically identical to ClassSafe today; the
	// split exists so a future policy addition (caching hint, request
	// coalescing) does not require an axis change.
	ClassReadOnly
	// ClassProbeThenCreate — caller owns its own probe loop; the
	// transport must force-disable inner retries so a probe-then-create
	// never silently doubles up.
	ClassProbeThenCreate
	// ClassIdempotentMutation — replay-safe mutation (set-state / delete
	// by id / rename); retries stay on.
	ClassIdempotentMutation
	// ClassUnsafeMutation — no dedupe key, no probe; the first failure
	// must surface.
	ClassUnsafeMutation
)

// String returns the lowercase, hyphen-form label for c. Stable across
// releases because the IdempotencyRegistry writes these labels to logs
// and to the CLI's --json envelope.
func (c Class) String() string {
	switch c {
	case ClassSafe:
		return "safe"
	case ClassReadOnly:
		return "read-only"
	case ClassProbeThenCreate:
		return "probe-then-create"
	case ClassIdempotentMutation:
		return "idempotent-mutation"
	case ClassUnsafeMutation:
		return "unsafe-mutation"
	default:
		return fmt.Sprintf("unknown(%d)", int(c))
	}
}

// ParseClass is the inverse of (Class).String; useful for tests and for
// the policy/inspect CLI subcommand. Returns an error on unknown input.
func ParseClass(s string) (Class, error) {
	switch s {
	case "safe":
		return ClassSafe, nil
	case "read-only":
		return ClassReadOnly, nil
	case "probe-then-create":
		return ClassProbeThenCreate, nil
	case "idempotent-mutation":
		return ClassIdempotentMutation, nil
	case "unsafe-mutation":
		return ClassUnsafeMutation, nil
	default:
		return 0, fmt.Errorf("policy: unknown class %q", s)
	}
}

// Entry is a single registration: a class, an optional list of
// supported variants, and a rationale string that ClassProbeThenCreate
// entries must populate.
//
// An entry with Variants == nil is treated as "applies to every variant
// of this method, including the empty string". An entry with Variants
// non-nil only fires for the listed variants; lookups for unlisted
// variants fall through to Classify's default (ClassUnsafeMutation).
type Entry struct {
	// Class is the idempotency classification.
	Class Class
	// Variants, when non-empty, restricts the entry to those variants.
	// Use nil/empty to register a wildcard.
	Variants []string
	// Rationale is required for ClassProbeThenCreate (otherwise
	// Validate fails); it explains how the caller recovers from the
	// lost response.
	Rationale string
}

// key is the registry's internal map key. It is unexported so callers
// cannot construct one and bypass MustRegister's duplicate detection.
type key struct {
	method  wire.Method
	variant string
}

// Registry is the immutable-after-construction classification table.
// Construct via NewRegistry; mutations happen only via MustRegister
// during the build phase (before NewRegistry returns).
type Registry struct {
	// entries is the (method, variant) -> Entry map.
	entries map[key]Entry
	// fallback is the class returned by Classify when no entry matches.
	// Defaults to ClassUnsafeMutation; tests can override via SetFallback.
	fallback Class
	// fallbackSet is true after an explicit SetFallback call. NewRegistry
	// only applies the default (ClassUnsafeMutation) when this is false,
	// so a caller who picked ClassSafe or ClassReadOnly as the fallback
	// is not silently overwritten.
	fallbackSet bool
	// sealed is true once NewRegistry has returned successfully; further
	// calls to MustRegister or SetFallback panic.
	sealed bool
}

// Sentinel errors. Use errors.Is for detection.
var (
	// ErrDuplicate signals a (method, variant) was registered twice.
	ErrDuplicate = errors.New("policy: duplicate registration")
	// ErrMissingMethod signals NewRegistry was called with methods[]
	// that did not include every declared Method.
	ErrMissingMethod = errors.New("policy: missing registration")
	// ErrProbeRationale signals Validate rejected a ClassProbeThenCreate
	// entry with an empty Rationale.
	ErrProbeRationale = errors.New("policy: probe-then-create entry missing rationale")
)

// NewRegistry seals reg after verifying that every method in methods[]
// has at least one registered entry in reg. It also calls Validate
// (which rejects probe-then-create entries with empty Rationale).
//
// reg must already have been populated via MustRegister; NewRegistry
// itself does not add entries. Pass methods == nil to mean "no method
// coverage check" (useful for tests and for building a sub-registry).
//
// The registry is immutable after this call: further calls to
// MustRegister or SetFallback panic. Callers that want to mutate after
// construction should rebuild the registry instead.
func NewRegistry(methods []wire.Method, reg *Registry) (*Registry, error) {
	if reg == nil {
		return nil, errors.New("policy: NewRegistry called with nil registry")
	}
	if reg.sealed {
		return nil, errors.New("policy: NewRegistry called on already-sealed registry")
	}
	// Default the fallback to ClassUnsafeMutation when the caller has
	// not already set one via SetFallback. Using fallbackSet rather
	// than `reg.fallback == 0` because ClassSafe is the zero value
	// and we must not silently overwrite an explicit Safe choice.
	if !reg.fallbackSet {
		reg.fallback = ClassUnsafeMutation
		reg.fallbackSet = true
	}
	declared := make(map[wire.Method]bool, len(methods))
	for _, m := range methods {
		declared[m] = true
	}
	// Detect methods that were never registered at all. We scan the
	// entries map the caller populated via MustRegister.
	seen := make(map[wire.Method]bool)
	for k := range reg.entries {
		seen[k.method] = true
	}
	var missing []wire.Method
	for m := range declared {
		if !seen[m] {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		sort.Slice(missing, func(i, j int) bool { return string(missing[i]) < string(missing[j]) })
		return nil, fmt.Errorf("%w: %s", ErrMissingMethod, formatMethods(missing))
	}
	if err := reg.Validate(); err != nil {
		return nil, err
	}
	reg.sealed = true
	return reg, nil
}

// MethodsRegistered returns the set of wire.Method values that have at
// least one registered entry. The result is sorted by Method for stable
// iteration order (tests rely on this).
func (r *Registry) MethodsRegistered() []wire.Method {
	out := make([]wire.Method, 0, len(r.entries))
	seen := make(map[wire.Method]bool)
	for k := range r.entries {
		if seen[k.method] {
			continue
		}
		seen[k.method] = true
		out = append(out, k.method)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

// Lookup returns the Entry registered for (method, variant). The second
// return value is false if no entry matches.
func (r *Registry) Lookup(method wire.Method, variant string) (Entry, bool) {
	if e, ok := r.entries[key{method: method, variant: variant}]; ok {
		return e, true
	}
	// Wildcard: any entry registered under the empty-string variant
	// (MustRegister normalizes nil/empty Variants to the empty string).
	for k, e := range r.entries {
		if k.method != method || k.variant != "" {
			continue
		}
		// Skip entries that explicitly listed variants (their empty
		// key would still be hit by direct lookup above; remaining
		// empty-key entries are wildcards).
		if len(e.Variants) > 0 {
			continue
		}
		return e, true
	}
	return Entry{}, false
}

// Classify returns the idempotency class for (method, variant), or the
// registry's fallback class (ClassUnsafeMutation by default) if no
// entry matches.
func (r *Registry) Classify(method wire.Method, variant string) Class {
	if e, ok := r.Lookup(method, variant); ok {
		return e.Class
	}
	return r.fallback
}

// Validate verifies the registry's internal invariants that cannot be
// checked at registration time:
//   - every ClassProbeThenCreate entry has a non-empty Rationale
//
// Validate does NOT check method coverage (that's NewRegistry's job);
// the registry can be Valid even with zero entries, since the caller
// might be running tests or building a sub-registry.
func (r *Registry) Validate() error {
	var problems []string
	for k, e := range r.entries {
		if e.Class != ClassProbeThenCreate {
			continue
		}
		if strings.TrimSpace(e.Rationale) == "" {
			problems = append(problems,
				fmt.Sprintf("%s/%q: probe-then-create entry missing rationale",
					k.method, k.variant))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%w: %s", ErrProbeRationale, strings.Join(problems, "; "))
	}
	return nil
}

// MustRegister registers entry for method. Variants in entry.Variants
// are expanded into individual (method, variant) keys; an entry with
// nil/empty Variants registers under the empty-string variant.
//
// MustRegister panics if any (method, variant) it would register is
// already present, or if the registry has been sealed by NewRegistry.
//
// MustRegister is the only mutation API; the registry is read-only
// after NewRegistry returns.
func MustRegister(reg *Registry, method wire.Method, entry Entry) {
	if reg.sealed {
		panic("policy: MustRegister called after NewRegistry")
	}
	variants := entry.Variants
	if len(variants) == 0 {
		variants = []string{""}
	}
	for _, v := range variants {
		k := key{method: method, variant: v}
		if _, exists := reg.entries[k]; exists {
			panic(fmt.Sprintf("%s: %s/%q already registered",
				ErrDuplicate.Error(), method, v))
		}
		reg.entries[k] = entry
	}
}

// SetFallback overrides the default class returned by Classify when no
// entry matches. Must be called BEFORE NewRegistry; calling after the
// seal is set panics.
func (r *Registry) SetFallback(c Class) {
	if r.sealed {
		panic("policy: SetFallback called after NewRegistry")
	}
	r.fallback = c
	r.fallbackSet = true
}

// formatMethods joins a list of methods into a comma-separated string
// for error messages.
func formatMethods(ms []wire.Method) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}
