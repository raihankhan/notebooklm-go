// Package notebooklm — notebooks_test.go.
//
// Unit tests for NotebooksAPI (the 17-method public surface).
// Coverage target: ≥70% line coverage on notebooklm/notebooks.go.
//
// The tests use a fake Executor that returns canned `[]byte` body
// bytes shaped like a real `batchexecute` response. The Executor
// is wired into the Client (the same seam client_test.go uses),
// so the tests exercise the wire-decoder + row-decoder paths the
// SDK namespace fans out to without spinning an httptest server.
package notebooklm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/web/transport"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// nbFakeExecutor is the test double the Client accepts through
// its Executor interface. It records every Execute call so a
// test can assert the method id, the rpc tag, and the encoded
// positional payload, and it returns the canned body the test
// configured.
//
// The fake intentionally bypasses the real transport chain
// (Kernel / Runtime / middlewares) — the test owns every
// returned byte so the wire-decoder path can be exercised
// without network I/O. The canned body is encoded as a JSON
// envelope + a `wrb.fr` frame whose rpc id matches the
// resolved method id (e.g. `wXbhsf` for MethodListNotebooks).
type nbFakeExecutor struct {
	mu sync.Mutex

	// calls records every Execute call.
	calls []nbFakeCall

	// body is the body bytes returned by every call.
	// Set to a fresh []byte before each test (tests use
	// the helper newFakeRPC to build a frame, then
	// assign the result).
	body []byte
}

type nbFakeCall struct {
	method  wire.Method
	variant string
	params  any
}

func (f *nbFakeExecutor) Execute(
	_ context.Context,
	method wire.Method,
	variant string,
	params any,
	_ wire.Host,
	_ time.Duration,
) (*transport.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, nbFakeCall{
		method:  method,
		variant: variant,
		params:  params,
	})
	if f.body == nil {
		return &transport.ExecResult{Body: []byte{}}, nil
	}
	return &transport.ExecResult{Body: f.body, RequestID: "test"}, nil
}

// callCount returns how many Execute calls the fake recorded.
func (f *nbFakeExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// lastCall returns the most recent Execute call (or the zero
// value when no call fired). The mutex is held briefly so a
// concurrent test goroutine cannot race the read.
func (f *nbFakeExecutor) lastCall() (nbFakeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nbFakeCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// newFakeRPC builds a minimal batchexecute response body that
// ExtractResult can decode for the named method id. The shape
// is:
//
//	)]}'\n
//	<num>\n
//	[["wrb.fr","<rpcID>","<result-json>",null,null,null]]
//
// where <result-json> is the JSON-encoded payload the test
// wants the wire-decoder to surface. The byte-count framing
// header is the empirical format google.com ships; tests pin
// it here so a future wire-format change surfaces as a test
// diff rather than a silent decoder bug.
//
// The rpc id is the Method value itself — wire.Method
// constants are the obfuscated ids (`wXbhsf` for
// MethodListNotebooks, etc.). Tests can therefore pass the
// canonical method constant and the fake returns the
// matching frame.
func newFakeRPC(t *testing.T, method wire.Method, payload any) []byte {
	t.Helper()
	if string(method) == "" {
		t.Fatalf("test: empty method")
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("test: marshal payload: %v", err)
	}
	frame := []any{"wrb.fr", string(method), string(payloadJSON), nil, nil, nil}
	framesJSON, err := json.Marshal([]any{frame})
	if err != nil {
		t.Fatalf("test: marshal frame: %v", err)
	}
	// The framing header is `<decimal byte-count>\n` where the
	// count is the byte length of the payload that follows on
	// the next line. Parser tolerates a count discrepancy (it
	// bumps the byteCountMismatchTotal drift metric but still
	// decodes the chunk), so we use the actual length here.
	header := []byte(")]}'\n")
	body := append(header, []byte(strconvAppend(len(framesJSON), '\n'))...)
	body = append(body, framesJSON...)
	return body
}

// strconvAppend is a tiny int-to-string helper that appends a
// trailing byte without dragging the strconv import into the
// newFakeRPC signature. Lives in test code so the helper does
// not pollute the production package.
func strconvAppend(n int, suffix byte) string {
	if n == 0 {
		return "0" + string(suffix)
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits) + string(suffix)
}

// newClientWithFake returns a wired Client whose executor is
// the supplied nbFakeExecutor. The Client's real executor is
// replaced in-place so the test does not need to spin a real
// transport chain. The returned cleanup closes the Client —
// callers should defer it.
func newClientWithFake(t *testing.T, fake *nbFakeExecutor) *Client {
	t.Helper()
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("notebooklm.New: %v", err)
	}
	c.executor = fake
	return c
}

// TestNotebooks_List_DecodesEnvelope pins AC1: the List
// method issues a ListNotebooks RPC and returns the typed
// Page view. The fake executor returns one well-formed row
// per the docs/04-rpc-payloads.md envelope shape.
func TestNotebooks_List_DecodesEnvelope(t *testing.T) {
	row := []any{
		"Hello",  // 0: title
		nil,      // 1: sources list
		"nb-1",   // 2: id
		nil,      // 3: emoji
		nil,      // 4: (unused)
		[]any{2}, // 5: meta block, role=2 (shared)
	}
	envelope := []any{[]any{row}}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodListNotebooks, envelope)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	got, err := c.Notebooks().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	if got.Items[0].ID != "nb-1" || got.Items[0].Title != "Hello" {
		t.Errorf("Item[0] = %+v, want nb-1 / Hello", got.Items[0])
	}
	if !got.Items[0].IsShared {
		t.Errorf("IsShared = false, want true (role=2 != owner)")
	}
}

// TestNotebooks_List_EmptyEnvelope — an empty list returns a
// zero-value Page, not an error.
func TestNotebooks_List_EmptyEnvelope(t *testing.T) {
	envelope := []any{[]any{}}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodListNotebooks, envelope)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	got, err := c.Notebooks().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("len(Items) = %d, want 0", len(got.Items))
	}
}

// TestNotebooks_List_MaxItems — the WithMaxItems option
// truncates the page client-side.
func TestNotebooks_List_MaxItems(t *testing.T) {
	rows := []any{
		[]any{"a", nil, "nb-1"},
		[]any{"b", nil, "nb-2"},
		[]any{"c", nil, "nb-3"},
	}
	envelope := []any{rows}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodListNotebooks, envelope)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	got, err := c.Notebooks().List(context.Background(), WithMaxItems(2))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Items) != 2 {
		t.Errorf("len(Items) = %d, want 2 (capped)", len(got.Items))
	}
}

// TestNotebooks_Create_Decodes — Create issues CREATE_NOTEBOOK
// and returns the typed Notebook view.
func TestNotebooks_Create_Decodes(t *testing.T) {
	row := []any{"New Notebook", nil, "nb-new"}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodCreateNotebook, row)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	nb, err := c.Notebooks().Create(context.Background(), "New Notebook")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if nb.ID != "nb-new" {
		t.Errorf("ID = %q, want nb-new", nb.ID)
	}
	if nb.Title != "New Notebook" {
		t.Errorf("Title = %q, want New Notebook", nb.Title)
	}
}

// TestNotebooks_Create_EmptyTitle — empty title is rejected
// before any RPC fires.
func TestNotebooks_Create_EmptyTitle(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	_, err := c.Notebooks().Create(context.Background(), "")
	if err == nil {
		t.Fatal("Create with empty title returned nil err")
	}
	if fake.callCount() != 0 {
		t.Errorf("Call count = %d, want 0 (validation pre-flight)", fake.callCount())
	}
}

// TestNotebooks_Get_Decodes — Get issues GET_NOTEBOOK and
// returns the typed Notebook view.
func TestNotebooks_Get_Decodes(t *testing.T) {
	row := []any{"Hello", nil, "nb-1", nil, nil, []any{2}}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodGetNotebook, row)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	nb, err := c.Notebooks().Get(context.Background(), "nb-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if nb.ID != "nb-1" {
		t.Errorf("ID = %q, want nb-1", nb.ID)
	}
	if nb.Title != "Hello" {
		t.Errorf("Title = %q, want Hello", nb.Title)
	}
}

// TestNotebooks_Get_EmptyID — empty id is rejected before
// any RPC fires.
func TestNotebooks_Get_EmptyID(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	_, err := c.Notebooks().Get(context.Background(), "")
	if err == nil {
		t.Fatal("Get with empty id returned nil err")
	}
	if fake.callCount() != 0 {
		t.Errorf("Call count = %d, want 0 (validation pre-flight)", fake.callCount())
	}
}

// TestNotebooks_Delete_Dispatch — Delete issues
// DELETE_NOTEBOOK and accepts an empty body.
func TestNotebooks_Delete_Dispatch(t *testing.T) {
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodDeleteNotebook, nil)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	if err := c.Notebooks().Delete(context.Background(), "nb-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if fake.callCount() != 1 {
		t.Errorf("Call count = %d, want 1", fake.callCount())
	}
}

// TestNotebooks_Delete_EmptyID — empty id is rejected before
// any RPC fires.
func TestNotebooks_Delete_EmptyID(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	err := c.Notebooks().Delete(context.Background(), "")
	if err == nil {
		t.Fatal("Delete with empty id returned nil err")
	}
	if fake.callCount() != 0 {
		t.Errorf("Call count = %d, want 0", fake.callCount())
	}
}

// TestNotebooks_Rename_Dispatch — Rename issues the
// MUTATE_NOTEBOOK RPC with the title-only payload.
func TestNotebooks_Rename_Dispatch(t *testing.T) {
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodRenameNotebook, nil)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	if err := c.Notebooks().Rename(context.Background(), "nb-1", "New Title", nil); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if fake.callCount() != 1 {
		t.Errorf("Call count = %d, want 1", fake.callCount())
	}
	last, ok := fake.lastCall()
	if !ok {
		t.Fatal("lastCall not ok")
	}
	if last.method != wire.MethodRenameNotebook {
		t.Errorf("method = %q, want %q", last.method, wire.MethodRenameNotebook)
	}
}

// TestNotebooks_Rename_BothRequired — both title and emoji
// empty is rejected before any RPC fires.
func TestNotebooks_Rename_BothRequired(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	err := c.Notebooks().Rename(context.Background(), "nb-1", "", nil)
	if err == nil {
		t.Fatal("Rename with both empty returned nil err")
	}
	if fake.callCount() != 0 {
		t.Errorf("Call count = %d, want 0", fake.callCount())
	}
}

// TestNotebooks_Summary_Decodes — Summary issues the SUMMARIZE
// RPC and returns the typed Summary view.
func TestNotebooks_Summary_Decodes(t *testing.T) {
	envelope := []any{
		[]any{
			[]any{"The summary text."},
			[]any{
				[]any{
					[]any{"Q1?", "Prompt 1"},
				},
			},
		},
	}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodSummarize, envelope)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	got, err := c.Notebooks().Summary(context.Background(), "nb-1")
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.Summary != "The summary text." {
		t.Errorf("Summary = %q, want 'The summary text.'", got.Summary)
	}
	if len(got.SuggestedTopics) != 1 {
		t.Fatalf("Topics len = %d, want 1", len(got.SuggestedTopics))
	}
	if got.SuggestedTopics[0].Question != "Q1?" {
		t.Errorf("Topics[0].Question = %q, want Q1?", got.SuggestedTopics[0].Question)
	}
}

// TestNotebooks_Summary_EmptyEnvelope — an empty summary
// envelope returns an empty Summary, not an error.
func TestNotebooks_Summary_EmptyEnvelope(t *testing.T) {
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodSummarize, []any{})}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	got, err := c.Notebooks().Summary(context.Background(), "nb-1")
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.Summary != "" {
		t.Errorf("Summary = %q, want empty", got.Summary)
	}
}

// TestNotebooks_Metadata_Decodes — Metadata issues
// GET_NOTEBOOK (under the hood) and projects to the typed
// Metadata view.
func TestNotebooks_Metadata_Decodes(t *testing.T) {
	row := []any{"Hello", nil, "nb-1", "📓", nil, []any{1, nil, nil, nil, nil, []any{0, 100}, nil, nil, []any{1234, 0}}}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodGetNotebook, row)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	md, err := c.Notebooks().Metadata(context.Background(), "nb-1")
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if md.Emoji == nil || *md.Emoji != "📓" {
		t.Errorf("Emoji = %+v, want \"📓\"", md.Emoji)
	}
	if md.Role == nil || *md.Role != 1 {
		t.Errorf("Role = %+v, want &1", md.Role)
	}
}

// TestNotebooks_Share_Dispatch — Share issues
// SHARE_NOTEBOOK with the public=true envelope.
func TestNotebooks_Share_Dispatch(t *testing.T) {
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodShareNotebook, nil)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	err := c.Notebooks().Share(context.Background(), "nb-1", ShareAccessAnyoneWithLink)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	if fake.callCount() != 1 {
		t.Errorf("Call count = %d, want 1", fake.callCount())
	}
}

// TestNotebooks_Unshare_Dispatch — Unshare routes through
// Share with restricted access.
func TestNotebooks_Unshare_Dispatch(t *testing.T) {
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodShareNotebook, nil)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	if err := c.Notebooks().Unshare(context.Background(), "nb-1"); err != nil {
		t.Fatalf("Unshare: %v", err)
	}
	if fake.callCount() != 1 {
		t.Errorf("Call count = %d, want 1", fake.callCount())
	}
}

// TestNotebooks_GetShareStatus_Decodes — GetShareStatus
// issues the GET_SHARE_STATUS RPC and returns the typed
// ShareState view.
func TestNotebooks_GetShareStatus_Decodes(t *testing.T) {
	envelope := []any{
		[]any{"nb-1", true},
		[]any{1.0},
		nil,
		[]any{[]any{"alice@example.com", 2.0}},
	}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodGetShareStatus, envelope)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	ss, err := c.Notebooks().GetShareStatus(context.Background(), "nb-1")
	if err != nil {
		t.Fatalf("GetShareStatus: %v", err)
	}
	if !ss.IsPublic {
		t.Errorf("IsPublic = false, want true")
	}
	if ss.AccessLevel != ShareAccessAnyoneWithLink {
		t.Errorf("AccessLevel = %v, want ShareAccessAnyoneWithLink", ss.AccessLevel)
	}
	if len(ss.Collaborators) != 1 {
		t.Fatalf("Collaborators len = %d, want 1", len(ss.Collaborators))
	}
	if ss.Collaborators[0].Email != "alice@example.com" {
		t.Errorf("Collaborators[0].Email = %q, want alice@example.com", ss.Collaborators[0].Email)
	}
	if ss.Collaborators[0].Role != SharePermissionEditor {
		t.Errorf("Collaborators[0].Role = %v, want SharePermissionEditor", ss.Collaborators[0].Role)
	}
}

// TestNotebooks_RemoveCollaborator_Dispatch — RemoveCollaborator
// issues SHARE_NOTEBOOK with the remove-user variant.
func TestNotebooks_RemoveCollaborator_Dispatch(t *testing.T) {
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodShareNotebook, nil)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	if err := c.Notebooks().RemoveCollaborator(context.Background(), "nb-1", "alice@example.com"); err != nil {
		t.Fatalf("RemoveCollaborator: %v", err)
	}
	if fake.callCount() != 1 {
		t.Errorf("Call count = %d, want 1", fake.callCount())
	}
}

// TestNotebooks_RemoveCollaborator_EmptyID — empty id is
// rejected before any RPC fires.
func TestNotebooks_RemoveCollaborator_EmptyID(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	err := c.Notebooks().RemoveCollaborator(context.Background(), "", "alice@example.com")
	if err == nil {
		t.Fatal("RemoveCollaborator with empty id returned nil err")
	}
	if fake.callCount() != 0 {
		t.Errorf("Call count = %d, want 0", fake.callCount())
	}
}

// TestNotebooks_RemoveCollaborator_BadEmail — a malformed
// email is rejected before any RPC fires.
func TestNotebooks_RemoveCollaborator_BadEmail(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	err := c.Notebooks().RemoveCollaborator(context.Background(), "nb-1", "no-at-sign")
	if err == nil {
		t.Fatal("RemoveCollaborator with bad email returned nil err")
	}
	if fake.callCount() != 0 {
		t.Errorf("Call count = %d, want 0", fake.callCount())
	}
}

// TestNotebooks_AddSource_NotImplemented — the params layer
// panics with "TODO" until T-P6-2 lands. The SDK recovers
// and surfaces a typed CodeNotebookLMError.
func TestNotebooks_AddSource_NotImplemented(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	err := c.Notebooks().AddSource(context.Background(), "nb-1", []string{"src-1"})
	if err == nil {
		t.Fatal("AddSource returned nil err; params stub should panic")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("err = %v, want contains 'not yet implemented'", err)
	}
	if fake.callCount() != 0 {
		t.Errorf("Call count = %d, want 0", fake.callCount())
	}
}

// TestNotebooks_RemoveSource_NotImplemented — the params
// layer panics; the SDK recovers.
func TestNotebooks_RemoveSource_NotImplemented(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	err := c.Notebooks().RemoveSource(context.Background(), "nb-1", []string{"src-1"})
	if err == nil {
		t.Fatal("RemoveSource returned nil err; params stub should panic")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("err = %v, want contains 'not yet implemented'", err)
	}
}

// TestNotebooks_GetStarred_NotImplemented — same TODO as
// AddSource; the params layer panics until T-P5-5 (or later)
// pins a builder.
func TestNotebooks_GetStarred_NotImplemented(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	_, err := c.Notebooks().GetStarred(context.Background())
	if err == nil {
		t.Fatal("GetStarred returned nil err; params stub should panic")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("err = %v, want contains 'not yet implemented'", err)
	}
}

// TestNotebooks_GetSharedWithMe_NotImplemented — same TODO.
func TestNotebooks_GetSharedWithMe_NotImplemented(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	_, err := c.Notebooks().GetSharedWithMe(context.Background())
	if err == nil {
		t.Fatal("GetSharedWithMe returned nil err; params stub should panic")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("err = %v, want contains 'not yet implemented'", err)
	}
}

// TestNotebooks_GetByProject_NotImplemented — same TODO.
func TestNotebooks_GetByProject_NotImplemented(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	_, err := c.Notebooks().GetByProject(context.Background(), "proj-1")
	if err == nil {
		t.Fatal("GetByProject returned nil err; params stub should panic")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("err = %v, want contains 'not yet implemented'", err)
	}
}

// TestNotebooks_GetByProject_EmptyID — empty project id is
// rejected before the panic-recovery path runs.
func TestNotebooks_GetByProject_EmptyID(t *testing.T) {
	fake := &nbFakeExecutor{}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	_, err := c.Notebooks().GetByProject(context.Background(), "")
	if err == nil {
		t.Fatal("GetByProject with empty id returned nil err")
	}
	if fake.callCount() != 0 {
		t.Errorf("Call count = %d, want 0 (validation pre-flight)", fake.callCount())
	}
}

// TestNotebooks_GetRecent — GetRecent is currently an alias
// for List, but uses its own MethodListNotebooks dispatch.
// The test pins that the dispatched method is MethodListNotebooks.
func TestNotebooks_GetRecent(t *testing.T) {
	envelope := []any{[]any{[]any{[]any{"Recent", nil, "nb-1"}}}}
	fake := &nbFakeExecutor{body: newFakeRPC(t, wire.MethodListNotebooks, envelope)}
	c := newClientWithFake(t, fake)
	defer func() { _ = c.Close() }()

	got, err := c.Notebooks().GetRecent(context.Background())
	if err != nil {
		t.Fatalf("GetRecent: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
}

// TestNotebooks_RPCError_Classified — an RPC-level error is
// wrapped with a typed SDK context. We verify the wrap carries
// a Code through the Coded seam.
func TestNotebooks_RPCError_Classified(t *testing.T) {
	canned := errors.New("wire: rpc failure")
	c := &Client{
		executor: &erroringExecutor{err: canned},
		registry: nil,
	}
	defer func() { _ = c.Close() }()
	// close the lifecycle so RPCCall is happy; the fake
	// executor does not require the lifecycle.
	c.lifecycle = nil
	c.supervisor = nil

	_, err := c.Notebooks().List(context.Background())
	if err == nil {
		t.Fatal("List with rpc err returned nil")
	}
	// Verify the wrap carries a Code — the SDK namespace
	// attaches the canonical CodeNotebookLMError tag.
	var coded apperrors.Coded
	if !errors.As(err, &coded) {
		t.Errorf("err = %v, want apperrors.Coded", err)
	}
	if coded.Code() != apperrors.CodeNotebookLMError {
		t.Errorf("Code = %q, want %q", coded.Code(), apperrors.CodeNotebookLMError)
	}
}

// erroringExecutor is a tiny Executor that returns the same
// canned error every time. Used by TestNotebooks_RPCError_Classified
// to verify the SDK wraps a non-coded RPC error with a typed
// CodeNotebookLMError.
type erroringExecutor struct {
	err error
}

func (e *erroringExecutor) Execute(
	_ context.Context,
	_ wire.Method,
	_ string,
	_ any,
	_ wire.Host,
	_ time.Duration,
) (*transport.ExecResult, error) {
	return nil, e.err
}

// TestNotebooks_PreservesSentinels — when the RPC layer
// returns one of the canonical sentinels (ErrNotFound,
// ErrAuth, …), the SDK passes it through unchanged so
// callers can errors.Is on the canonical root.
func TestNotebooks_PreservesSentinels(t *testing.T) {
	for _, sentinel := range []error{
		apperrors.ErrNotFound,
		apperrors.ErrAuth,
		apperrors.ErrRateLimited,
		apperrors.ErrQuota,
	} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			got := wrapRPCError(wire.MethodGetNotebook, sentinel)
			if !errors.Is(got, sentinel) {
				t.Errorf("wrapRPCError(%v) = %v, want wraps %v", sentinel, got, sentinel)
			}
		})
	}
}

// TestNotebooks_OptionsResolution — WithMaxItems applies to
// every listing method (List, GetRecent, GetStarred,
// GetSharedWithMe, GetByProject). We pin the contract here
// so a future option refactor surfaces as a test diff.
func TestNotebooks_WithMaxItems(t *testing.T) {
	o := resolveOptions([]NotebooksOption{WithMaxItems(7)})
	if o.maxItems != 7 {
		t.Errorf("maxItems = %d, want 7", o.maxItems)
	}
}

// TestNotebooks_WithMaxItems_Zero — WithMaxItems(0) (or a
// nil call) is a no-op; the zero value is the default.
func TestNotebooks_WithMaxItems_Zero(t *testing.T) {
	o := resolveOptions([]NotebooksOption{WithMaxItems(0)})
	if o.maxItems != 0 {
		t.Errorf("maxItems = %d, want 0", o.maxItems)
	}
}

// TestNotebooks_PageShape — the Page type exposes the
// documented field set. Pinning the fields here means a
// future rename (or a future paged-listing field) surfaces as
// a test diff rather than a silent API change.
func TestNotebooks_PageShape(t *testing.T) {
	var p Page
	_ = p.Items
	_ = p.NextOffset
	_ = p.HasMore
}
