// Tests for the notebook params builders.
//
// Two layers:
//  1. TestBuilders_GoldenBytes — byte-exact comparison against the
//     committed testdata/golden.json. Generated from the Python
//     originals by testdata/gengolden.py. This is the wire-shape
//     contract; every builder's output must match.
//  2. TestBuilders_ShapeGuards — pure-shape sanity checks that don't
//     need a Python reference: the position table, the trailing-tag
//     rule, and the slot-depth invariants.
package params

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// fixture mirrors the JSON shape of testdata/golden.json. Loaded once
// per test from disk so a reviewer can hand-edit the fixture for a
// targeted re-baseline without recompiling.
type fixture struct {
	Name     string          `json:"name"`
	Method   string          `json:"method"`
	RPCID    string          `json:"rpc_id"`
	Params   json.RawMessage `json:"params"`
	Expected string          `json:"expected"`
}

// loadFixture reads testdata/golden.json and returns the per-builder
// entries. The testdata path is computed relative to the test file's
// directory so the test runs from any cwd.
func loadFixture(t *testing.T) []fixture {
	t.Helper()
	path := filepath.Join("testdata", "golden.json")
	data, err := os.ReadFile(path) //nolint:gosec // G304: test fixture path
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var doc struct {
		Builders []fixture `json:"builders"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	return doc.Builders
}

// TestBuilders_GoldenBytes walks the golden table and asserts every
// builder's EncodeRequest output matches the committed expected bytes.
// The expected bytes were generated from the Python originals — any
// drift surfaces here as a test failure with the diff in the message.
func TestBuilders_GoldenBytes(t *testing.T) {
	rows := loadFixture(t)
	if len(rows) == 0 {
		t.Fatal("golden fixture is empty; regenerate via gengolden.py")
	}

	// Dispatch by fixture name. Keeping the switch here (rather than
	// passing the params value through a generic) is intentional: the
	// fixture name is the contract — if a builder's name changes, the
	// test must change too.
	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			var paramsValue any
			if len(row.Params) > 0 {
				if err := json.Unmarshal(row.Params, &paramsValue); err != nil {
					t.Fatalf("decode params: %v", err)
				}
			}
			got, err := wire.EncodeRequest(wire.Method(row.Method), paramsValue, row.RPCID)
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}
			if string(got) != row.Expected {
				t.Fatalf("EncodeRequest %s: bytes differ\n got: %q\nwant: %q",
					row.Name, got, row.Expected)
			}
		})
	}
}

// TestBuilders_AllExercised asserts the fixture table covers every
// builder function in this package. If a future contributor adds a
// builder but forgets the fixture entry, this test fails first.
func TestBuilders_AllExercised(t *testing.T) {
	rows := loadFixture(t)
	exercised := make(map[string]bool, len(rows))
	for _, r := range rows {
		exercised[r.Name] = true
	}
	// Each builder below must appear in the fixture under its literal
	// name. Aliases (BuildUnshare == BuildShare restricted) appear as
	// separate fixtures so a reviewer can see the bytes explicitly.
	required := []string{
		"BuildList",
		"BuildCreate",
		"BuildDelete",
		"BuildRename_title_only",
		"BuildRename_title_emoji",
		"BuildRename_emoji_only",
		"BuildGet",
		"BuildSummary",
		"BuildShare_public",
		"BuildShare_restricted",
		"BuildUnshare",
		"BuildGetShareStatus",
		"BuildRemoveCollaborator",
		"BuildSetShareAccess_full",
		"BuildSetShareAccess_chat",
		"BuildRemoveRecentlyViewed",
	}
	for _, name := range required {
		if !exercised[name] {
			t.Errorf("golden fixture missing %s — regenerate via gengolden.py", name)
		}
	}
}

// TestBuildList_Shape — pure-Go shape guard for the list payload.
// Doesn't need a Python reference; just pins the four-element shape
// and the trailing [2] tag.
func TestBuildList_Shape(t *testing.T) {
	got := BuildList()
	if len(got) != 4 {
		t.Fatalf("BuildList len = %d, want 4", len(got))
	}
	// slot 3 is the variant tag — must be [2], not the TPL.
	tag, ok := got[3].([]any)
	if !ok || len(tag) != 1 || tag[0] != 2 {
		t.Fatalf("BuildList variant tag = %v, want [2]", got[3])
	}
}

// TestBuildCreate_TemplateBlockFresh — wire.TemplateBlock must return
// a fresh slice every call; the protocol parser rejects shared mutable
// nested literals. We mutate one returned slice and assert the next
// call is unaffected.
func TestBuildCreate_TemplateBlockFresh(t *testing.T) {
	a := BuildCreate("alpha")
	a[3].([]any)[3].([]any)[0] = "MUTATED"
	b := BuildCreate("beta")
	if b[3].([]any)[3].([]any)[0] != 1 {
		t.Fatalf("BuildCreate shared template block; got %v", b[3])
	}
}

// TestBuildDelete_SingleID — the builder must reject batched shapes
// silently (it accepts any string), but the wire shape must be
// [[notebook_id], [2]]. Verifies the inline literal.
func TestBuildDelete_SingleID(t *testing.T) {
	got := BuildDelete("nb-1")
	if len(got) != 2 {
		t.Fatalf("BuildDelete len = %d, want 2", len(got))
	}
	ids, ok := got[0].([]any)
	if !ok || len(ids) != 1 || ids[0] != "nb-1" {
		t.Fatalf("BuildDelete ids slot = %v, want [[\"nb-1\"]]", got[0])
	}
	tag, ok := got[1].([]any)
	if !ok || len(tag) != 1 || tag[0] != 2 {
		t.Fatalf("BuildDelete tag slot = %v, want [2]", got[1])
	}
}

// TestBuildRename_TitleOnly — the emoji slot must be omitted (not
// present) for a title-only mutation so the wire shape matches its
// recorded cassette.
func TestBuildRename_TitleOnly(t *testing.T) {
	got := BuildRename("nb-1", "New Title", nil)
	// [notebook_id, [[null, null, null, change_property]]]
	changeProperty := got[1].([]any)[0].([]any)[3].([]any)
	if len(changeProperty) != 2 {
		t.Fatalf("BuildRename title-only change_property len = %d, want 2 (slot 0=null, slot 1=title)", len(changeProperty))
	}
	if changeProperty[0] != nil {
		t.Fatalf("BuildRename title-only slot 0 = %v, want null", changeProperty[0])
	}
	if changeProperty[1] != "New Title" {
		t.Fatalf("BuildRename title-only slot 1 = %v, want New Title", changeProperty[1])
	}
}

// TestBuildRename_TitleAndEmoji — when emoji is supplied, the slot
// rides at index 2 of the change_property block.
func TestBuildRename_TitleAndEmoji(t *testing.T) {
	emoji := "📓"
	got := BuildRename("nb-1", "New Title", &emoji)
	changeProperty := got[1].([]any)[0].([]any)[3].([]any)
	if len(changeProperty) != 3 {
		t.Fatalf("BuildRename title+emoji change_property len = %d, want 3", len(changeProperty))
	}
	if changeProperty[2] != emoji {
		t.Fatalf("BuildRename title+emoji slot 2 = %v, want %s", changeProperty[2], emoji)
	}
}

// TestBuildRename_EmojiOnly — when only emoji changes, slot 1 is null
// (the title slot). This matches the wire shape for an emoji-only
// mutation.
func TestBuildRename_EmojiOnly(t *testing.T) {
	emoji := "📓"
	got := BuildRename("nb-1", "", &emoji)
	changeProperty := got[1].([]any)[0].([]any)[3].([]any)
	if len(changeProperty) != 3 {
		t.Fatalf("BuildRename emoji-only change_property len = %d, want 3", len(changeProperty))
	}
	if changeProperty[1] != "" {
		t.Fatalf("BuildRename emoji-only slot 1 = %v, want empty string", changeProperty[1])
	}
	if changeProperty[2] != emoji {
		t.Fatalf("BuildRename emoji-only slot 2 = %v, want %s", changeProperty[2], emoji)
	}
}

// TestBuildGet_TrailingZeros — the trailing [None, 0] of the GET
// payload is load-bearing; the backend discriminates on it.
func TestBuildGet_TrailingZeros(t *testing.T) {
	got := BuildGet("nb-1")
	if len(got) != 5 {
		t.Fatalf("BuildGet len = %d, want 5", len(got))
	}
	if got[0] != "nb-1" {
		t.Fatalf("BuildGet slot 0 = %v, want nb-1", got[0])
	}
	if got[1] != nil {
		t.Fatalf("BuildGet slot 1 = %v, want null", got[1])
	}
	if got[3] != nil {
		t.Fatalf("BuildGet slot 3 = %v, want null", got[3])
	}
	if got[4] != 0 {
		t.Fatalf("BuildGet slot 4 = %v, want 0 (int)", got[4])
	}
}

// TestBuildShare_AccessFlagSemantics — the access flag rides in two
// slots of the inner envelope, plus the public-flag at slot 1 of the
// outer list. We pin each one independently.
func TestBuildShare_AccessFlagSemantics(t *testing.T) {
	// Public path: access=1, public flag=1.
	public := BuildShare("nb-1", true)
	if public[1] != 1 {
		t.Fatalf("BuildShare public flag = %v, want 1", public[1])
	}
	if public[0].([]any)[0].([]any)[2].([]any)[0] != 1 {
		t.Fatalf("BuildShare public access = %v, want [1]", public[0].([]any)[0].([]any)[2])
	}
	// Restricted path: access=0, public flag=1 (still 1 — the slot is
	// always 1 for the write RPC; access 0 flips the visibility).
	restricted := BuildShare("nb-1", false)
	if restricted[1] != 1 {
		t.Fatalf("BuildShare restricted flag = %v, want 1 (write flag, not visibility)", restricted[1])
	}
	if restricted[0].([]any)[0].([]any)[2].([]any)[0] != 0 {
		t.Fatalf("BuildShare restricted access = %v, want [0]", restricted[0].([]any)[0].([]any)[2])
	}
}

// TestBuildRemoveCollaborator_NotifyFalse — the singular-removal path
// sends notify=0, not the grant path's notify=1. Pin it explicitly.
func TestBuildRemoveCollaborator_NotifyFalse(t *testing.T) {
	got := BuildRemoveCollaborator("nb-1", "alice@example.com")
	if got[1] != 0 {
		t.Fatalf("BuildRemoveCollaborator notify = %v, want 0", got[1])
	}
	// Email rides at slot [0][0][1][0][0].
	email := got[0].([]any)[0].([]any)[1].([]any)[0].([]any)[0]
	if email != "alice@example.com" {
		t.Fatalf("BuildRemoveCollaborator email = %v, want alice@example.com", email)
	}
	// permission rides at slot [0][0][1][0][2] = 4 (the _REMOVE sentinel).
	permission := got[0].([]any)[0].([]any)[1].([]any)[0].([]any)[2]
	if permission != SharePermissionRemove {
		t.Fatalf("BuildRemoveCollaborator permission = %v, want %d (_REMOVE)", permission, SharePermissionRemove)
	}
}

// TestBuildSetShareAccess_SlotDepth — the view-level slot rides at
// depth 8 inside the inner envelope (one nesting level deeper than
// the rename path's slot 2). Pin it explicitly.
func TestBuildSetShareAccess_SlotDepth(t *testing.T) {
	got := BuildSetShareAccess("nb-1", ShareViewLevelChatOnly)
	// Path: [notebookID, [[null×8, [[level]]]]]
	level := got[1].([]any)[0].([]any)[8].([]any)[0].([]any)[0]
	if level != ShareViewLevelChatOnly {
		t.Fatalf("BuildSetShareAccess level = %v, want %d", level, ShareViewLevelChatOnly)
	}
}

// TestEscapeEmail_Rejects — the helper rejects empty / whitespace /
// @-less inputs. The backend silently drops a malformed address (per
// #2130), so we reject before dispatch.
func TestEscapeEmail_Rejects(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"no-at-sign",
		"alice@example.com\n",
		"\talice@example.com",
	}
	for _, c := range cases {
		if err := EscapeEmail(c); err == nil {
			t.Errorf("EscapeEmail(%q) returned nil err; want validation error", c)
		}
	}
	if err := EscapeEmail("alice@example.com"); err != nil {
		t.Errorf("EscapeEmail(alice@example.com) returned %v; want nil", err)
	}
}

// TestEncode_RoundTrip — Encode is the convenience wrapper around
// wire.Marshal. Spot-check one builder end-to-end.
func TestEncode_RoundTrip(t *testing.T) {
	got, err := Encode(BuildList)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `[null,1,null,[2]]`
	if string(got) != want {
		t.Fatalf("Encode(BuildList) = %q, want %q", got, want)
	}
}

// TestEncodeNoHTMLEscape — the byte output must not HTML-escape the
// `<`, `>`, or `&` characters (per AGENTS.md rule 3). Pin a builder
// that uses an emoji to assert the UTF-8 round-trip.
func TestEncodeNoHTMLEscape(t *testing.T) {
	emoji := "<&>📓"
	got, err := Encode(func() []any { return BuildRename("nb-1", "", &emoji) })
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// The HTML-escape form would be \u003c, \u0026, \u003e; we must
	// not see those sequences in the bytes.
	for _, esc := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(string(got), esc) {
			t.Errorf("Encode output contains HTML-escape sequence %s: %s", esc, got)
		}
	}
}

// TestBuilderCoverageCalls — exercises every Go builder directly so the
// coverage tool sees the function bodies. The golden-bytes table
// already pins the wire-shape contract; this test only needs to touch
// each builder once.
func TestBuilderCoverageCalls(t *testing.T) {
	emoji := "📓"
	// Each call must not panic. We discard the return value; the
	// golden table asserts the wire shape.
	_ = BuildList()
	_ = BuildCreate("title")
	_ = BuildDelete("nb-1")
	_ = BuildRename("nb-1", "New Title", &emoji)
	_ = BuildRename("nb-1", "Title Only", nil)
	_ = BuildRename("nb-1", "", &emoji)
	_ = BuildGet("nb-1")
	_ = BuildSummary("nb-1")
	_ = BuildMetadata("nb-1")
	_ = BuildShare("nb-1", true)
	_ = BuildShare("nb-1", false)
	_ = BuildUnshare("nb-1")
	_ = BuildGetShareStatus("nb-1")
	_ = BuildRemoveCollaborator("nb-1", "alice@example.com")
	_ = BuildSetShareAccess("nb-1", ShareViewLevelChatOnly)
	_ = BuildSetShareAccess("nb-1", ShareViewLevelFullNotebook)
	_ = BuildRemoveRecentlyViewed("nb-1")
	_ = BuildGetRecent()
}

// TestBuilderPanicStubs — the speculative builders (AddSource,
// RemoveSource, GetStarred, GetSharedWithMe, GetByProject) panic until
// T-P6-2 / T-P5-5 wire the upstream Python originals. The panic is
// the contract; we pin it here so a silent regression (e.g. someone
// swapping the panic for a "TODO: implement" that returns nil) is
// caught by the suite.
func TestBuilderPanicStubs(t *testing.T) {
	cases := []struct {
		name    string
		call    func()
		wantMsg string
	}{
		{"BuildAddSource", func() { _ = BuildAddSource("nb-1", []string{"url"}) }, "T-P6-2"},
		{"BuildRemoveSource", func() { _ = BuildRemoveSource("nb-1", []string{"src-1"}) }, "T-P6-2"},
		{"BuildGetStarred", func() { _ = BuildGetStarred() }, "T-P5-5"},
		{"BuildGetSharedWithMe", func() { _ = BuildGetSharedWithMe() }, "T-P5-5"},
		{"BuildGetByProject", func() { _ = BuildGetByProject("proj-1") }, "T-P5-5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s did not panic; expected TODO marker", c.name)
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("%s panic value = %v, want string", c.name, r)
				}
				if !strings.Contains(msg, c.wantMsg) {
					t.Errorf("%s panic message = %q, want substring %q", c.name, msg, c.wantMsg)
				}
			}()
			c.call()
		})
	}
}

// TestBuildSummary_Shape — pin the two-element trailing-tag shape
// directly (golden table covers bytes; this covers slot positions).
func TestBuildSummary_Shape(t *testing.T) {
	got := BuildSummary("nb-1")
	if len(got) != 2 || got[0] != "nb-1" {
		t.Fatalf("BuildSummary = %v, want [\"nb-1\", [2]]", got)
	}
	tag, ok := got[1].([]any)
	if !ok || len(tag) != 1 || tag[0] != 2 {
		t.Fatalf("BuildSummary tag slot = %v, want [2]", got[1])
	}
}

// TestBuildGetShareStatus_Shape — same shape guard for JFMDGd.
func TestBuildGetShareStatus_Shape(t *testing.T) {
	got := BuildGetShareStatus("nb-1")
	if len(got) != 2 || got[0] != "nb-1" {
		t.Fatalf("BuildGetShareStatus = %v, want [\"nb-1\", [2]]", got)
	}
}

// TestBuildUnshare_IsShareRestricted — the alias routes through
// BuildShare with public=false, so the slot-2 access must be
// RESTRICTED (0).
func TestBuildUnshare_IsShareRestricted(t *testing.T) {
	got := BuildUnshare("nb-1")
	row := got[0].([]any)[0].([]any)
	if access := row[2].([]any)[0]; access != ShareAccessRestricted {
		t.Errorf("BuildUnshare access = %v, want %d (RESTRICTED)", access, ShareAccessRestricted)
	}
}

// TestBuildGetRecent_EqualsBuildList — the recent RPC is currently the
// same payload as the list RPC; pin the alias so a future divergence
// surfaces here first.
func TestBuildGetRecent_EqualsBuildList(t *testing.T) {
	a, err := Encode(BuildGetRecent)
	if err != nil {
		t.Fatalf("Encode(BuildGetRecent): %v", err)
	}
	b, err := Encode(BuildList)
	if err != nil {
		t.Fatalf("Encode(BuildList): %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("BuildGetRecent bytes = %s, want equal to BuildList bytes %s", a, b)
	}
}

// TestBuildMetadata_EqualsBuildGet — same wire shape, different name.
func TestBuildMetadata_EqualsBuildGet(t *testing.T) {
	// BuildMetadata takes one arg; use the closure form.
	a, err := Encode(func() []any { return BuildMetadata("nb-1") })
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := Encode(func() []any { return BuildGet("nb-1") })
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("BuildMetadata bytes = %s, want equal to BuildGet bytes %s", a, b)
	}
}

// TestEscapeEmail_Accepts — counterpart to TestEscapeEmail_Rejects:
// pin a few inputs that pass validation.
func TestEscapeEmail_Accepts(t *testing.T) {
	cases := []string{
		"alice@example.com",
		"a.b+c@sub.example.co.uk",
		"user_name@domain.org",
	}
	for _, c := range cases {
		if err := EscapeEmail(c); err != nil {
			t.Errorf("EscapeEmail(%q) = %v, want nil", c, err)
		}
	}
}
