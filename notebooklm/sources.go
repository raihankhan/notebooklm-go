// Package notebooklm — sources.go: public SDK root for the Sources
// namespace.
//
// SourcesAPI is the typed view of one notebook's source list and the
// canonical entry point for adding a URL source. The two methods
// (`List`, `AddURL`) are the minimum-viable surface T-P6-1 ships;
// the rest of the source lifecycle (AddText, AddDrive, AddYouTube,
// Delete, Rename, Refresh, GetFulltext, …) lands in later phase
// tickets as additional methods on the same struct.
//
// Per AGENTS.md rule 5 the public SDK is mode=internal; the wire
// shapes live under `internal/web/params` and the typed decoders
// under `internal/web/rows`. SourcesAPI never imports encoding/json
// or any third-party library — the JSON adapter (`internal/web/
// wire`) is the single point of contact for the wire payload.
//
// Port of notebooklm-py::SourcesAPI in
// notebooklm-py/src/notebooklm/_sources.py — the Python class
// composes the URL add / list / rename / poll surface from the
// `_web/sources/` and `_web/rows/sources.py` primitives; the Go
// port mirrors that composition.
package notebooklm

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/raihankhan/notebooklm-go/internal/app/sourceadd"
	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/web/features/sources"
	sourcesparams "github.com/raihankhan/notebooklm-go/internal/web/params/sources"
	"github.com/raihankhan/notebooklm-go/internal/web/rows"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// SourcesAPI is the typed namespace for one notebook's sources.
//
// One SourcesAPI is bound to one Client; constructing it through
// `Client.Sources()` returns a value that shares the Client's
// Executor (so every method funnels through the same transport
// stack — supervisor admission, idempotency registry, refresh
// ladder, metrics).
//
// The struct is a thin wrapper around the features-layer
// `features.sources.Caller` seam — `Client.sourcesCaller` returns a
// value that implements that interface. SourcesAPI is safe for
// concurrent use because it shares the Client's thread-safety
// guarantees.
type SourcesAPI struct {
	client *Client
}

// SourcesOption tunes one SourcesAPI method call. The functional-
// options pattern lets later phase tickets grow the surface
// (filtering, sort, batch size, …) without breaking call sites that
// pass zero options today.
type SourcesOption func(*SourcesOptions)

// SourcesOptions is the per-call option bundle SourcesOption
// mutates. The zero value is the default; every method that accepts
// options must document what zero means in its own docstring.
//
// T-P6-1 owns only the List-flags slot (MaxItems); later tickets
// extend this with statuses / types / batch-size flags mirroring
// `_web/sources/listing.py::SourceLister.list` filters.
type SourcesOptions struct {
	// List flags.
	MaxItems int
}

// WithSourcesMaxItems caps the number of rows the List call returns.
// A non-positive value means "no cap". Applied client-side after
// the wire decode — the wire itself does not paginate today (the
// live GET_NOTEBOOK returns the full source list in one envelope).
//
// Mirrors NotebooksAPI.WithMaxItems; the two options live as
// separate per-namespace functions so each namespace's option bag
// stays self-contained. The public package cannot reuse the
// literal `WithMaxItems` name because Go's identifier namespace is
// per-package and NotebooksAPI.WithMaxItems (T-P5-5) already
// occupies it; the SourcesAPI counterpart disambiguates with the
// `WithSources…` prefix to keep call sites unambiguous.
func WithSourcesMaxItems(n int) SourcesOption {
	return func(o *SourcesOptions) {
		o.MaxItems = n
	}
}

// Sources returns the SourcesAPI for this Client. The returned value
// is bound to the Client — every method funnels through the same
// Executor / Supervisor / Registry stack the Client already owns.
//
// Calling Sources on a nil Client returns a SourcesAPI bound to nil;
// every method on it returns an error without dispatching an RPC.
// Mirrors the nil-tolerant `Client.Close()` pattern.
func (c *Client) Sources() *SourcesAPI {
	return &SourcesAPI{client: c}
}

// List returns the typed source list for one notebook, in backend-
// defined order. The backing RPC is `GetNotebook` (`rLM1Ne`) — the
// read path that surfaces the source list at `notebook[0][1]`. See
// `_web/sources/listing.py::SourceLister.list` for the Python
// original.
//
// The wire envelope is a single-element wrapper whose first
// element is the row list (`[[row1, row2, ...]]`); we unwrap
// through the features-layer decoder so a malformed envelope
// surfaces a *wire.ShapeDriftError rather than fabricating empty-id
// sources (the documented failure mode in
// `_web/sources/listing.py::list`).
//
// `opts.MaxItems > 0` truncates the page client-side after the
// decode; the wire itself does not paginate.
func (a *SourcesAPI) List(ctx context.Context, notebookID string, opts ...SourcesOption) (Page[Source], error) {
	if a == nil || a.client == nil {
		return Page[Source]{}, errors.New("notebooklm: SourcesAPI: nil client")
	}
	if err := ctx.Err(); err != nil {
		return Page[Source]{}, err
	}
	if err := validateNotebookIDStrict(notebookID); err != nil {
		return Page[Source]{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	var options SourcesOptions
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&options)
	}

	// Funnel through the features layer so the wire shape lives
	// under internal/web/params and the typed decoder lives under
	// internal/web/rows — the same composition pattern
	// features/notebooks.go uses.
	srcRows, err := sources.List(ctx, a.client.sourcesCaller(), notebookID)
	if err != nil {
		return Page[Source]{}, fmt.Errorf("notebooklm: SourcesAPI.List: %w", err)
	}
	if options.MaxItems > 0 && len(srcRows) > options.MaxItems {
		srcRows = srcRows[:options.MaxItems]
	}
	// Project the internal rows.Source view into the public Source
	// type so the SDK does not leak the internal row shape. Field-
	// by-field copy keeps the public surface independent of any
	// future widening of rows.Source.
	items := make([]Source, len(srcRows))
	for i, r := range srcRows {
		items[i] = Source{
			ID:          r.ID,
			Title:       r.Title,
			Kind:        r.Kind,
			StatusLabel: r.StatusLabel,
		}
	}
	return Page[Source]{Items: items}, nil
}

// AddURL adds a regular URL source to a notebook and returns the
// typed view of the freshly added source. The backing RPC is
// `AddSources` (`izAoDd`) — see `_web/sources/add.py::SourceAdd
// Service.add_url_source` for the Python original.
//
// The transport-level surface is the same probe-then-create pattern
// the Python `SourceAddService.add_url` runs: the create is issued
// with internal retries disabled (the SDK's transport layer honors
// that flag), and a 5xx / network failure that may have committed
// server-side is followed by a probe for the committed source
// before any retry. Phase 6 ships the minimum-viable surface
// (create + decode the row); the probe-then-create probe lands in
// a later ticket alongside the rest of the idempotency-coverage
// work.
//
// The URL is validated client-side before dispatch — a non-empty,
// non-whitespace, `http`-prefixed string — so a malformed URL
// surfaces as a typed *apperrors validation envelope rather than a
// 5xx from the backend.
//
// The signature accepts two variadic option slices:
//
//   - `SourcesOption` — the SDK-local options (WithSourcesMaxItems
//     for List, etc.). Reserved for AddURL callers that need them
//     in a future ticket; no SDK-local option currently applies
//     to AddURL.
//   - `sourceadd.AddOption` — the application-layer options,
//     currently `SetMIMEOverride(string)`. The MIME envelope is
//     forwarded into the wire spec at slot 10 so a CLI
//     `--mime-type` flag lands on the envelope the backend reads.
//
// The two variadics are kept separate because the sourceadd
// option belongs to the application-layer vocabulary (CLI
// flag → MIME override), while SourcesOption belongs to the
// SDK-local vocabulary (filtering, batch size, …). A future
// ticket that wants to fold them can switch the signature to
// `...Option` with a sealed interface, but until the option
// sets grow the two-variadic seam keeps each call site
// readable.
func (a *SourcesAPI) AddURL(ctx context.Context, notebookID string, rawURL string, opts ...SourcesOption) (Source, error) {
	return a.addURL(ctx, notebookID, rawURL, nil, opts...)
}

// AddURLWithAddOptions is the T-S3-004b extension of AddURL that
// accepts the application-layer `sourceadd.AddOption` slice. The
// two-sig split lets the CLI / MCP / REST adapter pass a MIME
// override (via SetMIMEOverride) without forcing every other
// caller to import the sourceadd package.
//
// Behaviour-wise AddURLWithAddOptions is identical to AddURL when
// addOpts is nil or empty — the AddOption slice is purely an
// extension seam.
func (a *SourcesAPI) AddURLWithAddOptions(ctx context.Context, notebookID string, rawURL string, addOpts []sourceadd.AddOption, opts ...SourcesOption) (Source, error) {
	return a.addURL(ctx, notebookID, rawURL, addOpts, opts...)
}

// addURL is the shared implementation behind AddURL +
// AddURLWithAddOptions. The two-arg-split is internal so the
// public API stays one-method-per-shape.
//
// The MIME inference rule: sourceadd.InferMIMEWithOverride
// returns "text/html" for KindURL by default; the explicit
// override (via SetMIMEOverride) replaces it. The wire builder
// (`sourcesparams.BuildAddSourceURL`) reads the resolved MIME
// into slot 10 of the spec.
//
// The URL is validated through sourcesparams.ValidateURL rather
// than params.ValidateURL — both helpers share the same rule
// (non-empty, non-whitespace, http-prefixed), but the sources
// package's helper is the canonical seam for the T-S3-004b
// surface. The two helpers stay in lockstep so a future
// relaxation (e.g. percent-encoded spaces) lands in both.
func (a *SourcesAPI) addURL(ctx context.Context, notebookID, rawURL string, addOpts []sourceadd.AddOption, opts ...SourcesOption) (Source, error) {
	if a == nil || a.client == nil {
		return Source{}, errors.New("notebooklm: SourcesAPI: nil client")
	}
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	if err := validateNotebookIDStrict(notebookID); err != nil {
		return Source{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	if err := sourcesparams.ValidateURL(rawURL); err != nil {
		// Wrap in the typed ValidationError envelope so adapters
		// can branch on apperrors.Classify uniformly.
		return Source{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	var options SourcesOptions
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&options)
	}
	var addOptions sourceadd.AddOptions
	for _, opt := range addOpts {
		if opt == nil {
			continue
		}
		opt(&addOptions)
	}

	mime := sourceadd.InferMIMEWithOverride(sourceadd.KindURL, rawURL, addOptions.MIMEOverride)

	src, err := sources.AddURLWithMIME(ctx, a.client.sourcesCaller(), notebookID, rawURL, mime)
	if err != nil {
		return Source{}, fmt.Errorf("notebooklm: SourcesAPI.AddURL: %w", err)
	}
	// Convert internal rows.Source to the public Source type. The
	// two structs share the same field set today (the public type
	// is a typed alias of the row view); the conversion is a
	// field-by-field copy so a future widening of the public
	// surface does not silently change row semantics.
	return Source{
		ID:          src.ID,
		Title:       src.Title,
		Kind:        src.Kind,
		StatusLabel: src.StatusLabel,
	}, nil
}

// AddYouTube adds a YouTube URL or short-id source to a notebook
// and returns the typed view of the freshly added source. The
// backing RPC is `AddSources` (`izAoDd`) — see
// `_web/sources/add.py::SourceAddService.add_youtube_source` for
// the Python original.
//
// The URL is validated client-side before dispatch — a non-empty,
// non-whitespace, `http`-prefixed string. The YouTube wire
// envelope rides at source-spec slot 7 (vs. slot 2 for the URL
// branch) — see `sourcesparams.BuildAddSourceYouTube`.
//
// The signature mirrors AddURL's two-variadic seam (SourcesOption
// for SDK-local options, sourceadd.AddOption for application-
// layer options like SetMIMEOverride). The two methods share the
// MIME inference rule (InferMIMEWithOverride for KindYouTube
// returns "text/html" by default).
//
// YouTube URLs come in two shapes the backend accepts:
//
//   - watch URLs: https://(www.|m.|music.)?youtube.com/watch?v=<id>
//   - short URLs: https://youtu.be/<id>
//
// The wire envelope accepts both shapes — the backend's own
// YouTube handler normalises the URL before ingest. The SDK
// does no client-side extraction; a caller that has only the
// video id (not a URL) should pass `https://youtu.be/<id>` so
// the wire envelope is canonical.
//
// Per AGENTS.md rule 1 the wire shape mirrors the Python
// `_web/sources/add.py::add_youtube_source` literal — a future
// schema drift surfaces as a *wire.ShapeDriftError at the
// features layer, not a silently-decoded URL.
func (a *SourcesAPI) AddYouTube(ctx context.Context, notebookID string, rawURL string, opts ...SourcesOption) (Source, error) {
	if a == nil || a.client == nil {
		return Source{}, errors.New("notebooklm: SourcesAPI: nil client")
	}
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	if err := validateNotebookIDStrict(notebookID); err != nil {
		return Source{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	if err := sourcesparams.ValidateYouTubeURL(rawURL); err != nil {
		return Source{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	var options SourcesOptions
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&options)
	}

	mime := sourceadd.InferMIMEWithOverride(sourceadd.KindYouTube, rawURL, "")

	src, err := sources.AddYouTube(ctx, a.client.sourcesCaller(), notebookID, rawURL, mime)
	if err != nil {
		return Source{}, fmt.Errorf("notebooklm: SourcesAPI.AddYouTube: %w", err)
	}
	return Source{
		ID:          src.ID,
		Title:       src.Title,
		Kind:        src.Kind,
		StatusLabel: src.StatusLabel,
	}, nil
}

// AddYouTubeWithAddOptions is the AddURLWithAddOptions counterpart
// for the YouTube branch. It carries the same `sourceadd.AddOption`
// variadic so a CLI `--mime-type` flag on the YouTube source-add
// path lands on the wire envelope slot 10.
//
// Behaviour is identical to AddYouTube when addOpts is nil or
// empty.
func (a *SourcesAPI) AddYouTubeWithAddOptions(ctx context.Context, notebookID string, rawURL string, addOpts []sourceadd.AddOption, opts ...SourcesOption) (Source, error) {
	if a == nil || a.client == nil {
		return Source{}, errors.New("notebooklm: SourcesAPI: nil client")
	}
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	if err := validateNotebookIDStrict(notebookID); err != nil {
		return Source{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	if err := sourcesparams.ValidateYouTubeURL(rawURL); err != nil {
		return Source{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	var options SourcesOptions
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&options)
	}
	var addOptions sourceadd.AddOptions
	for _, opt := range addOpts {
		if opt == nil {
			continue
		}
		opt(&addOptions)
	}

	mime := sourceadd.InferMIMEWithOverride(sourceadd.KindYouTube, rawURL, addOptions.MIMEOverride)

	src, err := sources.AddYouTube(ctx, a.client.sourcesCaller(), notebookID, rawURL, mime)
	if err != nil {
		return Source{}, fmt.Errorf("notebooklm: SourcesAPI.AddYouTube: %w", err)
	}
	return Source{
		ID:          src.ID,
		Title:       src.Title,
		Kind:        src.Kind,
		StatusLabel: src.StatusLabel,
	}, nil
}

// Source is the public SDK view of one decoded source row. Mirrors
// rows.Source one-for-one for the four fields T-P6-1 ships; the
// alias keeps the public surface separate from the internal row
// type so a future widening of the row decoder (drive_document_id,
// mime, …) does not force a public API break — the public SDK can
// grow Source independently and the row decoder stays an internal
// detail.
//
// Per AGENTS.md rule 5 the public SDK is mode=internal, so we
// re-declare the field set rather than import the internal row
// type. The trade-off is one struct copy per row; on a poll loop
// the cost is invisible next to the network round-trip.
type Source struct {
	ID          string
	Title       string
	Kind        string
	StatusLabel string
}

// sourcesCaller returns the features-layer Caller seam bound to
// this Client. The returned adapter funnels every SourcesAPI
// method through the Client's Executor so the supervisor /
// idempotency-registry / refresh-ladder / metrics surfaces all
// see the same dispatch path.
//
// Kept unexported because the Caller interface lives in
// internal/web/features/sources and the public SDK must not
// expose it.
func (c *Client) sourcesCaller() *clientCaller {
	return &clientCaller{client: c}
}

// clientCaller is the small adapter that turns a Client into a
// features-layer Caller. Implements the Caller interface
// (`features/sources.Caller`) by delegating to Client.RPCCall.
//
// The adapter is intentionally minimal: it does no caching, no
// retries, no envelope-unwrap. Each method routes one logical RPC
// through Client.RPCCall and surfaces the result. The features
// layer (sources.List / sources.AddURL) owns the envelope unwrap.
type clientCaller struct {
	client *Client
}

// Call implements the features.sources.Caller seam.
//
// method is the symbolic RPC name (e.g. wire.MethodGetNotebook);
// params is the positional payload the wire layer encodes; sourcePath
// is the notebook route path (e.g. "/notebook/nb-1"); allowNull
// controls whether a null result is treated as success (true) or
// raised as a typed error (false).
//
// The caller owns the wire decode step: it routes through
// Client.RPCCall (so the supervisor admission / idempotency
// registry / metrics counters all see the dispatch) and then
// funnels the raw bytes through wire.DecodeResponse +
// wire.Unmarshal so the features layer sees a typed `any`
// payload it can run rows.DecodeSource on directly. Keeping the
// decode here (rather than re-running it inside features/sources)
// satisfies rule 3: only this caller + the wire package touch
// encoding/json.
func (cc *clientCaller) Call(ctx context.Context, method wire.Method, params any, sourcePath string, allowNull bool) (any, error) {
	if cc == nil || cc.client == nil {
		return nil, errors.New("notebooklm: SourcesAPI: nil client caller")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := cc.client.RPCCall(ctx, method, params)
	if err != nil {
		return nil, err
	}
	body, ok := res.([]byte)
	if !ok {
		return nil, fmt.Errorf("notebooklm: SourcesAPI caller: rpc body not []byte: %T", res)
	}
	resp, err := wire.DecodeResponse(body, string(method), allowNull)
	if err != nil {
		return nil, err
	}
	if len(resp.Payload) == 0 {
		// Empty payload (with allowNull swallowing the ErrEmptyResult
		// path) surfaces as nil so the features layer's "no rows"
		// branches kick in.
		return nil, nil
	}
	var payload any
	if err := wire.Unmarshal(resp.Payload, &payload); err != nil {
		return nil, fmt.Errorf("notebooklm: SourcesAPI caller: unmarshal: %w", err)
	}
	return payload, nil
}

// validateNotebookIDStrict is the SourcesAPI-specific input guard.
// It wraps rows.ValidateNotebookID (the shared whitelist) and adds
// the URL-shape rejection that the SourcesAPI surface commits to in
// the ticket body — a notebook id must be opaque (no scheme, no
// path) so a caller passing a URL-shaped string sees a typed
// validation error rather than a backend 404 on the resource path
// the URL represents.
//
// Per AGENTS.md rule 5 we keep this helper private to the SDK root
// so the public surface is the only place a caller input guard
// surfaces.
func validateNotebookIDStrict(id string) error {
	if err := rows.ValidateNotebookID(id); err != nil {
		return err
	}
	if u, err := url.Parse(id); err == nil && u.Scheme != "" {
		return &sourceValidateError{Field: "notebook_id", Reason: "must not be URL-shaped"}
	}
	return nil
}

// sourceValidateError is the typed error validateNotebookIDStrict
// returns when the URL-shape rule fires. The List / AddURL entry
// points wrap it in the apperrors.Validation envelope so adapters
// can branch on apperrors.Classify uniformly.
type sourceValidateError struct {
	Field  string
	Reason string
}

func (e *sourceValidateError) Error() string {
	return "notebooklm: " + e.Field + " " + e.Reason
}

// rows re-export anchor — keeps the rows import live even when the
// features layer short-circuits before touching the rows package
// (the file would otherwise fail `goimports` cleanup for unused
// symbols after a future refactor that drops the rows reference).
var _ = rows.Source{}
