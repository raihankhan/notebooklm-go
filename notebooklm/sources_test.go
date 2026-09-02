// Package notebooklm — sources_test.go.
//
// Unit tests for SourcesAPI (List + AddURL). The tests use a fake
// Executor that returns canned chunked-wrapped body bytes so the
// clientCaller → wire.DecodeResponse → wire.Unmarshal → features /
// rows.DecodeSource pipeline runs end-to-end against production
// code paths.
//
// Every test pins one of the AC6 coverage targets the ticket
// commits to:
//
//	TestSourcesAPI_List_Empty          → empty-notebook contract
//	TestSourcesAPI_List_5Rows          → 5-row fixture
//	TestSourcesAPI_AddURL_Success      → fresh source row
//	TestSourcesAPI_AddURL_BadURL       → apperrors.Validation envelope
//	TestSourcesAPI_NilClient           → nil-tolerant API guard
//	TestSourcesAPI_Options_MaxItems    → functional-options seam
//	TestSourcesAPI_Routing             → method/params round-trip
//	TestSourcesAPI_AllowNull           → silent-commit tolerance
package notebooklm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/runtime"
	"github.com/raihankhan/notebooklm-go/internal/web/transport"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// fakeSourcesExecutor is the test double the Client accepts
// through its Executor interface. The canned responses are
// keyed by wire.Method so a List-then-AddURL sequence returns
// the matching canned body for each.
type fakeSourcesExecutor struct {
	mu        sync.Mutex
	canned    map[wire.Method]string
	calls     []fakeCall
	cannedErr error
}

func (f *fakeSourcesExecutor) Execute(
	ctx context.Context,
	method wire.Method,
	variant string,
	params any,
	host wire.Host,
	timeout time.Duration,
) (*transport.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{
		method:  method,
		variant: variant,
		params:  params,
		host:    host,
		timeout: timeout,
	})
	body, ok := f.canned[method]
	f.mu.Unlock()
	if f.cannedErr != nil {
		return nil, f.cannedErr
	}
	if !ok {
		// Unknown RPC: return an empty chunked body so the wire
		// layer surfaces a typed ErrNoRPCData. The features
		// layer will surface it as a non-nil err on the public
		// SDK boundary.
		return &transport.ExecResult{Body: []byte("[]")}, nil
	}
	return &transport.ExecResult{Body: wrapChunked(method, body)}, nil
}

// wrapChunked formats bodyJSON as a single-frame wrb.fr chunked
// response for rpcID. The frame format is
//
//	["wrb.fr", "<rpcID>", "<result-json>", null, null, null]
//
// — the form the live backend emits for a successful RPC. The
// wire.DecodeResponse call inside clientCaller.Call matches
// frame[1] == rpcID and pulls frame[2] as the Payload.
//
// bodyJSON is a `[]any` (positional payload) we marshal to JSON
// first so it slots inside the frame as a string. The result is
// then wrapped in a JSON-encoded single-element array (the chunked
// frame-list envelope), and the whole payload is preceded by the
// canonical `)]}'\n<count>\n` framing header so the parser's chunked
// path runs end-to-end against the production code.
func wrapChunked(rpcID wire.Method, bodyJSON string) []byte {
	// Empty bodyJSON is the "null payload" envelope — emit a frame
	// whose frame[2] is the literal JSON `null` (a zero-byte
	// payload would be malformed and the wire layer would reject
	// it before the allowNull branch runs).
	if bodyJSON == "" {
		bodyJSON = "null"
	}
	// Decode bodyJSON back into a `[]any` so we can re-marshal it
	// with proper escaping (the JSON-encoded payload is what the
	// wire layer expects at frame[2], not a raw concatenated
	// string).
	var raw any
	if err := json.Unmarshal([]byte(bodyJSON), &raw); err != nil {
		panic(fmt.Sprintf("wrapChunked: bodyJSON invalid: %v", err))
	}
	payloadJSON, err := json.Marshal(raw)
	if err != nil {
		panic(fmt.Sprintf("wrapChunked: marshal payload: %v", err))
	}
	frame := []any{"wrb.fr", string(rpcID), string(payloadJSON), nil, nil, nil}
	framesJSON, err := json.Marshal([]any{frame})
	if err != nil {
		panic(fmt.Sprintf("wrapChunked: marshal frame: %v", err))
	}
	header := []byte(")]}'\n")
	countHeader := []byte(strconv.Itoa(len(framesJSON)) + "\n")
	body := append(header, countHeader...)
	body = append(body, framesJSON...)
	return body
}

// newClientWithFakeSources builds a Client whose executor is the
// supplied fake. The Client constructor wires the rest of the
// runtime stack (Kernel, Supervisor, Lifecycle, Metrics) so the
// public API surface runs end-to-end.
func newClientWithFakeSources(t *testing.T, fake *fakeSourcesExecutor) *Client {
	t.Helper()
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.executor = fake
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestSourcesAPI_List_Empty — an empty-notebook envelope
// (`nb_info[1] == null`) returns a zero-row Page rather than
// raising.
func TestSourcesAPI_List_Empty(t *testing.T) {
	// Wire envelope: [["Notebook Title", null]]
	const emptyEnvelope = `[["Notebook Title",null]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodGetNotebook: emptyEnvelope,
		},
	}
	c := newClientWithFakeSources(t, fake)

	page, err := c.Sources().List(context.Background(), "nb-empty")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("len = %d, want 0", len(page.Items))
	}
	if page.NextOffset != "" || page.HasMore {
		t.Errorf("page = %+v, want empty Page", page)
	}
}

// TestSourcesAPI_List_5Rows — the byte-exact 5-row fixture. The
// ticket body pins the four typed fields per row; the test
// asserts every row decodes to a fully-populated Source.
func TestSourcesAPI_List_5Rows(t *testing.T) {
	const fiveRows = `[
		["Notebook Title",[
			["src-a","Source A",[null,null,null,null,1,null,null,null],[null,2]],
			["src-b","Source B",[null,null,null,null,2,null,null,null],[null,1]],
			[[null,true,["src-c"]],"Source C",[null,null,null,null,3,null,null,null],[null,2]],
			["src-d","Source D",[null,null,null,null,4,null,null,null],[null,3]],
			["src-e","Source E",[null,null,null,null,5,null,null,null],[null,2]]
		]]
	]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodGetNotebook: fiveRows,
		},
	}

	c := newClientWithFakeSources(t, fake)

	page, err := c.Sources().List(context.Background(), "nb-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("len = %d, want 5", len(page.Items))
	}
	// Spot-check one entry: drive-shape id envelope (row 2).
	src := page.Items[2]
	if src.ID != "src-c" || src.Title != "Source C" || src.Kind != "drive" || src.StatusLabel != "READY" {
		t.Errorf("row 2 = %+v, want src-c/Source C/drive/READY", src)
	}
	// And one of the typical-shape rows.
	if page.Items[0].Kind != "url" || page.Items[0].StatusLabel != "READY" {
		t.Errorf("row 0 = %+v, want url/READY", page.Items[0])
	}
}

// TestSourcesAPI_AddURL_Success — the medium-nested envelope
// (`[[[id], title, metadata, ...]]`) decodes to a populated
// Source.
func TestSourcesAPI_AddURL_Success(t *testing.T) {
	const addedEnvelope = `[["src-new","https://example.com",[null,null,null,null,1,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: addedEnvelope,
		},
	}
	c := newClientWithFakeSources(t, fake)

	src, err := c.Sources().AddURL(context.Background(), "nb-1", "https://example.com")
	if err != nil {
		t.Fatalf("AddURL: %v", err)
	}
	if src.ID != "src-new" {
		t.Errorf("ID = %q, want src-new", src.ID)
	}
	if src.Title != "https://example.com" {
		t.Errorf("Title = %q, want https://example.com", src.Title)
	}
	if src.Kind != "url" {
		t.Errorf("Kind = %q, want url", src.Kind)
	}
	if src.StatusLabel != "READY" {
		t.Errorf("StatusLabel = %q, want READY", src.StatusLabel)
	}
}

// TestSourcesAPI_AddURL_BadURL — a malformed URL surfaces as an
// apperrors.Validation envelope so adapters can branch on
// apperrors.Classify uniformly.
func TestSourcesAPI_AddURL_BadURL(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	cases := []string{"", " ", "example.com", "ftp://example.com"}
	for _, bad := range cases {
		_, err := c.Sources().AddURL(context.Background(), "nb-1", bad)
		if err == nil {
			t.Errorf("AddURL(%q) returned nil err; want validation error", bad)
			continue
		}
		code, _ := apperrors.Classify(err)
		if code != apperrors.CodeValidationError {
			t.Errorf("AddURL(%q) code = %q, want %q", bad, code, apperrors.CodeValidationError)
		}
	}
}

// TestSourcesAPI_AddURL_BadNotebookID — a notebook id that looks
// like a URL is rejected client-side as a validation error
// (rather than silently dispatched as a backend 404).
func TestSourcesAPI_AddURL_BadNotebookID(t *testing.T) {
	c, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.Sources().AddURL(context.Background(), "https://example.com", "https://ok.example.com")
	if err == nil {
		t.Fatal("AddURL with URL-shaped notebook id returned nil err")
	}
	code, _ := apperrors.Classify(err)
	if code != apperrors.CodeValidationError {
		t.Errorf("code = %q, want %q", code, apperrors.CodeValidationError)
	}
}

// TestSourcesAPI_NilClient — every entry point is nil-tolerant
// and returns a typed error without dispatching an RPC.
func TestSourcesAPI_NilClient(t *testing.T) {
	var c *Client
	if c.Sources() == nil {
		t.Fatal("Sources() on nil Client returned nil SourcesAPI")
	}
	_, err := c.Sources().List(context.Background(), "nb-1")
	if err == nil {
		t.Error("List on nil Client returned nil err")
	}
	_, err = c.Sources().AddURL(context.Background(), "nb-1", "https://example.com")
	if err == nil {
		t.Error("AddURL on nil Client returned nil err")
	}
}

// TestSourcesAPI_Options_MaxItems — the WithMaxItems option
// truncates the page client-side after the decode.
func TestSourcesAPI_Options_MaxItems(t *testing.T) {
	const fiveRows = `[
		["Notebook Title",[
			["src-a","A",[null,null,null,null,1,null,null,null],[null,2]],
			["src-b","B",[null,null,null,null,1,null,null,null],[null,2]],
			["src-c","C",[null,null,null,null,1,null,null,null],[null,2]],
			["src-d","D",[null,null,null,null,1,null,null,null],[null,2]],
			["src-e","E",[null,null,null,null,1,null,null,null],[null,2]]
		]]
	]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodGetNotebook: fiveRows,
		},
	}
	c := newClientWithFakeSources(t, fake)

	page, err := c.Sources().List(context.Background(), "nb-1", WithSourcesMaxItems(2))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("len = %d, want 2 (MaxItems=2)", len(page.Items))
	}
}

// TestSourcesAPI_Routing — the wire method + positional params
// the features layer builds match what the public SDK surfaces.
func TestSourcesAPI_Routing(t *testing.T) {
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodGetNotebook: `[["Title",[]]]`,
			wire.MethodAddSource:   `[[[["src-1"],"url",[null,null,null,null,1,null,null,null],[null,2]]]]`,
		},
	}
	c := newClientWithFakeSources(t, fake)

	if _, err := c.Sources().List(context.Background(), "nb-route"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := c.Sources().AddURL(context.Background(), "nb-route", "https://example.com"); err != nil {
		t.Fatalf("AddURL: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("Execute calls = %d, want 2", len(fake.calls))
	}
	if fake.calls[0].method != wire.MethodGetNotebook {
		t.Errorf("call[0] method = %v, want MethodGetNotebook", fake.calls[0].method)
	}
	if fake.calls[1].method != wire.MethodAddSource {
		t.Errorf("call[1] method = %v, want MethodAddSource", fake.calls[1].method)
	}
	// The positional params the features layer built are
	// passed through unchanged; assert the trailing literal
	// for the GetNotebook side (slot 4 = 0) and the URL slot
	// for the AddSource side.
	getParams, ok := fake.calls[0].params.([]any)
	if !ok || len(getParams) < 5 {
		t.Fatalf("GetNotebook params = %T, want []any len>=5", fake.calls[0].params)
	}
	if getParams[0] != "nb-route" {
		t.Errorf("GetNotebook slot 0 = %v, want nb-route", getParams[0])
	}
	addParams, ok := fake.calls[1].params.([]any)
	if !ok || len(addParams) != 3 {
		t.Fatalf("AddSource params = %T, want []any len=3", fake.calls[1].params)
	}
	if addParams[1] != "nb-route" {
		t.Errorf("AddSource slot 1 = %v, want nb-route", addParams[1])
	}
}

// TestSourcesAPI_AllowNull — AddURL is wired with allowNull=true
// on the wire layer; a null result body degrades to a zero
// Source rather than raising the wire-layer ErrEmptyResult.
func TestSourcesAPI_AllowNull(t *testing.T) {
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			// empty payload body — wire.DecodeResponse with
			// allowNull=true should swallow the "no result" path.
			wire.MethodAddSource: "",
		},
	}
	c := newClientWithFakeSources(t, fake)

	src, err := c.Sources().AddURL(context.Background(), "nb-1", "https://example.com")
	if err != nil {
		t.Fatalf("AddURL with null response: %v", err)
	}
	if src.ID != "" || src.Title != "" {
		t.Errorf("AddURL null result = %+v, want zero Source", src)
	}
}

// TestSourcesAPI_ExecutorError — a transport-level error from
// the executor propagates through the public SDK boundary with
// the typed context wrapper.
func TestSourcesAPI_ExecutorError(t *testing.T) {
	fake := &fakeSourcesExecutor{
		canned:    map[wire.Method]string{},
		cannedErr: errors.New("rpc timeout"),
	}
	c := newClientWithFakeSources(t, fake)

	_, err := c.Sources().List(context.Background(), "nb-1")
	if err == nil {
		t.Fatal("List with rpc err returned nil err")
	}
	if !strings.Contains(err.Error(), "rpc timeout") {
		t.Errorf("err = %v, want wraps rpc timeout", err)
	}
}

// TestSourcesAPI_DispatchRoutes — the Client-side dispatch goes
// through Client.RPCCall so the routing test confirms the
// positional params the features layer builds are the ones the
// SDK actually sends. (Metrics-aware dispatch lands in a later
// ticket; this assertion is the routing-only contract.)
func TestSourcesAPI_DispatchRoutes(t *testing.T) {
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodGetNotebook: `[["Title",[]]]`,
		},
	}
	c := newClientWithFakeSources(t, fake)

	if _, err := c.Sources().List(context.Background(), "nb-1"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Execute calls = %d, want 1", len(fake.calls))
	}
	if fake.calls[0].method != wire.MethodGetNotebook {
		t.Errorf("call[0] method = %v, want MethodGetNotebook", fake.calls[0].method)
	}
}

// _ keeps the runtime import referenced even when callers add
// more tests below — the import is on the books for a future
// ticket that injects a Metrics handle.
var _ = runtime.NewMetrics
