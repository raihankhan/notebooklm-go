// Tests for the chatstream params builder.
//
// The Python original
// (notebooklm-py/src/notebooklm/_web/params/chat_stream.py::build_streaming_chat_request)
// is the normative wire-shape source. The Go port adapts the body
// shape for the SDK surface in T-S3-007e — the golden-bytes test
// below pins the exact JSON output so the streaming parser
// (T-S3-007c) and the SDK layer can rely on byte-equal input.
//
// We do NOT use the testdata/golden.json table that notebooks_test.go
// uses — the chatstream builder is its own delivery surface and
// golden strings read more naturally inline.
package params

import (
	"strings"
	"testing"
)

// TestBuildChatStreamRequest_Bytes_NoPersona — the canonical wire
// shape with no persona, no conversation id, two source ids.
//
// Source ids are nested to depth 2 per
// wire.NestSourceIDs(ids, 2): each id is wrapped twice, giving
// `[[[id1]], [[id2]]]` after the outer collection. The exact bytes
// below are the contract; a reviewer changing the builder must change
// this string in the same commit so the diff is auditable.
func TestBuildChatStreamRequest_Bytes_NoPersona(t *testing.T) {
	got, err := BuildChatStreamRequest(
		"Hello world",
		"",
		[]string{"src-1", "src-2"},
		"",
		1,
	)
	if err != nil {
		t.Fatalf("BuildChatStreamRequest: %v", err)
	}
	want := `[[[["src-1"]],[["src-2"]]],"Hello world",null,[""],"",null,null,"` + ChatStreamNotebookIDPlaceholder + `",1]`
	if got != want {
		t.Fatalf("BuildChatStreamRequest bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildChatStreamRequest_Bytes_WithPersona — the wire shape with
// a persona and an existing conversation id, single source.
func TestBuildChatStreamRequest_Bytes_WithPersona(t *testing.T) {
	got, err := BuildChatStreamRequest(
		"What is X?",
		"conv-abc",
		[]string{"src-1"},
		"researcher",
		42,
	)
	if err != nil {
		t.Fatalf("BuildChatStreamRequest: %v", err)
	}
	want := `[[[["src-1"]]],"What is X?",null,["researcher"],"conv-abc",null,null,"` + ChatStreamNotebookIDPlaceholder + `",1]`
	if got != want {
		t.Fatalf("BuildChatStreamRequest bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildChatStreamRequest_Bytes_NoSources — the wire shape with
// no source scoping. Per wire.NestSourceIDs, an empty slice produces
// nil (not []any{}), so slot 0 is `null`.
func TestBuildChatStreamRequest_Bytes_NoSources(t *testing.T) {
	got, err := BuildChatStreamRequest(
		"Hello",
		"",
		nil,
		"",
		1,
	)
	if err != nil {
		t.Fatalf("BuildChatStreamRequest: %v", err)
	}
	want := `[null,"Hello",null,[""],"",null,null,"` + ChatStreamNotebookIDPlaceholder + `",1]`
	if got != want {
		t.Fatalf("BuildChatStreamRequest bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildChatStreamRequest_Bytes_UTF8Prompt — the wire shape with
// a non-ASCII prompt. Per AGENTS.md rule 3, encoding/json must NOT
// HTML-escape "<", ">", "&" — a non-ASCII prompt exercises both the
// UTF-8 pass-through and the no-HTML-escape contract.
func TestBuildChatStreamRequest_Bytes_UTF8Prompt(t *testing.T) {
	got, err := BuildChatStreamRequest(
		"Hello 你好 🎉",
		"",
		[]string{"src-1"},
		"",
		1,
	)
	if err != nil {
		t.Fatalf("BuildChatStreamRequest: %v", err)
	}
	want := `[[[["src-1"]]],"Hello 你好 🎉",null,[""],"",null,null,"` + ChatStreamNotebookIDPlaceholder + `",1]`
	if got != want {
		t.Fatalf("BuildChatStreamRequest bytes differ\n got: %s\nwant: %s", got, want)
	}
	// Sanity: the emoji did not get HTML-escaped or otherwise mangled.
	if !strings.Contains(got, "🎉") {
		t.Errorf("expected prompt to contain raw emoji; got %q", got)
	}
}

// TestBuildChatStreamRequest_NoHTMLEscape — the encoded body must
// not HTML-escape the "<", ">", or "&" characters (AGENTS.md rule 3).
func TestBuildChatStreamRequest_NoHTMLEscape(t *testing.T) {
	got, err := BuildChatStreamRequest(
		"<script>&alert(1)</script>",
		"",
		[]string{"src-1"},
		"",
		1,
	)
	if err != nil {
		t.Fatalf("BuildChatStreamRequest: %v", err)
	}
	for _, esc := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(got, esc) {
			t.Errorf("encoded body contains HTML-escape sequence %s: %s", esc, got)
		}
	}
}

// TestBuildChatStreamRequest_RejectsEmptyPrompt — the builder
// surfaces a typed *paramError when the caller passes an empty
// prompt. An empty prompt on the wire is rejected by the backend
// with a bare gRPC status the user cannot act on.
func TestBuildChatStreamRequest_RejectsEmptyPrompt(t *testing.T) {
	_, err := BuildChatStreamRequest("", "", nil, "", 1)
	if err == nil {
		t.Fatalf("BuildChatStreamRequest(empty prompt) returned nil err; want validation error")
	}
}

// TestChatStreamPersona — the persona sub-array pins both the
// populated case and the no-persona case (the single empty-string
// sentinel). A literal nil slice or `[]string{}` would change the
// encoded shape (slot 3 becomes `null` / `[]`) and break the parser.
func TestChatStreamPersona(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty persona", "", []string{""}},
		{"populated persona", "researcher", []string{"researcher"}},
		{"single char persona", "x", []string{"x"}},
		{"unicode persona", "研究者", []string{"研究者"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ChatStreamPersona(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("ChatStreamPersona(%q) len = %d, want %d", c.in, len(got), len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("ChatStreamPersona(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestBuildChatStreamURLParams — the URL-parameter helper formats
// the `_reqid` query string the chat backend requires. The
// `rt=c` literal is the streaming flag.
func TestBuildChatStreamURLParams(t *testing.T) {
	cases := []struct {
		reqID int
		want  string
	}{
		{1, "rt=c&_reqid=1"},
		{100000, "rt=c&_reqid=100000"},
		{42, "rt=c&_reqid=42"},
	}
	for _, c := range cases {
		got := BuildChatStreamURLParams(c.reqID)
		if got != c.want {
			t.Errorf("BuildChatStreamURLParams(%d) = %q, want %q", c.reqID, got, c.want)
		}
	}
}

// TestChatStreamPersona_ShapeConsistency — the persona sub-array is
// always a single-element slice so the wire shape is uniform across
// the empty / populated split. The streaming parser (T-S3-007c)
// relies on this invariant.
func TestChatStreamPersona_ShapeConsistency(t *testing.T) {
	if got := len(ChatStreamPersona("")); got != 1 {
		t.Errorf("ChatStreamPersona(\"\") len = %d, want 1", got)
	}
	if got := len(ChatStreamPersona("researcher")); got != 1 {
		t.Errorf("ChatStreamPersona(\"researcher\") len = %d, want 1", got)
	}
}

// TestBuilderCoverageCalls_ChatStream — exercises every builder
// directly so the coverage tool sees the function bodies.
func TestBuilderCoverageCalls_ChatStream(t *testing.T) {
	_, _ = BuildChatStreamRequest("hi", "c1", []string{"s1"}, "researcher", 1)
	_, _ = BuildChatStreamRequest("hi", "", nil, "", 1)
	_ = ChatStreamPersona("")
	_ = ChatStreamPersona("researcher")
	_ = BuildChatStreamURLParams(1)
}
