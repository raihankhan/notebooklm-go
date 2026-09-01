package wire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMarshal_NoHTMLEscape(t *testing.T) {
	// <, >, & must be passed through verbatim, matching Python's json.dumps.
	// Default encoding/json would emit \u003c, \u003e, \u0026.
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"less-than", map[string]any{"<": "<"}, `{"<":"<"}`},
		{"ampersand", map[string]any{"&": "&"}, `{"&":"&"}`},
		{"gt", map[string]any{">": ">"}, `{">":">"}`},
		{"emoji", map[string]any{"emoji": "😀"}, `{"emoji":"😀"}`},
		{"null", map[string]any{"k": nil}, `{"k":null}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("Marshal(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarshal_NoTrailingNewline(t *testing.T) {
	got, err := Marshal(map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if n := len(got); n > 0 && got[n-1] == '\n' {
		t.Fatalf("Marshal appended a trailing newline: %q", got)
	}
	// Re-adding the newline we removed must yield valid JSON: this protects
	// us against accidentally trimming anything other than the trailing '\n'.
	if !json.Valid(append(got, '\n')) {
		t.Fatalf("Marshal output is not valid JSON when newline is re-appended: %q", got)
	}
}

func TestMarshalSorted_DeepNesting(t *testing.T) {
	// Outer map inserted "b" then "a" should produce "a" then "b"; inner
	// map inserted "y" then "x" should produce "x" then "y".
	in := map[string]any{
		"b": map[string]any{"y": 2, "x": 1},
		"a": []any{map[string]any{"z": true}},
	}
	got, err := MarshalSorted(in)
	if err != nil {
		t.Fatalf("MarshalSorted: %v", err)
	}
	want := `{"a":[{"z":true}],"b":{"x":1,"y":2}}`
	if string(got) != want {
		t.Fatalf("MarshalSorted = %q, want %q", got, want)
	}
}

func TestUnmarshal_LargeIntIDsAreJSONNumber(t *testing.T) {
	// 20-digit id, well past float64's 53-bit mantissa.
	const id = "12345678901234567890"
	doc := []byte(`{"id":` + id + `,"name":"source"}`)

	var out struct {
		ID   json.Number `json:"id"`
		Name string      `json:"name"`
	}
	if err := Unmarshal(doc, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.ID.String() != id {
		t.Fatalf("Unmarshal lost id precision: got %q, want %q", out.ID.String(), id)
	}
	// json.Number is a string alias; verify the type, not just the value.
	if reflect.TypeOf(out.ID).String() != "json.Number" {
		t.Fatalf("id type = %T, want json.Number", out.ID)
	}
}

func TestUnmarshal_RejectsTrailingGarbage(t *testing.T) {
	doc := []byte(`{"a":1}{"b":2}`)
	var out map[string]any
	if err := Unmarshal(doc, &out); err == nil {
		t.Fatalf("Unmarshal accepted trailing tokens: out=%v", out)
	}
}

func TestUnmarshal_AnyValueIsJSONNumber(t *testing.T) {
	doc := []byte(`{"id":42,"name":"alice"}`)
	var out map[string]any
	if err := Unmarshal(doc, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	id, ok := out["id"].(json.Number)
	if !ok {
		t.Fatalf("out[\"id\"] type = %T, want json.Number", out["id"])
	}
	if id.String() != "42" {
		t.Fatalf("out[\"id\"] = %q, want \"42\"", id)
	}
}

func TestMarshal_EmptyAndScalars(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"empty map", map[string]any{}, `{}`},
		{"nil", nil, `null`},
		{"empty slice", []any{}, `[]`},
		{"string", "hello", `"hello"`},
		{"true", true, `true`},
		{"int", 42, `42`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("Marshal(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarshal_ByteCompatWithPythonDumps(t *testing.T) {
	// Specific byte-level comparison vs. the Python json.dumps output for a
	// shape we expect to see in RPC payloads. The Python reference is
	// json.dumps(payload, separators=(",", ":"), ensure_ascii=False,
	// sort_keys=True).
	in := map[string]any{
		"method": "foo",
		"params": []any{map[string]any{"b": 2, "a": 1}},
	}
	got, err := MarshalSorted(in)
	if err != nil {
		t.Fatalf("MarshalSorted: %v", err)
	}
	want := `{"method":"foo","params":[{"a":1,"b":2}]}`
	if string(got) != want {
		t.Fatalf("MarshalSorted = %q, want %q", got, want)
	}
	// Sanity: ensure HTML-safe chars are not escaped anywhere in the output.
	for _, r := range "\u003c\u003e\u0026" {
		if strings.ContainsRune(string(got), r) {
			t.Fatalf("Marshal output contains HTML-escaped rune %q: %q", r, got)
		}
	}
}
