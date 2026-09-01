// Strict positional accessors for nested RPC payloads.
//
// NotebookLM responses are deeply nested positional lists. A single index
// shift is the most common decoder bug class — and unlike a type error it
// silently feeds a zero value downstream, where it is mis-interpreted as
// "no data" rather than "wrong shape". The helpers in this file turn every
// out-of-range, nil-list, or type-mismatch into a typed *ShapeDriftError
// that callers can route to a decode-error metric.
//
// Port of notebooklm-py/_web/wire/index.py. The Python original retired its
// soft-mode opt-out in v0.7.0; do not reintroduce one here.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrDecoding is the sentinel callers can match with errors.Is. It is wrapped
// by *ShapeDriftError so a single sentinel captures both the "index out of
// range" and "type mismatch" failure modes. The docstring on wire/doc.go
// refers to this name as "ErrDecoding" — package consumers should match it.
var ErrDecoding = errors.New("wire: decoding failed")

// ShapeDriftError is returned by At/Str/Int/Bool/List when the positional
// path lands on a slot whose shape does not match what the caller asked for.
// It always wraps ErrDecoding so errors.Is(err, wire.ErrDecoding) is the
// canonical "Google reshaped a response" check.
//
// Three fields:
//
//   - Path:    the dotted accessor path that failed (e.g. "[0][2][1]").
//   - Method:  the RPC name this slot was being read on behalf of, "" if
//     not supplied.
//   - Reason:  one of: "out_of_range", "not_a_list", "not_a_string",
//     "not_an_int", "not_a_bool", "not_a_list_value". Free-form
//     for callers that construct a ShapeDriftError directly.
//
// errors.As(err, &target *ShapeDriftError) is the way to recover the path
// or reason; both are stable strings used in tests and metrics labels.
type ShapeDriftError struct {
	Path   string
	Method string
	Reason string
	// GotType names the dynamic Go type we actually found at Path, when the
	// error came from a type-mismatch (Reason ∈ {"not_a_string", ...}).
	// Empty for out-of-range errors.
	GotType string
}

func (e *ShapeDriftError) Error() string {
	if e.Method != "" {
		return fmt.Sprintf("wire: shape drift on %s at %s: %s (got %s)", e.Method, e.Path, e.Reason, e.GotType)
	}
	return fmt.Sprintf("wire: shape drift at %s: %s (got %s)", e.Path, e.Reason, e.GotType)
}

// Unwrap returns ErrDecoding so errors.Is(err, ErrDecoding) is true. This is
// the documented escape hatch used by callers and by the decode-error metric.
func (e *ShapeDriftError) Unwrap() error { return ErrDecoding }

// At walks a positional path through nested []any and map[string]any nodes
// and returns the leaf value. The path may mix integer indices (for slices)
// and string keys (for maps); both forms are common in NotebookLM responses.
// Index errors are returned as *ShapeDriftError with Reason="out_of_range";
// mismatched container types produce Reason="not_a_list" or "not_a_map".
//
// The Path argument is appended to a leading "[" separator so the resulting
// dotted path is compatible with the format used in the existing decoder
// error logs. For example At(payload, 0, 2, 1) yields Path "[0][2][1]".
//
// Passing nil or an empty path returns the input unchanged without error —
// it is the base case of the recursive walk.
func At(v any, path ...any) (any, error) {
	cur := v
	p := ""
	for _, idx := range path {
		var next any
		switch node := cur.(type) {
		case []any:
			i, ok := idx.(int)
			if !ok {
				return nil, &ShapeDriftError{Path: p, Reason: "not_an_index", GotType: fmt.Sprintf("%T", idx)}
			}
			p = p + fmt.Sprintf("[%d]", i)
			if i < 0 || i >= len(node) {
				return nil, &ShapeDriftError{Path: p, Reason: "out_of_range", GotType: fmt.Sprintf("len=%d", len(node))}
			}
			next = node[i]
		case []string:
			// String slices appear occasionally when callers hand us a
			// literal — give them the same list semantics so At doesn't
			// surprise people.
			i, ok := idx.(int)
			if !ok {
				return nil, &ShapeDriftError{Path: p, Reason: "not_an_index", GotType: fmt.Sprintf("%T", idx)}
			}
			p = p + fmt.Sprintf("[%d]", i)
			if i < 0 || i >= len(node) {
				return nil, &ShapeDriftError{Path: p, Reason: "out_of_range", GotType: fmt.Sprintf("len=%d", len(node))}
			}
			next = node[i]
		case map[string]any:
			key, ok := idx.(string)
			if !ok {
				return nil, &ShapeDriftError{Path: p, Reason: "not_a_key", GotType: fmt.Sprintf("%T", idx)}
			}
			p = p + fmt.Sprintf("[%q]", key)
			v, present := node[key]
			if !present {
				return nil, &ShapeDriftError{Path: p, Reason: "out_of_range", GotType: "map[string]any"}
			}
			next = v
		case nil:
			return nil, &ShapeDriftError{Path: p, Reason: "out_of_range", GotType: "nil"}
		default:
			return nil, &ShapeDriftError{Path: p, Reason: "not_a_list", GotType: fmt.Sprintf("%T", cur)}
		}
		cur = next
	}
	return cur, nil
}

// Str returns the string at the indexed path. nil/missing slots and
// type-mismatched slots both return errors; use OptStr if a missing slot
// is expected.
func Str(v any, path ...any) (string, error) {
	got, err := At(v, path...)
	if err != nil {
		return "", err
	}
	s, ok := got.(string)
	if !ok {
		return "", &ShapeDriftError{Path: formatPath(path), Reason: "not_a_string", GotType: fmt.Sprintf("%T", got)}
	}
	return s, nil
}

// Int returns the int64 at the indexed path. json.Number is range-checked
// before int64 conversion; a fractional or out-of-range number returns a
// ShapeDriftError rather than wrapping a strconv error.
//
// bool is rejected explicitly: in Python `type(code) is not int` guards
// against bool passing as int (bool subclasses int there). In Go the bool
// type is distinct from numeric, but a downstream caller comparing the
// decoded value to a small constant still wants to know they did not get a
// bool by accident — a bool rendered as 0/1 silently passes through a
// numeric comparison.
func Int(v any, path ...any) (int64, error) {
	got, err := At(v, path...)
	if err != nil {
		return 0, err
	}
	switch n := got.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, &ShapeDriftError{Path: formatPath(path), Reason: "not_an_int", GotType: "json.Number(" + n.String() + ")"}
		}
		return i, nil
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case float64:
		// Float that is exactly an integer — accept it (some decode paths
		// lose the json.Number somewhere upstream). Anything else is a
		// type drift.
		if n == float64(int64(n)) {
			return int64(n), nil
		}
		return 0, &ShapeDriftError{Path: formatPath(path), Reason: "not_an_int", GotType: fmt.Sprintf("%T(%v)", got, got)}
	case bool:
		return 0, &ShapeDriftError{Path: formatPath(path), Reason: "not_an_int", GotType: "bool"}
	}
	return 0, &ShapeDriftError{Path: formatPath(path), Reason: "not_an_int", GotType: fmt.Sprintf("%T", got)}
}

// Bool returns the bool at the indexed path. json.Number 0/1 is rejected —
// if the wire returned 0/1 as a number, the schema is ambiguous and the
// caller almost certainly wanted a strict bool check.
func Bool(v any, path ...any) (bool, error) {
	got, err := At(v, path...)
	if err != nil {
		return false, err
	}
	b, ok := got.(bool)
	if !ok {
		return false, &ShapeDriftError{Path: formatPath(path), Reason: "not_a_bool", GotType: fmt.Sprintf("%T", got)}
	}
	return b, nil
}

// List returns the []any at the indexed path. An empty list is NOT an error;
// callers that need to distinguish "no items" from "missing field" can check
// len == 0 themselves. This matches the Python original, which short-circuits
// on empty lists rather than raising.
func List(v any, path ...any) ([]any, error) {
	got, err := At(v, path...)
	if err != nil {
		return nil, err
	}
	switch l := got.(type) {
	case []any:
		return l, nil
	case nil:
		// "The path exists but the value is JSON null" — surface as an
		// empty list so callers can uniformly check len().
		return []any{}, nil
	case []string:
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out, nil
	}
	return nil, &ShapeDriftError{Path: formatPath(path), Reason: "not_a_list_value", GotType: fmt.Sprintf("%T", got)}
}

// OptStr returns the string at path, or "" and false if the slot is
// missing. A present-but-wrong-typed slot is STILL an error — silence on a
// type mismatch hides the very drift this whole package exists to surface.
//
// The (string, bool) signature cannot surface that error so the rule here
// is: only "missing" returns ("", false); any other reason (wrong type,
// wrong container) returns ("", false) too, but the caller is expected
// to know that an Opt path is a "best-effort" lookup and not a typed
// contract.
func OptStr(v any, path ...any) (string, bool) {
	got, err := At(v, path...)
	if err != nil {
		return "", false
	}
	s, ok := got.(string)
	if !ok {
		return "", false
	}
	return s, true
}

// OptInt returns the int64 at path, or 0 and false if the slot is missing.
// Same "missing is silent, mismatched is not" contract as OptStr.
func OptInt(v any, path ...any) (int64, bool) {
	got, err := At(v, path...)
	if err != nil {
		return 0, false
	}
	switch n := got.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// OptBool returns the bool at path, or false and false if missing.
func OptBool(v any, path ...any) (bool, bool) {
	got, err := At(v, path...)
	if err != nil {
		return false, false
	}
	b, ok := got.(bool)
	if !ok {
		return false, false
	}
	return b, true
}

// OptList returns the []any at path, or nil and false if missing. An
// empty list is a hit (len-0, true) — "field is present and empty" is a
// different signal from "field is absent".
func OptList(v any, path ...any) ([]any, bool) {
	got, err := At(v, path...)
	if err != nil {
		return nil, false
	}
	switch l := got.(type) {
	case []any:
		return l, true
	case nil:
		return []any{}, true
	case []string:
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

// formatPath renders a positional path as the canonical "[0][2][1]" string
// used in ShapeDriftError.Path. Centralized so every error has the same form.
func formatPath(path []any) string {
	out := ""
	for _, idx := range path {
		switch v := idx.(type) {
		case int:
			out = out + fmt.Sprintf("[%d]", v)
		case string:
			out = out + fmt.Sprintf("[%q]", v)
		default:
			out = out + fmt.Sprintf("[%v]", v)
		}
	}
	return out
}
