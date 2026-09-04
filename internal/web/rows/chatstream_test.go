// Tests for the chat streaming parser.
//
// The Python original is normative; the test cases below mirror its
// fixtures: a plain-answer run, a cited run (with one citation), a
// multi-chunk stream that exercises the longest-marked-answer selection,
// a mid-stream "er" error frame, and a too-long question that the
// server rejected (the bare [3] INVALID_ARGUMENT shape, issue #1472).
package rows

import (
	"errors"
	"strings"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// buildWrbFrame returns the JSON string for one ["wrb.fr", nil,
// innerJSON, ...] frame. `inner` is the JSON-encoded inner payload
// (a list with [0] = answer record, [4] = isFinalResponse flag).
func buildWrbFrame(innerJSON string) string {
	fr := []any{"wrb.fr", nil, innerJSON}
	b, _ := wire.Marshal(fr)
	return string(b)
}

// buildChunkBody wraps one or more wrb.fr frames in a chunked body.
// Each frame is decoded back to []any and re-marshaled as part of a
// top-level list of frames (the wire shape: one chunked line carries
// one outer JSON list of frame entries).
func buildChunkBody(frames ...string) string {
	out := make([]any, 0, len(frames))
	for _, f := range frames {
		var fr []any
		if err := wire.Unmarshal([]byte(f), &fr); err != nil {
			continue
		}
		out = append(out, fr)
	}
	b, _ := wire.Marshal(out)
	return ")]}'\n" + string(b) + "\n"
}

// buildAnswerRecord returns the JSON string for the inner answer record
// (the [text, _, _, _, typeBlock] shape) wrapped in the envelope
// [record, _, _, _, isFinal].
func buildAnswerRecord(text string, isFinal bool, citations []any, turnKey []any) string {
	typeBlock := []any{nil, nil, nil, citations, 1}
	if turnKey == nil {
		turnKey = []any{"session-id"}
	}
	ansRec := []any{text, nil, turnKey, nil, typeBlock}
	inner := []any{ansRec, nil, nil, nil, isFinal}
	b, _ := wire.Marshal(inner)
	return string(b)
}

// plainAnswerBody builds a single-chunk streaming response with no
// citations. The answer carries the final-chunk marker.
func plainAnswerBody() string {
	inner := buildAnswerRecord("This is the answer.", true, nil, nil)
	fr := buildWrbFrame(inner)
	return buildChunkBody(fr)
}

// citedAnswerBody builds a streaming response with one citation whose
// detail block carries a 2-element fragment (two text blocks covering
// [100, 250)) and a relevance score.
func citedAnswerBody() string {
	detail := []any{
		nil,  // 0
		nil,  // 1
		0.85, // 2: score
		[]any{
			[]any{nil, 100, 250}, // 3: fragment range: [[None, start, end]]
		},
		[]any{
			[]any{
				[]any{100, 150, "First half of the cited fragment. "},
				[]any{150, 250, "Second half of the cited fragment."},
			},
		}, // 4: fragment.elements
		[]any{nil, "11111111-2222-3333-4444-555555555555"}, // 5: source id
	}
	citation := []any{
		[]any{"chunk-abc"}, // 0: chunk id block
		detail,             // 1: detail block
	}
	cites := []any{citation}
	inner := buildAnswerRecord("Here is the answer [1].", true, cites, nil)
	fr := buildWrbFrame(inner)
	return buildChunkBody(fr)
}

// multiChunkBody assembles two chunks on separate lines (the wire
// stream emits one wrb.fr frame per chunk). The parser should select
// the final-marked chunk's answer.
//
// Each line is the JSON-encoded list [[wrb.fr, ...]] — one frame per
// chunk. Wire framing often includes a byte-count header before each
// payload line; omitting it exercises the "bare line is a payload"
// branch of the parser (per doc 03).
func multiChunkBody() string {
	interim := buildAnswerRecord("Here is a partial answer.", false, nil, nil)
	final := buildAnswerRecord(
		"Here is a partial answer, and now it is complete with more detail.",
		true, nil, nil,
	)
	interimFr := buildWrbFrame(interim)
	finalFr := buildWrbFrame(final)
	// Wrap each frame in a single-frame list (the chunked payload
	// shape is [[frame1], [frame2], ...] per chunk).
	interimChunk := wrapFrameList(interimFr)
	finalChunk := wrapFrameList(finalFr)
	return ")]}'\n" + interimChunk + "\n" + finalChunk + "\n"
}

// wrapFrameList takes the JSON string of one frame and produces the
// JSON string of [[frame]] — a single-frame chunk payload.
func wrapFrameList(frame string) string {
	var fr []any
	if err := wire.Unmarshal([]byte(frame), &fr); err != nil {
		return ""
	}
	b, _ := wire.Marshal([]any{fr})
	return string(b)
}

// midStreamErrorBody emits a wrb.fr with null inner JSON followed by an
// "er" frame. The "er" frame is the documented error signal and
// raises ChatError.
func midStreamErrorBody() string {
	wrb := []any{"wrb.fr", nil, nil}
	er := []any{"er", "rpc-id-1", 7}
	b, _ := wire.Marshal([]any{wrb, er})
	return ")]}'\n" + string(b) + "\n"
}

// oversizedPromptBody simulates the server returning a null wrb.fr
// payload + a [3] INVALID_ARGUMENT status. Issue #1472.
func oversizedPromptBody() string {
	wrb := []any{
		"wrb.fr", nil, nil, nil, nil,
		[]any{3, nil, nil},
	}
	b, _ := wire.Marshal([]any{wrb})
	return ")]}'\n" + string(b) + "\n"
}

// rateLimitedBody simulates a UserDisplayableError rate-limit rejection.
func rateLimitedBody() string {
	wrb := []any{
		"wrb.fr", nil, nil, nil, nil,
		[]any{
			8,
			"Too many requests",
			[]any{
				[]any{"type.googleapis.com/google.rpc.UserDisplayableError", nil, nil},
			},
		},
	}
	b, _ := wire.Marshal([]any{wrb})
	return ")]}'\n" + string(b) + "\n"
}

// TestParseChatStream_PlainAnswer — a single-chunk answer without
// citations. The parser returns the assembled answer with no
// references and no error.
func TestParseChatStream_PlainAnswer(t *testing.T) {
	body := plainAnswerBody()
	r, err := ParseChatStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseChatStream: %v", err)
	}
	if r.Answer != "This is the answer." {
		t.Errorf("Answer = %q, want %q", r.Answer, "This is the answer.")
	}
	if len(r.References) != 0 {
		t.Errorf("References = %v, want empty", r.References)
	}
}

// TestParseChatStream_CitedAnswer — a single-chunk answer that carries
// one citation. The parser surfaces the source id, the cited text,
// and the source-side fragment range.
func TestParseChatStream_CitedAnswer(t *testing.T) {
	body := citedAnswerBody()
	r, err := ParseChatStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseChatStream: %v", err)
	}
	if r.Answer != "Here is the answer [1]." {
		t.Errorf("Answer = %q, want %q", r.Answer, "Here is the answer [1].")
	}
	if len(r.References) != 1 {
		t.Fatalf("References len = %d, want 1: %+v", len(r.References), r.References)
	}
	ref := r.References[0]
	if ref.SourceID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("SourceID = %q, want uuid", ref.SourceID)
	}
	if !strings.Contains(ref.CitedText, "First half") {
		t.Errorf("CitedText = %q, expected to contain \"First half\"", ref.CitedText)
	}
	if ref.ChunkID != "chunk-abc" {
		t.Errorf("ChunkID = %q, want chunk-abc", ref.ChunkID)
	}
	if ref.CitationNumber != 1 {
		t.Errorf("CitationNumber = %d, want 1", ref.CitationNumber)
	}
	if ref.FragmentStart != 100 || ref.FragmentEnd != 250 {
		t.Errorf("Fragment range = [%d, %d), want [100, 250)", ref.FragmentStart, ref.FragmentEnd)
	}
}

// TestParseChatStream_MultiChunkAssembly — two chunks: interim (shorter)
// and final (longer). The parser picks the final-marked answer.
func TestParseChatStream_MultiChunkAssembly(t *testing.T) {
	body := multiChunkBody()
	r, err := ParseChatStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseChatStream: %v", err)
	}
	if !strings.HasPrefix(r.Answer, "Here is a partial answer, and now") {
		t.Errorf("Answer = %q, expected the longer final-marked chunk", r.Answer)
	}
	if !strings.Contains(r.Answer, "complete with more detail") {
		t.Errorf("Answer = %q, expected to include the final-chunk tail", r.Answer)
	}
}

// TestParseChatStream_MidStreamError — an "er" frame mid-stream. The
// parser raises ChatError wrapping the offending frame.
func TestParseChatStream_MidStreamError(t *testing.T) {
	body := midStreamErrorBody()
	_, err := ParseChatStream(strings.NewReader(body))
	if err == nil {
		t.Fatalf("ParseChatStream on 'er' frame returned nil err")
	}
	if !strings.Contains(err.Error(), "error frame") {
		t.Errorf("error message = %q, expected to mention 'error frame'", err.Error())
	}
	var chatErr *ChatError
	if !errors.As(err, &chatErr) {
		t.Errorf("err is %T, want *ChatError", err)
	}
}

// TestParseChatStream_OversizedPrompt — a wrb.fr with null inner JSON
// and a [3] status. The parser raises ChatError mentioning the
// INVALID_ARGUMENT status.
func TestParseChatStream_OversizedPrompt(t *testing.T) {
	body := oversizedPromptBody()
	_, err := ParseChatStream(strings.NewReader(body))
	if err == nil {
		t.Fatalf("ParseChatStream on oversized-prompt response returned nil err")
	}
	if !strings.Contains(err.Error(), "rejected by the server") {
		t.Errorf("err = %q, expected to mention 'rejected by the server'", err.Error())
	}
}

// TestParseChatStream_RateLimited — a UserDisplayableError marker in
// the error payload triggers the rate-limit wording.
func TestParseChatStream_RateLimited(t *testing.T) {
	body := rateLimitedBody()
	_, err := ParseChatStream(strings.NewReader(body))
	if err == nil {
		t.Fatalf("ParseChatStream on rate-limited response returned nil err")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("err = %q, expected to mention 'rate limited'", err.Error())
	}
}

// TestParseChatStream_EmptyBody — a body with only the anti-XSSI
// prefix and no payload at all. The parser raises ChatResponseParseError.
func TestParseChatStream_EmptyBody(t *testing.T) {
	body := ")]}'\n"
	_, err := ParseChatStream(strings.NewReader(body))
	if err == nil {
		t.Fatalf("ParseChatStream on empty body returned nil err")
	}
	if !strings.Contains(err.Error(), "No parseable chunks") {
		t.Errorf("err = %q, expected ChatResponseParseError", err.Error())
	}
	var parseErr *ChatResponseParseError
	if !errors.As(err, &parseErr) {
		t.Errorf("err is %T, want *ChatResponseParseError", err)
	}
}

// TestParseChatStream_NilReader — a nil io.Reader is a programming
// error, not a parse failure.
func TestParseChatStream_NilReader(t *testing.T) {
	_, err := ParseChatStream(nil)
	if err == nil {
		t.Fatalf("ParseChatStream(nil) returned nil err")
	}
}

// TestParseChatStream_BytesInput — the byte-slice variant produces
// the same result as the io.Reader variant.
func TestParseChatStream_BytesInput(t *testing.T) {
	body := citedAnswerBody()
	r, err := ParseChatStreamBytes([]byte(body))
	if err != nil {
		t.Fatalf("ParseChatStreamBytes: %v", err)
	}
	if len(r.References) != 1 {
		t.Errorf("References len = %d, want 1", len(r.References))
	}
}

// TestParseChatStream_ReaderAndBytesAgree — the two entry points
// return identical ChatStreamResult values for the same input.
func TestParseChatStream_ReaderAndBytesAgree(t *testing.T) {
	body := plainAnswerBody()
	rA, errA := ParseChatStream(strings.NewReader(body))
	rB, errB := ParseChatStreamBytes([]byte(body))
	if (errA == nil) != (errB == nil) {
		t.Fatalf("err mismatch: %v vs %v", errA, errB)
	}
	if errA == nil && rA.Answer != rB.Answer {
		t.Errorf("Answer differs: %q vs %q", rA.Answer, rB.Answer)
	}
}

// TestStripAntiXSSI — the prefix-stripping behavior the chat wire
// relies on. Pinned here so a future "fix" to skip the prefix gets
// caught.
func TestStripAntiXSSI(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain prefix LF", ")]}'\nhi", "hi"},
		{"plain prefix CRLF", ")]}'\r\nhi", "hi"},
		{"plain prefix CR only", ")]}'\rhi", "hi"},
		{"no prefix", "hi", "hi"},
		{"empty", "", ""},
		{"exact prefix only", ")]}'", ""},
		{"exact prefix LF", ")]}'\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripAntiXSSI(c.in); got != c.want {
				t.Errorf("stripAntiXSSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestUUIDPattern — the regex the citation source-id extractor applies
// to every string it sees.
func TestUUIDPattern(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"11111111-2222-3333-4444-555555555555", true},
		{"ABCDEF01-2345-6789-ABCD-EF0123456789", true},
		{"not-a-uuid", false},
		{"11111111-2222-3333-4444", false},
		{"", false},
	}
	for _, c := range cases {
		if got := uuidPattern.MatchString(c.in); got != c.want {
			t.Errorf("uuidPattern.MatchString(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestExtractUUIDFromNested — the recursive walker the parser uses
// when source-id data is buried in a nested list.
func TestExtractUUIDFromNested(t *testing.T) {
	got := extractUUIDFromNested(
		[]any{nil, []any{"11111111-2222-3333-4444-555555555555"}, "noise"},
		5,
	)
	if got != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("extractUUIDFromNested = %q, want uuid", got)
	}
	if extractUUIDFromNested(nil, 5) != "" {
		t.Errorf("nil input should return empty")
	}
	if extractUUIDFromNested("not-a-uuid", 5) != "" {
		t.Errorf("non-UUID string should return empty")
	}
}

// TestAssignCitationNumbers_DenseFill — when a ref has no wire ordinal
// it gets filled in to match its 1-based position.
func TestAssignCitationNumbers_DenseFill(t *testing.T) {
	refs := []ChatReference{
		{CitedText: "a"},
		{CitedText: "b", CitationNumber: 5}, // pre-set wire ordinal
		{CitedText: "c"},
	}
	out := assignCitationNumbers(refs)
	if out[0].CitationNumber != 1 {
		t.Errorf("refs[0].CitationNumber = %d, want 1", out[0].CitationNumber)
	}
	if out[1].CitationNumber != 5 {
		t.Errorf("refs[1].CitationNumber = %d, want 5 (preserved)", out[1].CitationNumber)
	}
	if out[2].CitationNumber != 3 {
		t.Errorf("refs[2].CitationNumber = %d, want 3 (dense-fill respects hole)", out[2].CitationNumber)
	}
}

// TestParseCitations_HoleInSequence — a missing detail row keeps the
// raw ordinals on surrounding rows rather than re-densifying.
func TestParseCitations_HoleInSequence(t *testing.T) {
	detail := []any{
		nil, nil, 0.5,
		[]any{[]any{nil, 0, 100}},                     // [3]: fragment range
		[]any{[]any{[]any{0, 50, "text"}}},            // [4]: fragment
		[]any{"22222222-3333-4444-5555-666666666666"}, // [5]: source id
	}
	bad := []any{"not-a-citation"} // malformed row, will be skipped
	good := []any{
		[]any{"chunk-1"},
		detail,
	}
	cites := []any{bad, good}
	refs := parseCitations(cites, nil)
	if len(refs) != 1 {
		t.Fatalf("refs len = %d, want 1 (bad row skipped)", len(refs))
	}
	if refs[0].CitationNumber != 2 {
		t.Errorf("refs[0].CitationNumber = %d, want 2 (raw wire ordinal preserved)", refs[0].CitationNumber)
	}
}

// TestParseChatStream_NoChunks_Garbage — a body of pure garbage
// (non-JSON noise) yields ChatResponseParseError.
func TestParseChatStream_NoChunks_Garbage(t *testing.T) {
	body := ")]}'\nthis-is-not-json\nand neither is this\n"
	_, err := ParseChatStream(strings.NewReader(body))
	if err == nil {
		t.Fatalf("ParseChatStream on garbage returned nil err")
	}
}

// TestParseChatStream_UTF8Answer — a non-ASCII answer passes through
// the parser unchanged (the chat wire format encodes UTF-8 verbatim
// via json.Marshal without HTML-escaping).
func TestParseChatStream_UTF8Answer(t *testing.T) {
	inner := buildAnswerRecord("Hello 你好 🎉", true, nil, nil)
	fr := buildWrbFrame(inner)
	body := buildChunkBody(fr)
	r, err := ParseChatStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseChatStream: %v", err)
	}
	if r.Answer != "Hello 你好 🎉" {
		t.Errorf("Answer = %q, want UTF-8 pass-through", r.Answer)
	}
}
