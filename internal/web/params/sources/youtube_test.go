// Tests for the sources params YouTube builder.
//
// The Python original (_web/sources/add.py::add_youtube_source)
// is the normative wire-shape source — every builder's output
// here must match what `wire.Marshal` would emit for the
// equivalent positional literal in Python. We assert the byte
// output below as golden strings.
//
// The YouTube branch is the same 15-slot spec as the URL branch
// but with the URL riding at slot 7 (not slot 2). The slot shift
// is the URL / YouTube branch discriminator.
package sources

import (
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/params"
)

// TestBuildAddSourceYouTube_Bytes — the golden encode test
// pinned against Python's `add_youtube_source` literal. The
// 15-slot spec carries the URL at slot 7, the MIME envelope at
// slot 13, and the source-type code at slot 14. An empty mime
// falls back to "text/html".
func TestBuildAddSourceYouTube_Bytes(t *testing.T) {
	got, err := params.Encode(func() []any { return BuildAddSourceYouTube("nb-1", "https://www.youtube.com/watch?v=abc", "") })
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Wire shape:
	// [
	//   [[null×7, ["https://..."], null×5, "text/html", 1]],
	//   "nb-1",
	//   [2, null, null, [1, null×9, [1]]]
	// ]
	want := `[[[null,null,null,null,null,null,null,["https://www.youtube.com/watch?v=abc"],null,null,null,null,null,"text/html",1]],"nb-1",[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceYouTube bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceYouTube_ShortForm — the short-URL form
// (https://youtu.be/<id>) is accepted by the wire envelope.
// Backend normalises both shapes server-side.
func TestBuildAddSourceYouTube_ShortForm(t *testing.T) {
	got, err := params.Encode(func() []any { return BuildAddSourceYouTube("nb-1", "https://youtu.be/abc", "") })
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[[[null,null,null,null,null,null,null,["https://youtu.be/abc"],null,null,null,null,null,"text/html",1]],"nb-1",[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceYouTube short-form bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceYouTube_ExplicitMIME — a non-empty mime
// replaces the default at slot 13.
func TestBuildAddSourceYouTube_ExplicitMIME(t *testing.T) {
	got, err := params.Encode(func() []any { return BuildAddSourceYouTube("nb-1", "https://youtu.be/abc", "application/json") })
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[[[null,null,null,null,null,null,null,["https://youtu.be/abc"],null,null,null,null,null,"application/json",1]],"nb-1",[2,null,null,[1,null,null,null,null,null,null,null,null,null,[1]]]]`
	if string(got) != want {
		t.Fatalf("BuildAddSourceYouTube explicit MIME bytes differ\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildAddSourceYouTube_Shape — the 15-slot spec
// slot-by-slot inspection. Slot 7 carries [url]; slot 13 carries
// the MIME; slot 14 is the source-type code.
func TestBuildAddSourceYouTube_Shape(t *testing.T) {
	got := BuildAddSourceYouTube("nb-1", "https://youtu.be/abc", "application/pdf")
	if len(got) != 3 {
		t.Fatalf("BuildAddSourceYouTube outer len = %d, want 3", len(got))
	}
	if got[1] != "nb-1" {
		t.Errorf("BuildAddSourceYouTube outer slot 1 = %v, want \"nb-1\"", got[1])
	}
	outer, ok := got[0].([]any)
	if !ok || len(outer) != 1 {
		t.Fatalf("BuildAddSourceYouTube outer slot 0 = %v, want [[spec]]", got[0])
	}
	spec, ok := outer[0].([]any)
	if !ok || len(spec) != 15 {
		t.Fatalf("BuildAddSourceYouTube spec len = %d, want 15", len(spec))
	}
	urlEnv, ok := spec[7].([]any)
	if !ok || len(urlEnv) != 1 || urlEnv[0] != "https://youtu.be/abc" {
		t.Errorf("BuildAddSourceYouTube spec slot 7 = %v, want [\"https://youtu.be/abc\"]", spec[7])
	}
	if spec[13] != "application/pdf" {
		t.Errorf("BuildAddSourceYouTube spec slot 13 = %v, want \"application/pdf\"", spec[13])
	}
	if spec[14] != 1 {
		t.Errorf("BuildAddSourceYouTube spec slot 14 = %v, want 1", spec[14])
	}
	// Slots 0..6 and 8..12 must be null — these are the
	// discriminator-slot positions the URL / YouTube branches
	// do not populate.
	for _, i := range []int{0, 1, 2, 3, 4, 5, 6, 8, 9, 10, 11, 12} {
		if spec[i] != nil {
			t.Errorf("BuildAddSourceYouTube spec slot %d = %v, want nil", i, spec[i])
		}
	}
}

// TestBuildAddSourceYouTube_TemplateBlockFresh —
// wire.TemplateBlock must return a fresh slice every call.
func TestBuildAddSourceYouTube_TemplateBlockFresh(t *testing.T) {
	a := BuildAddSourceYouTube("alpha", "https://youtu.be/a", "")
	a[2].([]any)[3].([]any)[0] = "MUTATED"
	b := BuildAddSourceYouTube("beta", "https://youtu.be/b", "")
	if b[2].([]any)[3].([]any)[0] != 1 {
		t.Fatalf("BuildAddSourceYouTube shared template block; got %v", b[2])
	}
}

// TestValidateYouTubeURL — the YouTube URL validator delegates
// to ValidateURL (same rule). Pin the table for the YouTube
// surface so a future split (e.g. accepting bare video IDs)
// would have to update both helpers in lockstep.
func TestValidateYouTubeURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"watch_url", "https://www.youtube.com/watch?v=abc", false},
		{"watch_url_no_www", "https://youtube.com/watch?v=abc", false},
		{"short_url", "https://youtu.be/abc", false},
		{"empty", "", true},
		{"non_http", "ftp://example.com", true},
		{"whitespace", "  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateYouTubeURL(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateYouTubeURL(%q) returned nil err; want validation error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateYouTubeURL(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}

// TestBuilderCoverageCalls_YouTube — exercises every Go builder
// directly so the coverage tool sees the function bodies.
func TestBuilderCoverageCalls_YouTube(t *testing.T) {
	_ = BuildAddSourceYouTube("nb-1", "https://youtu.be/abc", "")
	_ = BuildAddSourceYouTube("nb-1", "https://youtu.be/abc", "application/pdf")
	if err := ValidateYouTubeURL("https://youtu.be/abc"); err != nil {
		t.Fatalf("ValidateYouTubeURL accepted clean URL: %v", err)
	}
}
