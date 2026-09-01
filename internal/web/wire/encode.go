// Request-side encoding for the batchexecute protocol.
//
// Port of notebooklm-py/_web/wire/encoder.py. The encoder wraps an RPC
// invocation into the triple-nested positional shape that the
// batchexecute endpoint consumes:
//
//  1. inner   = [resolvedID, jsonCompact(params), null, null]
//  2. request = [[inner]]
//  3. body    = "f.req=" + urlencode(jsonCompact(request))
//     + "&at="  + urlencode(csrf)
//     + "&"
//
// The Python original uses json.dumps(separators=(",", ":")) — wire.Marshal
// mirrors that byte-for-byte. The percent encoder is wire.escapeAll, which
// matches Python's quote(s, safe="") (space → %20, '/' → %2F, etc.).
//
// This file uses the canonical wire.Method type, declared in methods.go
// (T-P1-2). When this branch was first written, the stub alias lived here;
// it was removed in the post-merge rebase once T-P1-2's named type landed.
package wire

import "strings"

// EncodeRequest produces the triple-nested positional body the
// batchexecute endpoint consumes:
//
//	[[ "rpc", jsonCompact(params), resolvedID, null, null ]]
//
// The "rpc" tag is the literal sentinel the protocol expects as index 0
// of the inner list. resolvedID is the obfuscated id from the Method table
// — it must match the rpcids= URL parameter on the same call, or the
// server returns a malformed-request error.
//
// params is encoded via wire.MarshalSorted so a payload built from an
// untyped map produces deterministic key order; this matters for the
// golden-bytes test table — every byte must match the Python reference.
//
// The returned bytes are the JSON body only (no "f.req=" wrapping). Use
// BuildRequestBody when the full application/x-www-form-urlencoded body is
// needed.
func EncodeRequest(method Method, params any, resolvedID string) ([]byte, error) {
	// Python emits this shape as a literal list literal; we mirror it as
	// []any so the encoder walks it deterministically.
	inner := []any{
		method,     // index 0: the "rpc" tag (per Python encoder)
		params,     // index 1: payload
		resolvedID, // index 2: the resolved obfuscated id
		nil,        // index 3: null placeholder
		nil,        // index 4: null placeholder
	}
	request := []any{inner}
	return MarshalSorted(request)
}

// BuildRequestBody is the alias returning the form-urlencoded body that
// gets POSTed to the batchexecute endpoint. It is the function name in
// the doc 03 reference; EncodeRequest is the lower-level primitive that
// returns just the JSON body.
//
// csrf is the SNlM0e token; pass "" to omit the "&at=" segment. The
// trailing "&" is real and load-bearing — the protocol parser expects it.
func BuildRequestBody(method Method, params any, resolvedID string, csrf string) ([]byte, error) {
	body, err := EncodeRequest(method, params, resolvedID)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("f.req=")
	b.WriteString(escapeAll(string(body)))
	if csrf != "" {
		b.WriteString("&at=")
		b.WriteString(escapeAll(csrf))
	}
	b.WriteByte('&')
	return []byte(b.String()), nil
}

// NestSourceIDs wraps each id in `depth` inner lists and returns the
// collected slice. nil and empty inputs return nil (no panic, no []any{}).
// Per doc 03 §"Source-id nesting":
//
//	depth 1 → [[id1], [id2]]
//	depth 2 → [[[id1]], [[id2]]]
//	depth 3 → [[[[id1]]], [[[id2]]]]
//
// The depth varies per RPC and sometimes per slot within one RPC — audio
// generation sends depth 2 in one slot and depth 1 in another, in the same
// payload. Copy the depth from the Python builder; never assume.
//
// depth < 1 is a programming error and panics. The Python original raises
// ValueError; we mirror with panic because this is a misuse of the API
// (caller is supposed to be reading the spec).
func NestSourceIDs(ids []string, depth int) []any {
	if depth < 1 {
		panic("wire: NestSourceIDs depth must be >= 1")
	}
	if len(ids) == 0 {
		return nil
	}
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	for d := 0; d < depth; d++ {
		next := make([]any, len(out))
		for i, v := range out {
			next[i] = []any{v}
		}
		out = next
	}
	return out
}

// TemplateBlock returns the canonical request-options wrapper that
// CREATE_NOTEBOOK, every ADD_SOURCE variant, ADD_SOURCE_FILE, GET_NOTEBOOK,
// and the label RPCs all carry at the same offset:
//
//	[2, null, null, [1, null, null, null, null, null, null, null, null, null, [1]]]
//
// Per doc 03 §"The shared template block", this wrapper became mandatory
// with Google's Gemini-3.5 rollout; the older degenerate tails are now
// rejected with gRPC status 3/5/9.
//
// opts is currently unused — the wrapper is identical across all RPCs
// that use it. We accept it as a parameter anyway so callers in later
// phases can extend the wrapper without an API break.
//
// Each call returns a fresh slice — the protocol parser detects shared
// mutable nested literals and rejects them. Never share one between calls.
func TemplateBlock(_ map[string]string) []any {
	// Inner: [1, null, null, null, null, null, null, null, null, null, [1]]
	inner := []any{
		1, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		[]any{1},
	}
	return []any{
		2, nil, nil, inner,
	}
}

// ArtifactOptions returns the longer sibling used by artifact RPCs, with a
// trailing capability projection:
//
//	[2, null, null, [1, ...], [[1, 4, 8, 2, 3, 6]]]
//
// The capability projection list is the documented constant; the inner
// block mirrors TemplateBlock. As with TemplateBlock, opts is reserved
// for a future extension point — the shape is constant across artifact
// RPCs today.
//
// Each call returns a fresh slice.
func ArtifactOptions(_ map[string]any) []any {
	inner := []any{
		1, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		[]any{1},
	}
	capabilities := []any{
		[]any{1, 4, 8, 2, 3, 6},
	}
	return []any{
		2, nil, nil, inner, capabilities,
	}
}
