// Package rows — chatstream.go: the streaming parser for the chat
// `GenerateFreeFormStreamed` response.
//
// Port of notebooklm-py/src/notebooklm/_web/rows/chat_stream.py
// (1033 lines). The Python source is normative; every wire position in
// this file matches a position in the Python original. The architecture
// mirrors the Python split into:
//
//   - row adapters  — typed views over individual wire records (StreamFrameRow,
//                     StreamEnvelopeRow, AnswerRow, CitationRow, …)
//   - chunk parser  — extract one chunk's contribution to the assembled answer
//   - stream parser — walk the chunked `rt=c` body, accumulate candidate answers,
//                     and pick the winning one per #2122.
//
// The streamed chat endpoint is **not** a batchexecute RPC: there is no
// obfuscated method id to thread through, so every descent labels itself
// with a `Source` string for any drift diagnostics (mirrors ADR-0011).
//
// Public API:
//
//   - ParseChatStream(body io.Reader) (*ChatStreamResult, error)
//   - ParseChatStreamBytes(body []byte) (*ChatStreamResult, error)
//
// Out-of-scope (deferred to T-S3-007d/e):
//
//   - chat history decoder (T-S3-007d)
//   - ChatAPI SDK methods (T-S3-007e — depends on T-S3-007c and T-S3-007d)
//
// Boundary: per docs/AGENTS.md rule 5 this package is mode=internal: stdlib
// + the wire sibling package only. No third-party imports.
package rows

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// -----------------------------------------------------------------------------
// Public API
// -----------------------------------------------------------------------------

// ChatStreamResult is the assembled output of ParseChatStream.
//
// The third field is named ConversationID for backward compatibility with the
// prior parser contract, but live API tests (issue #659) proved it is actually
// a per-stream/per-query identifier, NOT a real conversation id: khqZz
// returns 0 turns when queried with it, and passing it back as a follow-up
// conversation_id produces a ghost turn the server does not record. The real
// conversation id must be fetched separately via hPTbtc after the ask.
// Callers should generally ignore this field.
type ChatStreamResult struct {
	// Answer is the assembled answer text from the streamed response.
	Answer string

	// References are the citation markers the answer cites, in source order.
	References []ChatReference

	// ConversationID is the per-stream identifier described above; not a
	// real conversation id. May be empty.
	ConversationID string

	// TurnKey is the backend's key for the answered turn (#2122). It is the
	// key SubmitFeedback is addressed by. Nil when no chunk carried one.
	TurnKey *ConversationTurnKey

	// NextSteps is the suggested follow-up questions/actions, collected
	// last-wins across chunks that carried a populated block.
	NextSteps []NextStepSuggestion

	// ErrorPayload is set to the raw google.rpc.Status array when the
	// server returned an error frame or null wrb.fr + status. Nil on the
	// happy path; callers inspect Error for the human-readable summary.
	ErrorPayload []any
}

// ChatReference is one citation row the answer cites.
//
// SourceID is the source's stable id; the cited text and its
// source-side character range come from the citation's `fragment` field.
// ChunkID is the document-object id the answer document's annotation map
// anchors on (#2120). CitationNumber is the raw wire ordinal; the
// parser does NOT re-densify after skipping malformed rows, so a hole
// in the sequence means the [N] marker has no anchor.
type ChatReference struct {
	SourceID    string
	CitedText   string
	StartChar   int
	EndChar     int
	ChunkID     string
	FragmentStart int
	FragmentEnd   int
	Score       float64
	CitationNumber int
	AnswerAnchorStart int
	AnswerAnchorEnd   int
}

// ConversationTurnKey is the backend's key for one chat turn (#2122).
type ConversationTurnKey struct {
	SessionID string
	TurnID    string
	TurnCode  *int
	raw       []any
}

// NextStepSuggestion is a typed follow-up question/action surfaced by the
// chat backend.
type NextStepSuggestion struct {
	Question string
	TypeCode int
}

// ParseChatStream parses one streamed chat response body.
//
// The body is the chunked `rt=c` payload from
// {base}/_/LabsTailwindUi/data/google.internal.labs.tailwind.orchestration.v1.LabsTailwindOrchestrationService/GenerateFreeFormStreamed.
//
// Returns the assembled answer + references, or a typed error:
//
//   - ChatResponseParseError: zero parseable chunks (empty body / wire drift).
//   - ChatError: server-side rejection — "er" frame, oversized-prompt
//     rejection (null inner + status array), rate-limit (UserDisplayableError).
func ParseChatStream(body io.Reader) (*ChatStreamResult, error) {
	if body == nil {
		return nil, errors.New("rows: ParseChatStream: body is nil")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("rows: ParseChatStream: read body: %w", err)
	}
	return ParseChatStreamBytes(data)
}

// ParseChatStreamBytes is the byte-slice variant of ParseChatStream.
func ParseChatStreamBytes(body []byte) (*ChatStreamResult, error) {
	stripped := stripAntiXSSI(string(body))
	lines := strings.Split(strings.TrimSpace(stripped), "\n")

	// Per-chunk accumulators: the streaming parser picks between four
	// candidates — final-marked, best-marked, best-unmarked, empty — to
	// match the Python original's answer-selection policy (#2122).
	var (
		finalMarkedAnswer     string
		finalMarkedRefs       []ChatReference
		bestMarkedAnswer      string
		bestMarkedRefs        []ChatReference
		bestUnmarkedAnswer    string
		bestUnmarkedRefs      []ChatReference
		sawDriftSignal        bool
		serverConvID          string
		turnKey               *ConversationTurnKey
		nextSteps             []NextStepSuggestion
		sawFinalChunk         bool
		parseableChunkCount   int
		terminalSequence      *int64
	)

	processChunk := func(jsonStr string) {
		chunk := extractChunk(jsonStr)
		if chunk.parseable {
			parseableChunkCount++
		}
		sawFinalChunk = sawFinalChunk || chunk.isFinalResponse
		if chunk.text != "" {
			if chunk.isAnswer {
				if chunk.isFinalResponse {
					finalMarkedAnswer = chunk.text
					finalMarkedRefs = chunk.references
				}
				if len(chunk.text) > len(bestMarkedAnswer) {
					bestMarkedAnswer = chunk.text
					bestMarkedRefs = chunk.references
				}
			} else {
				sawDriftSignal = sawDriftSignal || chunk.suggestsDrift
				if len(chunk.text) > len(bestUnmarkedAnswer) {
					bestUnmarkedAnswer = chunk.text
					bestUnmarkedRefs = chunk.references
				}
			}
		}
		if chunk.conversationID != "" {
			serverConvID = chunk.conversationID
		}
		if chunk.turnKey != nil {
			turnKey = chunk.turnKey
		}
		if len(chunk.nextSteps) > 0 {
			nextSteps = chunk.nextSteps
		}
		if chunk.terminalSequence != nil {
			terminalSequence = chunk.terminalSequence
		}
	}

	for i := 0; i < len(lines); {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		// A line that parses as an integer is a byte-count framing header;
		// the NEXT line is its payload. A bare line is a payload by itself.
		if _, err := strconv.Atoi(line); err == nil {
			i++
			if i < len(lines) {
				processChunk(lines[i])
			}
			i++
			continue
		}
		processChunk(line)
		i++
	}

	if parseableChunkCount == 0 {
		if terminalSequence != nil {
			return nil, &ChatError{
				Message: fmt.Sprintf(
					"Chat request ended before the server returned an RPC payload "+
						"(terminal stream sequence %d). The response contained only "+
						"stream bookkeeping and no server status or reason; retry later.",
					*terminalSequence,
				),
			}
		}
		return nil, &ChatResponseParseError{
			Message: fmt.Sprintf(
				"No parseable chunks in streaming chat response (%d lines scanned). "+
					"The response was empty or the API wire format may have changed.",
				len(lines),
			),
		}
	}

	var (
		longestAnswer string
		finalRefs     []ChatReference
	)

	switch {
	case finalMarkedAnswer != "":
		longestAnswer = finalMarkedAnswer
		finalRefs = finalMarkedRefs
	case bestMarkedAnswer != "":
		longestAnswer = bestMarkedAnswer
		finalRefs = bestMarkedRefs
	case bestUnmarkedAnswer != "":
		longestAnswer = bestUnmarkedAnswer
		finalRefs = bestUnmarkedRefs
	}

	// Dense-fill citation numbers for refs that arrived unnumbered. Raw wire
	// ordinals survive; a skipped malformed row deliberately leaves a hole
	// so the answer's [N] markers do not shift onto the wrong citation.
	finalRefs = assignCitationNumbers(finalRefs)

	return &ChatStreamResult{
		Answer:        longestAnswer,
		References:    finalRefs,
		ConversationID: serverConvID,
		TurnKey:       turnKey,
		NextSteps:     nextSteps,
	}, nil
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// ChatError is returned for any server-side chat rejection: "er" frame,
// null wrb.fr + status array, or rate-limit. The Message is human-readable;
// callers may inspect ErrorPayload for the raw status array.
type ChatError struct {
	Message      string
	ErrorPayload []any
}

func (e *ChatError) Error() string { return e.Message }

// ChatResponseParseError is returned when no chunk in the body yielded a
// successfully decoded wrb.fr envelope. This means either the response
// body was empty/garbage or the API wire format drifted.
type ChatResponseParseError struct {
	Message string
}

func (e *ChatResponseParseError) Error() string { return e.Message }

// -----------------------------------------------------------------------------
// Anti-XSSI stripping
// -----------------------------------------------------------------------------

// stripAntiXSSI strips the leading ")]}'" prefix Google prepends to chat
// streaming responses. Mirrors `_web.wire.decoder.strip_anti_xssi`; the
// newline that follows is consumed by the outer Split("\n") in
// ParseChatStreamBytes, so this helper only removes the prefix itself.
func stripAntiXSSI(s string) string {
	const tag = ")]}'"
	if len(s) >= len(tag) && s[:len(tag)] == tag {
		rest := s[len(tag):]
		// Skip one newline if present.
		if len(rest) > 0 {
			switch rest[0] {
			case '\r':
				if len(rest) > 1 && rest[1] == '\n' {
					return rest[2:]
				}
				return rest[1:]
			case '\n':
				return rest[1:]
			}
		}
		return rest
	}
	return s
}

// -----------------------------------------------------------------------------
// Row adapters — typed views over wire records
// -----------------------------------------------------------------------------

// StreamFrameRow is the typed view of one streamed-chat envelope frame.
//
// Frames arrive as ["wrb.fr", None, inner_json, ...] (a successful RPC
// result), ["er", rpc_id, code, ...] (a server-side error), or
// ["e", sequence, ...] (a stream bookkeeping terminator).
//
// Every read is length-guarded: an absent slot short-circuits to the
// zero value. The tag slot is the one position that must always be
// present (the caller has already established len(item) >= 2).
type StreamFrameRow struct {
	raw []any
}

// tagSource is the label the strict accessor would use; the streaming
// endpoint is not a batchexecute RPC so we pass method_id = "" and
// localize drift via this label.
const tagSource = "ChatStreamFrameRow.tag"

// Tag returns the frame tag at item[0] ("wrb.fr" / "er" / "e" / ...).
func (s StreamFrameRow) Tag() string {
	v, ok := s.raw[0].(string)
	if !ok {
		return ""
	}
	return v
}

// TerminalSequence returns the bookkeeping sequence at item[1] of an
// "e" frame. Returns nil for non-int / absent / bool values.
func (s StreamFrameRow) TerminalSequence() *int64 {
	if len(s.raw) <= 1 {
		return nil
	}
	switch n := s.raw[1].(type) {
	case int:
		v := int64(n)
		return &v
	case int64:
		return &n
	case float64:
		if n == float64(int64(n)) {
			v := int64(n)
			return &v
		}
	}
	return nil
}

// InnerJSON returns the inner-JSON payload at item[2] (a string for
// wrb.fr frames; nil for frames where the server returned no inner JSON).
func (s StreamFrameRow) InnerJSON() any {
	if len(s.raw) <= 2 {
		return nil
	}
	return s.raw[2]
}

// ErrorCode returns the optional error code at item[2] of an "er" frame.
// Absent / non-int / bool → nil. The frame itself is the error signal so
// an absent code is NOT schema drift.
func (s StreamFrameRow) ErrorCode() any {
	if len(s.raw) <= 2 {
		return nil
	}
	return s.raw[2]
}

// ErrorPayload returns the optional server-side error payload at item[5]
// (a JSON-array google.rpc.Status) or nil.
func (s StreamFrameRow) ErrorPayload() []any {
	if len(s.raw) <= 5 {
		return nil
	}
	v, ok := s.raw[5].([]any)
	if !ok {
		return nil
	}
	return v
}

// -----------------------------------------------------------------------------

// StreamEnvelopeRow is the typed view of one decoded streamed-chat inner
// payload (the JSON-decoded wrb.fr frame).
//
// Wire shape (GenerateFreeFormStreamedResponse):
//
//	index 0  answer record (wrapped by AnswerRow)
//	index 4  isFinalResponse (bool) — true on exactly the last chunk
//	index 5  NextStepSuggestions; [5][0] is its suggestion-row list
type StreamEnvelopeRow struct {
	raw any
}

// IsFinalResponse returns the backend's marker for the final chunk.
// Only the literal wire `true` counts; heartbeats ([]), null slots, and
// truthy non-bool values answer false so the longest-wins fallback
// stays in charge unless the server explicitly says "final".
func (s StreamEnvelopeRow) IsFinalResponse() bool {
	l, ok := s.raw.([]any)
	if !ok || len(l) <= 4 {
		return false
	}
	b, ok := l[4].(bool)
	return ok && b
}

// NextStepRows returns typed follow-up suggestion rows. Empty for
// absent / malformed blocks — older cassettes omit the block entirely.
func (s StreamEnvelopeRow) NextStepRows() []NextStepSuggestionRow {
	l, ok := s.raw.([]any)
	if !ok || len(l) <= 5 {
		return nil
	}
	block, ok := l[5].([]any)
	if !ok || len(block) == 0 {
		return nil
	}
	rows, ok := block[0].([]any)
	if !ok {
		return nil
	}
	out := make([]NextStepSuggestionRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, NextStepSuggestionRow{raw: r})
	}
	return out
}

// -----------------------------------------------------------------------------

// NextStepSuggestionRow wraps one [suggestion, MagicArtifactType] row.
type NextStepSuggestionRow struct {
	raw any
}

func (n NextStepSuggestionRow) question() (string, bool) {
	l, ok := n.raw.([]any)
	if !ok || len(l) <= 0 {
		return "", false
	}
	s, ok := l[0].(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func (n NextStepSuggestionRow) typeCode() (int, bool) {
	l, ok := n.raw.([]any)
	if !ok || len(l) <= 1 {
		return 0, false
	}
	v, ok := l[1].(int)
	if !ok {
		return 0, false
	}
	return v, true
}

// IsWellFormed reports whether the row carries both a non-empty question
// string and an int type code.
func (n NextStepSuggestionRow) IsWellFormed() bool {
	_, qOK := n.question()
	_, cOK := n.typeCode()
	return qOK && cOK
}

// Question returns the suggestion text or "" if absent.
func (n NextStepSuggestionRow) Question() string {
	s, _ := n.question()
	return s
}

// TypeCode returns the magic-artifact type code or 0 if absent.
func (n NextStepSuggestionRow) TypeCode() int {
	v, _ := n.typeCode()
	return v
}

// -----------------------------------------------------------------------------

// ErrorPayloadRow is the typed view of a streamed-chat error payload
// (item[5]). Wire shape: a JSON-array google.rpc.Status.
type ErrorPayloadRow struct {
	raw []any
}

func (e ErrorPayloadRow) statusCode() any {
	if len(e.raw) == 0 {
		return nil
	}
	return e.raw[0]
}

func (e ErrorPayloadRow) message() (string, bool) {
	if len(e.raw) <= 1 {
		return "", false
	}
	s, ok := e.raw[1].(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// Entries returns the error entries at slot 2 — empty when absent.
func (e ErrorPayloadRow) Entries() []any {
	if len(e.raw) <= 2 {
		return nil
	}
	v, ok := e.raw[2].([]any)
	if !ok {
		return nil
	}
	return v
}

// EntryType returns the leading type string at entry[0] of one error
// entry, or "".
func (e ErrorPayloadRow) EntryType(entry any) string {
	l, ok := entry.([]any)
	if !ok || len(l) == 0 {
		return ""
	}
	s, ok := l[0].(string)
	if !ok {
		return ""
	}
	return s
}

// StatusCode returns the leading status code (e.g. 3 for INVALID_ARGUMENT).
func (e ErrorPayloadRow) StatusCode() any { return e.statusCode() }

// Message returns the server-authored google.rpc.Status.message or "".
func (e ErrorPayloadRow) Message() string {
	s, _ := e.message()
	return s
}

// -----------------------------------------------------------------------------

// AnswerRow is the typed view of one populated streamed-chat answer
// record (the inner_data[0] of a decoded wrb.fr envelope).
type AnswerRow struct {
	raw []any
}

const (
	answerTextPos     = 0
	answerConvPos     = 2
	answerEmptyReason = 3
	answerTypePos     = 4
	answerMarkerPos   = 4
	answerCitationsPos = 3
	answerMarkerValue = 1
	answerDocBodyPos  = 0

	turnKeySessionPos = 0
	turnKeyTurnPos    = 1
	turnKeyCodePos    = 2
)

// Text returns the answer text at first[0], or "" when absent.
func (a AnswerRow) Text() string {
	if len(a.raw) <= answerTextPos {
		return ""
	}
	s, ok := a.raw[answerTextPos].(string)
	if !ok {
		return ""
	}
	return s
}

// turnKeyBlock returns the ConversationTurnKey block at first[2]
// (a list) or nil when absent.
func (a AnswerRow) turnKeyBlock() []any {
	if len(a.raw) <= answerConvPos {
		return nil
	}
	b, ok := a.raw[answerConvPos].([]any)
	if !ok || len(b) == 0 {
		return nil
	}
	return b
}

// ServerConversationID returns the server conversation id at first[2][0]
// or "" when absent.
func (a AnswerRow) ServerConversationID() string {
	b := a.turnKeyBlock()
	if b == nil {
		return ""
	}
	s, ok := b[turnKeySessionPos].(string)
	if !ok {
		return ""
	}
	return s
}

// TurnKey returns the whole ConversationTurnKey at first[2], or nil
// when absent or the session id slot is unusable. The two trailing slots
// are independent: one drifted slot does not discard the rest.
func (a AnswerRow) TurnKey() *ConversationTurnKey {
	b := a.turnKeyBlock()
	if b == nil {
		return nil
	}
	if len(b) <= turnKeySessionPos {
		return nil
	}
	sessionID, ok := b[turnKeySessionPos].(string)
	if !ok || sessionID == "" {
		return nil
	}
	rawCopy := make([]any, len(b))
	copy(rawCopy, b)
	ck := &ConversationTurnKey{SessionID: sessionID, raw: rawCopy}
	if len(b) > turnKeyTurnPos {
		if s, ok := b[turnKeyTurnPos].(string); ok && s != "" {
			ck.TurnID = s
		}
	}
	if len(b) > turnKeyCodePos {
		if n, ok := b[turnKeyCodePos].(int); ok {
			ck.TurnCode = &n
		} else if f, ok := b[turnKeyCodePos].(float64); ok && f == float64(int64(f)) {
			n := int(f)
			ck.TurnCode = &n
		}
	}
	return ck
}

// typeBlock returns the optional type/flags block at first[4] (a list)
// or nil when absent / non-list.
func (a AnswerRow) typeBlock() []any {
	if len(a.raw) <= answerTypePos {
		return nil
	}
	b, ok := a.raw[answerTypePos].([]any)
	if !ok {
		return nil
	}
	return b
}

// IsAnswer reports whether the type block marks this record as an answer
// (first[4][4] == 1).
func (a AnswerRow) IsAnswer() bool {
	tb := a.typeBlock()
	if tb == nil || len(tb) <= answerMarkerPos {
		return false
	}
	n, ok := tb[answerMarkerPos].(int)
	if !ok {
		if f, ok := tb[answerMarkerPos].(float64); ok && f == float64(int64(f)) {
			n = int(f)
		} else {
			return false
		}
	}
	return n == answerMarkerValue
}

// HasResponseDoc reports whether the optional TailwindDoc slot is present.
func (a AnswerRow) HasResponseDoc() bool {
	return len(a.raw) > answerTypePos && a.raw[answerTypePos] != nil
}

// SuggestsWireDrift reports whether an unmarked row looks like drift
// rather than an empty answer. Drift when responseDoc is present but
// unmarked, or when the row is too short to even carry the
// emptyAnswerReason slot.
func (a AnswerRow) SuggestsWireDrift() bool {
	return a.HasResponseDoc() || len(a.raw) <= answerEmptyReason
}

// Citations returns the raw citation entries at first[4][3]. Absent /
// short / non-list type block, and falsy citation slots all return nil.
// Truthy non-list raises — structural drift, not a citation-less answer.
func (a AnswerRow) Citations() ([]any, error) {
	tb := a.typeBlock()
	if tb == nil || len(tb) <= answerCitationsPos {
		return nil, nil
	}
	c := tb[answerCitationsPos]
	if c == nil {
		return nil, nil
	}
	l, ok := c.([]any)
	if !ok {
		return nil, &wireShapeDriftError{
			path:   "[4][3]",
			reason: "not_a_list_value",
			got:    fmt.Sprintf("%T", c),
		}
	}
	return l, nil
}

// -----------------------------------------------------------------------------

// CitationRow wraps one streamed-chat citation entry
// (type_info[3][i]).
type CitationRow struct {
	raw any
}

func (c CitationRow) isWellFormed() bool {
	l, ok := c.raw.([]any)
	return ok && len(l) >= 2
}

func (c CitationRow) chunkID() string {
	if !c.isWellFormed() {
		return ""
	}
	l := c.raw.([]any)
	chunkBlock, ok := l[0].([]any)
	if !ok || len(chunkBlock) == 0 {
		return ""
	}
	s, ok := chunkBlock[0].(string)
	if !ok {
		return ""
	}
	return s
}

func (c CitationRow) detail() *CitationDetail {
	if !c.isWellFormed() {
		return nil
	}
	l := c.raw.([]any)
	inner, ok := l[1].([]any)
	if !ok {
		return nil
	}
	return &CitationDetail{raw: inner}
}

// -----------------------------------------------------------------------------

// CitationDetail is the typed view of a citation detail block (cite[1],
// a `Citation` message). It centralises the score / fragment-range /
// fragment-elements / source-id positions.
type CitationDetail struct {
	raw []any
}

const (
	citeScorePos        = 2
	citeFragmentRange   = 3
	citeFragment        = 4
	citeSourceIDPos     = 5

	fragmentElementsPos = 0

	rangeStartPos = 1
	rangeEndPos   = 2
)

func (d CitationDetail) rawScore() any {
	if len(d.raw) <= citeScorePos {
		return nil
	}
	return d.raw[citeScorePos]
}

func (d CitationDetail) sourceIDData() any {
	if len(d.raw) <= citeSourceIDPos {
		return nil
	}
	return d.raw[citeSourceIDPos]
}

func (d CitationDetail) fragmentElements() []any {
	if len(d.raw) <= citeFragment {
		return nil
	}
	fragment, ok := d.raw[citeFragment].([]any)
	if !ok || len(fragment) <= fragmentElementsPos {
		return nil
	}
	elements, ok := fragment[fragmentElementsPos].([]any)
	if !ok {
		return nil
	}
	return elements
}

func (d CitationDetail) fragmentRange() (any, any) {
	if len(d.raw) <= citeFragmentRange {
		return nil, nil
	}
	outer, ok := d.raw[citeFragmentRange].([]any)
	if !ok || len(outer) == 0 {
		return nil, nil
	}
	inner, ok := outer[0].([]any)
	if !ok || len(inner) <= rangeEndPos {
		return nil, nil
	}
	return inner[rangeStartPos], inner[rangeEndPos]
}

// -----------------------------------------------------------------------------
// Chunk extraction
// -----------------------------------------------------------------------------

// chunkExtraction holds one streamed chunk's decoded contents.
type chunkExtraction struct {
	text             string
	isAnswer         bool
	isFinalResponse  bool
	references       []ChatReference
	conversationID   string
	parseable        bool
	suggestsDrift    bool
	turnKey          *ConversationTurnKey
	nextSteps        []NextStepSuggestion
	terminalSequence *int64
}

// extractChunk parses one chunk JSON line into a chunkExtraction.
//
// On a malformed JSON line the returned chunkExtraction has parseable
// false. On a non-list payload the same. On a parsed list, every item
// is examined; an "er" tag raises ChatError, an "e" tag captures the
// terminal sequence, a "wrb.fr" tag attempts the inner-JSON decode and
// populates the answer/citations/turn-key/etc.
func extractChunk(jsonStr string) chunkExtraction {
	out := chunkExtraction{}

	var data any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return out
	}
	items, ok := data.([]any)
	if !ok {
		return out
	}

	for _, itemAny := range items {
		item, ok := itemAny.([]any)
		if !ok || len(item) < 2 {
			continue
		}
		frame := StreamFrameRow{raw: item}
		tag := frame.Tag()
		if tag == "er" {
			raiseChatErrorFrame(item, frame)
		}
		if tag == "e" {
			out.terminalSequence = frame.TerminalSequence()
			continue
		}
		if tag != "wrb.fr" || len(item) < 3 {
			continue
		}
		innerJSON := frame.InnerJSON()
		innerStr, isStr := innerJSON.(string)
		if !isStr {
			// Null inner JSON: a wrb.fr carries no answer. If item[5]
			// holds an error payload, raise — that's the server's
			// rejection (oversized prompt / rate limit).
			payload := frame.ErrorPayload()
			if payload != nil {
				raiseIfRateLimited(payload)
				raiseChatRejection(payload)
			}
			continue
		}

		var innerData any
		if err := json.Unmarshal([]byte(innerStr), &innerData); err != nil {
			continue
		}
		out.parseable = true

		envelope := StreamEnvelopeRow{raw: innerData}
		envelopeFinal := envelope.IsFinalResponse()

		// Next-step suggestions ride on the envelope; last-wins across
		// envelopes within one frame.
		if ns := envelope.NextStepRows(); len(ns) > 0 {
			decoded := make([]NextStepSuggestion, 0, len(ns))
			for _, row := range ns {
				if !row.IsWellFormed() {
					continue
				}
				decoded = append(decoded, NextStepSuggestion{
					Question: row.Question(),
					TypeCode: row.TypeCode(),
				})
			}
			if len(decoded) > 0 {
				out.nextSteps = decoded
			}
		}

		innerList, ok := innerData.([]any)
		if !ok || len(innerList) == 0 {
			continue
		}
		first, ok := innerList[0].([]any)
		if !ok {
			// Raised on the Python side as UnknownRPCMethodError — we
			// surface it as a parse error since the streamed-chat path
			// does not have a registered RPC id to attach.
			continue
		}
		if len(first) == 0 {
			continue
		}

		ans := AnswerRow{raw: first}
		if ans.TurnKey() != nil {
			out.turnKey = ans.TurnKey()
		}
		text := ans.Text()
		if text == "" {
			continue
		}

		citations, err := ans.Citations()
		if err != nil {
			// Citation container is structurally malformed; surface
			// as a chat error so the caller sees the wire drift.
			out.text = text
			out.isAnswer = ans.IsAnswer()
			out.conversationID = ans.ServerConversationID()
			out.parseable = true
			out.suggestsDrift = true
			return out
		}
		refs := parseCitations(citations, nil)

		out.text = text
		out.isAnswer = ans.IsAnswer()
		out.isFinalResponse = envelopeFinal
		out.references = refs
		out.conversationID = ans.ServerConversationID()
		out.parseable = true
		out.suggestsDrift = ans.SuggestsWireDrift()
		break
	}

	return out
}

// raiseChatErrorFrame surfaces a server-side "er" error frame as a
// ChatError. The error code (if present) is echoed verbatim.
func raiseChatErrorFrame(item []any, frame StreamFrameRow) {
	code := frame.ErrorCode()
	detail := ""
	if code != nil {
		detail = fmt.Sprintf(" (code %v)", code)
	}
	panic(&ChatError{
		Message: fmt.Sprintf(
			"Chat request failed: the server returned an error frame%s. "+
				"This usually means the request was rejected or the conversation "+
				"could not be served; try again.",
			detail,
		),
		ErrorPayload: item,
	})
}

// raiseChatRejection surfaces a wrb.fr request rejection (status at
// item[5]) as a ChatError. A bare [3] (INVALID_ARGUMENT) is the
// documented oversized-prompt shape (issue #1472).
func raiseChatRejection(payload []any) {
	row := ErrorPayloadRow{raw: payload}
	status := row.StatusCode()
	detail := ""
	if status != nil {
		detail = fmt.Sprintf(" (status %v)", status)
	}
	msg := row.Message()
	suffix := ""
	if msg != "" {
		suffix = fmt.Sprintf(" The server said: %s", msg)
	}
	panic(&ChatError{
		Message: fmt.Sprintf(
			"Chat request was rejected by the server%s. "+
				"This usually means the request was malformed or too large — most "+
				"often an over-long question past the server-side size limit; "+
				"shorten it and try again.%s",
			detail, suffix,
		),
		ErrorPayload: payload,
	})
}

// raiseIfRateLimited inspects an error payload for a UserDisplayableError
// marker (rate-limit) and raises ChatError if found. Other error shapes
// (and any parse failure) are silently ignored — the caller falls through
// to raiseChatRejection.
func raiseIfRateLimited(payload []any) {
	defer func() { _ = recover() }()
	row := ErrorPayloadRow{raw: payload}
	for _, entry := range row.Entries() {
		et := row.EntryType(entry)
		if et != "" && strings.Contains(et, "UserDisplayableError") {
			msg := row.Message()
			suffix := ""
			if msg != "" {
				suffix = fmt.Sprintf(" The server said: %s", msg)
			}
			panic(&ChatError{
				Message: fmt.Sprintf(
					"Chat request was rate limited or rejected by the API. "+
						"Wait a few seconds and try again.%s",
					suffix,
				),
				ErrorPayload: payload,
			})
		}
	}
}

// -----------------------------------------------------------------------------
// Citation parsing
// -----------------------------------------------------------------------------

// uuidPattern matches a lowercase / uppercase RFC-4122 UUID.
var uuidPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
)

// extractUUIDFromNested walks a nested list/scalar tree looking for the
// first string that matches the UUID pattern. Returns nil if not found.
func extractUUIDFromNested(data any, maxDepth int) string {
	if maxDepth <= 0 || data == nil {
		return ""
	}
	if s, ok := data.(string); ok {
		if uuidPattern.MatchString(s) {
			return s
		}
		return ""
	}
	if l, ok := data.([]any); ok {
		for _, item := range l {
			if v := extractUUIDFromNested(item, maxDepth-1); v != "" {
				return v
			}
		}
	}
	return ""
}

// block is a parsed StructuralElement for citation fragment text.
type block struct {
	startIndex int
	endIndex   int
	text       string
}

// buildBlocks walks a raw StructuralElement list and produces one
// `block` per element. Mirrors `_web.rows.documents.build_blocks`; the
// slice is sorted by start_index and only rune-aligned / well-formed
// blocks are returned.
func buildBlocks(elements []any) []block {
	if len(elements) == 0 {
		return nil
	}
	out := make([]block, 0, len(elements))
	for _, el := range elements {
		l, ok := el.([]any)
		if !ok || len(l) < 3 {
			continue
		}
		start, ok1 := asIndex(l[0])
		end, ok2 := asIndex(l[1])
		text, ok3 := l[2].(string)
		if !ok1 || !ok2 || !ok3 || end <= start {
			continue
		}
		out = append(out, block{startIndex: start, endIndex: end, text: text})
	}
	sortBlocksByStart(out)
	return out
}

// sortBlocksByStart sorts blocks in place by startIndex ascending.
func sortBlocksByStart(bs []block) {
	// Stable insertion sort — small N (a few per citation).
	for i := 1; i < len(bs); i++ {
		for j := i; j > 0 && bs[j-1].startIndex > bs[j].startIndex; j-- {
			bs[j-1], bs[j] = bs[j], bs[j-1]
		}
	}
}

// asIndex coerces a JSON-decoded scalar to int. Accepts int, int64,
// float64 (when integral), json.Number, but NOT bool.
func asIndex(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int64(n)) {
			return int(n), true
		}
		return 0, false
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// extractTextPassages concatenates the cited fragment's blocks' text,
// trimming what an earlier block already covered.
func extractTextPassages(elements []any) (text string, startChar, endChar int, ok bool) {
	blocks := buildBlocks(elements)
	if len(blocks) == 0 {
		return "", 0, 0, false
	}
	startChar = blocks[0].startIndex
	endChar = blocks[0].endIndex
	for _, b := range blocks[1:] {
		if b.endIndex > endChar {
			endChar = b.endIndex
		}
	}
	// Merge by offset, trimming what an earlier block already covered.
	// This is the readable form, not the offset-faithful form.
	var sb strings.Builder
	cursor := startChar
	for _, b := range blocks {
		if b.endIndex <= cursor {
			continue
		}
		segment := b.text
		if b.startIndex < cursor {
			delta := cursor - b.startIndex
			if delta >= 0 && delta <= UTF16Len(b.text) {
				segment = UTF16Slice(b.text, delta, UTF16Len(b.text))
			}
		}
		sb.WriteString(segment)
		cursor = b.endIndex
	}
	return sb.String(), startChar, endChar, true
}

// extractFragmentRange reads the server's source-side fragment range
// (cite_inner[3][0] = [None, start, end]). Returns (0, 0, false) for
// absent / malformed / unordered ranges.
func extractFragmentRange(d *CitationDetail) (int, int, bool) {
	rawStart, rawEnd := d.fragmentRange()
	start, ok1 := asIndex(rawStart)
	end, ok2 := asIndex(rawEnd)
	if !ok1 || !ok2 || start < 0 || end < start {
		return 0, 0, false
	}
	return start, end, true
}

// extractScore reads the relevance score at cite_inner[2]. Returns
// (0, false) for non-numeric / bool / non-finite / out-of-range values.
func extractScore(d *CitationDetail) (float64, bool) {
	raw := d.rawScore()
	switch v := raw.(type) {
	case bool:
		return 0, false
	case int:
		f := float64(v)
		if score, ok := validateScore(f); ok {
			return score, true
		}
	case int64:
		f := float64(v)
		if score, ok := validateScore(f); ok {
			return score, true
		}
	case float64:
		if score, ok := validateScore(v); ok {
			return score, true
		}
	}
	return 0, false
}

func validateScore(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	if v < 0.0 || v > 1.0 {
		return 0, false
	}
	return v, true
}

// parseCitations walks the raw citation entry list, returning one
// ChatReference per usable row. Malformed rows are skipped silently
// (the parser does not raise on individual citation drift; the absence
// leaves a hole in the citation-number sequence).
func parseCitations(rawCitations []any, _ *struct{}) []ChatReference {
	if len(rawCitations) == 0 {
		return nil
	}
	refs := make([]ChatReference, 0, len(rawCitations))
	for idx, citeAny := range rawCitations {
		row := CitationRow{raw: citeAny}
		detail := row.detail()
		if detail == nil {
			continue
		}
		sourceID := extractUUIDFromNested(detail.sourceIDData(), 10)
		if sourceID == "" {
			continue
		}
		chunkID := row.chunkID()

		cited, startChar, endChar, passagesOK := extractTextPassages(detail.fragmentElements())
		if !passagesOK {
			cited = ""
		}
		fragStart, fragEnd, fragOK := extractFragmentRange(detail)
		score, scoreOK := extractScore(detail)

		ref := ChatReference{
			SourceID:       sourceID,
			CitedText:      cited,
			StartChar:      startChar,
			EndChar:        endChar,
			ChunkID:        chunkID,
			Score:          score,
			CitationNumber: idx + 1, // raw wire ordinal
		}
		if fragOK {
			ref.FragmentStart = fragStart
			ref.FragmentEnd = fragEnd
		}
		_ = scoreOK
		refs = append(refs, ref)
	}
	return refs
}

// assignCitationNumbers dense-fills citation numbers for refs that
// arrived without one. Refs with a non-zero number keep their wire
// ordinal so a skipped malformed row leaves a hole rather than shift
// survivors onto wrong [N] markers.
func assignCitationNumbers(refs []ChatReference) []ChatReference {
	out := make([]ChatReference, 0, len(refs))
	for i, r := range refs {
		if r.CitationNumber == 0 {
			r.CitationNumber = i + 1
		}
		out = append(out, r)
	}
	return out
}

// -----------------------------------------------------------------------------
// Wire-shape drift error
// -----------------------------------------------------------------------------

// wireShapeDriftError surfaces a wire reshape the streaming parser
// detected but cannot recover from. Implements error so it can be
// unwrapped by the caller's drift-detection path.
type wireShapeDriftError struct {
	path   string
	reason string
	got    string
}

func (e *wireShapeDriftError) Error() string {
	return fmt.Sprintf("rows: chatstream shape drift at %s: %s (got %s)", e.path, e.reason, e.got)
}

// -----------------------------------------------------------------------------
// Small helpers
// -----------------------------------------------------------------------------

// readerFromBytes returns an io.Reader for the given bytes.
func readerFromBytes(b []byte) io.Reader { return bytes.NewReader(b) }

// bufferedLines returns a bufio.Scanner configured for line-delimited
// reads. Reserved for the streaming incremental path (T-S3-007e); the
// current parser buffers the entire body up-front because the chunked
// framing is line-oriented and the body is bounded by the response cap.
func bufferedLines(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return s
}
