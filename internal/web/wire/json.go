package wire

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

// Marshal encodes v as JSON with the project's wire settings:
//
//   - HTML-significant characters (<, >, &) are NOT escaped
//     (SetEscapeHTML(false)).
//   - The trailing newline that json.Encoder.Encode appends is stripped.
//   - Map keys are emitted in lexical order, mirroring Python's
//     json.dumps(separators=(",", ":")) output.
//
// This is the ONLY json.Marshal / json.Encoder call site in the module
// (docs/AGENTS.md rule 3). Callers that need deterministic key ordering for a
// payload built from untyped maps should prefer MarshalSorted.
func Marshal(v any) ([]byte, error) {
	return MarshalSorted(v)
}

// MarshalSorted is like Marshal but sorts map keys deterministically before
// encoding. encoding/json sorts string-keyed maps on its own, but it does NOT
// sort when the map type is map[any]any or when the value is nested behind an
// any. The NotebookLM wire payloads we mirror arrive as nested map[string]any
// trees from RPC decoder sites, so we walk and sort them here.
//
// The sort is stable in lexical byte order, matching the order Python 3's
// json.dumps emits with sort_keys=True.
func MarshalSorted(v any) ([]byte, error) {
	sorted := sortKeys(v)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(sorted); err != nil {
		return nil, err
	}

	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// Unmarshal decodes JSON bytes into v, using UseNumber() so that large
// integer ids arrive as json.Number rather than float64. NotebookLM ids are
// up to 20-digit decimals that lose precision when round-tripped through
// float64; preserving the original textual form is required by the Python
// port (see docs/AGENTS.md rule 3).
//
// This is the ONLY json.Unmarshal / json.Decoder call site in the module.
func Unmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// A trailing token (whitespace, EOF) is fine; anything else means the
	// payload was malformed and we must not silently accept it.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

// sortKeys returns a copy of v with map keys recursively sorted. String-keyed
// maps are reordered in place; any-keyed maps are rebuilt with string keys
// coerced to their lexical form (json.Marshal rejects int keys when the map
// type is map[any]any unless they are strings, so we preserve the value
// exactly and rely on the caller not to mix numeric keys into a heterogeneous
// map). Slices and arrays are walked element by element.
//
// Scalars (strings, numbers, bools, nils) are returned unchanged.
func sortKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			return t
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = sortKeys(t[k])
		}
		return out
	case map[any]any:
		if len(t) == 0 {
			return t
		}
		// Promote to map[string]any to give encoding/json a stable order.
		// Non-string keys (int, float, bool) are coerced via fmt's default
		// representation — the wire payloads we accept use only string keys,
		// but we keep the path for completeness.
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[coerceKey(k)] = sortKeys(val)
		}
		keys := make([]string, 0, len(out))
		for k := range out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		ordered := make(map[string]any, len(out))
		for _, k := range keys {
			ordered[k] = out[k]
		}
		return ordered
	case []any:
		if len(t) == 0 {
			return t
		}
		out := make([]any, len(t))
		for i := range t {
			out[i] = sortKeys(t[i])
		}
		return out
	default:
		return v
	}
}

// coerceKey renders an any-key as the string encoding/json would emit.
// In practice the wire payloads we see only contain string keys; this helper
// exists so the sortKeys walk does not panic on a non-string any-key.
func coerceKey(k any) string {
	switch v := k.(type) {
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		// json.Marshal always emits a JSON string for a string key, but for
		// non-string keys it emits the literal. Trim surrounding quotes if
		// present so the result is a usable map key.
		s := string(b)
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			return s[1 : len(s)-1]
		}
		return s
	}
}
