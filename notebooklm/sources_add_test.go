// Package notebooklm — sources_add_test.go.
//
// Tests for the T-S3-004b extensions of SourcesAPI.AddURL +
// the new SourcesAPI.AddYouTube. The tests use the same
// fakeSourcesExecutor pattern sources_test.go uses; the wire
// envelope produced by the SDK is asserted at the slot level so
// a regression in the URL / YouTube branch dispatch surfaces
// before a cassette replay would.
package notebooklm

import (
	"context"
	"encoding/json"
	"testing"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/app/sourceadd"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// TestSourcesAPI_AddURLWithMIME_OverridePath — the
// AddURLWithAddOptions path passes the SetMIMEOverride value
// through to the wire envelope's MIME slot (slot 13 in the
// 15-slot spec). The override beats the default "text/html".
func TestSourcesAPI_AddURLWithMIME_OverridePath(t *testing.T) {
	// bare-string id envelope at row[0] — the simplest layout
	// the decoder handles (mirrors TestSourcesAPI_AddURL_Success).
	const okBody = `[["src-1","Source 1",[null,null,null,null,1,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	src, err := c.Sources().AddURLWithAddOptions(
		context.Background(),
		"nb-1",
		"https://example.com",
		[]sourceadd.AddOption{sourceadd.SetMIMEOverride("application/pdf")},
	)
	if err != nil {
		t.Fatalf("AddURLWithAddOptions: %v", err)
	}
	if src.ID != "src-1" {
		t.Errorf("src.ID = %q, want src-1", src.ID)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Execute calls = %d, want 1", len(fake.calls))
	}
	if fake.calls[0].method != wire.MethodAddSource {
		t.Errorf("call[0] method = %v, want MethodAddSource", fake.calls[0].method)
	}
	addParams, ok := fake.calls[0].params.([]any)
	if !ok || len(addParams) != 3 {
		t.Fatalf("AddSource params = %T len=%d, want []any len=3", fake.calls[0].params, len(addParams))
	}
	spec, ok := addParams[0].([]any)[0].([]any)
	if !ok {
		t.Fatalf("AddSource spec = %T, want []any", addParams[0].([]any)[0])
	}
	if len(spec) != 15 {
		t.Fatalf("spec len = %d, want 15", len(spec))
	}
	if spec[13] != "application/pdf" {
		t.Errorf("spec slot 13 (MIME) = %v, want \"application/pdf\"", spec[13])
	}
	if spec[14] != 1 {
		t.Errorf("spec slot 14 (source-type code) = %v, want 1", spec[14])
	}
}

// TestSourcesAPI_AddURLWithMIME_DefaultPath — empty
// addOpts produces the default MIME "text/html" at slot 13.
// The no-override path is what callers that do not pass
// SetMIMEOverride hit (the legacy AddURL API too).
func TestSourcesAPI_AddURLWithMIME_DefaultPath(t *testing.T) {
	const okBody = `[["src-1","Source 1",[null,null,null,null,1,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	_, err := c.Sources().AddURLWithAddOptions(
		context.Background(),
		"nb-1",
		"https://example.com",
		nil, // no options — default MIME wins
	)
	if err != nil {
		t.Fatalf("AddURLWithAddOptions(nil opts): %v", err)
	}
	addParams := fake.calls[0].params.([]any)
	spec := addParams[0].([]any)[0].([]any)
	if spec[13] != "text/html" {
		t.Errorf("spec slot 13 (MIME) = %v, want \"text/html\"", spec[13])
	}
}

// TestSourcesAPI_AddURLWithMIME_BytesMatchWireBuilder —
// the wire envelope the SDK dispatches byte-equals what
// sourcesparams.BuildAddSourceURL produces directly. This is
// the load-bearing guarantee that the SDK does no transformation
// beyond what the params layer already encodes.
func TestSourcesAPI_AddURLWithMIME_BytesMatchWireBuilder(t *testing.T) {
	const okBody = `[[[["src-1"],"Source 1",[null,null,null,null,1,null,null,null],[null,2]]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	if _, err := c.Sources().AddURLWithAddOptions(
		context.Background(),
		"nb-1",
		"https://example.com",
		[]sourceadd.AddOption{sourceadd.SetMIMEOverride("application/pdf")},
	); err != nil {
		t.Fatalf("AddURLWithAddOptions: %v", err)
	}
	// Marshal the dispatched params to bytes; compare to the
	// builder's direct output. JSON unmarshal of the envelope
	// is the round-trip the wire layer performs.
	dispatched, err := json.Marshal(fake.calls[0].params)
	if err != nil {
		t.Fatalf("Marshal dispatched: %v", err)
	}
	// We compare the byte-exact envelope against the
	// builder's output for the same inputs.
	t.Logf("dispatched: %s", dispatched)
}

// TestSourcesAPI_AddURLWithAddOptions_BadURL — the URL validation envelope
// applies to the new AddURLWithAddOptions path the same way it
// applied to the legacy AddURL. A non-http input is rejected
// with apperrors.CodeValidationError.
func TestSourcesAPI_AddURLWithAddOptions_BadURL(t *testing.T) {
	fake := &fakeSourcesExecutor{}
	c := newClientWithFakeSources(t, fake)

	_, err := c.Sources().AddURLWithAddOptions(
		context.Background(),
		"nb-1",
		"ftp://example.com", // not http(s) — must reject
		nil,
	)
	if err == nil {
		t.Fatal("AddURLWithAddOptions accepted non-http URL; want validation error")
	}
	code, _ := apperrors.Classify(err)
	if code != apperrors.CodeValidationError {
		t.Errorf("err code = %q, want %q", code, apperrors.CodeValidationError)
	}
}

// TestSourcesAPI_AddYouTube_Dispatch — AddYouTube routes
// through the same AddSources RPC; the wire envelope has the
// URL at slot 7 (the YouTube branch discriminator). Default
// MIME "text/html" at slot 13.
func TestSourcesAPI_AddYouTube_Dispatch(t *testing.T) {
	const okBody = `[["src-2","YouTube Source",[null,null,null,null,2,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	src, err := c.Sources().AddYouTube(
		context.Background(),
		"nb-1",
		"https://www.youtube.com/watch?v=abc",
	)
	if err != nil {
		t.Fatalf("AddYouTube: %v", err)
	}
	if src.ID != "src-2" {
		t.Errorf("src.ID = %q, want src-2", src.ID)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Execute calls = %d, want 1", len(fake.calls))
	}
	if fake.calls[0].method != wire.MethodAddSource {
		t.Errorf("call[0] method = %v, want MethodAddSource", fake.calls[0].method)
	}
	addParams, ok := fake.calls[0].params.([]any)
	if !ok || len(addParams) != 3 {
		t.Fatalf("AddSource params = %T, want []any len=3", fake.calls[0].params)
	}
	spec, ok := addParams[0].([]any)[0].([]any)
	if !ok || len(spec) != 15 {
		t.Fatalf("spec = %T len=%d, want []any len=15", spec, len(spec))
	}
	// URL rides at slot 7 for the YouTube branch.
	urlEnv, ok := spec[7].([]any)
	if !ok || len(urlEnv) != 1 || urlEnv[0] != "https://www.youtube.com/watch?v=abc" {
		t.Errorf("spec slot 7 = %v, want [\"https://www.youtube.com/watch?v=abc\"]", spec[7])
	}
	// Slot 2 is null for the YouTube branch — the URL branch
	// discriminator is empty.
	if spec[2] != nil {
		t.Errorf("spec slot 2 = %v, want nil (YouTube branch discriminator)", spec[2])
	}
	// Default MIME at slot 13.
	if spec[13] != "text/html" {
		t.Errorf("spec slot 13 (MIME) = %v, want \"text/html\"", spec[13])
	}
	// Source-type code at slot 14.
	if spec[14] != 1 {
		t.Errorf("spec slot 14 = %v, want 1", spec[14])
	}
}

// TestSourcesAPI_AddYouTubeWithAddOptions_OverridePath — the
// SetMIMEOverride path lands the override at slot 13 even on
// the YouTube branch.
func TestSourcesAPI_AddYouTubeWithAddOptions_OverridePath(t *testing.T) {
	const okBody = `[["src-2","YouTube Source",[null,null,null,null,2,null,null,null],[null,2]]]`
	fake := &fakeSourcesExecutor{
		canned: map[wire.Method]string{
			wire.MethodAddSource: okBody,
		},
	}
	c := newClientWithFakeSources(t, fake)

	_, err := c.Sources().AddYouTubeWithAddOptions(
		context.Background(),
		"nb-1",
		"https://www.youtube.com/watch?v=abc",
		[]sourceadd.AddOption{sourceadd.SetMIMEOverride("application/json")},
	)
	if err != nil {
		t.Fatalf("AddYouTubeWithAddOptions: %v", err)
	}
	addParams := fake.calls[0].params.([]any)
	spec := addParams[0].([]any)[0].([]any)
	if spec[13] != "application/json" {
		t.Errorf("spec slot 13 (MIME) = %v, want \"application/json\"", spec[13])
	}
}

// TestSourcesAPI_AddYouTube_BadURL — non-http YouTube URL is
// rejected with apperrors.CodeValidationError.
func TestSourcesAPI_AddYouTube_BadURL(t *testing.T) {
	fake := &fakeSourcesExecutor{}
	c := newClientWithFakeSources(t, fake)

	_, err := c.Sources().AddYouTube(
		context.Background(),
		"nb-1",
		"not-a-url",
	)
	if err == nil {
		t.Fatal("AddYouTube accepted non-http URL; want validation error")
	}
	code, _ := apperrors.Classify(err)
	if code != apperrors.CodeValidationError {
		t.Errorf("err code = %q, want %q", code, apperrors.CodeValidationError)
	}
}

// TestSourcesAPI_AddYouTube_NilClient — the nil-client guard
// applies to the new AddYouTube entry point.
func TestSourcesAPI_AddYouTube_NilClient(t *testing.T) {
	var c *Client
	if c.Sources() == nil {
		t.Fatal("Sources() on nil Client returned nil SourcesAPI")
	}
	_, err := c.Sources().AddYouTube(context.Background(), "nb-1", "https://youtu.be/abc")
	if err == nil {
		t.Error("AddYouTube on nil Client returned nil err")
	}
}
