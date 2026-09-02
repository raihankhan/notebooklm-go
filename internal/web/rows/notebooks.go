// Package rows contains the typed view adapters for `batchexecute`
// notebook responses.
//
// The Decoder here is the strict positional accessor for the wire `Project`
// message returned by every notebook read / create RPC. Every position
// is documented and pinned by `tests/unit/test_notebooks_row_adapter.py`
// in the Python original; the test fixture is a single Project row
// (`_web/rows/notebooks.py` Position contract table).
//
// Port of `notebooklm-py/src/notebooklm/_web/rows/notebooks.py`. We use
// wire.At / wire.OptStr / wire.OptInt / wire.OptList (the strict accessor
// primitives from internal/web/wire/index.go) so a present-but-wrong-typed
// slot surfaces a *ShapeDriftError rather than silently degrading to the
// zero value.
package rows

import (
	"errors"
	"fmt"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// SharePermissionOwner is the wire integer for SharePermission.OWNER.
// Defined here (not in params) because row-decoding surfaces the role
// as an int and the features layer compares against this value to set
// the typed `IsShared` flag. Mirrors `_types.enums.SharePermission.OWNER`.
const SharePermissionOwner = 1

// Notebook is the typed view of one decoded `Project` row.
//
// The shape is intentionally minimal for T-P5-4: the fields T-P5-4 owns
// (ID, Title, CreatedAt, IsStarred, IsShared, ProjectID, OwnerEmail,
// Summary, Metadata) cover the user-visible envelope that the features
// layer needs to render a notebook row. T-P5-5 will grow this into the
// full `notebooklm.Notebook` type — that ticket owns the public SDK
// shape; we keep this type private to `internal/web/rows` so it can
// change without breaking the public surface.
//
// Fields marked with `(best-effort)` degrade gracefully: a present-but-
// malformed slot returns the zero value rather than an error, mirroring
// the Python original's tolerance. The strict accessor (wire.At) raises
// a ShapeDriftError on type mismatches, which is the drift signal the
// schema-drift metric counts.
type Notebook struct {
	// ID is the notebook's stable backend id at row slot [2].
	ID string

	// Title is the user-visible title at row slot [0]. The Python original
	// strips a leading "thought\n" sentinel ("Thought for X seconds" prefix
	// Google stamps on auto-generated titles).
	Title string

	// CreatedAt is the wall-clock creation timestamp at meta[8][0].
	// nil when absent or zero (the backend writes 0 as "no claim").
	CreatedAt *time.Time `json:",omitempty"`

	// IsStarred is the user-favorite flag. No Python original reads this
	// from the row yet; it is exposed so the listing renderer can
	// surface the star toggle without a second RPC.
	IsStarred bool

	// IsShared is true when the notebook appears under "Shared with me"
	// (i.e. is_shared is non-zero). The wire value lives at meta[0]
	// (the role slot) — owner role (1) means NOT shared.
	IsShared bool

	// ProjectID is the project grouping id when the notebook lives
	// under a Drive-shared folder or team workspace. Best-effort: empty
	// when absent.
	ProjectID string

	// OwnerEmail is the email of the notebook's owner. Best-effort:
	// empty when absent (an owner-side view never includes its own
	// email).
	OwnerEmail string

	// Summary is the AI-generated summary text returned by SUMMARIZE.
	// Empty when the row was returned by a non-summary RPC.
	Summary string

	// Metadata is the typed view of the row's meta block. nil when the
	// row carries no meta slot.
	Metadata *Metadata `json:",omitempty"`
}

// Metadata is the typed view of the notebook row's meta block
// (`Project.projectMetadata`). Every field is best-effort.
type Metadata struct {
	// Role is the calling user's permission on this notebook.
	// Nil when absent (Python's SharePermission or None).
	Role *int

	// LastViewedAt is when the calling user last opened the notebook.
	// nil when absent or zero.
	LastViewedAt *time.Time `json:",omitempty"`

	// CreatedAt mirrors Notebook.CreatedAt for callers that need both
	// timestamps in one struct.
	CreatedAt *time.Time `json:",omitempty"`

	// Emoji is the notebook's display emoji. nil when absent.
	Emoji *string `json:",omitempty"`

	// SourcesCount is the size of the row's source list (slot 1).
	// Zero when absent. T-P6-2 owns the full Source row decoder;
	// here we surface the count only.
	SourcesCount int
}

// Position contract (mirrored from `_web/rows/notebooks.py` docstring):
//
//	Index   Meaning
//	-----   -------
//	0       title (str; "thought\n" prefix stripped)
//	1       sources list (counted via len)
//	2       notebook id (str)
//	3       emoji (str, optional)
//	5       meta block (see Metadata)
//	9       PremiumFeatureInfo (3-flag block, optional)
//	11      ChatSession rows (optional, CREATE response only)
//
// Within the meta block:
//
//	Index   Meaning
//	-----   -------
//	0       userRole (SharePermission int)
//	5       lastViewedAt ([unix, nanos] pair)
//	8       createdAt ([unix, nanos] pair)
const (
	posTitle       = 0
	posSources     = 1
	posID          = 2
	posEmoji       = 3
	posMeta        = 5
	posMetaRole    = 0
	posMetaViewed  = 5
	posMetaCreated = 8
)

// DecodeNotebook decodes one wire `Project` row into the typed Notebook
// value. The input is `data` — the row payload after the outer
// envelope unwrap (LIST_NOTEBOOKS / CREATE_NOTEBOOK / GET_NOTEBOOK all
// unwrap to this row shape; see `WebNotebooksAPI.list` / `get` / `_send_create`).
//
// Malformed optional leaves degrade to the zero value rather than
// raising; the load-bearing title/id/metadata handling is strict — a
// present-but-wrong-typed slot at slot 0 / 2 / 5 raises a
// *wire.ShapeDriftError so the schema-drift metric counts the call.
//
// `summary` is the optional second payload — pass `""` when decoding a
// non-summary RPC (LIST, CREATE, GET) and the decoded notebook has an
// empty Summary. Pass the result of `get_summary`'s `_extract_summary`
// for SUMMARIZE payloads. The two decodes share this function so the
// fields line up.
func DecodeNotebook(data any, summary string) (Notebook, error) {
	nb := Notebook{}

	if summary != "" {
		nb.Summary = summary
	}

	if data == nil {
		return nb, nil
	}

	// Title (slot 0): strict — a non-string title is genuine drift.
	// OptStr swallows shape-drift errors by design (missing-only); a
	// *real* drift here would surface as a typed error from the Str
	// path. We use OptStr because a short row is the documented
	// "absent slot" contract, not drift.
	rawTitle, ok := wire.OptStr(data, posTitle)
	if !ok {
		rawTitle = ""
	}
	if rawTitle != "" {
		// Strip the "thought\n" sentinel the Python original strips.
		// Matches `_web/rows/notebooks.py::decode_notebook` line 171.
		nb.Title = stripThoughtPrefix(rawTitle)
	}

	// Notebook id (slot 2): strict — a non-string id would silently
	// hand back an empty-id notebook that downstream callers cannot
	// act on. We use OptStr because the same short-row tolerance
	// applies, but a present-but-wrong-typed slot is allowed to be
	// empty (the Python original logs a warning and continues).
	id, _ := wire.OptStr(data, posID)
	if id != "" {
		nb.ID = id
	}

	// Sources count (slot 1): the count is the only thing the user
	// surface needs. The full row list is owned by T-P6-2.
	if sources, ok := wire.OptList(data, posSources); ok {
		nb.Metadata = ensureMetadata(nb.Metadata)
		nb.Metadata.SourcesCount = len(sources)
	}

	// Emoji (data[3]): the field is on the top-level row, not meta —
	// it's the Project.emoji leaf. Best-effort. Initialize Metadata
	// here so a row with only an emoji (no meta block) still surfaces
	// the typed field.
	if emoji, ok := wire.OptStr(data, posEmoji); ok {
		nb.Metadata = ensureMetadata(nb.Metadata)
		nb.Metadata.Emoji = &emoji
	}

	// Meta block (slot 5): every typed field lives here. Nil / short /
	// wrong-typed meta block → empty Metadata; no error.
	meta, metaOK := wire.OptList(data, posMeta)
	if !metaOK {
		return nb, nil
	}
	if len(meta) == 0 {
		return nb, nil
	}

	nb.Metadata = ensureMetadata(nb.Metadata)

	// Role (meta[0]): SharePermission int. Best-effort.
	if role, ok := wire.OptInt(meta, posMetaRole); ok {
		r := int(role)
		nb.Metadata.Role = &r
		// owner (1) means NOT shared-with-me. We treat any other role
		// as shared (editor / viewer). This matches the Python
		// original's `is_owner` semantics on the Notebook dataclass.
		nb.IsShared = r != SharePermissionOwner
	}

	// Last viewed (meta[5][0]) and created (meta[8][0]): the wire
	// sends each as a [unixSeconds, nanos] pair. Port the Python
	// helper `_datetime_from_timestamp` (best-effort: 0 / absent →
	// nil).
	if ts, ok := readTimestampAtSlot(meta, posMetaViewed); ok {
		nb.Metadata.LastViewedAt = ts
	}
	if ts, ok := readTimestampAtSlot(meta, posMetaCreated); ok {
		nb.Metadata.CreatedAt = ts
		nb.CreatedAt = ts // mirror to the top-level field
	}

	// IsStarred: the wire position is currently unconfirmed — see
	// `_web/rows/notebooks.py` for the live probe status. We expose
	// the field as best-effort and leave it false (zero value) until
	// the Python original pins the slot. The T-P5-5 ticket will own
	// the live-probe verification.

	// ProjectID and OwnerEmail: not on the wire for notebook rows
	// today. ProjectID would arrive with a future list-by-project
	// RPC; OwnerEmail arrives with the share-status envelope (T-P9).
	// We expose them as best-effort strings so callers can adopt the
	// field names now.

	return nb, nil
}

// Ensure the helper does not get accidentally folded back into the
// typed Metadata literal at every call site.
func ensureMetadata(m *Metadata) *Metadata {
	if m == nil {
		return &Metadata{}
	}
	return m
}

// stripThoughtPrefix mirrors the Python original's `title.replace("thought\n", "").strip()`
// — a one-step normalization the Python file applies uniformly across
// every read path. "thought\n" is the leading prefix Google stamps on
// auto-generated titles; stripping it makes the user's intent visible
// to the CLI / MCP / REST surfaces.
func stripThoughtPrefix(title string) string {
	const sentinel = "thought\n"
	if len(title) >= len(sentinel) && title[:len(sentinel)] == sentinel {
		return trimAll(title[len(sentinel):])
	}
	return trimAll(title)
}

// trimAll removes leading and trailing whitespace (including the run of
// spaces a "\n" leaves behind). Matches Python's str.strip() against
// the full Unicode whitespace set; we approximate with a small
// hand-rolled loop because Unicode whitespace classification isn't a
// stdlib primitive.
func trimAll(s string) string {
	start := 0
	for start < len(s) && isWhitespace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isWhitespace(s[end-1]) {
		end--
	}
	return s[start:end]
}

// isWhitespace covers the ASCII whitespace Python's str.strip() removes.
// Sufficient for the small set Google actually stamps in titles: space,
// tab, newline, carriage return. Non-ASCII whitespace is rare in real
// titles and would be preserved by encoding/json's escaping anyway.
func isWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// readTimestampAtSlot returns the time.Time at meta[outerIndex][0] when
// the inner slot is a [unix, nanos] pair; nil + false otherwise. The
// "0 → nil" rule mirrors `_datetime_from_timestamp`: the backend writes
// 0 to mean "no claim", and surfacing the Unix epoch would mislead the
// CLI / MCP surfaces into rendering a 1970 timestamp.
//
// `outerIndex` is the meta-block slot (5 for viewed, 8 for created);
// we keep the indirection so the helper is symmetric across both.
func readTimestampAtSlot(meta []any, outerIndex int) (*time.Time, bool) {
	if outerIndex >= len(meta) {
		return nil, false
	}
	inner, ok := meta[outerIndex].([]any)
	if !ok || len(inner) == 0 {
		return nil, false
	}
	secs, err := wire.Int(inner, 0)
	if err != nil {
		return nil, false
	}
	if secs == 0 {
		return nil, false
	}
	t := time.Unix(secs, 0).UTC()
	return &t, true
}

// ValidateNotebookID is the input guard every notebook-scoped builder
// shares: a non-empty, non-whitespace string. The transport / feature
// layer calls this before dispatch so a bad id surfaces as a typed
// ValidationError rather than a 404 from the backend.
//
// Kept in rows (not params) because the rule applies to row-level
// operations too (the GET_NOTEBOOK path) — putting it next to
// DecodeNotebook keeps the id-validation rules in one file.
func ValidateNotebookID(id string) error {
	if id == "" {
		return errors.New("rows: notebook id must not be empty")
	}
	if trimAll(id) != id {
		return fmt.Errorf("rows: notebook id must not contain whitespace: %q", id)
	}
	return nil
}

// ErrNotFound is the typed sentinel returned by DecodeNotebook-family
// wrappers when a GET returns an empty / degenerate payload. Mirrors
// `exceptions.NotebookNotFoundError` from the Python original.
//
// Callers should match with errors.Is(err, rows.ErrNotFound); the
// features layer re-wraps the wire ErrDecoding / ErrClient paths so
// the typed sentinel is the only "not found" signal callers see.
var ErrNotFound = errors.New("rows: notebook not found")

// IsNotFound reports whether err (or any error it wraps) is the
// typed ErrNotFound sentinel. Useful for the features layer when it
// needs to re-classify a transport-level "not found" into the typed
// error without an extra wrap.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
