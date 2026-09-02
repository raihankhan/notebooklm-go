// Tests for the sources features layer.
//
// The Caller interface is the only seam between this package and
// the SDK. We test each feature by constructing a fake Caller that
// returns a canned payload; the typed-decoder / envelope-unwrap
// paths are exercised end-to-end without involving the transport
// or RPC plumbing.
package sources

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// fakeCaller is a tiny in-memory Caller for tests. Records the
// (method, params, sourcePath, allowNull) tuple on every call so a
// test can assert the feature routed the right RPC and payload.
type fakeCaller struct {
	response any
	err      error
	calls    []fakeCall
	count    atomic.Int32
}

type fakeCall struct {
	method     wire.Method
	params     any
	sourcePath string
	allowNull  bool
}

func (f *fakeCaller) Call(_ context.Context, method wire.Method, params any, sourcePath string, allowNull bool) (any, error) {
	f.count.Add(1)
	f.calls = append(f.calls, fakeCall{method: method, params: params, sourcePath: sourcePath, allowNull: allowNull})
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

// TestList_DecodesEnvelope — the GET_NOTEBOOK envelope is
// [[nb_info, ...]] with the source list at nb_info[1]; the
// features layer unwraps correctly.
func TestList_DecodesEnvelope(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			// nb_info: [title, sources]
			[]any{
				"Notebook Title",
				[]any{
					[]any{[]any{"src-1"}, "Source 1", []any{nil, nil, nil, nil, 1, nil, nil, nil}, []any{nil, 2}},
					[]any{[]any{"src-2"}, "Source 2", []any{nil, nil, nil, nil, 2, nil, nil, nil}, []any{nil, 1}},
				},
			},
		},
	}
	got, err := List(context.Background(), caller, "nb-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "src-1" || got[0].Title != "Source 1" || got[0].Kind != "url" || got[0].StatusLabel != "READY" {
		t.Errorf("got[0] = %+v, want src-1/Source 1/url/READY", got[0])
	}
	if got[1].ID != "src-2" || got[1].Kind != "youtube" || got[1].StatusLabel != "PROCESSING" {
		t.Errorf("got[1] = %+v, want src-2/youtube/PROCESSING", got[1])
	}
}

// TestList_EmptyNotebook — a notebook with no sources
// (nb_info[1] is null) returns an empty list rather than raising.
func TestList_EmptyNotebook(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{
				"Notebook Title",
				nil,
			},
		},
	}
	got, err := List(context.Background(), caller, "nb-empty")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestList_NoNotebookInfo — a null nb_info returns an empty list.
func TestList_NoNotebookInfo(t *testing.T) {
	caller := &fakeCaller{
		response: []any{nil},
	}
	got, _ := List(context.Background(), caller, "nb-x")
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestList_NilPayload — a nil response degrades to an empty list
// rather than raising.
func TestList_NilPayload(t *testing.T) {
	caller := &fakeCaller{response: nil}
	got, err := List(context.Background(), caller, "nb-1")
	if err != nil {
		t.Fatalf("List nil payload: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestList_EmptyEnvelope — an empty envelope `[]` is also a "no
// sources" response.
func TestList_EmptyEnvelope(t *testing.T) {
	caller := &fakeCaller{response: []any{}}
	got, _ := List(context.Background(), caller, "nb-1")
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestList_SchemaDrift — a truthy payload that does not match the
// envelope is genuine drift and surfaces a *wire.ShapeDriftError.
func TestList_SchemaDrift(t *testing.T) {
	caller := &fakeCaller{response: "not-a-list"}
	_, err := List(context.Background(), caller, "nb-1")
	if err == nil {
		t.Fatal("List with non-list payload returned nil err")
	}
	var drift *wire.ShapeDriftError
	if !errors.As(err, &drift) {
		t.Errorf("err = %v, want *wire.ShapeDriftError", err)
	}
}

// TestList_NilCaller — defensive guard.
func TestList_NilCaller(t *testing.T) {
	_, err := List(context.Background(), nil, "nb-1")
	if err == nil {
		t.Fatal("List with nil caller returned nil err")
	}
}

// TestList_CallerError — the Caller returned an error; we propagate it.
func TestList_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := List(context.Background(), caller, "nb-1")
	if err == nil {
		t.Fatal("List with caller err returned nil err")
	}
}

// TestList_Routing — the wire method / sourcePath / allowNull
// values match the canonical ADD_SOURCE surface contract.
func TestList_Routing(t *testing.T) {
	caller := &fakeCaller{}
	_, _ = List(context.Background(), caller, "nb-route")
	if len(caller.calls) != 1 {
		t.Fatalf("Calls len = %d, want 1", len(caller.calls))
	}
	call := caller.calls[0]
	if call.method != wire.MethodGetNotebook {
		t.Errorf("method = %v, want MethodGetNotebook", call.method)
	}
	if call.sourcePath != "/notebook/nb-route" {
		t.Errorf("sourcePath = %q, want /notebook/nb-route", call.sourcePath)
	}
	if call.allowNull {
		t.Errorf("allowNull = true, want false")
	}
}

// TestAddURL_DecodesMediumNested — the ADD_SOURCE response is the
// medium-nested shape: `[[[id], title, metadata, ...]]`.
func TestAddURL_DecodesMediumNested(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{
				[]any{"src-new"},
				"https://example.com",
				[]any{nil, nil, nil, nil, 1, nil, nil, nil},
				[]any{nil, 2},
			},
		},
	}
	got, err := AddURL(context.Background(), caller, "nb-1", "https://example.com")
	if err != nil {
		t.Fatalf("AddURL: %v", err)
	}
	if got.ID != "src-new" {
		t.Errorf("ID = %q, want src-new", got.ID)
	}
	if got.Title != "https://example.com" {
		t.Errorf("Title = %q, want https://example.com", got.Title)
	}
	if got.Kind != "url" {
		t.Errorf("Kind = %q, want url", got.Kind)
	}
	if got.StatusLabel != "READY" {
		t.Errorf("StatusLabel = %q, want READY", got.StatusLabel)
	}
}

// TestAddURL_NilResponse — a null response (the documented "silent
// commit" failure mode for AddSources) returns a zero Source
// rather than raising the wire-layer ErrEmptyResult.
func TestAddURL_NilResponse(t *testing.T) {
	caller := &fakeCaller{response: nil}
	got, err := AddURL(context.Background(), caller, "nb-1", "https://example.com")
	if err != nil {
		// allowNull=true is on the caller dispatch path, so the
		// caller is expected to return nil here. If the fake
		// caller returns the nil response with no error, the
		// features layer should accept that as "no decoded row".
		t.Fatalf("AddURL with nil response: %v", err)
	}
	if got.ID != "" || got.Title != "" {
		t.Errorf("AddURL nil result = %+v, want zero Source", got)
	}
}

// TestAddURL_NilCaller — defensive guard.
func TestAddURL_NilCaller(t *testing.T) {
	_, err := AddURL(context.Background(), nil, "nb-1", "https://example.com")
	if err == nil {
		t.Fatal("AddURL with nil caller returned nil err")
	}
}

// TestAddURL_CallerError — caller error is propagated.
func TestAddURL_CallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := AddURL(context.Background(), caller, "nb-1", "https://example.com")
	if err == nil {
		t.Fatal("AddURL with caller err returned nil err")
	}
}

// TestAddURL_Routing — the wire method / sourcePath / allowNull
// values match the canonical ADD_SOURCE surface contract.
func TestAddURL_Routing(t *testing.T) {
	caller := &fakeCaller{}
	_, _ = AddURL(context.Background(), caller, "nb-route", "https://example.com")
	if len(caller.calls) != 1 {
		t.Fatalf("Calls len = %d, want 1", len(caller.calls))
	}
	call := caller.calls[0]
	if call.method != wire.MethodAddSource {
		t.Errorf("method = %v, want MethodAddSource", call.method)
	}
	if call.sourcePath != "/notebook/nb-route" {
		t.Errorf("sourcePath = %q, want /notebook/nb-route", call.sourcePath)
	}
	if !call.allowNull {
		t.Errorf("allowNull = false, want true (silent commit tolerance)")
	}
}

// TestTypeName — the stable-name helper used in
// *ShapeDriftError diagnostics.
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

// TestCallerIsReusedAcrossFeatures — the same Caller implementation
// serves both List and AddURL. This is a sanity test that the
// interface seam is uniform.
func TestCallerIsReusedAcrossFeatures(t *testing.T) {
	caller := &fakeCaller{}
	_, _ = List(context.Background(), caller, "nb-1")
	_, _ = AddURL(context.Background(), caller, "nb-1", "https://example.com")
	if caller.count.Load() != 2 {
		t.Errorf("count = %d, want 2", caller.count.Load())
	}
}

// TestList_NbInfoShortRow — a notebook with nb_info of len <= 1
// returns an empty list. Mirrors `_web/sources/listing.py::list`
// line 178-183.
func TestList_NbInfoShortRow(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{"Title Only"},
		},
	}
	got, err := List(context.Background(), caller, "nb-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestList_SourcesNotList — nb_info[1] is a non-list (e.g. a
// string) → *ShapeDriftError.
func TestList_SourcesNotList(t *testing.T) {
	caller := &fakeCaller{
		response: []any{
			[]any{"Title", "not-a-list"},
		},
	}
	_, err := List(context.Background(), caller, "nb-1")
	if err == nil {
		t.Fatal("List with non-list sources slot returned nil err")
	}
	var drift *wire.ShapeDriftError
	if !errors.As(err, &drift) {
		t.Errorf("err = %v, want *wire.ShapeDriftError", err)
	}
}

// TestList_PropagatesCallerError — caller error is propagated with
// the features.sources.List context wrapper.
func TestList_PropagatesCallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := List(context.Background(), caller, "nb-1")
	if err == nil {
		t.Fatal("List with rpc err returned nil err")
	}
	if !contains(err.Error(), "rpc timeout") {
		t.Errorf("err = %v, want wraps rpc timeout", err)
	}
}

// TestAddURL_PropagatesCallerError — caller error is propagated
// with the features.sources.AddURL context wrapper.
func TestAddURL_PropagatesCallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc timeout")}
	_, err := AddURL(context.Background(), caller, "nb-1", "https://example.com")
	if err == nil {
		t.Fatal("AddURL with rpc err returned nil err")
	}
	if !contains(err.Error(), "rpc timeout") {
		t.Errorf("err = %v, want wraps rpc timeout", err)
	}
}

// contains is a tiny helper so we don't pull strings into every
// test file. Matches substring; sufficient for these tests.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// _ keeps fmt imported when callers add further tests below.
var _ = fmt.Sprintf
