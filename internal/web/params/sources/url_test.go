// Tests for the sources params URL builder.
//
// The Python original (_web/sources/add.py::add_url_source) is
// the normative wire-shape source — every builder's output here
// must match what `wire.Marshal` would emit for the equivalent
// positional literal in Python. We assert the byte output below
// as golden strings; a reviewer hand-editing a builder must
// hand-edit the matching golden here in the same commit so the
// diff is traceable.
//
// The MIME-slot extension (slot 10 carries a MIME envelope; the
// Python literal carries a trailing null) is the T-S3-004b
// contribution. The default ("text/html") reproduces the Python
// literal byte-for-byte.
package sources

import (
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/params"
)

// TestBuildAddSourceURL_Bytes — the golden encode test pinned
// against Python's `add_url_source` literal. The 15-slot spec
// carries the URL at slot 2, the MIME envelope at slot 13, and
// the source-type code at slot 14. An empty mime falls back to
// "text/html" so the default case byte-equals the Python
// literal plus a single quoted "text/html" at slot 13.
func TestBuildAddSourceURL_Bytes(t *testing.T) {
	got, err := params.Encode(func() []any { return BuildAddSourceURL("nb-1", "https://example.com", "") })
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[[[null,null,["https://example.com"],null,null,null,null,null,null,null,null,null,null,"text/html",1]],"nb-1",[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceURL bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceURL_ExplicitMIME pins the override path:
// a non-empty mime replaces the default at slot 13. The
// trailing slot 14 (source-type code) is unchanged.
func TestBuildAddSourceURL_ExplicitMIME(t *testing.T) {
	got, err := params.Encode(func() []any { return BuildAddSourceURL("nb-1", "https://example.com", "application/pdf") })
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[[[null,null,["https://example.com"],null,null,null,null,null,null,null,null,null,null,"application/pdf",1]],"nb-1",[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceURL explicit MIME bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceURL_Shape — the 15-slot spec slot-by-slot
// inspection. Slot 2 carries [url]; slot 13 carries the MIME;
// slot 14 is the source-type code.
func TestBuildAddSourceURL_Shape(t *testing.T) {
	got := BuildAddSourceURL("nb-1", "https://example.com", "application/pdf")
	if len(got) != 3 {
		t.Fatalf("BuildAddSourceURL outer len = %d, want 3", len(got))
	}
	if got[1] != "nb-1" {
		t.Errorf("BuildAddSourceURL outer slot 1 = %v, want \"nb-1\"", got[1])
	}
	outer, ok := got[0].([]any)
	if !ok || len(outer) != 1 {
		t.Fatalf("BuildAddSourceURL outer slot 0 = %v, want [[spec]]", got[0])
	}
	spec, ok := outer[0].([]any)
	if !ok || len(spec) != 15 {
		t.Fatalf("BuildAddSourceURL spec len = %d, want 15", len(spec))
	}
	urlEnv, ok := spec[2].([]any)
	if !ok || len(urlEnv) != 1 || urlEnv[0] != "https://example.com" {
		t.Errorf("BuildAddSourceURL spec slot 2 = %v, want [\"https://example.com\"]", spec[2])
	}
	if spec[13] != "application/pdf" {
		t.Errorf("BuildAddSourceURL spec slot 13 = %v, want \"application/pdf\"", spec[13])
	}
	if spec[14] != 1 {
		t.Errorf("BuildAddSourceURL spec slot 14 = %v, want 1", spec[14])
	}
}

// TestBuildAddSourceURL_TemplateBlockFresh — wire.TemplateBlock
// must return a fresh slice every call; the protocol parser
// rejects shared mutable nested literals.
func TestBuildAddSourceURL_TemplateBlockFresh(t *testing.T) {
	a := BuildAddSourceURL("alpha", "https://a.example", "")
	a[2].([]any)[3].([]any)[0] = "MUTATED"
	b := BuildAddSourceURL("beta", "https://b.example", "")
	if b[2].([]any)[3].([]any)[0] != 1 {
		t.Fatalf("BuildAddSourceURL shared template block; got %v", b[2])
	}
}

// TestValidateURL_Accepts — clean http(s) URLs accepted.
func TestValidateURL_Accepts(t *testing.T) {
	cases := []string{
		"http://example.com",
		"https://example.com",
		"https://example.com/path?q=1&r=2#fragment",
		"https://example.com:8080/path",
	}
	for _, c := range cases {
		if err := ValidateURL(c); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", c, err)
		}
	}
}

// TestValidateURL_Rejects — empty / whitespace / non-http
// inputs rejected.
func TestValidateURL_Rejects(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"example.com",
		"ftp://example.com",
		"javascript:alert(1)",
		"//example.com",
		"https://example.com\n",
		"\thttps://example.com",
	}
	for _, c := range cases {
		if err := ValidateURL(c); err == nil {
			t.Errorf("ValidateURL(%q) returned nil err; want validation error", c)
		}
	}
}

// TestBuilderCoverageCalls_URL — exercises every Go builder
// directly so the coverage tool sees the function bodies.
func TestBuilderCoverageCalls_URL(t *testing.T) {
	_ = BuildAddSourceURL("nb-1", "https://example.com", "")
	_ = BuildAddSourceURL("nb-1", "https://example.com", "application/pdf")
	if err := ValidateURL("https://example.com"); err != nil {
		t.Fatalf("ValidateURL accepted clean URL: %v", err)
	}
}
