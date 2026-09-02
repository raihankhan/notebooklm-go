// Tests for the canonical --json envelope. Each test pins a single
// acceptance criterion from T-P5-2 (issue #53). The tests intentionally
// hand-construct the expected byte string rather than relying on json.Unmarshal
// round-trip — the wire shape is the contract, and the round-trip would
// silently accept a backwards-incompatible reordering of fields.
package serialize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// TestEnvelopeSuccess covers AC3 (byte-clean JSON) and the field-omission
// behavior: an empty data envelope must NOT carry a "data":null field, and
// a populated data envelope must carry the payload.
func TestEnvelopeSuccess(t *testing.T) {
	t.Run("empty data omits the field", func(t *testing.T) {
		got, err := MarshalSuccess(nil, "req-1")
		if err != nil {
			t.Fatalf("MarshalSuccess: %v", err)
		}
		want := `{"ok":true,"request_id":"req-1"}`
		if string(got) != want {
			t.Fatalf("MarshalSuccess(nil) = %q, want %q", got, want)
		}
	})

	t.Run("simple map data", func(t *testing.T) {
		got, err := MarshalSuccess(map[string]any{"id": "abc", "title": "Foo"}, "req-2")
		if err != nil {
			t.Fatalf("MarshalSuccess: %v", err)
		}
		// Map keys are sorted: id before title.
		want := `{"ok":true,"data":{"id":"abc","title":"Foo"},"request_id":"req-2"}`
		if string(got) != want {
			t.Fatalf("MarshalSuccess(map) = %q, want %q", got, want)
		}
	})

	t.Run("nested struct data", func(t *testing.T) {
		type inner struct {
			Count int      `json:"count"`
			Items []string `json:"items"`
		}
		type outer struct {
			ID    string `json:"id"`
			Inner inner  `json:"inner"`
		}
		got, err := MarshalSuccess(outer{ID: "n1", Inner: inner{Count: 2, Items: []string{"a", "b"}}}, "req-3")
		if err != nil {
			t.Fatalf("MarshalSuccess: %v", err)
		}
		want := `{"ok":true,"data":{"id":"n1","inner":{"count":2,"items":["a","b"]}},"request_id":"req-3"}`
		if string(got) != want {
			t.Fatalf("MarshalSuccess(nested) = %q, want %q", got, want)
		}
	})

	t.Run("empty request_id is omitted", func(t *testing.T) {
		got, err := MarshalSuccess(map[string]any{"x": 1}, "")
		if err != nil {
			t.Fatalf("MarshalSuccess: %v", err)
		}
		if strings.Contains(string(got), "request_id") {
			t.Fatalf("expected request_id to be omitted when empty, got %q", got)
		}
	})
}

// TestEnvelopeError covers AC2 and AC4 — the ErrorBody field-omission
// semantics and the variadic opts contract. Each subtest pins one
// combination of optional fields so a future change that drops or adds a
// tag fails loudly.
func TestEnvelopeError(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		got, err := MarshalError("VALIDATION_ERROR", "missing flag", "")
		if err != nil {
			t.Fatalf("MarshalError: %v", err)
		}
		want := `{"ok":false,"error":{"code":"VALIDATION_ERROR","message":"missing flag"}}`
		if string(got) != want {
			t.Fatalf("MarshalError(minimal) = %q, want %q", got, want)
		}
	})

	t.Run("with retry_after", func(t *testing.T) {
		got, err := MarshalError("RATE_LIMITED", "slow down", "req-1", WithRetryAfter(30))
		if err != nil {
			t.Fatalf("MarshalError: %v", err)
		}
		want := `{"ok":false,"error":{"code":"RATE_LIMITED","message":"slow down","retry_after":30},"request_id":"req-1"}`
		if string(got) != want {
			t.Fatalf("MarshalError(retry_after) = %q, want %q", got, want)
		}
	})

	t.Run("with id and notebook_id", func(t *testing.T) {
		got, err := MarshalError("NOT_FOUND", "source gone", "req-2",
			WithID("src-123"), WithNotebookID("nb-456"))
		if err != nil {
			t.Fatalf("MarshalError: %v", err)
		}
		want := `{"ok":false,"error":{"code":"NOT_FOUND","message":"source gone","id":"src-123","notebook_id":"nb-456"},"request_id":"req-2"}`
		if string(got) != want {
			t.Fatalf("MarshalError(id+notebook_id) = %q, want %q", got, want)
		}
	})

	t.Run("with method_id", func(t *testing.T) {
		got, err := MarshalError("NOTEBOOKLM_ERROR", "rpc failed", "",
			WithMethodID("ABC123"))
		if err != nil {
			t.Fatalf("MarshalError: %v", err)
		}
		want := `{"ok":false,"error":{"code":"NOTEBOOKLM_ERROR","message":"rpc failed","method_id":"ABC123"}}`
		if string(got) != want {
			t.Fatalf("MarshalError(method_id) = %q, want %q", got, want)
		}
	})

	t.Run("all opts combined", func(t *testing.T) {
		got, err := MarshalError("RATE_LIMITED", "hitting the ceiling", "req-9",
			WithRetryAfter(60),
			WithNotebookID("nb-7"),
			WithMethodID("XYZ"))
		if err != nil {
			t.Fatalf("MarshalError: %v", err)
		}
		want := `{"ok":false,"error":{"code":"RATE_LIMITED","message":"hitting the ceiling","retry_after":60,"notebook_id":"nb-7","method_id":"XYZ"},"request_id":"req-9"}`
		if string(got) != want {
			t.Fatalf("MarshalError(all) = %q, want %q", got, want)
		}
	})
}

// TestEnvelopeNoTrailingNewline asserts AC3 verbatim — the bytes returned
// must be exactly the JSON document, no trailing '\n'. This protects
// callers that pipe the bytes directly into `jq` without further trimming.
func TestEnvelopeNoTrailingNewline(t *testing.T) {
	cases := []struct {
		name string
		run  func() ([]byte, error)
	}{
		{"success with data", func() ([]byte, error) {
			return MarshalSuccess(map[string]any{"k": 1}, "r")
		}},
		{"success empty data", func() ([]byte, error) {
			return MarshalSuccess(nil, "")
		}},
		{"error with retry_after", func() ([]byte, error) {
			return MarshalError("RATE_LIMITED", "x", "r", WithRetryAfter(10))
		}},
		{"error minimal", func() ([]byte, error) {
			return MarshalError("AUTH_ERROR", "x", "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.run()
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if n := len(got); n == 0 || got[n-1] == '\n' {
				t.Fatalf("trailing newline present in %q", got)
			}
			if !json.Valid(got) {
				t.Fatalf("output is not valid JSON: %q", got)
			}
		})
	}
}

// TestEnvelopeNoCredentials covers AC7 (no-credentials). Marshaling a struct
// that contains a credential-shaped substring (SNlM0e=…) in a field must
// not leak it onto the wire. The Data field is intentionally NOT redacted
// by the envelope — domain structs are user content, and a blanket redaction
// would mask real data. The Error.Message field IS redacted: that is the
// load-bearing credential-handling primitive of the envelope (docs/AGENTS.md
// rule 4).
func TestEnvelopeNoCredentials(t *testing.T) {
	t.Run("error message is redacted", func(t *testing.T) {
		got, err := MarshalError("AUTH_ERROR",
			"refresh failed: SNlM0e=abc123&FdrFJe=def456", "req-r")
		if err != nil {
			t.Fatalf("MarshalError: %v", err)
		}
		body := string(got)
		if strings.Contains(body, "SNlM0e=abc123") {
			t.Fatalf("credential leaked through error message: %q", body)
		}
		if strings.Contains(body, "FdrFJe=def456") {
			t.Fatalf("credential leaked through error message: %q", body)
		}
		if !strings.Contains(body, "[REDACTED]") {
			t.Fatalf("expected [REDACTED] marker in redacted body, got %q", body)
		}
	})

	t.Run("data payload is not blindly redacted", func(t *testing.T) {
		// Domain structs are user content. The envelope does not redact them;
		// callers route credential-bearing fields through internal/redact at
		// their boundary. This test pins that the envelope does not strip
		// arbitrary data, which would silently corrupt notebook output.
		type row struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		got, err := MarshalSuccess([]row{{Title: "n", Body: "hello"}}, "")
		if err != nil {
			t.Fatalf("MarshalSuccess: %v", err)
		}
		if !strings.Contains(string(got), `"body":"hello"`) {
			t.Fatalf("expected unredacted data payload, got %q", got)
		}
	})
}

// TestEnvelopeRoundTrip proves the envelope decodes back to the same shape
// via wire.Unmarshal. UseNumber is what the module relies on for large ids;
// the round-trip must preserve id strings without float64 precision loss.
func TestEnvelopeRoundTrip(t *testing.T) {
	cases := []Envelope{
		{OK: true, Data: map[string]any{"id": "12345678901234567890", "n": 42}, RequestID: "req-r"},
		{OK: false, Error: &ErrorBody{Code: "NOT_FOUND", Message: "gone", ID: "src-1"}, RequestID: "req-e"},
	}
	for i, env := range cases {
		b, err := wire.Marshal(env)
		if err != nil {
			t.Fatalf("Marshal[%d]: %v", i, err)
		}
		var back Envelope
		if err := wire.Unmarshal(b, &back); err != nil {
			t.Fatalf("Unmarshal[%d]: %v", i, err)
		}
		if back.OK != env.OK {
			t.Fatalf("round-trip[%d]: ok = %v, want %v", i, back.OK, env.OK)
		}
		if back.RequestID != env.RequestID {
			t.Fatalf("round-trip[%d]: request_id = %q, want %q", i, back.RequestID, env.RequestID)
		}
		if (back.Error == nil) != (env.Error == nil) {
			t.Fatalf("round-trip[%d]: error presence mismatch", i)
		}
		if env.Error != nil {
			if back.Error.Code != env.Error.Code || back.Error.Message != env.Error.Message {
				t.Fatalf("round-trip[%d]: error body mismatch: got %+v, want %+v",
					i, back.Error, env.Error)
			}
			if (back.Error.RetryAfter == nil) != (env.Error.RetryAfter == nil) {
				t.Fatalf("round-trip[%d]: retry_after presence mismatch", i)
			}
		}
	}
}

// TestEnvelopeFieldOrder pins the canonical field order on the wire. The
// Python CLI emits keys in this order (ok, data, error, request_id); a
// drift here would not break parsers but would make a Python-produced
// payload and a Go-produced payload differ at the byte level, breaking
// test fixtures that diff the two.
func TestEnvelopeFieldOrder(t *testing.T) {
	got, err := MarshalSuccess(map[string]any{"a": 1}, "rid")
	if err != nil {
		t.Fatalf("MarshalSuccess: %v", err)
	}
	s := string(got)
	idxOK := strings.Index(s, `"ok"`)
	idxData := strings.Index(s, `"data"`)
	idxReq := strings.Index(s, `"request_id"`)
	if idxOK >= idxData || idxData >= idxReq {
		t.Fatalf("field order ok/data/request_id not preserved: %q", s)
	}
}
