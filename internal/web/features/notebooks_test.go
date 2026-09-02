// Tests for the notebook features layer.
//
// The Caller interface is the only seam between this package and the
// SDK. We test each feature by constructing a fake Caller that
// returns a canned payload; the typed-decoder / not-found / envelope-
// unwrap paths are exercised end-to-end without involving the
// transport or RPC plumbing.
package features

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/rows"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// fakeCaller is a tiny in-memory Caller for tests. Records the
// (method, params, sourcePath, allowNull) tuple on every call so a
// test can assert the feature routed the right RPC and payload.
type fakeCaller struct {
	// response is the payload the next call returns; nil = error below.
	response any

	// err is the error returned alongside response.
	err error

	// calls records every dispatch.
	calls []fakeCall

	// count tracks how many times the caller was invoked.
	count atomic.Int32
}

type fakeCall struct {
	method     wire.Method
	params     any
	sourcePath string
	allowNull  bool
}

func (f *fakeCaller) Call(ctx context.Context, method wire.Method, params any, sourcePath string, allowNull bool) (any, error) {
	f.count.Add(1)
	f.calls = append(f.calls, fakeCall{method: method, params: params, sourcePath: sourcePath, allowNull: allowNull})
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

// TestListPaged_DecodesEnvelope — the LIST_NOTEBOOKS envelope is
// [[row1, row2, ...]]; the wrapper unwraps correctly.
func TestListPaged_DecodesEnvelope(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{
				[]any{"Hello", nil, "nb-1", nil, nil, []any{1}},
				[]any{"World", nil, "nb-2", nil, nil, []any{2}},
			},
		},
	}
	got, err := ListPaged(context.Background(), caller, ListOptions{})
	if err != nil {
		t.Fatalf("ListPaged: %v", err)
	}
	if len(got.Notebooks) != 2 {
		t.Fatalf("Notebooks len = %d, want 2", len(got.Notebooks))
	}
	if got.Notebooks[0].ID != "nb-1" || got.Notebooks[0].Title != "Hello" {
		t.Errorf("Notebooks[0] = %+v, want nb-1 / Hello", got.Notebooks[0])
	}
	if got.Notebooks[1].ID != "nb-2" {
		t.Errorf("Notebooks[1].ID = %q, want nb-2", got.Notebooks[1].ID)
	}
	if got.Notebooks[1].IsShared != true {
		t.Errorf("Notebooks[1].IsShared = false, want true (role=2 ≠ owner)")
	}
}

// TestListPaged_NilPayload — a nil response degrades to an empty page
// rather than raising.
func TestListPaged_NilPayload(t *testing.T) {
	caller := &fakeCaller{response: nil}
	got, err := ListPaged(context.Background(), caller, ListOptions{})
	if err != nil {
		t.Fatalf("ListPaged nil payload: %v", err)
	}
	if len(got.Notebooks) != 0 {
		t.Errorf("Notebooks len = %d, want 0", len(got.Notebooks))
	}
}

// TestListPaged_EmptyEnvelope — an empty envelope `[]` is also a "no
// notebooks" response.
func TestListPaged_EmptyEnvelope(t *testing.T) {
	caller := &fakeCaller{response: []any{}}
	got, _ := ListPaged(context.Background(), caller, ListOptions{})
	if len(got.Notebooks) != 0 {
		t.Errorf("Notebooks len = %d, want 0", len(got.Notebooks))
	}
}

// TestListPaged_SchemaDrift — a truthy payload that does not match
// the envelope is genuine drift and surfaces a *wire.ShapeDriftError.
func TestListPaged_SchemaDrift(t *testing.T) {
	caller := &fakeCaller{response: "not-a-list"}
	_, err := ListPaged(context.Background(), caller, ListOptions{})
	if err == nil {
		t.Fatal("ListPaged with non-list payload returned nil err")
	}
	var drift *wire.ShapeDriftError
	if !errors.As(err, &drift) {
		t.Errorf("err = %v, want *wire.ShapeDriftError", err)
	}
}

// TestListPaged_MaxItems — the MaxItems option truncates the page
// client-side.
func TestListPaged_MaxItems(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{
				[]any{"a", nil, "nb-1"},
				[]any{"b", nil, "nb-2"},
				[]any{"c", nil, "nb-3"},
			},
		},
	}
	got, _ := ListPaged(context.Background(), caller, ListOptions{MaxItems: 2})
	if len(got.Notebooks) != 2 {
		t.Errorf("Notebooks len = %d, want 2 (capped)", len(got.Notebooks))
	}
}

// TestListPaged_NilCaller — defensive guard; the SDK should never
// hand a nil Caller to the feature, but if it does, we surface a
// typed error rather than panic.
func TestListPaged_NilCaller(t *testing.T) {
	_, err := ListPaged(context.Background(), nil, ListOptions{})
	if err == nil {
		t.Fatal("ListPaged with nil caller returned nil err")
	}
}

// TestListPaged_CallerError — the Caller returned an error; we
// propagate it.
func TestListPaged_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := ListPaged(context.Background(), caller, ListOptions{})
	if err == nil {
		t.Fatal("ListPaged with caller err returned nil err")
	}
}

// TestGet_Decodes — the GET_NOTEBOOK envelope is [row, ...] — the row
// lives at index 0.
func TestGet_Decodes(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{"Hello", nil, "nb-1", nil, nil, []any{2}},
		},
	}
	got, err := Get(context.Background(), caller, "nb-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "nb-1" {
		t.Errorf("ID = %q, want nb-1", got.ID)
	}
}

// TestGet_NotFound — an empty GET_NOTEBOOK envelope surfaces
// rows.ErrNotFound so callers can branch on the typed sentinel.
func TestGet_NotFound(t *testing.T) {
	caller := &fakeCaller{response: []any{}}
	_, err := Get(context.Background(), caller, "nb-missing")
	if !errors.Is(err, rows.ErrNotFound) {
		t.Errorf("Get(empty) err = %v, want wraps rows.ErrNotFound", err)
	}
}

// TestGet_EmptyID — a missing id surfaces as a typed ValidationError
// before any RPC fires.
func TestGet_EmptyID(t *testing.T) {
	caller := &fakeCaller{}
	_, err := Get(context.Background(), caller, "")
	if err == nil {
		t.Fatal("Get with empty id returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked %d times for empty id; want 0 (validation pre-flight)", caller.count.Load())
	}
}

// TestSummary_ExtractsString — the SUMMARIZE envelope is
// [[summary_string, ...], topics, ...]; we descend to result[0][0][0].
func TestSummary_ExtractsString(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{
				[]any{"The summary text."},
				nil,
			},
		},
	}
	got, err := Summary(context.Background(), caller, "nb-1")
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got != "The summary text." {
		t.Errorf("Summary = %q, want 'The summary text.'", got)
	}
}

// TestSummary_NoSummary — an absent summary degrades to "".
func TestSummary_NoSummary(t *testing.T) {
	caller := &fakeCaller{response: []any{}}
	got, _ := Summary(context.Background(), caller, "nb-1")
	if got != "" {
		t.Errorf("Summary = %q, want empty", got)
	}
}

// TestDescribe_BothFields — the typed wrapper exposes both the summary
// string and the suggested topics.
func TestDescribe_BothFields(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{
				[]any{"The summary."},
				[]any{
					[]any{
						[]any{"Q1?", "Prompt 1"},
						[]any{"Q2?", "Prompt 2"},
					},
				},
			},
		},
	}
	got, err := Describe(context.Background(), caller, "nb-1")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got.Summary != "The summary." {
		t.Errorf("Summary = %q, want 'The summary.'", got.Summary)
	}
	if len(got.SuggestedTopics) != 2 {
		t.Fatalf("Topics len = %d, want 2", len(got.SuggestedTopics))
	}
	if got.SuggestedTopics[0].Question != "Q1?" || got.SuggestedTopics[0].Prompt != "Prompt 1" {
		t.Errorf("Topics[0] = %+v, want Q1? / Prompt 1", got.SuggestedTopics[0])
	}
}

// TestDescribe_NoTopics — an absent topics block degrades to nil.
func TestDescribe_NoTopics(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{
				[]any{"The summary."},
			},
		},
	}
	got, _ := Describe(context.Background(), caller, "nb-1")
	if got.Summary != "The summary." {
		t.Errorf("Summary = %q, want 'The summary.'", got.Summary)
	}
	if len(got.SuggestedTopics) != 0 {
		t.Errorf("Topics len = %d, want 0", len(got.SuggestedTopics))
	}
}

// TestCreate_Decodes — the CREATE_NOTEBOOK response is a single row.
func TestCreate_Decodes(t *testing.T) {
	caller := &fakeCaller{
		response: []any{"New Notebook", nil, "nb-new"},
	}
	got, err := Create(context.Background(), caller, "New Notebook")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "nb-new" {
		t.Errorf("ID = %q, want nb-new", got.ID)
	}
	if got.Title != "New Notebook" {
		t.Errorf("Title = %q, want New Notebook", got.Title)
	}
}

// TestCreate_EmptyTitle — empty title is a validation error; no RPC.
func TestCreate_EmptyTitle(t *testing.T) {
	caller := &fakeCaller{}
	_, err := Create(context.Background(), caller, "")
	if err == nil {
		t.Fatal("Create with empty title returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked %d times for empty title; want 0", caller.count.Load())
	}
}

// TestDelete_Dispatches — DELETE_NOTEBOOK sends the [id, [2]] payload
// with allowNull=true.
func TestDelete_Dispatches(t *testing.T) {
	caller := &fakeCaller{}
	if err := Delete(context.Background(), caller, "nb-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(caller.calls) != 1 {
		t.Fatalf("Calls len = %d, want 1", len(caller.calls))
	}
	call := caller.calls[0]
	if call.method != wire.MethodDeleteNotebook {
		t.Errorf("method = %v, want MethodDeleteNotebook", call.method)
	}
	if !call.allowNull {
		t.Errorf("allowNull = false, want true")
	}
}

// TestRename_DispatchAndRefresh — Rename sends the title+emoji payload
// then refreshes via Get.
func TestRename_DispatchAndRefresh(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{"New Title", nil, "nb-1"},
		},
	}
	emoji := "📓"
	_, err := Rename(context.Background(), caller, "nb-1", "New Title", &emoji)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if len(caller.calls) < 2 {
		t.Fatalf("Calls len = %d, want >=2 (rename + get)", len(caller.calls))
	}
	if caller.calls[0].method != wire.MethodRenameNotebook {
		t.Errorf("Call[0].method = %v, want MethodRenameNotebook", caller.calls[0].method)
	}
	if caller.calls[1].method != wire.MethodGetNotebook {
		t.Errorf("Call[1].method = %v, want MethodGetNotebook (refresh)", caller.calls[1].method)
	}
}

// TestShare_Dispatch — Share(notebookID, true) sends the
// [[[id, ..., [1], [1, ""]]], 1, nil, [2]] payload.
func TestShare_Dispatch(t *testing.T) {
	caller := &fakeCaller{}
	if err := Share(context.Background(), caller, "nb-1", true); err != nil {
		t.Fatalf("Share: %v", err)
	}
	if caller.calls[0].method != wire.MethodShareNotebook {
		t.Errorf("method = %v, want MethodShareNotebook", caller.calls[0].method)
	}
}

// TestUnshare_Dispatch — Unshare routes through Share with public=false.
func TestUnshare_Dispatch(t *testing.T) {
	caller := &fakeCaller{}
	if err := Unshare(context.Background(), caller, "nb-1"); err != nil {
		t.Fatalf("Unshare: %v", err)
	}
	if caller.calls[0].method != wire.MethodShareNotebook {
		t.Errorf("method = %v, want MethodShareNotebook", caller.calls[0].method)
	}
}

// TestRemoveCollaborator_ValidateEmail — a malformed email is rejected
// before any RPC fires.
func TestRemoveCollaborator_ValidateEmail(t *testing.T) {
	caller := &fakeCaller{}
	err := RemoveCollaborator(context.Background(), caller, "nb-1", "no-at-sign")
	if err == nil {
		t.Fatal("RemoveCollaborator with bad email returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked for bad email; want 0")
	}
}

// TestRemoveFromRecent_Dispatch — RemoveFromRecent sends the
// [notebook_id] payload with allowNull=true.
func TestRemoveFromRecent_Dispatch(t *testing.T) {
	caller := &fakeCaller{}
	if err := RemoveFromRecent(context.Background(), caller, "nb-1"); err != nil {
		t.Fatalf("RemoveFromRecent: %v", err)
	}
	if caller.calls[0].method != wire.MethodRemoveRecentlyViewed {
		t.Errorf("method = %v, want MethodRemoveRecentlyViewed", caller.calls[0].method)
	}
	if !caller.calls[0].allowNull {
		t.Errorf("allowNull = false, want true")
	}
}

// TestCallerIsReusedAcrossFeatures — the same Caller implementation
// serves every feature. This is a sanity test that the interface
// seam is uniform.
func TestCallerIsReusedAcrossFeatures(t *testing.T) {
	caller := &fakeCaller{}
	_ = Delete(context.Background(), caller, "nb-1")
	_ = Unshare(context.Background(), caller, "nb-1")
	_ = RemoveFromRecent(context.Background(), caller, "nb-1")
	if caller.count.Load() != 3 {
		t.Errorf("count = %d, want 3", caller.count.Load())
	}
}

// TestIsNotFound — the typed-error classifier used by every feature.
// nil → false; rows.ErrNotFound (and wraps) → true; wire.ErrClient
// carrying grpc status 5 → true; other errors → false.
func TestIsNotFound(t *testing.T) {
	if isNotFound(nil) {
		t.Errorf("isNotFound(nil) = true; want false")
	}
	if !isNotFound(rows.ErrNotFound) {
		t.Errorf("isNotFound(rows.ErrNotFound) = false; want true")
	}
	wrapped := fmt.Errorf("features.Get: %w", rows.ErrNotFound)
	if !isNotFound(wrapped) {
		t.Errorf("isNotFound(wrapped) = false; want true")
	}
	clientErr := fmt.Errorf("rpc failure: %w", wire.ErrClient)
	clientErrWithStatus := fmt.Errorf("%w: status 5 NOT_FOUND", clientErr)
	if !isNotFound(clientErrWithStatus) {
		t.Errorf("isNotFound(wire.ErrClient status 5) = false; want true")
	}
	clientErrOther := fmt.Errorf("%w: status 3 INVALID_ARGUMENT", clientErr)
	if isNotFound(clientErrOther) {
		t.Errorf("isNotFound(wire.ErrClient status 3) = true; want false")
	}
	if isNotFound(errors.New("other")) {
		t.Errorf("isNotFound(other) = true; want false")
	}
}

// TestContainsStatus — the substring-based status-code matcher.
// "status 5" → true; "status 5x" → false; "status 50" → false (not 5).
func TestContainsStatus(t *testing.T) {
	cases := []struct {
		msg  string
		code int
		want bool
	}{
		{"status 5 NOT_FOUND", 5, true},
		{"status 3 INVALID_ARGUMENT", 5, false},
		{"status 50 INTERNAL", 5, false},
		{"grpc: status 5 NOT_FOUND", 5, true},
		{"no status here", 5, false},
		{"status abc", 5, false},
	}
	for _, c := range cases {
		got := containsStatus(c.msg, c.code)
		if got != c.want {
			t.Errorf("containsStatus(%q, %d) = %v, want %v", c.msg, c.code, got, c.want)
		}
	}
}

// TestTypeName — the stable-name helper used in *ShapeDriftError
// diagnostics. nil → "nil"; known types → short name.
func TestTypeName(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "nil"},
		{[]any{1, 2}, "[]any"},
		{map[string]any{"k": "v"}, "map[string]any"},
		{"hello", "string"},
		{true, "bool"},
		{float64(1.5), "float64"},
	}
	for _, c := range cases {
		got := typeName(c.in)
		if got != c.want {
			t.Errorf("typeName(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGet_RPCError — when the caller returns an error, Get
// propagates it (or classifies as ErrNotFound).
func TestGet_RPCError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := Get(context.Background(), caller, "nb-1")
	if err == nil {
		t.Fatal("Get with rpc err returned nil err")
	}
	if errors.Is(err, rows.ErrNotFound) {
		t.Errorf("Get with generic rpc err classified as not-found; want propagation")
	}
}

// TestGet_WrappedNotFound — a caller error that wraps
// rows.ErrNotFound surfaces as ErrNotFound.
func TestGet_WrappedNotFound(t *testing.T) {
	caller := &fakeCaller{err: fmt.Errorf("transport: %w", rows.ErrNotFound)}
	_, err := Get(context.Background(), caller, "nb-1")
	if !errors.Is(err, rows.ErrNotFound) {
		t.Errorf("Get with wrapped ErrNotFound = %v, want wraps ErrNotFound", err)
	}
}

// TestSummary_NilCaller — defensive guard.
func TestSummary_NilCaller(t *testing.T) {
	_, err := Summary(context.Background(), nil, "nb-1")
	if err == nil {
		t.Fatal("Summary with nil caller returned nil err")
	}
}

// TestSummary_EmptyID — validation pre-flight.
func TestSummary_EmptyID(t *testing.T) {
	caller := &fakeCaller{}
	_, err := Summary(context.Background(), caller, "")
	if err == nil {
		t.Fatal("Summary with empty id returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked for empty id; want 0")
	}
}

// TestSummary_CallerError — caller error is propagated.
func TestSummary_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := Summary(context.Background(), caller, "nb-1")
	if err == nil {
		t.Fatal("Summary with rpc err returned nil err")
	}
}

// TestDescribe_NilCaller — defensive guard.
func TestDescribe_NilCaller(t *testing.T) {
	_, err := Describe(context.Background(), nil, "nb-1")
	if err == nil {
		t.Fatal("Describe with nil caller returned nil err")
	}
}

// TestDescribe_EmptyID — validation pre-flight.
func TestDescribe_EmptyID(t *testing.T) {
	caller := &fakeCaller{}
	_, err := Describe(context.Background(), caller, "")
	if err == nil {
		t.Fatal("Describe with empty id returned nil err")
	}
}

// TestDescribe_CallerError — caller error is propagated.
func TestDescribe_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := Describe(context.Background(), caller, "nb-1")
	if err == nil {
		t.Fatal("Describe with rpc err returned nil err")
	}
}

// TestDescribe_NilPayload — nil response → empty description.
func TestDescribe_NilPayload(t *testing.T) {
	caller := &fakeCaller{response: nil}
	got, err := Describe(context.Background(), caller, "nb-1")
	if err != nil {
		t.Fatalf("Describe nil: %v", err)
	}
	if got.Summary != "" {
		t.Errorf("Summary = %q, want empty", got.Summary)
	}
	if len(got.SuggestedTopics) != 0 {
		t.Errorf("Topics len = %d, want 0", len(got.SuggestedTopics))
	}
}

// TestCreate_NilCaller — defensive guard.
func TestCreate_NilCaller(t *testing.T) {
	_, err := Create(context.Background(), nil, "title")
	if err == nil {
		t.Fatal("Create with nil caller returned nil err")
	}
}

// TestCreate_CallerError — caller error is propagated.
func TestCreate_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := Create(context.Background(), caller, "title")
	if err == nil {
		t.Fatal("Create with rpc err returned nil err")
	}
}

// TestDelete_NilCaller — defensive guard.
func TestDelete_NilCaller(t *testing.T) {
	err := Delete(context.Background(), nil, "nb-1")
	if err == nil {
		t.Fatal("Delete with nil caller returned nil err")
	}
}

// TestDelete_EmptyID — validation pre-flight.
func TestDelete_EmptyID(t *testing.T) {
	caller := &fakeCaller{}
	err := Delete(context.Background(), caller, "")
	if err == nil {
		t.Fatal("Delete with empty id returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked for empty id; want 0")
	}
}

// TestDelete_CallerError — caller error is propagated.
func TestDelete_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	if err := Delete(context.Background(), caller, "nb-1"); err == nil {
		t.Fatal("Delete with rpc err returned nil err")
	}
}

// TestRename_NilCaller — defensive guard.
func TestRename_NilCaller(t *testing.T) {
	_, err := Rename(context.Background(), nil, "nb-1", "title", nil)
	if err == nil {
		t.Fatal("Rename with nil caller returned nil err")
	}
}

// TestRename_EmptyID — validation pre-flight.
func TestRename_EmptyID(t *testing.T) {
	caller := &fakeCaller{}
	_, err := Rename(context.Background(), caller, "", "title", nil)
	if err == nil {
		t.Fatal("Rename with empty id returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked for empty id; want 0")
	}
}

// TestRename_CallerError — caller error is propagated.
func TestRename_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := Rename(context.Background(), caller, "nb-1", "title", nil)
	if err == nil {
		t.Fatal("Rename with rpc err returned nil err")
	}
}

// TestShare_NilCaller — defensive guard.
func TestShare_NilCaller(t *testing.T) {
	err := Share(context.Background(), nil, "nb-1", true)
	if err == nil {
		t.Fatal("Share with nil caller returned nil err")
	}
}

// TestShare_EmptyID — validation pre-flight.
func TestShare_EmptyID(t *testing.T) {
	caller := &fakeCaller{}
	err := Share(context.Background(), caller, "", true)
	if err == nil {
		t.Fatal("Share with empty id returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked for empty id; want 0")
	}
}

// TestShare_CallerError — caller error is propagated.
func TestShare_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	if err := Share(context.Background(), caller, "nb-1", true); err == nil {
		t.Fatal("Share with rpc err returned nil err")
	}
}

// TestRemoveCollaborator_NilCaller — defensive guard.
func TestRemoveCollaborator_NilCaller(t *testing.T) {
	err := RemoveCollaborator(context.Background(), nil, "nb-1", "alice@example.com")
	if err == nil {
		t.Fatal("RemoveCollaborator with nil caller returned nil err")
	}
}

// TestRemoveCollaborator_EmptyID — validation pre-flight.
func TestRemoveCollaborator_EmptyID(t *testing.T) {
	caller := &fakeCaller{}
	err := RemoveCollaborator(context.Background(), caller, "", "alice@example.com")
	if err == nil {
		t.Fatal("RemoveCollaborator with empty id returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked for empty id; want 0")
	}
}

// TestRemoveCollaborator_CallerError — caller error is propagated.
func TestRemoveCollaborator_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	err := RemoveCollaborator(context.Background(), caller, "nb-1", "alice@example.com")
	if err == nil {
		t.Fatal("RemoveCollaborator with rpc err returned nil err")
	}
}

// TestRemoveFromRecent_NilCaller — defensive guard.
func TestRemoveFromRecent_NilCaller(t *testing.T) {
	err := RemoveFromRecent(context.Background(), nil, "nb-1")
	if err == nil {
		t.Fatal("RemoveFromRecent with nil caller returned nil err")
	}
}

// TestRemoveFromRecent_EmptyID — validation pre-flight.
func TestRemoveFromRecent_EmptyID(t *testing.T) {
	caller := &fakeCaller{}
	err := RemoveFromRecent(context.Background(), caller, "")
	if err == nil {
		t.Fatal("RemoveFromRecent with empty id returned nil err")
	}
	if caller.count.Load() != 0 {
		t.Errorf("Caller invoked for empty id; want 0")
	}
}

// TestRemoveFromRecent_CallerError — caller error is propagated.
func TestRemoveFromRecent_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	err := RemoveFromRecent(context.Background(), caller, "nb-1")
	if err == nil {
		t.Fatal("RemoveFromRecent with rpc err returned nil err")
	}
}

// TestListPaged_CallerErrorExtended — caller error is propagated
// with features.ListPaged context.
func TestListPaged_CallerErrorExtended(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := ListPaged(context.Background(), caller, ListOptions{})
	if err == nil {
		t.Fatal("ListPaged with rpc err returned nil err")
	}
}

// TestListPaged_OuterNil — outer[0] is nil → empty page (the
// "no notebooks" response).
func TestListPaged_OuterNil(t *testing.T) {
	caller := &fakeCaller{
		response: []any{nil},
	}
	got, err := ListPaged(context.Background(), caller, ListOptions{})
	if err != nil {
		t.Fatalf("ListPaged outer-nil: %v", err)
	}
	if len(got.Notebooks) != 0 {
		t.Errorf("Notebooks len = %d, want 0", len(got.Notebooks))
	}
}

// TestListPaged_InnerNotList — outer[0] is a non-list (e.g. a
// string) → *ShapeDriftError.
func TestListPaged_InnerNotList(t *testing.T) {
	caller := &fakeCaller{
		response: []any{"not-a-list-of-rows"},
	}
	_, err := ListPaged(context.Background(), caller, ListOptions{})
	if err == nil {
		t.Fatal("ListPaged with non-list inner returned nil err")
	}
	var drift *wire.ShapeDriftError
	if !errors.As(err, &drift) {
		t.Errorf("err = %v, want *wire.ShapeDriftError", err)
	}
}
