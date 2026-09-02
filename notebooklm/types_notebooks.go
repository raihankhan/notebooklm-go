// Package notebooklm — types_notebooks.go.
//
// Public typed views for the NotebooksAPI surface. The internal layer
// (internal/web/rows/notebooks.go) carries its own private `Notebook`
// type so the wire-shape work can iterate without churning the public
// API; this file is the seam that projects the internal views into the
// stable public types the SDK ships.
//
// Per docs/AGENTS.md rule 1, every type here is a faithful port of the
// corresponding Python original in `notebooklm-py`. The Python types are
// normative — port the field set verbatim, never add fields Google
// does not emit, and never drop fields the Python surface exposes.
package notebooklm

import "time"

// Notebook is the public typed view of one decoded Project row.
//
// The field set mirrors `notebooklm-py/src/notebooklm/_types/notebooks.py`
// (`@dataclass class Notebook`). Fields Google does not yet surface
// (ProjectID, OwnerEmail) are exposed as best-effort strings so callers
// can adopt the field names now; the TODO rows in
// internal/web/rows/notebooks.go track the live-probe status.
type Notebook struct {
	// ID is the notebook's stable backend id. Empty when the row
	// did not carry the id slot (the short-row tolerance).
	ID string

	// Title is the user-visible title with the leading "thought\n"
	// sentinel the Python original strips.
	Title string

	// CreatedAt is the wall-clock creation timestamp. nil when
	// the backend writes 0 (its "no claim" sentinel).
	CreatedAt *time.Time

	// IsStarred is the user-favorite flag. False until the wire
	// probe lands the slot — see T-P5-5 TODO.
	IsStarred bool

	// IsShared is true when the notebook appears under "Shared
	// with me" (i.e. role != OWNER).
	IsShared bool

	// ProjectID is the project-grouping id when the notebook
	// lives under a Drive-shared folder or team workspace. Empty
	// when absent (the row did not carry the slot).
	ProjectID string

	// OwnerEmail is the email of the notebook's owner. Empty on
	// owner-side views (the row omits its own email).
	OwnerEmail string

	// Summary is the AI-generated summary text returned by
	// SUMMARIZE. Empty when the row was returned by a non-summary
	// RPC (LIST, CREATE, GET).
	Summary string

	// Metadata is the typed view of the row's meta block. nil
	// when the row carried no meta slot.
	Metadata *Metadata
}

// Metadata is the typed view of the Notebook row's meta block.
//
// Mirrors `_types.notebooks.NotebookMetadata` (Python dataclass).
// Every field is best-effort: a row that omits the meta block
// returns a zero-value Metadata, never an error.
type Metadata struct {
	// Role is the calling user's permission on this notebook.
	// 1 = OWNER, 2 = EDITOR, 3 = VIEWER. nil when absent.
	Role *int

	// LastViewedAt is when the calling user last opened the
	// notebook. nil when absent or zero.
	LastViewedAt *time.Time

	// CreatedAt mirrors Notebook.CreatedAt for callers that
	// need both timestamps in one struct.
	CreatedAt *time.Time

	// Emoji is the notebook's display emoji. nil when absent.
	Emoji *string

	// SourcesCount is the size of the row's source list.
	// Zero when the row carried no sources slot.
	SourcesCount int
}

// Summary is the typed view of the SUMMARIZE RPC response.
//
// Mirrors `_types.notebooks.NotebookDescription`. The description
// envelope carries the summary text plus a suggested-topics list;
// the public SDK exposes both as typed fields.
type Summary struct {
	// Summary is the AI-generated summary text. Empty when the
	// backend returned an empty / null summary envelope.
	Summary string

	// SuggestedTopics are the (question, prompt) pairs the
	// backend recommends. Nil when the topics slot was absent.
	SuggestedTopics []Topic
}

// Topic is one (question, prompt) entry from the SUMMARIZE
// suggested-topics list. Mirrors `_types.notebooks.SuggestedTopic`.
//
// Per-topic field checks degrade to empty strings; a partially
// populated topics list is more useful than an empty one (the
// permissive contract `_web/rows/notebooks.py` applies).
type Topic struct {
	// Question is the user-facing question the topic poses.
	Question string

	// Prompt is the ready-to-send prompt that surfaces the
	// topic in the notebook's chat.
	Prompt string
}

// ShareAccess is the typed view of `_types.enums.ShareAccess`.
// Mirrors `notebooklm/_types/enums.py::ShareAccess`. The numeric
// values are wire-stable (RESTRICTED = 0, ANYONE_WITH_LINK = 1).
type ShareAccess int

const (
	// ShareAccessRestricted — only invited collaborators can open
	// the notebook.
	ShareAccessRestricted ShareAccess = 0

	// ShareAccessAnyoneWithLink — anyone with the share URL can
	// open the notebook.
	ShareAccessAnyoneWithLink ShareAccess = 1
)

// ShareState is the typed view of the GET_SHARE_STATUS response.
//
// Mirrors `_types.share.ShareStatusResponse`. The public SDK keeps
// the field set minimal so the type stays backend-agnostic when the
// share envelope grows.
type ShareState struct {
	// ID is the notebook id the share envelope refers to.
	ID string

	// IsPublic is true when the notebook is reachable through a
	// public share link. False for restricted notebooks.
	IsPublic bool

	// AccessLevel is the canonical ShareAccess value
	// (RESTRICTED = 0, ANYONE_WITH_LINK = 1).
	AccessLevel ShareAccess

	// Collaborators are the per-email permission records.
	// Nil when absent.
	Collaborators []Collaborator
}

// Collaborator is one (email, role) entry from the share status
// envelope. Mirrors `_types.share.ShareCollaborator`.
//
// Role values mirror SharePermission (1 = OWNER, 2 = EDITOR,
// 3 = VIEWER, 4 = REMOVE). The 4 sentinel is write-only and never
// surfaces in GET_SHARE_STATUS responses.
type Collaborator struct {
	// Email is the granted user's email address.
	Email string

	// Role is the granted permission (OWNER/EDITOR/VIEWER).
	Role SharePermission
}

// SharePermission is the typed view of `_types.enums.SharePermission`.
// Mirrors `notebooklm/_types/enums.py::SharePermission`. The numeric
// values are wire-stable; the SDK exposes named constants so callers
// do not pass magic numbers across the boundary.
type SharePermission int

const (
	// SharePermissionOwner — owner role. Read-only on the wire;
	// cannot be assigned through the public Share RPC.
	SharePermissionOwner SharePermission = 1

	// SharePermissionEditor — the user can edit the notebook.
	SharePermissionEditor SharePermission = 2

	// SharePermissionViewer — read-only access.
	SharePermissionViewer SharePermission = 3
)

// Page is the typed envelope the listing methods (List, GetRecent,
// GetStarred, GetSharedWithMe, GetByProject) return.
//
// Notebooks do not currently page server-side — the live
// ListRecentlyViewedProjects endpoint returns the whole list in
// one envelope — but the type exposes NextOffset / HasMore so a
// future paged listing can land without renaming call sites.
//
// `Page` uses a concrete `[]Notebook` rather than generics: every
// existing envelope in this module is `[]Notebook`-shaped, and the
// concrete slice keeps the call sites readable.
type Page struct {
	// Items is the slice of decoded rows in the page.
	Items []Notebook

	// NextOffset is the opaque continuation token returned by the
	// backend. Empty when the current page is terminal.
	NextOffset string

	// HasMore is true when the backend reports additional pages
	// beyond this one. False when there are no more rows to fetch.
	HasMore bool
}
