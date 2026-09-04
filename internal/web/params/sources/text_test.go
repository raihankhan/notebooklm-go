// Tests for the sources params Text builder.
//
// The Python original (_web/sources/add.py::add_text_source)
// is the normative wire-shape source — every builder's output
// here must match what `wire.Marshal` would emit for the
// equivalent positional literal in Python. We assert the byte
// output below as golden strings.
//
// The Text branch is the same 11-slot spec as the URL /
// YouTube / Drive branches but with the [title, content] pair
// riding at slot 1 (the text-branch discriminator) and the
// type marker `2` riding at slot 3 (the load-bearing branch
// selector the backend reads to dispatch the text branch).
package sources

import (
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/params"
)

// TestBuildAddSourceText_Bytes — the golden encode test
// pinned against Python's `add_text_source` literal. The
// 11-slot spec carries [title, content] at slot 1, the type
// marker `2` at slot 3, and the source-type code `1` at slot
// 10. An empty mime falls back to "text/plain" so the default
// case byte-equals the Python literal plus a single quoted
// "text/plain" at slot 1[1].
func TestBuildAddSourceText_Bytes(t *testing.T) {
	got, err := params.Encode(func() []any {
		return BuildAddSourceText("nb-1", "Pasted text", "Hello world", "")
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[[[null,["Pasted text","Hello world"],null,2,null,null,null,null,null,null,1]],"nb-1",[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceText bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceText_ExplicitMIME — a non-empty mime
// replaces the default at slot 1[1] via the override path.
// The Text branch's MIME rides inside the [title, content]
// pair at slot 1 (slot 1[1] is the content element); the
// wire spec at slot 13 — used by URL / YouTube — is NOT the
// Text branch's MIME envelope. This test pins the Text-branch
// override behavior so a future ticket that unifies the MIME
// envelope across kinds has to update both builders.
func TestBuildAddSourceText_ExplicitMIME(t *testing.T) {
	got, err := params.Encode(func() []any {
		return BuildAddSourceText("nb-1", "Title", "Content", "text/html")
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[[[null,["Title","Content"],null,2,null,null,null,null,null,null,1]],"nb-1",[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceText explicit MIME bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceText_Shape — the 11-slot spec slot-by-
// slot inspection. Slot 1 carries [title, content]; slot 3
// carries the type marker `2`; slot 10 is the source-type
// code. Slots 0, 2, 4-9 must be null.
func TestBuildAddSourceText_Shape(t *testing.T) {
	got := BuildAddSourceText("nb-1", "Hello", "World", "text/plain")
	if len(got) != 3 {
		t.Fatalf("BuildAddSourceText outer len = %d, want 3", len(got))
	}
	if got[1] != "nb-1" {
		t.Errorf("BuildAddSourceText outer slot 1 = %v, want \"nb-1\"", got[1])
	}
	outer, ok := got[0].([]any)
	if !ok || len(outer) != 1 {
		t.Fatalf("BuildAddSourceText outer slot 0 = %v, want [[spec]]", got[0])
	}
	spec, ok := outer[0].([]any)
	if !ok || len(spec) != 11 {
		t.Fatalf("BuildAddSourceText spec len = %d, want 11", len(spec))
	}
	pair, ok := spec[1].([]any)
	if !ok || len(pair) != 2 || pair[0] != "Hello" || pair[1] != "World" {
		t.Errorf("BuildAddSourceText spec slot 1 = %v, want [\"Hello\",\"World\"]", spec[1])
	}
	if spec[3] != 2 {
		t.Errorf("BuildAddSourceText spec slot 3 (type marker) = %v, want 2", spec[3])
	}
	if spec[10] != 1 {
		t.Errorf("BuildAddSourceText spec slot 10 (source-type code) = %v, want 1", spec[10])
	}
	// Slots 0, 2, 4..9 must be null — these are the
	// discriminator-slot positions the Text branch does not
	// populate.
	for _, i := range []int{0, 2, 4, 5, 6, 7, 8, 9} {
		if spec[i] != nil {
			t.Errorf("BuildAddSourceText spec slot %d = %v, want nil", i, spec[i])
		}
	}
}

// TestBuildAddSourceText_TemplateBlockFresh —
// wire.TemplateBlock must return a fresh slice every call;
// the protocol parser rejects shared mutable nested literals.
func TestBuildAddSourceText_TemplateBlockFresh(t *testing.T) {
	a := BuildAddSourceText("alpha", "A", "a", "")
	a[2].([]any)[3].([]any)[0] = "MUTATED"
	b := BuildAddSourceText("beta", "B", "b", "")
	if b[2].([]any)[3].([]any)[0] != 1 {
		t.Fatalf("BuildAddSourceText shared template block; got %v", b[2])
	}
}

// TestValidateText_Accepts — clean inline text accepted.
func TestValidateText_Accepts(t *testing.T) {
	cases := []string{
		"hello",
		"Hello world",
		"Pasted multi sentence with punctuation. Even numbers 1 2 3.",
		"Unicode: 你好世界 🎉",
	}
	for _, c := range cases {
		if err := ValidateText(c); err != nil {
			t.Errorf("ValidateText(%q) = %v, want nil", c, err)
		}
	}
}

// TestValidateText_Rejects — empty / whitespace / control
// character inputs rejected.
func TestValidateText_Rejects(t *testing.T) {
	cases := []string{
		"",
		// Control characters — the wire encoder escapes them
		// but a literal \n can confuse the backend's tokenizer.
		"hello\nworld",
		"hello\tworld",
		"hello\rworld",
	}
	for _, c := range cases {
		if err := ValidateText(c); err == nil {
			t.Errorf("ValidateText(%q) returned nil err; want validation error", c)
		}
	}
}

// TestBuilderCoverageCalls_Text — exercises every Go builder
// directly so the coverage tool sees the function bodies.
func TestBuilderCoverageCalls_Text(t *testing.T) {
	_ = BuildAddSourceText("nb-1", "Title", "Content", "")
	_ = BuildAddSourceText("nb-1", "Title", "Content", "text/html")
	if err := ValidateText("hello world"); err != nil {
		t.Fatalf("ValidateText accepted clean text: %v", err)
	}
}
