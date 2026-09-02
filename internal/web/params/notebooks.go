// Package params contains the byte-stable request-payload builders for the
// `batchexecute` notebook surface.
//
// Every exported builder corresponds to one Python original in
// notebooklm-py/src/notebooklm/_web/notebooks.py (and _web/sharing.py for the
// share RPCs). Per AGENTS.md rule 1 the Python original is normative — we
// copy the positional shape position-for-position, and each function cites
// the Python symbol it ports in a comment so a future reviewer can audit
// the diff without leaving the file.
//
// JSON encoding is delegated to wire.Marshal (AGENTS.md rule 3): this
// package never imports encoding/json directly. The byte output of every
// builder is locked by a golden-bytes test under testdata/golden/.
//
// All 17 builders listed in the T-P5-4 ticket body are present here, even
// when the wire shape is currently indistinguishable from a sibling:
// `BuildGetRecent` aliases `BuildList` because the wire currently has no
// dedicated "recent-only" RPC; the alias is the documented seam for adding
// it later without churning call sites.
package params

import (
	"strings"

	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// ShareAccess is the wire integer for the `ShareNotebook` access-level
// envelope (port of `_types.enums.ShareAccess`). 0 = RESTRICTED, 1 =
// ANYONE_WITH_LINK. Distinct from `ShareViewLevel` (the chat-only /
// full-notebook axis), which rides on the same `s0tc2d` mutation but on a
// different slot.
const (
	ShareAccessRestricted     = 0
	ShareAccessAnyoneWithLink = 1
)

// SharePermission is the wire integer for the per-user permission slot on
// `SHARE_NOTEBOOK` (port of `_types.enums.SharePermission`). OWNER (1) is
// read-only and cannot be assigned through this RPC; EDITOR = 2, VIEWER =
// 3; the value 4 is the write-only `_REMOVE` sentinel the backend uses for
// revocation.
const (
	SharePermissionOwner  = 1
	SharePermissionEditor = 2
	SharePermissionViewer = 3
	SharePermissionRemove = 4 // _REMOVE; write-only sentinel
)

// ShareViewLevel is the wire integer for the `SetShareAccess` slot on the
// generic `s0tc2d` mutator (port of `_types.enums.ShareViewLevel`).
// 0 = FULL_NOTEBOOK, 1 = CHAT_ONLY.
const (
	ShareViewLevelFullNotebook = 0
	ShareViewLevelChatOnly     = 1
)

// BuildList returns the positional payload for `ListNotebooks` (`wXbhsf`,
// `ListRecentlyViewedProjects`). The trailing `[2]` is the single-element
// variant tag the backend discriminates on — NOT the shared template
// block.
//
// Port of `_web/notebooks.py::WebNotebooksAPI.list` — the literal at line
// 499: `params = [None, 1, None, [2]]`. See docs/04-rpc-payloads.md
// §"Notebooks" for the canonical contract.
func BuildList() []any {
	return []any{nil, 1, nil, []any{2}}
}

// BuildCreate returns the positional payload for `CreateNotebook`
// (`CCqFvf`, `CreateProject`). The trailing template block was made
// mandatory by the Gemini-3.5 rollout (#1546); the old flat `[2], [1]`
// tail is rejected with gRPC status 3/5/9 on migrated backends.
//
// Port of `_web/params/notebooks.py::build_create_notebook_params`. The
// fresh-per-call template block (wire.TemplateBlock) is load-bearing — the
// protocol parser detects shared mutable nested literals and rejects them.
func BuildCreate(title string) []any {
	return []any{title, nil, nil, wire.TemplateBlock(nil)}
}

// BuildDelete returns the positional payload for `DeleteNotebook`
// (`WWINqb`, `DeleteProjects`). Despite the plural name, the live backend
// accepts exactly one id here — live-probed batch variants
// ([[id1,id2,id3],[2]] / [ids,[2,2,2]] / etc.) all return rpc_code=3
// (invalid argument). Delete one notebook per call.
//
// Port of `_web/notebooks.py::WebNotebooksAPI.delete` — the literal at
// line 756: `params = [[notebook_id], [2]]`.
func BuildDelete(notebookID string) []any {
	return []any{[]any{notebookID}, []any{2}}
}

// BuildRename returns the positional payload for `RenameNotebook`
// (`s0tc2d`, `MutateProject`). This is a generic notebook mutator: the
// same RPC also carries chat config and `SetShareAccess` mutations — each
// with a different positional shape, distinguished by which slot is
// populated. The change-property variant lives at slot 3 (tag 2 = title,
// tag 3 = emoji).
//
// `emoji == ""` (empty string) is sent verbatim and CLEARS the emoji; a
// nil emoji drops the slot entirely so the title-only mutation matches
// its recorded-cassette byte shape. Callers must validate that at least
// one of title/emoji is supplied (see docs/04-rpc-payloads.md §"Notebooks").
//
// Port of `_web/params/notebooks.py::build_update_notebook_params`.
func BuildRename(notebookID, title string, emoji *string) []any {
	changeProperty := []any{nil, title}
	if emoji != nil {
		changeProperty = append(changeProperty, *emoji)
	}
	return []any{notebookID, []any{[]any{nil, nil, nil, changeProperty}}}
}

// BuildGet returns the positional payload for `GetNotebook` (`rLM1Ne`,
// `GetProject`). The Gemini-3.5 rollout migrated the trailing template
// block from the flat `[2]` to the nested wire.TemplateBlock wrapper
// (#1549); verified live-compatible against un-migrated accounts — the
// nested shape returns a byte-identical decoded notebook.
//
// Port of `_web/params/notebooks.py::build_get_notebook_params`. Trailing
// `None, 0` is unchanged: only the template block at position 2 migrated.
func BuildGet(notebookID string) []any {
	return []any{notebookID, nil, wire.TemplateBlock(nil), nil, 0}
}

// BuildSummary returns the positional payload for `Summarize`
// (`VfAZjd`, `GenerateNotebookGuide`). The response carries both the
// summary string and the suggested-topics list at `result[0]`.
//
// Port of `_web/notebooks.py::WebNotebooksAPI.get_summary` /
// `get_description` — the literal at line 806 / 851:
// `params = [notebook_id, [2]]`.
func BuildSummary(notebookID string) []any {
	return []any{notebookID, []any{2}}
}

// BuildMetadata is the alias for BuildGet used by the notebook metadata
// surface. There is no dedicated `GET_METADATA` RPC on master —
// `NotebookAPI.get_metadata` (port of `_notebook_metadata.py`) composes
// `GetProject` with a source-list pass to produce the typed
// `NotebookMetadata`. The T-P5-5 ticket will pin this composite; here we
// just expose the same shape under its future name so callers can adopt
// it without a later rename.
//
// TODO(T-P5-5): if the metadata composite grows a dedicated wire shape,
// replace this body with the new builder and keep the signature stable.
func BuildMetadata(notebookID string) []any {
	return BuildGet(notebookID)
}

// BuildAddSource is reserved for the notebook-level source-membership
// RPC. It is intentionally a stub: notebooklm-py has no such RPC today —
// source lifecycle lives in `notebooklm._web.sources` (T-P6-2 owns that
// port). Returning nil is the documented "not yet wired" sentinel; the
// TODO is what callers grep for.
//
// TODO(T-P6-2): replace with the port of `_web/sources/add.py::AddSource`.
// Until then the function panics rather than silently building a wrong
// payload.
func BuildAddSource(_ string, _ []string) []any {
	panic("params.BuildAddSource: not implemented — source lifecycle is T-P6-2 (notebooks-go issue #56)")
}

// BuildRemoveSource is the symmetric counterpart to BuildAddSource.
// See BuildAddSource for the rationale.
//
// TODO(T-P6-2): replace with the port of `_web/sources/delete.py`.
func BuildRemoveSource(_ string, _ []string) []any {
	panic("params.BuildRemoveSource: not implemented — source lifecycle is T-P6-2 (notebooks-go issue #56)")
}

// BuildShare returns the positional payload for `ShareNotebook`
// (`QDyure`, `LabsTailwindSharingService.ShareProject`). The four-slot
// envelope at index 0 carries [notebookID, [access, access, ""]] where
// access is `ShareAccessRestricted` (0) or `ShareAccessAnyoneWithLink`
// (1). The trailing `1` and `[2]` are the public-flag and the single-tag
// variant respectively.
//
// Port of `_web/sharing.py::WebSharingAPI.set_public` — the literal at
// line 88: `params = [[[notebook_id, None, [access.value],
// [access.value, ""]]], 1, None, [2]]`.
func BuildShare(notebookID string, public bool) []any {
	access := ShareAccessRestricted
	if public {
		access = ShareAccessAnyoneWithLink
	}
	row := []any{
		notebookID,
		nil,
		[]any{access},
		[]any{access, ""},
	}
	return []any{
		[]any{row},
		1,
		nil,
		[]any{2},
	}
}

// BuildUnshare is the alias for `BuildShare(notebookID, false)` — the
// public-flag toggle is the only difference. The slot-2 access value
// stays the same as for the public path; only the `1` public-flag at
// slot 1 of the inner envelope inverts.
//
// Port of `_web/sharing.py::WebSharingAPI.set_public(notebook_id, False)`
// — same shape as the public branch; the semantic flag rides inside the
// wrapper, not in the access value itself.
func BuildUnshare(notebookID string) []any {
	return BuildShare(notebookID, false)
}

// BuildGetShareStatus returns the positional payload for
// `GetShareStatus` (`JFMDGd`,
// `LabsTailwindSharingService.GetProjectDetails`). The response cannot
// report `view_level` (the backend omits it); callers that need it must
// use BuildSetShareAccess instead.
//
// Port of `_web/sharing.py::WebSharingAPI.get_status` — the literal at
// line 59: `params = [notebook_id, [2]]`.
func BuildGetShareStatus(notebookID string) []any {
	return []any{notebookID, []any{2}}
}

// BuildRemoveCollaborator returns the positional payload for
// `ShareNotebook` (`QDyure`) in the singular-removal variant. The
// grant-batch path is rejected by the backend when ANY entry targets a
// user that is already absent — the whole request becomes a silent
// no-op. The single-entry path is the safe shape; the grant path is not
// exposed here.
//
// `notify` is fixed to `false` (matching the historical remove-user
// payload); the grant path's `[0/1, message]` block reduces to
// `[0, ""]`.
//
// Port of `_web/sharing.py::WebSharingAPI.remove_user` and the
// `_share_params` helper at line 145.
func BuildRemoveCollaborator(notebookID, email string) []any {
	row := []any{
		notebookID,
		[]any{
			[]any{email, nil, SharePermissionRemove},
		},
		nil,
		[]any{0, ""},
	}
	return []any{
		[]any{row},
		0, // notify=false
		nil,
		[]any{2},
	}
}

// BuildSetShareAccess returns the positional payload for the
// view-level variant of `RenameNotebook` (`s0tc2d`). The backend
// distinguishes this from a rename by which slot is populated: a
// view-level mutation carries a single `[[level.value]]` at slot 8 of
// the inner change-property block (one nesting level deeper than the
// rename path).
//
// Port of `_web/sharing.py::WebSharingAPI.set_view_level` — the literal
// at line 122:
// `params = [notebook_id, [[None, None, None, None, None, None, None, None, [[level.value]]]]]`.
func BuildSetShareAccess(notebookID string, level int) []any {
	return []any{
		notebookID,
		[]any{[]any{nil, nil, nil, nil, nil, nil, nil, nil, []any{[]any{level}}}},
	}
}

// BuildGetRecent is the alias for BuildList. The wire has no dedicated
// "recent-only" RPC; the only list endpoint is `ListRecentlyViewedProjects`
// (`wXbhsf`), which already returns notebooks in recency order. The alias
// exists so callers can adopt the more specific name now and a future
// filter RPC (a hypothetical `RECENT` variant) can replace the body
// without renaming the function.
func BuildGetRecent() []any {
	return BuildList()
}

// BuildRemoveRecentlyViewed returns the positional payload for
// `RemoveRecentlyViewed` (`fejl7e`,
// `LabsTailwindNotebookService.RemoveRecentlyViewed`). The single-arg
// payload is the notebook id; allowNull=true on the call site because
// the backend returns null on a successful removal.
//
// Port of `_web/notebooks.py::WebNotebooksAPI.remove_from_recent` line
// 886: `params = [notebook_id]`.
func BuildRemoveRecentlyViewed(notebookID string) []any {
	return []any{notebookID}
}

// BuildGetStarred is reserved for a future `LIST_STARRED` RPC. There is
// no Python original today: the `is_starred` column lives on every
// existing row and the Python client surfaces it via `Notebook.from_api_response`
// but does not yet expose a dedicated list endpoint.
//
// TODO(T-P5-5 or later): port from `_web/notebooks.py` once the Python
// original lands a builder. Until then this is a stub — panicking keeps
// the wrong-payload failure mode loud.
func BuildGetStarred() []any {
	panic("params.BuildGetStarred: not implemented — defer to T-P5-5 when notebooklm-py adds a list_starred endpoint")
}

// BuildGetSharedWithMe is the same TODO as BuildGetStarred. The
// `is_shared` column exists on every row and `LIST_NOTEBOOKS` already
// filters shared-with-me through a slot argument that the live probe
// has not yet pinned. We deliberately do NOT call BuildList here — a
// silent alias would mask the missing RPC.
//
// TODO(T-P5-5 or later): port from `_web/notebooks.py` once the Python
// original lands a list_shared_with_me builder. Until then this is a
// stub.
func BuildGetSharedWithMe() []any {
	panic("params.BuildGetSharedWithMe: not implemented — defer to T-P5-5 when notebooklm-py adds a list_shared_with_me endpoint")
}

// BuildGetByProject is the same TODO as BuildGetStarred. The
// `project_id` column exists on every row but no dedicated list-by-project
// endpoint has been captured.
//
// TODO(T-P5-5 or later): port from `_web/notebooks.py` once the Python
// original lands a list_by_project builder. Until then this is a stub.
func BuildGetByProject(_ string) []any {
	panic("params.BuildGetByProject: not implemented — defer to T-P5-5 when notebooklm-py adds a list_by_project endpoint")
}

// Encode is the thin convenience wrapper that encodes a builder result
// through wire.Marshal and returns the JSON bytes. Tests use this so the
// golden-bytes table reads the same way the transport will read it.
//
// This wrapper exists so callers do not have to import wire.Marshal
// directly — params owns the wire shapes, the bytes are an implementation
// detail of the same shape. Returning a single byte slice (no error
// shadowing) is fine: every builder returns plain `[]any` literals, which
// always marshal cleanly. A future builder that needs a richer value
// (e.g. a channel — defensive only) will surface here, not at the wire.
func Encode(builder func() []any) ([]byte, error) {
	return wire.Marshal(builder())
}

// EscapeEmail is a defensive helper used by the share builders to
// reject whitespace and control characters in the email slot. The
// backend silently drops a malformed address (the per-grant failure
// mode #2130 documents), so we reject early with a typed error rather
// than letting the wrong identity leak into a share envelope.
//
// Lives in this file because the share builders are the only callers;
// a future artifact/label share builder can reuse it.
func EscapeEmail(email string) error {
	if email == "" {
		return &paramError{Field: "email", Reason: "must not be empty"}
	}
	if strings.ContainsAny(email, " \t\r\n") {
		return &paramError{Field: "email", Reason: "must not contain whitespace"}
	}
	if !strings.Contains(email, "@") {
		return &paramError{Field: "email", Reason: "must contain '@'"}
	}
	return nil
}

// paramError is a small typed error so callers can route "bad share
// argument" into the same ValidationError class the public SDK exposes,
// without dragging a stdlib errors import into every build site.
type paramError struct {
	Field  string
	Reason string
}

func (e *paramError) Error() string {
	return "params: " + e.Field + " " + e.Reason
}
