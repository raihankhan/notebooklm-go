// Package features contains the higher-level "business logic" wrappers
// that sit between the SDK root (notebooklm.NotebooksAPI) and the byte
// builders (internal/web/params) + row adapters (internal/web/rows).
//
// Every exported function takes a Caller — the minimal interface the
// SDK exposes for dispatching one RPC — rather than reaching for the
// concrete client type. That way the MCP / REST / CLI adapters can
// each construct their own Caller and exercise these wrappers without
// pulling in the public SDK surface (which would re-introduce the
// import cycle the boundary rules forbid).
//
// Per AGENTS.md rule 5 (boundary table) and rule 3 (one JSON encoder)
// this package imports wire (encode + decode) and the params/rows
// siblings, never encoding/json. The transport layer's Caller interface
// hides the concrete RPC plumbing behind a single method.
package features

import (
	"context"
	"errors"
	"fmt"

	"github.com/raihankhan/notebooklm-go/internal/web/params"
	"github.com/raihankhan/notebooklm-go/internal/web/rows"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// ShareAccess is the typed view of the public-sharing toggle the
// `set_public` builder accepts. Mirrors `_types.enums.ShareAccess` from
// the Python original. The integer values are wire-stable (see
// docs/04-rpc-payloads.md §"Sharing").
type ShareAccess int

const (
	// AccessRestricted — only invited collaborators can open the notebook.
	AccessRestricted ShareAccess = params.ShareAccessRestricted
	// AccessAnyoneWithLink — anyone with the share URL can open the notebook.
	AccessAnyoneWithLink ShareAccess = params.ShareAccessAnyoneWithLink
)

// Caller is the minimal interface this package needs from the SDK.
//
// One method per RPC. Implementations live in `notebooklm` (the public
// SDK root) and the test fixtures; the interface boundary here is the
// seam that lets `notebooklm.NotebooksAPI` drive every feature without
// the feature package importing the SDK in return.
//
// Every method:
//
//   - Returns the wire-decoded payload (`any`, after the
//     `wire.DecodeResponse` envelope-unwrap) so callers can run the
//     rows.Decoder on it.
//   - Returns an error the caller should NOT match against
//     `wire.ErrDecoding` directly — the rows / features layers re-wrap
//     drift errors with their own typed vocabulary. Use the typed
//     errors in this package.
//
// `method` is the obfuscated id (`wire.Method`) the SDK resolved for
// the call; the features layer never reads it (it is opaque to row
// decoding), but the parameter keeps the contract explicit so a future
// schema-drift diagnostic has the method id available without an
// extra round-trip.
type Caller interface {
	Call(ctx context.Context, method wire.Method, params any, sourcePath string, allowNull bool) (any, error)
}

// Page is one page of a paged list. Notebooks do not currently page
// (ListRecentlyViewedProjects returns the whole list in one envelope),
// but the type exists so the CLI / MCP surfaces can adopt a uniform
// Page<T> envelope without churn when paged listings land.
type Page struct {
	// Notebooks is the slice of decoded rows in the page.
	Notebooks []rows.Notebook

	// NextCursor is the opaque continuation token returned by the
	// backend. Empty when there is no next page.
	NextCursor string

	// TotalCount is the backend-reported total when the wire carries
	// one; zero otherwise.
	TotalCount int
}

// ListOptions tunes the list call. Zero-value is the default
// (recency-ordered, no filter).
type ListOptions struct {
	// MaxItems caps the returned slice; 0 means "all".
	MaxItems int
}

// ListPaged returns the typed notebooks visible to the calling user, in
// recency order. The backing RPC is `ListRecentlyViewedProjects`
// (`wXbhsf`) — see `_web/notebooks.py::WebNotebooksAPI.list`.
//
// The wire envelope is a single-element wrapper whose first element is
// the row list (`[[row1, row2, ...]]`); we unwrap through wire.At so a
// malformed envelope surfaces a *wire.ShapeDriftError rather than
// fabricating empty-id notebooks (the documented failure mode in
// `_web/notebooks.py::list`).
//
// `opts.MaxItems > 0` truncates the page client-side after the decode;
// the wire itself does not paginate. The T-P5-5 ticket may grow this
// with a `LIST_NOTEBOOKS` server-side filter that the live probe has
// not yet confirmed.
func ListPaged(ctx context.Context, c Caller, opts ListOptions) (Page, error) {
	if c == nil {
		return Page{}, errors.New("features.ListPaged: caller is nil")
	}
	raw, err := c.Call(ctx, wire.MethodListNotebooks, params.BuildList(), "/", false)
	if err != nil {
		return Page{}, fmt.Errorf("features.ListPaged: %w", err)
	}
	rows, err := decodeNotebookRows(raw)
	if err != nil {
		return Page{}, err
	}
	if opts.MaxItems > 0 && len(rows) > opts.MaxItems {
		rows = rows[:opts.MaxItems]
	}
	return Page{Notebooks: rows}, nil
}

// Get fetches one notebook by id and returns its typed view. The
// backing RPC is `GetProject` (`rLM1Ne`). A not-found id (empty
// payload, rpc status 5) surfaces as rows.ErrNotFound so callers can
// branch on the typed sentinel rather than matching against a raw
// transport error.
func Get(ctx context.Context, c Caller, notebookID string) (rows.Notebook, error) {
	if err := rows.ValidateNotebookID(notebookID); err != nil {
		return rows.Notebook{}, err
	}
	if c == nil {
		return rows.Notebook{}, errors.New("features.Get: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodGetNotebook, params.BuildGet(notebookID), sourcePath, false)
	if err != nil {
		if isNotFound(err) {
			return rows.Notebook{}, rows.ErrNotFound
		}
		return rows.Notebook{}, fmt.Errorf("features.Get: %w", err)
	}
	// GET_NOTEBOOK returns [nb_info, ...] — the row payload lives at
	// index 0. _web/notebooks.py::WebNotebooksAPI.get line 707-712
	// guards the empty-payload case before parsing; we mirror that
	// with rows.ErrNotFound so the contract is uniform.
	inner, ok := raw.([]any)
	if !ok || len(inner) == 0 {
		return rows.Notebook{}, rows.ErrNotFound
	}
	return rows.DecodeNotebook(inner[0], "")
}

// Summary fetches the AI-generated summary text for one notebook.
// The backing RPC is `GenerateNotebookGuide` (`VfAZjd`) — see
// `_web/notebooks.py::WebNotebooksAPI.get_summary`.
//
// The wire envelope is `[[[summary_string, ...], topics, ...]]`; we
// descend through wire.At to `result[0][0]` and stringify what we
// find. Empty / absent / null summaries degrade to "" (the documented
// "no summary yet" contract) rather than a drift error.
func Summary(ctx context.Context, c Caller, notebookID string) (string, error) {
	if err := rows.ValidateNotebookID(notebookID); err != nil {
		return "", err
	}
	if c == nil {
		return "", errors.New("features.Summary: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodSummarize, params.BuildSummary(notebookID), sourcePath, false)
	if err != nil {
		return "", fmt.Errorf("features.Summary: %w", err)
	}
	return extractSummary(raw), nil
}

// Describe is the typed-view wrapper over Summary. The Python original
// has both a string-only `get_summary` and a typed `get_description`
// (`NotebookDescription(summary, suggested_topics)`); we expose the
// typed wrapper here and let the string-only version live on Summary
// for callers that only need the headline text.
//
// Topics are best-effort: a malformed or absent topics block degrades
// to nil; the summary string is the load-bearing field.
func Describe(ctx context.Context, c Caller, notebookID string) (Description, error) {
	if err := rows.ValidateNotebookID(notebookID); err != nil {
		return Description{}, err
	}
	if c == nil {
		return Description{}, errors.New("features.Describe: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	raw, err := c.Call(ctx, wire.MethodSummarize, params.BuildSummary(notebookID), sourcePath, false)
	if err != nil {
		return Description{}, fmt.Errorf("features.Describe: %w", err)
	}
	desc := Description{Summary: extractSummary(raw)}
	desc.SuggestedTopics = extractSuggestedTopics(raw)
	return desc, nil
}

// Description is the typed summary + suggested-topics bundle the
// `Describe` wrapper returns. Mirrors `_types.notebooks.NotebookDescription`.
//
// Topics carry the question and its ready-to-send prompt (the two
// fields the backend reliably returns). Per-topic shape checks degrade
// to empty strings; a partially-populated topics list is more useful
// than an empty one (the same permissive contract `_web/rows/notebooks.py`
// applies for malformed per-topic entries).
type Description struct {
	Summary         string
	SuggestedTopics []Topic
}

// Topic is one (question, prompt) pair from the SUMMARIZE suggested-topics
// list. Mirrors `_types.notebooks.SuggestedTopic`.
type Topic struct {
	Question string
	Prompt   string
}

// Create issues CREATE_NOTEBOOK (`CCqFvf`) and returns the typed view
// of the freshly created notebook. The CREATE_NOTEBOOK RPC is a probe-
// then-create class operation (a transport failure after the server
// committed cannot be told apart from a clean failure); the SDK's
// transport layer handles the probe loop. Callers that need the
// probe-then-create contract should use the SDK root's
// `Notebooks.Create` directly — that wrapper owns the probe.
func Create(ctx context.Context, c Caller, title string) (rows.Notebook, error) {
	if title == "" {
		return rows.Notebook{}, errors.New("features.Create: title must not be empty")
	}
	if c == nil {
		return rows.Notebook{}, errors.New("features.Create: caller is nil")
	}
	raw, err := c.Call(ctx, wire.MethodCreateNotebook, params.BuildCreate(title), "/", false)
	if err != nil {
		return rows.Notebook{}, fmt.Errorf("features.Create: %w", err)
	}
	return rows.DecodeNotebook(raw, "")
}

// Delete issues DELETE_NOTEBOOK (`WWINqb`). Idempotent: deleting an
// already-absent notebook succeeds (the server returns null, which the
// transport's allowNull path swallows) and never raises ErrNotFound.
// Real failures (403 / 5xx / auth / transport) still propagate.
func Delete(ctx context.Context, c Caller, notebookID string) error {
	if err := rows.ValidateNotebookID(notebookID); err != nil {
		return err
	}
	if c == nil {
		return errors.New("features.Delete: caller is nil")
	}
	_, err := c.Call(ctx, wire.MethodDeleteNotebook, params.BuildDelete(notebookID), "/", true)
	if err != nil {
		return fmt.Errorf("features.Delete: %w", err)
	}
	return nil
}

// Rename updates a notebook's title (and optionally its emoji). The
// backing RPC is the generic `MutateProject` (`s0tc2d`); the change-
// property variant carries title at slot 2 and emoji at slot 3. Pass
// `emoji = nil` for a title-only mutation (matches the long-standing
// title-only byte shape).
func Rename(ctx context.Context, c Caller, notebookID, title string, emoji *string) (rows.Notebook, error) {
	if err := rows.ValidateNotebookID(notebookID); err != nil {
		return rows.Notebook{}, err
	}
	if title == "" && (emoji == nil || *emoji == "") {
		return rows.Notebook{}, errors.New("features.Rename: at least one of title or emoji must be set")
	}
	if c == nil {
		return rows.Notebook{}, errors.New("features.Rename: caller is nil")
	}
	sourcePath := "/"
	_, err := c.Call(ctx, wire.MethodRenameNotebook, params.BuildRename(notebookID, title, emoji), sourcePath, true)
	if err != nil {
		return rows.Notebook{}, fmt.Errorf("features.Rename: %w", err)
	}
	// The Python original refreshes the notebook after the mutation so
	// the caller sees the updated title; mirror that here.
	return Get(ctx, c, notebookID)
}

// Share sets the public-link visibility for a notebook. The backing
// RPC is `ShareProject` (`QDyure`); the access value rides at slot 2
// of the inner envelope.
func Share(ctx context.Context, c Caller, notebookID string, public bool) error {
	if err := rows.ValidateNotebookID(notebookID); err != nil {
		return err
	}
	if c == nil {
		return errors.New("features.Share: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	_, err := c.Call(ctx, wire.MethodShareNotebook, params.BuildShare(notebookID, public), sourcePath, true)
	if err != nil {
		return fmt.Errorf("features.Share: %w", err)
	}
	return nil
}

// Unshare is the alias for Share(notebookID, false). Kept as a named
// wrapper because every CLI / MCP surface renders "unshare" and
// "share-off" as different commands; a future change to the public-
// off payload (the backend has occasionally added fields to the
// RESTRICTED branch) would land here.
func Unshare(ctx context.Context, c Caller, notebookID string) error {
	return Share(ctx, c, notebookID, false)
}

// RemoveCollaborator revokes one user's access. The singular path is
// safe; a batched removal only works when every target is currently
// shared (the backend silently drops the whole request otherwise).
func RemoveCollaborator(ctx context.Context, c Caller, notebookID, email string) error {
	if err := rows.ValidateNotebookID(notebookID); err != nil {
		return err
	}
	if err := params.EscapeEmail(email); err != nil {
		return err
	}
	if c == nil {
		return errors.New("features.RemoveCollaborator: caller is nil")
	}
	sourcePath := "/notebook/" + notebookID
	_, err := c.Call(ctx, wire.MethodShareNotebook, params.BuildRemoveCollaborator(notebookID, email), sourcePath, true)
	if err != nil {
		return fmt.Errorf("features.RemoveCollaborator: %w", err)
	}
	return nil
}

// RemoveFromRecent drops a notebook from the recency-ordered home list.
// The backing RPC is `RemoveRecentlyViewedProject` (`fejl7e`).
func RemoveFromRecent(ctx context.Context, c Caller, notebookID string) error {
	if err := rows.ValidateNotebookID(notebookID); err != nil {
		return err
	}
	if c == nil {
		return errors.New("features.RemoveFromRecent: caller is nil")
	}
	_, err := c.Call(ctx, wire.MethodRemoveRecentlyViewed, []any{notebookID}, "/", true)
	if err != nil {
		return fmt.Errorf("features.RemoveFromRecent: %w", err)
	}
	return nil
}

// decodeNotebookRows unwraps a LIST_NOTEBOOKS envelope into a slice of
// typed Notebook rows. The envelope is [[row1, row2, ...]]; a falsy /
// non-list payload degrades to [] (the documented "no notebooks"
// contract); a truthy payload that does not match the envelope is
// schema drift and surfaces a *wire.ShapeDriftError.
//
// Mirrors `_web/notebooks.py::WebNotebooksAPI.list` lines 510-531.
func decodeNotebookRows(raw any) ([]rows.Notebook, error) {
	if raw == nil {
		return nil, nil
	}
	outer, ok := raw.([]any)
	if !ok {
		return nil, &wire.ShapeDriftError{
			Path:    "",
			Method:  string(wire.MethodListNotebooks),
			Reason:  "not_a_list",
			GotType: typeName(raw),
		}
	}
	if len(outer) == 0 {
		return nil, nil
	}
	inner, err := wire.At(outer, 0)
	if err != nil {
		return nil, err
	}
	list, ok := inner.([]any)
	if !ok {
		// outer[0] is null = "no notebooks". Treat as empty.
		if inner == nil {
			return nil, nil
		}
		return nil, &wire.ShapeDriftError{
			Path:    "[0]",
			Method:  string(wire.MethodListNotebooks),
			Reason:  "not_a_list",
			GotType: typeName(inner),
		}
	}
	out := make([]rows.Notebook, 0, len(list))
	for i, row := range list {
		nb, err := rows.DecodeNotebook(row, "")
		if err != nil {
			return nil, fmt.Errorf("features.decodeNotebookRows[%d]: %w", i, err)
		}
		out = append(out, nb)
	}
	return out, nil
}

// extractSummary descends a SUMMARIZE envelope to outer[0][0][0] and
// returns the summary string. Mirrors `_web/notebooks.py::_extract_summary`.
//
// The expected shape is `[[[summary_string, ...], topics, ...]]` — the
// outer wrapper is the rpc envelope; result[0] is the summary+topics
// row; result[0][0] is the summary list; result[0][0][0] is the string.
// An empty / absent / null summary degrades to "" rather than drift;
// the "no summary yet" case is routine and must not log a schema-drift
// warning on every healthy new notebook.
func extractSummary(raw any) string {
	if raw == nil {
		return ""
	}
	outer, ok := raw.([]any)
	if !ok || len(outer) == 0 {
		return ""
	}
	o0, err := wire.At(outer, 0)
	if err != nil || o0 == nil {
		return ""
	}
	inner, ok := o0.([]any)
	if !ok || len(inner) == 0 {
		return ""
	}
	summaryRow, ok := inner[0].([]any)
	if !ok || len(summaryRow) == 0 {
		return ""
	}
	first, ok := summaryRow[0].(string)
	if !ok {
		// Tolerant: a non-string summary degrades to "" rather than drift
		// (the same permissive contract `_extract_summary` applies).
		return ""
	}
	return first
}

// extractSuggestedTopics descends a SUMMARIZE envelope to
// result[0][0][1][0] and returns the typed Topic slice. Mirrors
// `_web/notebooks.py::_extract_suggested_topics`.
//
// A present-but-malformed topic entry degrades to "" fields rather
// than dropping the whole row (a partial response is more useful than
// an empty list); a wholly absent topics slot degrades to nil.
func extractSuggestedTopics(raw any) []Topic {
	if raw == nil {
		return nil
	}
	outer, ok := raw.([]any)
	if !ok || len(outer) == 0 {
		return nil
	}
	o0, err := wire.At(outer, 0)
	if err != nil || o0 == nil {
		return nil
	}
	inner, ok := o0.([]any)
	if !ok || len(inner) < 2 {
		return nil
	}
	container, ok := inner[1].([]any)
	if !ok || len(container) == 0 {
		return nil
	}
	list, ok := container[0].([]any)
	if !ok {
		return nil
	}
	out := make([]Topic, 0, len(list))
	for _, item := range list {
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
		out = append(out, t)
	}
	return out
}

// isNotFound classifies a transport-layer error as a typed "not
// found" signal. The wire layer surfaces a "rpc id 5 with NOT_FOUND
// status" path via wire.ErrClient and a "get_or_none caught an
// empty payload" path via rows.ErrNotFound; both roll up here.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if rows.IsNotFound(err) {
		return true
	}
	// wire.ErrClient with grpc status 5 (NOT_FOUND) — the wire layer
	// already wrapped it; we re-classify rather than re-wrap so the
	// features layer never has to know about wire internals.
	if errors.Is(err, wire.ErrClient) {
		// Best-effort: errors.As for the grpc status code. The wire
		// layer's typed error carries it as a sibling attribute; we
		// match on substring to avoid a second typed error just for
		// the status code. The wire layer's error message already
		// contains "status 5" in the NOT_FOUND case.
		return containsStatus(err.Error(), 5)
	}
	return false
}

// containsStatus reports whether errMsg contains the wire-layer status
// fragment for code. We hand-roll this rather than importing strings
// because every error message the wire layer emits is plain ASCII.
func containsStatus(errMsg string, code int) bool {
	needle := "status "
	if code < 0 {
		return false
	}
	// Linear scan — error messages are < 300 chars.
	for i := 0; i+len(needle) < len(errMsg); i++ {
		if errMsg[i:i+len(needle)] != needle {
			continue
		}
		rest := errMsg[i+len(needle):]
		// Parse the integer at the start of rest.
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 0 {
			continue
		}
		val := 0
		for k := 0; k < j; k++ {
			val = val*10 + int(rest[k]-'0')
		}
		if val == code {
			return true
		}
	}
	return false
}

// typeName is the small helper that gives the *ShapeDriftError.GotType
// field a stable format. fmt.Sprintf("%T", x) returns the package-
// qualified type name; we trim the package prefix so the diagnostic
// stays readable.
func typeName(v any) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case []any:
		return "[]any"
	case map[string]any:
		return "map[string]any"
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "float64"
	}
	return fmt.Sprintf("%T", v)
}
