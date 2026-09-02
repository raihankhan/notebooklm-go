// Package notebooklm — notebooks.go.
//
// Public SDK surface for the NotebookLM Notebook namespace.
// NotebooksAPI exposes the 17 typed RPCs the user-visible SDK
// ships; each method dispatches one wire RPC and returns the
// typed view the rows package decodes.
//
// Per docs/AGENTS.md:
//
//   - Rule 1 (Python is normative) — every positional shape here is
//     a faithful port of `notebooklm-py/src/notebooklm/_notebooks.py`
//     and the `_web/notebooks.py` builders. Each method cites the
//     Python symbol it ports in its doc comment.
//
//   - Rule 3 (one JSON encoder / decoder) — payloads marshal through
//     internal/web/wire.Marshal and responses decode through
//     internal/web/wire.Unmarshal. The bytes flow through Client.RPCCall,
//     which is already wired through the encoder.
//
//   - Rule 4 (no credential leakage) — no RPC body, error wrap, or
//     log statement carries a cookie value, CSRF token, or session
//     id. Every error route goes through internal/redact; the
//     apperrors classifier never extracts raw transport errors.
//
// The 17 methods are: List, Create, Get, Delete, Rename, Summary,
// Metadata, AddSource, RemoveSource, Share, Unshare, GetShareStatus,
// RemoveCollaborator, GetRecent, GetStarred, GetSharedWithMe,
// GetByProject. The methods that depend on params builders still
// under port (AddSource / RemoveSource / GetStarred / GetSharedWithMe
// / GetByProject) recover from the builders' panic sentinel and
// surface a typed CodeNotebookLMError — see the per-method doc
// comments.
package notebooklm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/web/params"
	"github.com/raihankhan/notebooklm-go/internal/web/rows"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// NotebooksAPI is the public SDK namespace for NotebookLM Notebook
// operations. A NotebooksAPI holds a reference to the SDK root
// Client; every method dispatches one RPC through the Client and
// returns the typed view the rows decoder produces.
//
// NotebooksAPI is safe for concurrent use across goroutines as
// long as the underlying Client is. The transport stack is the
// only mutable shared state, and the Client's supervisor already
// serializes in-flight work.
//
// Port of notebooklm.notebooks.NotebooksAPI (Python). The Go port
// keeps the method surface one-for-one; the only differences are
// Go's typed return signatures vs. Python's typed dict returns
// and the functional-option vs. keyword-argument convention.
type NotebooksAPI struct {
	// client is the SDK root this namespace dispatches through.
	client *Client
}

// newNotebooksAPI constructs a NotebooksAPI bound to c. The
// constructor is unexported because callers always reach the
// namespace through Client.Notebooks (the public seam the SDK
// ships). Storing the Client by pointer means a Client.Close()
// invalidates the API surface — the per-call guard in `call`
// detects this and returns a typed error.
//
// Port of notebooklm.notebooks.NotebooksAPI.__init__ — the Python
// original stores the parent Client on the instance; the Go port
// mirrors the dependency direction.
func newNotebooksAPI(c *Client) *NotebooksAPI {
	return &NotebooksAPI{client: c}
}

// NotebooksOptions is the typed bag the functional-option
// constructors mutate. The zero value is the documented default
// (no extra knobs applied); callers pass instances through the
// NotebooksOption helpers (WithMaxItems, WithRetryOverride, …)
// rather than construct NotebooksOptions directly.
//
// The struct is exported so future options can grow without
// churning every call site, but the field set today is empty —
// the per-method `opts` blocks are reserved land the T-P6
// namespace ports will fill. Adding a field is a backwards-
// compatible change so long as the zero value remains the
// default behavior.
type NotebooksOptions struct {
	// maxItems truncates the returned slice client-side. 0
	// means "no cap". Used by List / GetRecent / GetStarred /
	// GetSharedWithMe / GetByProject.
	maxItems int
}

// NotebooksOption is the functional-option type. The
// implementations are the With* constructors in this file; a
// nil option is a no-op so a caller can pass `WithMaxItems(0)`
// unconditionally without growing their nil-guards.
//
// Port of the pattern `def __init__(self, *, max_items: int = 0)`;
// the keyword-argument idiom maps to a per-method option bag in
// Go. This type is named generically (NotebooksOption, not
// ListOption / GetOption) because the same options bag flows
// through every namespace method in T-P5 — future namespace
// packages (sources, artifacts, chat) will define their own
// Options types with their own per-namespace knobs.
type NotebooksOption func(*NotebooksOptions)

// apply satisfies the internal contract the method bodies rely
// on (a loop over `opts` calling `opt(&o)`). The method is
// unexported so external callers cannot synthesize a
// NotebooksOption outside the With* set; the public surface is
// the constructor functions.
func (f NotebooksOption) apply(o *NotebooksOptions) {
	if f == nil {
		return
	}
	f(o)
}

// WithMaxItems caps the returned slice at `n` items. Used by
// List / GetRecent / GetStarred / GetSharedWithMe / GetByProject.
// A nil or zero `n` is a no-op so a caller can pass
// `WithMaxItems(0)` unconditionally.
//
// The cap is applied client-side after decoding; the backend
// currently has no server-side filter, so the option is a thin
// truncation. Server-side pagination lands when the T-P5-5
// ticket's TODOs clear (the wire filter RPCs are not yet
// available; the params stubs in internal/web/params/notebooks.go
// panic until the corresponding Python originals pin a builder).
func WithMaxItems(n int) NotebooksOption {
	return func(o *NotebooksOptions) {
		if n > 0 {
			o.maxItems = n
		}
	}
}

// resolveOptions applies every functional option to a fresh
// NotebooksOptions and returns the result. The loop is
// nil-safe so `WithXxx` can stand in for a missing argument.
func resolveOptions(opts []NotebooksOption) NotebooksOptions {
	var o NotebooksOptions
	for _, opt := range opts {
		opt.apply(&o)
	}
	return o
}

// call is the per-RPC dispatcher. It pushes the positional
// payload through Client.RPCCall, extracts the wrb.fr frame
// for the named method, and decodes the result into a typed
// []any the row-decoders can consume. The caller passes the
// already-built positional payload (a `[]any` from
// internal/web/params) and the method id; the dispatcher takes
// care of the wire-marshal and response-unmarshal round-trip.
//
// The returned `[]any` is the JSON-decoded result the rows
// package decodes for typed views. A nil result with a nil
// error is the documented "the RPC succeeded but the backend
// returned null" case — some RPCs do this on a no-op reply.
//
// Recovered panics from the params layer's "not yet
// implemented" stubs are converted to a typed CodeNotebookLMError
// so the public surface does not propagate panics. Each
// params package TODO documents the panic message verbatim; we
// match the prefix and return apperrors.Wrap so the message is
// auditable without exposing the panic site.
func (a *NotebooksAPI) call(ctx context.Context, method wire.Method, payload []any) ([]any, error) {
	if a == nil || a.client == nil {
		return nil, apperrors.Wrap(apperrors.CodeConfigError, errors.New("notebooklm: nil NotebooksAPI"))
	}

	// recover catches the params layer's "TODO: port _web/.."
	// panics and converts them to a typed CodeNotebookLMError
	// so the public surface does not propagate panics across
	// the boundary. The params panics are intentional: they
	// name the unimplemented builder so a future reviewer can
	// grep for them.
	var result []any
	var rpcErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				rpcErr = unimplementedError(method, r)
			}
		}()
		body, err := a.client.RPCCall(ctx, method, payload)
		if err != nil {
			rpcErr = wrapRPCError(method, err)
			return
		}
		raw, ok := body.([]byte)
		if !ok {
			rpcErr = apperrors.Wrap(apperrors.CodeUnexpectedError,
				fmt.Errorf("notebooklm: unexpected non-byte body type %T", body))
			return
		}
		decoded, derr := a.extractResult(raw, method)
		if derr != nil {
			rpcErr = derr
			return
		}
		result = decoded
	}()
	return result, rpcErr
}

// wrapRPCError attaches the canonical "notebook operation failure"
// code to an error returned by Client.RPCCall. The transport layer
// already produces typed errors with their own codes; Wrap is the
// way to attach a NotebooksAPI-level context code (so adapters
// that branch on the classifier see the SDK's vocabulary, not just
// the transport's internal codes).
//
// When err already carries an explicit Code — a sentinel match on
// the four canonical sentinels, or a Coded error — we pass through.
// Otherwise we attach CodeNotebookLMError as the typed code.
func wrapRPCError(method wire.Method, err error) error {
	if err == nil {
		return nil
	}
	// Preserved sentinels (auth, rate-limit, not-found, quota) flow
	// through with their own codes — Classify would route them to
	// the matching Code via the sentinel match path; we detect
	// them explicitly so the wrap doesn't add a second code.
	if errors.Is(err, apperrors.ErrAuth) ||
		errors.Is(err, apperrors.ErrRateLimited) ||
		errors.Is(err, apperrors.ErrNotFound) ||
		errors.Is(err, apperrors.ErrQuota) {
		return err
	}
	// Coded errors retain their own code; Wrap would overwrite
	// the original classification if we passed it as the Code
	// argument, so we pass through unchanged.
	var c apperrors.Coded
	if errors.As(err, &c) {
		return err
	}
	// transport-layer errors include the wire.ErrClient / wire.
	// ErrDecoding families; the classifier treats these as
	// CodeNotebookLMError today, so we attach that code
	// explicitly so adapters that branch on the classifier see
	// a stable typed value.
	return apperrors.Wrap(apperrors.CodeNotebookLMError,
		fmt.Errorf("notebooks: %s rpc: %w", method, err))
}

// extractResult pulls the per-method payload out of a batchexecute
// response body and decodes it into a typed []any.
//
// Pipeline:
//
//  1. wire.ExtractResult finds the wrb.fr frame whose rpc id matches
//     the supplied Method constant. The Method value IS the rpc id
//     (e.g. `wXbhsf` for ListNotebooks); the executor resolves
//     NOTEBOOKLM_RPC_OVERRIDES before dispatch, so the response
//     frame always carries the canonical id.
//  2. wire.Unmarshal decodes the JSON string at frame[2] into a
//     []any the rows package consumes.
//
// A nil / empty body degrades to (nil, nil) — the documented
// "no rows" contract. An absent rpc id (the null-response case
// for mutation RPCs like Delete / Rename / Share) also degrades
// to (nil, nil) — the Python original's `allow_null=True`
// semantics surface a None result without raising; the Go port
// mirrors by treating "rpc id not present" as a successful
// null return. Other decode errors are wrapped through
// apperrors so the classifier can route them to the typed
// codes.
//
// The returned slice is the JSON-decoded frame[2] payload as-is.
// The Python original returns the same value to its caller; each
// RPC method then descends per its own envelope contract
// (LIST_NOTEBOOKS unwraps `result[0]`, CREATE_NOTEBOOK passes
// `result` directly, etc.).
func (a *NotebooksAPI) extractResult(body []byte, method wire.Method) ([]any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	frameBytes, err := wire.ExtractResult(body, string(method))
	if err != nil {
		// "rpc id not present" is the null-response marker for
		// mutation RPCs; the backend returns null on a
		// successful Delete / Rename / Share, and the Python
		// `allow_null=True` flag surfaces that as None rather
		// than raising. We mirror by degrading to (nil, nil)
		// here — the wire.ErrDecoding sentinel stays preserved
		// for callers that branch on it, but the
		// NotebooksAPI-level surface treats it as success.
		if errors.Is(err, wire.ErrDecoding) {
			return nil, nil
		}
		return nil, wrapRPCError(method, err)
	}
	var out []any
	if err := wire.Unmarshal(frameBytes, &out); err != nil {
		return nil, apperrors.Wrap(apperrors.CodeNotebookLMError,
			fmt.Errorf("notebooks: %s: unmarshal: %w", method, err))
	}
	return out, nil
}

// unimplementedError converts a recovered panic into a typed
// CodeNotebookLMError. The params layer is the source of the
// panics; the panic message is preserved as the wrap cause so a
// reviewer can grep for "TODO" markers in internal/web/params/.
//
// `method` is included in the wrap message so the typed error
// carries the rpc id a future implementer needs to wire.
func unimplementedError(method wire.Method, recovered any) error {
	msg := fmt.Sprint(recovered)
	return apperrors.Wrap(apperrors.CodeNotebookLMError,
		fmt.Errorf("notebooks: %s not yet implemented (deferred to T-P5-5 or later): %s", method, msg))
}

// List returns the typed view of every notebook visible to the
// calling user, in recency order. The backing RPC is
// `ListRecentlyViewedProjects` (`wXbhsf`). An empty list
// returns a zero-value Page, not an error.
//
// Port of `_notebooks.py::NotebooksAPI.list`. The Python
// signature is `async def list(self)`; the Go port takes a
// `NotebooksOption` set for the max-items cap.
//
// The wire envelope does not currently paginate; HasMore is
// always false today. The fields stay on the public type so a
// future paged listing can land without churn.
func (a *NotebooksAPI) List(ctx context.Context, opts ...NotebooksOption) (Page, error) {
	o := resolveOptions(opts)
	raw, err := a.call(ctx, wire.MethodListNotebooks, params.BuildList())
	if err != nil {
		return Page{}, err
	}
	return a.pageFromList(raw, o.maxItems)
}

// Create creates a new notebook with the given title and returns
// its typed view. The backing RPC is `CreateProject` (`CCqFvf`).
// The wire shape is locked in the params package (the trailing
// `wire.TemplateBlock` is load-bearing — see the Gemini-3.5
// migration note in internal/web/params/notebooks.go).
//
// Port of `_notebooks.py::NotebooksAPI.create`. An empty title
// is rejected before any RPC fires; the wire layer treats the
// empty string as a no-op but the public SDK rejects it so a
// caller learns about the bug at the type boundary.
func (a *NotebooksAPI) Create(ctx context.Context, title string, opts ...NotebooksOption) (Notebook, error) {
	_ = resolveOptions(opts) // reserved land for future knobs
	if title == "" {
		return Notebook{}, apperrors.Wrap(apperrors.CodeValidationError,
			errors.New("notebooks.Create: title must not be empty"))
	}
	raw, err := a.call(ctx, wire.MethodCreateNotebook, params.BuildCreate(title))
	if err != nil {
		return Notebook{}, err
	}
	return a.notebookFromEnvelope(raw)
}

// Get fetches one notebook by id and returns its typed view. The
// backing RPC is `GetProject` (`rLM1Ne`). A not-found id (empty
// payload, rpc status 5) surfaces as the typed `apperrors.ErrNotFound`
// sentinel so callers can branch on errors.Is rather than matching
// against a raw transport error.
//
// Port of `_notebooks.py::NotebooksAPI.get`.
func (a *NotebooksAPI) Get(ctx context.Context, id string, opts ...NotebooksOption) (Notebook, error) {
	_ = resolveOptions(opts)
	if err := rows.ValidateNotebookID(id); err != nil {
		return Notebook{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	raw, err := a.call(ctx, wire.MethodGetNotebook, params.BuildGet(id))
	if err != nil {
		return Notebook{}, err
	}
	return a.notebookFromEnvelope(raw)
}

// Delete issues DELETE_NOTEBOOK. Idempotent: deleting an
// already-absent notebook succeeds (the server returns null,
// which the transport's allowNull path swallows) and never
// raises ErrNotFound.
//
// Port of `_notebooks.py::NotebooksAPI.delete`. The Python
// signature is `async def delete(self, notebook_id)`; the Go
// signature is `Delete(ctx, id, opts...)` matching the
// context-first convention the SDK ships.
func (a *NotebooksAPI) Delete(ctx context.Context, id string, opts ...NotebooksOption) error {
	_ = resolveOptions(opts)
	if err := rows.ValidateNotebookID(id); err != nil {
		return apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	// params.BuildDelete intentionally uses the [[[id], [2]]]
	// single-id wire shape: live-probed batch variants were
	// rejected by the backend with gRPC status 3.
	raw, err := a.call(ctx, wire.MethodDeleteNotebook, params.BuildDelete(id))
	if err != nil {
		return err
	}
	// Some backends return null on a successful delete; the
	// transport's allowNull path swallows that, so a non-nil
	// raw here means a typed decode error already surfaced.
	_ = raw
	return nil
}

// Rename updates a notebook's title (and optionally its emoji).
// The backing RPC is the generic `MutateProject` (`s0tc2d`);
// the change-property variant carries title at slot 2 and emoji
// at slot 3. Pass `nil` for emoji to omit the slot entirely
// (matches the documented title-only byte shape).
//
// At least one of title or emoji must be supplied; an empty
// title with a nil emoji is rejected before any RPC fires
// (a mutation that touches neither field is a no-op the
// backend rejects anyway, but the SDK short-circuits for a
// typed error at the boundary).
//
// Port of `_notebooks.py::NotebooksAPI.rename`.
func (a *NotebooksAPI) Rename(ctx context.Context, id, title string, emoji *string, opts ...NotebooksOption) error {
	_ = resolveOptions(opts)
	if err := rows.ValidateNotebookID(id); err != nil {
		return apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	if title == "" && (emoji == nil || *emoji == "") {
		return apperrors.Wrap(apperrors.CodeValidationError,
			errors.New("notebooks.Rename: at least one of title or emoji must be set"))
	}
	_, err := a.call(ctx, wire.MethodRenameNotebook, params.BuildRename(id, title, emoji))
	return err
}

// Summary fetches the AI-generated summary text plus the
// suggested-topics list for one notebook. The backing RPC is
// `GenerateNotebookGuide` (`VfAZjd`). An empty / absent / null
// summary degrades to "" (the documented "no summary yet"
// contract) rather than an error.
//
// Port of `_notebooks.py::NotebooksAPI.get_summary` and
// `get_description`. The Python surface exposes both a
// string-only and a typed-return variant; the Go SDK surfaces
// only the typed form (Summary) so callers do not have to
// branch on return shape.
func (a *NotebooksAPI) Summary(ctx context.Context, id string, opts ...NotebooksOption) (Summary, error) {
	_ = resolveOptions(opts)
	if err := rows.ValidateNotebookID(id); err != nil {
		return Summary{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	raw, err := a.call(ctx, wire.MethodSummarize, params.BuildSummary(id))
	if err != nil {
		return Summary{}, err
	}
	return summaryFromEnvelope(raw)
}

// Metadata composes a `GetProject` (`rLM1Ne`) call with the
// per-row source count. There is no dedicated `GET_METADATA`
// RPC today; the Python original (port of
// `_notebook_metadata.py::NotebookMetadataAPI.get`) composes
// the two RPCs itself. The Go port mirrors the composite so the
// public SDK has the typed surface today and the dedicated wire
// shape (when it lands) can replace the body without changing
// the signature.
//
// Port of `_notebook_metadata.py::NotebookMetadataAPI.get`. The
// result is the same `Notebook` shape `Get` returns but with
// Metadata.SourcesCount populated when the row's source list is
// present.
func (a *NotebooksAPI) Metadata(ctx context.Context, id string, opts ...NotebooksOption) (Metadata, error) {
	_ = resolveOptions(opts)
	if err := rows.ValidateNotebookID(id); err != nil {
		return Metadata{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	raw, err := a.call(ctx, wire.MethodGetNotebook, params.BuildMetadata(id))
	if err != nil {
		return Metadata{}, err
	}
	nb, err := a.notebookFromEnvelope(raw)
	if err != nil {
		return Metadata{}, err
	}
	if nb.Metadata != nil {
		return *nb.Metadata, nil
	}
	return Metadata{}, nil
}

// AddSource adds one or more sources to a notebook. The wire
// builder for this RPC is not yet implemented (T-P6-2 owns the
// port); the params layer panics on every call and the SDK
// recovers to surface a typed CodeNotebookLMError. Once T-P6-2
// lands, the body of this method becomes a one-line call to
// `params.BuildAddSource` and the wire.AddSource RPC dispatch.
//
// Port of `_sources.py::SourcesAPI.add`. The Python signature
// is `add(self, notebook_id, source_id, *, multiple=False)`
// — a single source per RPC with an optional multi-source
// variant. The Go port takes `source` as `[]string` so the
// signature can grow to the multi-source variant when the wire
// shape lands.
func (a *NotebooksAPI) AddSource(ctx context.Context, id string, source []string, opts ...NotebooksOption) (err error) {
	_ = resolveOptions(opts)
	if vErr := rows.ValidateNotebookID(id); vErr != nil {
		return apperrors.Wrap(apperrors.CodeValidationError, vErr)
	}
	// params.BuildAddSource panics with a "TODO" marker until
	// T-P6-2 lands the port. The defer-recover here runs
	// before the BuildAddSource argument expression evaluates,
	// so a panic in the builder surfaces as a typed error
	// rather than escaping the SDK boundary.
	defer func() {
		if r := recover(); r != nil {
			err = unimplementedError(wire.MethodAddSource, r)
		}
	}()
	_, callErr := a.call(ctx, wire.MethodAddSource, params.BuildAddSource(id, source))
	return callErr
}

// RemoveSource removes one or more sources from a notebook.
// Symmetric counterpart to AddSource; see AddSource for the
// TODO status. T-P6-2 owns the port.
//
// Port of `_sources.py::SourcesAPI.delete`.
func (a *NotebooksAPI) RemoveSource(ctx context.Context, id string, source []string, opts ...NotebooksOption) (err error) {
	_ = resolveOptions(opts)
	if vErr := rows.ValidateNotebookID(id); vErr != nil {
		return apperrors.Wrap(apperrors.CodeValidationError, vErr)
	}
	// See AddSource for the rationale — the params builder
	// panics with a "TODO" marker until T-P6-2 lands. The
	// defer-recover here catches the panic at the SDK
	// boundary so the public surface never propagates a Go
	// panic.
	defer func() {
		if r := recover(); r != nil {
			err = unimplementedError(wire.MethodDeleteSource, r)
		}
	}()
	_, callErr := a.call(ctx, wire.MethodDeleteSource, params.BuildRemoveSource(id, source))
	return callErr
}

// Share sets the public-link visibility for a notebook. The
// backing RPC is `ShareProject` (`QDyure`); the access value
// rides at slot 2 of the inner envelope. Passing
// `ShareAccessAnyoneWithLink` flips the public toggle; passing
// `ShareAccessRestricted` is equivalent to Unshare and is
// exposed as that method for clarity.
//
// Port of `_sharing.py::WebSharingAPI.set_public`.
func (a *NotebooksAPI) Share(ctx context.Context, id string, access ShareAccess, opts ...NotebooksOption) error {
	_ = resolveOptions(opts)
	if err := rows.ValidateNotebookID(id); err != nil {
		return apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	public := access == ShareAccessAnyoneWithLink
	_, err := a.call(ctx, wire.MethodShareNotebook, params.BuildShare(id, public))
	return err
}

// Unshare clears the public-link visibility for a notebook.
// Equivalent to `Share(id, ShareAccessRestricted)`; surfaced
// as a named method because every CLI / MCP / REST surface
// renders "unshare" as a distinct command. The wire payload is
// identical to the public-flag-off branch of BuildShare; the
// alias exists so the call site reads clearly.
//
// Port of `_sharing.py::WebSharingAPI.set_public(notebook_id, False)`.
func (a *NotebooksAPI) Unshare(ctx context.Context, id string, opts ...NotebooksOption) error {
	return a.Share(ctx, id, ShareAccessRestricted, opts...)
}

// GetShareStatus fetches the typed view of a notebook's share
// envelope. The backing RPC is
// `LabsTailwindSharingService.GetProjectDetails` (`JFMDGd`).
//
// The response cannot report `view_level` (the backend omits
// it); callers that need the chat-only / full-notebook toggle
// must use a separate RPC the Phase 9 ticket will pin.
//
// Port of `_sharing.py::WebSharingAPI.get_status`.
func (a *NotebooksAPI) GetShareStatus(ctx context.Context, id string, opts ...NotebooksOption) (ShareState, error) {
	_ = resolveOptions(opts)
	if err := rows.ValidateNotebookID(id); err != nil {
		return ShareState{}, apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	raw, err := a.call(ctx, wire.MethodGetShareStatus, params.BuildGetShareStatus(id))
	if err != nil {
		return ShareState{}, err
	}
	return shareStateFromEnvelope(raw)
}

// RemoveCollaborator revokes one user's access. The singular
// path is safe; the grant-batch path is not exposed here (a
// batch that targets any already-absent user silently no-ops
// the whole request).
//
// `notify` is fixed to `false` (matching the historical
// remove-user payload) so the Share RPC carries the
// `[0, ""]` block the per-grant message slot expects.
//
// Port of `_sharing.py::WebSharingAPI.remove_user`.
func (a *NotebooksAPI) RemoveCollaborator(ctx context.Context, id, email string, opts ...NotebooksOption) error {
	_ = resolveOptions(opts)
	if err := rows.ValidateNotebookID(id); err != nil {
		return apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	if err := params.EscapeEmail(email); err != nil {
		return apperrors.Wrap(apperrors.CodeValidationError, err)
	}
	_, err := a.call(ctx, wire.MethodShareNotebook, params.BuildRemoveCollaborator(id, email))
	return err
}

// GetRecent is the alias for `List`; the wire has no dedicated
// "recent-only" RPC today. The alias exists so callers can
// adopt the more specific name now and a future filter RPC can
// replace the body without renaming the function.
//
// Port of `_notebooks.py::NotebooksAPI.recently_viewed`. The
// Python original exposes a method by this name even though
// the wire has no dedicated RPC; the Go port mirrors.
func (a *NotebooksAPI) GetRecent(ctx context.Context, opts ...NotebooksOption) (Page, error) {
	o := resolveOptions(opts)
	raw, err := a.call(ctx, wire.MethodListNotebooks, params.BuildGetRecent())
	if err != nil {
		return Page{}, err
	}
	return a.pageFromList(raw, o.maxItems)
}

// GetStarred returns the typed view of every starred notebook.
// The wire builder for this RPC is not yet implemented
// (notebooklm-py has no `LIST_STARRED` endpoint today); the
// params layer panics on every call and the SDK recovers to
// surface a typed CodeNotebookLMError.
//
// The `is_starred` column lives on every existing row and the
// Python client surfaces it via `Notebook.from_api_response`
// but does not yet expose a dedicated list endpoint. When the
// Python original pins a builder, T-P5-5 or a later ticket
// ports it.
func (a *NotebooksAPI) GetStarred(ctx context.Context, opts ...NotebooksOption) (page Page, err error) {
	// params.BuildGetStarred panics with a "TODO" marker until
	// the Python original pins a list_starred builder. The
	// defer-recover here catches the panic at the SDK boundary
	// so the public surface never propagates a Go panic.
	defer func() {
		if r := recover(); r != nil {
			err = unimplementedError(wire.MethodListNotebooks, r)
		}
	}()
	raw, callErr := a.call(ctx, wire.MethodListNotebooks, params.BuildGetStarred())
	if callErr != nil {
		return Page{}, callErr
	}
	o := resolveOptions(opts)
	return a.pageFromList(raw, o.maxItems)
}

// GetSharedWithMe returns the typed view of every notebook
// shared with the calling user. The wire builder for this RPC
// is not yet implemented (notebooklm-py has no
// `LIST_SHARED_WITH_ME` endpoint today); see GetStarred for
// the deferred-TODO status.
//
// The `is_shared` column exists on every row and
// `LIST_NOTEBOOKS` already filters shared-with-me through a
// slot argument that the live probe has not yet pinned. We
// deliberately do NOT call params.BuildList here — a silent
// alias would mask the missing RPC.
func (a *NotebooksAPI) GetSharedWithMe(ctx context.Context, opts ...NotebooksOption) (page Page, err error) {
	// See GetStarred for the panic-recover rationale. The
	// params builder is a stub until T-P5-5 (or later) ports
	// the Python original.
	defer func() {
		if r := recover(); r != nil {
			err = unimplementedError(wire.MethodListNotebooks, r)
		}
	}()
	raw, callErr := a.call(ctx, wire.MethodListNotebooks, params.BuildGetSharedWithMe())
	if callErr != nil {
		return Page{}, callErr
	}
	o := resolveOptions(opts)
	return a.pageFromList(raw, o.maxItems)
}

// GetByProject returns the typed view of every notebook under
// the supplied project id. The wire builder for this RPC is
// not yet implemented (notebooklm-py has no
// `LIST_BY_PROJECT` endpoint today); see GetStarred for the
// deferred-TODO status.
//
// The `project_id` column exists on every row but no
// dedicated list-by-project endpoint has been captured.
func (a *NotebooksAPI) GetByProject(ctx context.Context, projectID string, opts ...NotebooksOption) (page Page, err error) {
	if projectID == "" {
		return Page{}, apperrors.Wrap(apperrors.CodeValidationError,
			errors.New("notebooks.GetByProject: projectID must not be empty"))
	}
	// See GetStarred for the panic-recover rationale.
	defer func() {
		if r := recover(); r != nil {
			err = unimplementedError(wire.MethodListNotebooks, r)
		}
	}()
	raw, callErr := a.call(ctx, wire.MethodListNotebooks, params.BuildGetByProject(projectID))
	if callErr != nil {
		return Page{}, callErr
	}
	o := resolveOptions(opts)
	return a.pageFromList(raw, o.maxItems)
}

// pageFromList converts a decoded LIST_NOTEBOOKS envelope into
// the public Page type. The envelope shape is
// `[[row1, row2, ...]]` — one outer wrapper at the LIST_NOTEBOOKS
// slot (the Python original's `result[0]`); the per-row
// payloads live at `envelope[0]`.
//
// A nil envelope / empty inner list degrades to a zero-value
// Page so a "no notebooks" response is the same value a real
// "no rows" page returns. A malformed row surfaces a typed
// CodeNotebookLMError — the wire shape is the contract and a
// silently-skipped row would mask schema drift.
func (a *NotebooksAPI) pageFromList(raw []any, maxItems int) (Page, error) {
	if len(raw) == 0 {
		return Page{}, nil
	}
	rows, ok := raw[0].([]any)
	if !ok {
		return Page{}, nil
	}
	rowsOut := make([]Notebook, 0, len(rows))
	for _, row := range rows {
		nb, err := a.decodeRow(row)
		if err != nil {
			return Page{}, err
		}
		rowsOut = append(rowsOut, nb)
	}
	if maxItems > 0 && len(rowsOut) > maxItems {
		rowsOut = rowsOut[:maxItems]
	}
	return Page{Items: rowsOut}, nil
}

// notebookFromEnvelope converts a single-row RPC envelope (GET,
// CREATE) into the public Notebook view. The wire shape is the
// row array itself — the JSON-decoded `frame[2]` payload is the
// row (slot 0 = title, slot 2 = id, slot 5 = meta), so the
// envelope helper passes `raw` directly to decodeRow.
//
// An empty envelope degrades to a zero-value Notebook, not an
// error — the contract matches the short-row tolerance the
// rows package documents.
func (a *NotebooksAPI) notebookFromEnvelope(raw []any) (Notebook, error) {
	if len(raw) == 0 {
		return Notebook{}, nil
	}
	return a.decodeRow(raw)
}

// decodeRow delegates the per-row positional decoding to the
// rows package and projects the internal view to the public
// type. Centralized so any future field added to rows.Notebook
// only updates one site.
//
// Decoding errors that wrap a *wire.ShapeDriftError propagate
// unchanged so the schema-drift metric still counts them;
// other errors are wrapped through apperrors so the classifier
// sees a single typed value.
func (a *NotebooksAPI) decodeRow(row any) (Notebook, error) {
	decoded, err := rows.DecodeNotebook(row, "")
	if err != nil {
		// The rows package surfaces *wire.ShapeDriftError on a
		// present-but-wrong-typed slot; preserve that wrap so
		// the schema-drift metric still counts the bad call.
		// Other errors (transport drift, etc.) get the
		// canonical CodeNotebookLMError tag.
		return Notebook{}, apperrors.Wrap(apperrors.CodeNotebookLMError, err)
	}
	return notebookFromRows(decoded), nil
}

// summaryFromEnvelope converts a SUMMARIZE envelope into the
// public Summary view. The wire envelope shape is
// `[[[summary_str], [[topic_row, ...]]]]` — `result[0]` is
// `[ [summary_str], [[topic, ...]] ]`, then `result[0][0][0]`
// is the summary slot and `result[0][1][0]` is the topics list.
// The descent is hand-written so the SDK does not depend on
// the features package's Caller seam (which is reserved for
// internal/cli / internal/mcpsrv use).
//
// Mirrors internal/web/features/notebooks.go::extractSummary and
// extractSuggestedTopics; the descent here is identical.
func summaryFromEnvelope(raw []any) (Summary, error) {
	out := Summary{}
	if len(raw) == 0 {
		return out, nil
	}
	outer, ok := raw[0].([]any)
	if !ok || len(outer) == 0 {
		return out, nil
	}
	// outer[0] = [summary_str, ...]
	if first, ok := outer[0].([]any); ok && len(first) > 0 {
		if s, ok := first[0].(string); ok {
			out.Summary = s
		}
	}
	// outer[1] = [[topic_row, ...]] — wrapped once more so the
	// backend can extend the list without reshaping the slot.
	if len(outer) < 2 {
		return out, nil
	}
	topicsWrap, ok := outer[1].([]any)
	if !ok || len(topicsWrap) == 0 {
		return out, nil
	}
	topics, ok := topicsWrap[0].([]any)
	if !ok {
		return out, nil
	}
	for _, item := range topics {
		row, ok := item.([]any)
		if !ok || len(row) < 2 {
			continue
		}
		t := Topic{}
		if s, ok := row[0].(string); ok {
			t.Question = s
		}
		if s, ok := row[1].(string); ok {
			t.Prompt = s
		}
		out.SuggestedTopics = append(out.SuggestedTopics, t)
	}
	return out, nil
}

// shareStateFromEnvelope converts a GET_SHARE_STATUS envelope
// into the public ShareState view. The wire shape is the
// top-level data — `[user_entries, public_block, ...]` — so
// `raw` IS the share envelope and the helper reads slots
// directly without descending.
//
// A malformed envelope degrades to a zero-value ShareState
// rather than a drift error — the Python original is similarly
// tolerant for missing share envelopes.
//
// wire.Unmarshal uses UseNumber(), so numeric wire values are
// json.Number — not float64. The decode helpers accept both
// shapes so the SDK tolerates either decoder style; the
// typical wire payload uses integer literals (1 / 2 / 3),
// which UseNumber decodes as json.Number.
func shareStateFromEnvelope(raw []any) (ShareState, error) {
	out := ShareState{AccessLevel: ShareAccessRestricted}
	if len(raw) == 0 {
		return out, nil
	}
	// first slot carries [id, public_bool, ...]
	first, ok := raw[0].([]any)
	if !ok || len(first) == 0 {
		return out, nil
	}
	if s, ok := first[0].(string); ok {
		out.ID = s
	}
	if b, ok := first[1].(bool); ok {
		out.IsPublic = b
	}
	// second slot carries the access-level envelope
	if len(raw) >= 2 {
		accessRow, ok := raw[1].([]any)
		if ok && len(accessRow) > 0 {
			if n := numericInt(accessRow[0]); n != 0 {
				out.AccessLevel = ShareAccess(n)
			}
		}
	}
	// collaborator list at raw[3] when present — guarded
	// by length so a missing slot returns nil collaborators.
	// The exact slot index is best-effort: the wire envelope
	// may grow collaborators at additional positions in
	// future; we accept what we can identify and skip the
	// rest rather than raising a drift error.
	for i := 2; i < len(raw); i++ {
		candidate, ok := raw[i].([]any)
		if !ok || len(candidate) == 0 {
			continue
		}
		// collaborators are arrays of [email, role]; a mixed
		// shape degrades to nil rather than a bad entry
		// because the wire envelope is not yet pinned.
		var collabs []Collaborator
		for _, c := range candidate {
			row, ok := c.([]any)
			if !ok || len(row) < 2 {
				continue
			}
			email, okE := row[0].(string)
			if !okE {
				continue
			}
			if n := numericInt(row[1]); n != 0 {
				collabs = append(collabs, Collaborator{
					Email: email,
					Role:  SharePermission(n),
				})
			}
		}
		if len(collabs) > 0 {
			out.Collaborators = collabs
			break
		}
	}
	return out, nil
}

// numericInt coerces a wire-decoded numeric value to int. The
// wire package decodes with UseNumber() so integers arrive as
// json.Number; a non-UseNumber path returns float64. Both are
// accepted here; non-numeric inputs return 0 so callers can
// ignore them via a sentinel-free "no value" check.
//
// The 0 sentinel is safe because ShareAccess / SharePermission
// do not currently use 0 as a wire-stable value (RESTRICTED
// is 0 for ShareAccess, but a 0 read means "absent slot" and
// degrades to the default; the only sensible role is 1/2/3,
// so a 0 means "no role" — call sites filter on that).
func numericInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return int(i)
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// notebookFromRows projects rows.Notebook (the private
// decoder view) into the public notebooklm.Notebook type.
// The conversion is purely structural — every field on the
// internal view has a 1:1 partner on the public type, and
// both types carry the same semantics.
//
// nil → zero-value (rather than a placeholder Notebook{}
// with an empty id) is the documented contract; callers that
// need to distinguish "absent" from "row with no metadata"
// should check the resulting Notebook.Metadata for nil.
func notebookFromRows(in rows.Notebook) Notebook {
	if in.Metadata == nil {
		return Notebook{
			ID:         in.ID,
			Title:      in.Title,
			CreatedAt:  in.CreatedAt,
			IsStarred:  in.IsStarred,
			IsShared:   in.IsShared,
			ProjectID:  in.ProjectID,
			OwnerEmail: in.OwnerEmail,
			Summary:    in.Summary,
		}
	}
	md := Metadata{
		Role:         in.Metadata.Role,
		LastViewedAt: in.Metadata.LastViewedAt,
		CreatedAt:    in.Metadata.CreatedAt,
		Emoji:        in.Metadata.Emoji,
		SourcesCount: in.Metadata.SourcesCount,
	}
	return Notebook{
		ID:         in.ID,
		Title:      in.Title,
		CreatedAt:  in.CreatedAt,
		IsStarred:  in.IsStarred,
		IsShared:   in.IsShared,
		ProjectID:  in.ProjectID,
		OwnerEmail: in.OwnerEmail,
		Summary:    in.Summary,
		Metadata:   &md,
	}
}
