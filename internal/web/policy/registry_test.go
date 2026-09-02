// Tests for the IdempotencyRegistry.
//
// Coverage target: 100% on internal/web/policy (per docs/10-implementation-plan.md
// Phase 1 / T-P1-4 acceptance). The tests below exercise every branch
// of every function: the constructor's missing-method path, the
// duplicate-registration panic, the rationale-required path, the
// wildcard vs. variant-specific lookup, the fallback class, and the
// post-seal mutation panics.
package policy

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// allMethodIDs returns every wire.Method declared on master, sorted.
// Used to drive the coverage-exhaustion test that verifies every
// declared Method has an entry.
func allMethodIDs(t *testing.T) []wire.Method {
	t.Helper()
	entries := wire.AllMethods()
	out := make([]wire.Method, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Method)
	}
	return out
}

// buildFullRegistry registers an entry for every declared Method and
// returns the unsealed registry. The caller is expected to call
// NewRegistry to seal it. Rationales are populated for probe-then-create
// entries so Validate passes.
func buildFullRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := &Registry{
		entries:  make(map[key]Entry),
		fallback: ClassUnsafeMutation,
	}
	for _, m := range allMethodIDs(t) {
		// Default every method to safe. Real classification is the
		// job of a later phase; this test cares about the registry
		// machinery, not the assignments.
		MustRegister(reg, m, Entry{
			Class:     ClassSafe,
			Rationale: "defaulted by registry_test.go",
		})
	}
	return reg
}

// TestClass_String covers every label, including the unknown default.
func TestClass_String(t *testing.T) {
	cases := []struct {
		c    Class
		want string
	}{
		{ClassSafe, "safe"},
		{ClassReadOnly, "read-only"},
		{ClassProbeThenCreate, "probe-then-create"},
		{ClassIdempotentMutation, "idempotent-mutation"},
		{ClassUnsafeMutation, "unsafe-mutation"},
		{Class(99), "unknown(99)"},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("Class(%d).String() = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// TestParseClass covers every label and rejects unknown values.
func TestParseClass(t *testing.T) {
	for _, label := range []string{
		"safe", "read-only", "probe-then-create",
		"idempotent-mutation", "unsafe-mutation",
	} {
		if _, err := ParseClass(label); err != nil {
			t.Errorf("ParseClass(%q) returned err: %v", label, err)
		}
	}
	if _, err := ParseClass("nope"); err == nil {
		t.Errorf("ParseClass(nope) returned nil err")
	}
}

// TestNewRegistry_FullCoverage exercises AC1's positive case: a
// registry built over every declared Method succeeds.
func TestNewRegistry_FullCoverage(t *testing.T) {
	reg := buildFullRegistry(t)
	methods := allMethodIDs(t)
	got, err := NewRegistry(methods, reg)
	if err != nil {
		t.Fatalf("NewRegistry over all methods: %v", err)
	}
	if got == nil {
		t.Fatalf("NewRegistry returned nil registry without error")
	}
	if len(got.MethodsRegistered()) != len(methods) {
		t.Errorf("MethodsRegistered len = %d, want %d",
			len(got.MethodsRegistered()), len(methods))
	}
}

// TestNewRegistry_MissingMethod exercises AC1's negative case: drop one
// method from the inputs and NewRegistry must return ErrMissingMethod.
func TestNewRegistry_MissingMethod(t *testing.T) {
	methods := allMethodIDs(t)
	if len(methods) < 2 {
		t.Skip("need at least two methods to test the drop-one path")
	}
	dropped := methods[0]
	// Register only methods[1] (skipping dropped) so the registry has
	// one fewer entry than methods[] claims.
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, methods[1], Entry{Class: ClassSafe, Rationale: "x"})
	_, err := NewRegistry(methods, reg)
	if err == nil {
		t.Fatalf("NewRegistry succeeded after dropping %q", dropped)
	}
	if !errors.Is(err, ErrMissingMethod) {
		t.Errorf("err = %v, want errors.Is(ErrMissingMethod)", err)
	}
	if !strings.Contains(err.Error(), string(dropped)) {
		t.Errorf("err message does not name the dropped method %q: %v", dropped, err)
	}
}

// TestMustRegister_DuplicatePanics exercises AC1: panicking on duplicate
// (method, variant) registration.
func TestMustRegister_DuplicatePanics(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	defer func() {
		if recover() == nil {
			t.Errorf("MustRegister second call did not panic")
		}
	}()
	MustRegister(reg, wire.MethodListNotebooks, Entry{Class: ClassSafe})
	MustRegister(reg, wire.MethodListNotebooks, Entry{Class: ClassReadOnly}) // panic
}

// TestMustRegister_SealedPanics exercises the post-seal mutation guard.
func TestMustRegister_SealedPanics(t *testing.T) {
	reg := buildFullRegistry(t)
	if _, err := NewRegistry(allMethodIDs(t), reg); err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Errorf("MustRegister on sealed registry did not panic")
		}
	}()
	MustRegister(reg, wire.MethodListNotebooks, Entry{Class: ClassSafe})
}

// TestSetFallback_SealedPanics covers the post-seal mutation guard for
// SetFallback.
func TestSetFallback_SealedPanics(t *testing.T) {
	reg := buildFullRegistry(t)
	if _, err := NewRegistry(allMethodIDs(t), reg); err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Errorf("SetFallback on sealed registry did not panic")
		}
	}()
	reg.SetFallback(ClassSafe)
}

// TestClassify_DefaultUnsafe covers the no-entry fallback path. We
// register an entry with explicit (non-wildcard) Variants so that
// looking up an unlisted variant is the only way to reach the
// fallback.
func TestClassify_DefaultUnsafe(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodAddSource, Entry{
		Class:     ClassIdempotentMutation,
		Variants:  []string{"url", "paste"},
		Rationale: "id-keyed by source id",
	})
	// Build a registry with one declared method to keep NewRegistry
	// happy, and classify an unlisted variant.
	methods := []wire.Method{wire.MethodAddSource}
	got, err := NewRegistry(methods, reg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got.Classify(wire.MethodAddSource, "this-variant-is-never-registered") != ClassUnsafeMutation {
		t.Errorf("Classify with unlisted variant did not return fallback unsafe")
	}
}

// TestSetFallback_Overrides covers the SetFallback happy path.
func TestSetFallback_Overrides(t *testing.T) {
	reg := buildFullRegistry(t)
	reg.SetFallback(ClassSafe)
	got, err := NewRegistry(allMethodIDs(t), reg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	methods := got.MethodsRegistered()
	if len(methods) == 0 {
		t.Skip("no methods registered")
	}
	if got.Classify(methods[0], "nonsense") != ClassSafe {
		t.Errorf("Classify after SetFallback(Safe) returned non-Safe")
	}
}

// TestValidate_ProbeRationale covers AC3: a probe-then-create entry
// without rationale fails Validate.
func TestValidate_ProbeRationale(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodListNotebooks, Entry{
		Class:     ClassProbeThenCreate,
		Rationale: "", // missing — Validate must reject
	})
	err := reg.Validate()
	if err == nil {
		t.Fatalf("Validate accepted probe-then-create without rationale")
	}
	if !errors.Is(err, ErrProbeRationale) {
		t.Errorf("err = %v, want errors.Is(ErrProbeRationale)", err)
	}
}

// TestValidate_ProbeRationaleWhitespace covers that whitespace-only
// rationale is also rejected.
func TestValidate_ProbeRationaleWhitespace(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodListNotebooks, Entry{
		Class:     ClassProbeThenCreate,
		Rationale: "   \t\n  ",
	})
	err := reg.Validate()
	if err == nil {
		t.Fatalf("Validate accepted whitespace-only rationale")
	}
	if !errors.Is(err, ErrProbeRationale) {
		t.Errorf("err = %v, want errors.Is(ErrProbeRationale)", err)
	}
}

// TestValidate_OK covers the happy path.
func TestValidate_OK(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodListNotebooks, Entry{
		Class:     ClassProbeThenCreate,
		Rationale: "caller owns the probe loop",
	})
	if err := reg.Validate(); err != nil {
		t.Errorf("Validate on populated registry: %v", err)
	}
}

// TestLookup_ExactAndWildcard covers the per-variant vs wildcard lookup.
func TestLookup_ExactAndWildcard(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodAddSource, Entry{
		Class:     ClassIdempotentMutation,
		Variants:  []string{"url", "paste"},
		Rationale: "id-keyed by source id",
	})
	// Exact hit.
	if _, ok := reg.Lookup(wire.MethodAddSource, "url"); !ok {
		t.Errorf("Lookup exact variant url returned !ok")
	}
	// No hit for unlisted variant.
	if _, ok := reg.Lookup(wire.MethodAddSource, "drive"); ok {
		t.Errorf("Lookup unlisted variant drive returned ok")
	}
	// Different method.
	if _, ok := reg.Lookup(wire.MethodListNotebooks, "url"); ok {
		t.Errorf("Lookup wrong method returned ok")
	}
}

// TestLookup_WildcardEntry covers the nil-Variants wildcard.
func TestLookup_WildcardEntry(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodListNotebooks, Entry{
		Class:     ClassSafe,
		Rationale: "list is read-only regardless of variant",
	})
	for _, v := range []string{"", "stream", "anything"} {
		if _, ok := reg.Lookup(wire.MethodListNotebooks, v); !ok {
			t.Errorf("Lookup wildcard for variant %q returned !ok", v)
		}
	}
}

// TestLookup_EmptyVariantsSlice covers the non-nil-but-empty Variants
// branch (treated as wildcard just like nil).
func TestLookup_EmptyVariantsSlice(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodListNotebooks, Entry{
		Class:     ClassSafe,
		Variants:  []string{}, // non-nil but empty: same as nil
		Rationale: "wildcard via empty slice",
	})
	if _, ok := reg.Lookup(wire.MethodListNotebooks, "anything"); !ok {
		t.Errorf("Lookup with empty-slice Variants did not match")
	}
}

// TestLookup_ExplicitEmptyVariant covers the case where an entry is
// registered with Variants: [""] (explicit empty string). Lookup with
// a different variant should not match, even though the entry was
// registered under the empty-string key.
func TestLookup_ExplicitEmptyVariant(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodListNotebooks, Entry{
		Class:     ClassSafe,
		Variants:  []string{""}, // explicitly maps to empty-string variant
		Rationale: "explicit empty variant",
	})
	// Lookup with empty-string variant hits the exact-match path.
	if _, ok := reg.Lookup(wire.MethodListNotebooks, ""); !ok {
		t.Errorf("Lookup empty-string variant returned !ok")
	}
	// Lookup with a different variant misses both exact and wildcard
	// because the entry explicitly listed Variants != nil.
	if _, ok := reg.Lookup(wire.MethodListNotebooks, "other"); ok {
		t.Errorf("Lookup non-empty variant against explicit-empty entry returned ok")
	}
}

// TestMethodsRegistered_Empty covers the empty-registry path. Useful
// when a caller passes an empty declared list AND a pre-populated
// registry built from zero MustRegister calls.
func TestMethodsRegistered_Empty(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	_, err := NewRegistry(nil, reg)
	if err != nil {
		t.Fatalf("NewRegistry with empty registry and nil methods: %v", err)
	}
}

// TestMethodsRegistered_Dedup exercises the dedup-by-method branch.
// When the same method is registered under multiple variants,
// MethodsRegistered must report it exactly once.
func TestMethodsRegistered_Dedup(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodAddSource, Entry{
		Class:     ClassIdempotentMutation,
		Variants:  []string{"url", "paste", "drive"},
		Rationale: "id-keyed by source id",
	})
	MustRegister(reg, wire.MethodDeleteSource, Entry{
		Class:     ClassIdempotentMutation,
		Rationale: "single-id delete",
	})
	methods := []wire.Method{wire.MethodAddSource, wire.MethodDeleteSource}
	got, err := NewRegistry(methods, reg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got := got.MethodsRegistered(); len(got) != 2 {
		t.Errorf("MethodsRegistered returned %d methods, want 2 (one with 3 variants should dedup)", len(got))
	}
}

// TestNewRegistry_NilRegistry covers the nil-registry guard.
func TestNewRegistry_NilRegistry(t *testing.T) {
	_, err := NewRegistry(nil, nil)
	if err == nil {
		t.Fatalf("NewRegistry(nil, nil) returned no error")
	}
	if !strings.Contains(err.Error(), "nil registry") {
		t.Errorf("err message missing 'nil registry': %v", err)
	}
}

// TestNew_InitializesEntries covers the public New() constructor.
// A fresh Registry must have a non-nil entries map so the
// first MustRegister call does not panic with a nil-map
// assignment. The constructor is what external callers use
// when they need to allocate a Registry without reaching into
// the unexported entries field.
func TestNew_InitializesEntries(t *testing.T) {
	reg := New()
	if reg == nil {
		t.Fatalf("New() returned nil")
	}
	if reg.entries == nil {
		t.Errorf("New().entries is nil; first MustRegister would panic")
	}
	// And it is usable end-to-end.
	MustRegister(reg, wire.MethodListNotebooks, Entry{
		Class: ClassSafe, Rationale: "x",
	})
	sealed, err := NewRegistry([]wire.Method{wire.MethodListNotebooks}, reg)
	if err != nil {
		t.Errorf("seal after New() + MustRegister: %v", err)
	}
	if sealed == nil {
		t.Errorf("sealed registry is nil")
	}
}

// TestNewRegistry_SealedPanics covers the post-seal re-entry guard.
func TestNewRegistry_SealedPanics(t *testing.T) {
	reg := buildFullRegistry(t)
	if _, err := NewRegistry(allMethodIDs(t), reg); err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	_, err := NewRegistry(allMethodIDs(t), reg)
	if err == nil {
		t.Fatalf("NewRegistry on sealed registry did not error")
	}
	if !strings.Contains(err.Error(), "sealed") {
		t.Errorf("err message missing 'sealed': %v", err)
	}
}

// TestClassify_AllFiveClasses covers AC2: a table over the five
// classes, asserting Classify returns the registered class.
func TestClassify_AllFiveClasses(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodListNotebooks, Entry{Class: ClassSafe, Rationale: "x"})
	MustRegister(reg, wire.MethodGetNotebook, Entry{Class: ClassReadOnly, Rationale: "x"})
	MustRegister(reg, wire.MethodAddSource, Entry{
		Class: ClassProbeThenCreate, Rationale: "x",
	})
	MustRegister(reg, wire.MethodDeleteSource, Entry{
		Class: ClassIdempotentMutation, Rationale: "x",
	})
	MustRegister(reg, wire.MethodCreateNotebook, Entry{
		Class: ClassUnsafeMutation, Rationale: "x",
	})
	cases := []struct {
		method wire.Method
		want   Class
	}{
		{wire.MethodListNotebooks, ClassSafe},
		{wire.MethodGetNotebook, ClassReadOnly},
		{wire.MethodAddSource, ClassProbeThenCreate},
		{wire.MethodDeleteSource, ClassIdempotentMutation},
		{wire.MethodCreateNotebook, ClassUnsafeMutation},
	}
	for _, tc := range cases {
		if got := reg.Classify(tc.method, ""); got != tc.want {
			t.Errorf("Classify(%s) = %s, want %s", tc.method, got, tc.want)
		}
	}
}

// TestMethodsRegistered_Sorted covers the stable-iteration contract.
func TestMethodsRegistered_Sorted(t *testing.T) {
	reg := buildFullRegistry(t)
	got, err := NewRegistry(allMethodIDs(t), reg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ms := got.MethodsRegistered()
	if !sort.SliceIsSorted(ms, func(i, j int) bool {
		return string(ms[i]) < string(ms[j])
	}) {
		t.Errorf("MethodsRegistered not sorted: %v", ms)
	}
}

// TestNewRegistry_FullValidateRejectsProbe ensures the constructor's
// Validate call rejects probe-without-rationale before sealing.
func TestNewRegistry_FullValidateRejectsProbe(t *testing.T) {
	reg := &Registry{entries: make(map[key]Entry)}
	MustRegister(reg, wire.MethodListNotebooks, Entry{
		Class:     ClassProbeThenCreate,
		Rationale: "",
	})
	methods := []wire.Method{wire.MethodListNotebooks}
	_, err := NewRegistry(methods, reg)
	if err == nil {
		t.Fatalf("NewRegistry accepted probe-without-rationale")
	}
	if !errors.Is(err, ErrProbeRationale) {
		t.Errorf("err = %v, want errors.Is(ErrProbeRationale)", err)
	}
}

// TestNewRegistry_EmptyMethodsList ensures the missing-method error
// path fires when declared[] is empty (every registered entry becomes
// "extra" by definition).
func TestNewRegistry_EmptyMethodsList(t *testing.T) {
	reg := buildFullRegistry(t)
	// Declare a method that is NOT in the registered set (buildFullRegistry
	// covers every declared Method on master, so we need to invent a
	// bogus method id and declare it).
	bogus := wire.Method("bogus-not-registered-method")
	_, err := NewRegistry([]wire.Method{bogus}, reg)
	if err == nil {
		t.Fatalf("NewRegistry with undeclared bogus method succeeded")
	}
	if !errors.Is(err, ErrMissingMethod) {
		t.Errorf("err = %v, want errors.Is(ErrMissingMethod)", err)
	}
}
