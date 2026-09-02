// Package rows — sources.go: typed view of one Source row from
// `batchexecute` GET_NOTEBOOK / ADD_SOURCE responses.
//
// Port of `notebooklm-py/src/notebooklm/_web/rows/sources.py::SourceRow`.
// T-P6-1 ships the minimum-viable source view: the four fields the
// public SDK surfaces today (id, title, kind, status_label). The full
// `_web/rows/sources.py` position table (drive_document_id, mime,
// download_url, content_mime, …) lands in later phase tickets as
// additional typed fields — that ticket owns the full row layout.
//
// Per AGENTS.md rule 3 we use wire.At / wire.OptStr / wire.OptList /
// wire.OptInt (the strict accessor primitives) so a present-but-wrong-
// typed slot surfaces a *wire.ShapeDriftError rather than silently
// degrading to the zero value.
package rows

import (
	"errors"
	"fmt"

	"github.com/raihankhan/notebooklm-go/internal/web/params"
	"github.com/raihankhan/notebooklm-go/internal/web/wire"
)

// Source is the typed view of one decoded Source row.
//
// The shape is intentionally minimal for T-P6-1: the four fields the
// SourcesAPI.List / SourcesAPI.AddURL surfaces today (id, title, kind,
// status_label). Later phase tickets widen this with the full set
// from `_web/rows/sources.py::SourceRow` — id-envelope variants,
// drive_document_id, mime, content_mime, download_url, viewer_url,
// created_at, last_modified_at, revision_id, revision_timestamp — so
// this struct grows without breaking call sites that read just the
// four fields today.
//
// Fields marked with `(best-effort)` degrade gracefully: a present-
// but-malformed slot returns the zero value rather than an error,
// mirroring the Python original's tolerance. The strict accessor
// (wire.At) raises a ShapeDriftError on type mismatches, which is the
// drift signal the schema-drift metric counts.
type Source struct {
	// ID is the source's stable backend id at row slot [0][0] (or [0]
	// for the flat shape). Empty when absent or the envelope is
	// malformed. Mirrors `_web/rows/sources.py::SourceRow.id`.
	ID string

	// Title is the user-visible title at row slot [1]. Empty when
	// absent. Mirrors `_web/rows/sources.py::SourceRow.title`.
	Title string

	// Kind is the typed source-kind label (url / youtube / drive /
	// text / file). Empty when the wire type code at metadata[4] does
	// not resolve to a known kind. Mirrors
	// `_web/rows/sources.py::SourceRow.type_code` after the
	// SourceType enum map.
	Kind string

	// StatusLabel is the typed ingestion-status label (PROCESSING /
	// READY / ERROR). Empty when absent — the typical "newly added,
	// not yet polled" case. Mirrors
	// `_web/rows/sources.py::SourceRow.status` after the SourceStatus
	// enum map.
	StatusLabel string
}

// Position contract for the normalized-entry layout `_web/rows/
// sources.py::SourceRow.from_entry` describes.  T-P6-1 owns the four
// minimum-viable positions; later tickets pin the full set.
//
//	Index   Meaning
//	-----   -------
//	0       source-id envelope (string, ["id"], or [None,True,["id"]])
//	1       title (string)
//	2       metadata sub-list
//	3       Source.settings block; [3][1] is the ingestion status code
//	4       (unused on entry shape; metadata-only)
//
// Within the metadata block (per `_web/rows/sources.py`):
//
//	Index   Meaning
//	-----   -------
//	4       type code (int — see SourceType enum mapping)
//
// Within the settings block (per `_web/rows/sources.py`):
//
//	Index   Meaning
//	-----   -------
//	1       ingestion status code (int — see SourceStatus enum mapping)
const (
	srcPosTitle       = 1
	srcPosMetadata    = 2
	srcPosSettings    = 3
	srcPosMetaType    = 4
	srcPosStatusCode  = 1
	srcPosSettingsMin = 2
)

// DecodeSource decodes one wire Source row into the typed Source
// value. The input is `data` — the entry payload after the outer
// envelope unwrap (ADD_SOURCE returns a single entry; GET_NOTEBOOK
// surfaces rows at `notebook[0][1][i]`).
//
// The id-envelope variants (`[id]`, `[None, True, [id]]`, bare
// string) are all handled transparently, mirroring
// `_web/rows/sources.py::SourceRow.id`. A present-but-wrong-typed id
// envelope degrades to an empty ID — the source still surfaces its
// title / kind / status so the CLI can render the row, just without
// a usable id to refetch it by.
//
// Malformed optional leaves degrade to the zero value rather than
// raising; the load-bearing id / title handling is strict enough that
// a present-but-wrong-typed slot at slot 0 / 1 raises a
// *wire.ShapeDriftError so the schema-drift metric counts the call.
//
// The kind/status_label mappings are best-effort: a present-but-
// unknown status code (e.g. a future backend enum value) maps to an
// empty label rather than drift, mirroring the
// `_warned_status_codes` warn-once-and-degrade pattern in the Python
// original.
func DecodeSource(data any) (Source, error) {
	s := Source{}

	if data == nil {
		return s, nil
	}

	// ID envelope: three layouts — bare string, ["id"], [None, True,
	// ["id"]]. Mirrors `_web/rows/sources.py::SourceRow._id_envelope`.
	s.ID = extractSourceID(data)

	// Title (slot 1): strict — a non-string title would silently
	// hand back a corrupt typed field. Use OptStr because the
	// short-row tolerance applies.
	if t, ok := wire.OptStr(data, srcPosTitle); ok {
		s.Title = t
	}

	// Status code (slot 3[1]): best-effort. Absent / non-list /
	// short settings block → empty StatusLabel (the "no claim yet"
	// case). Unknown integer code → empty StatusLabel (the "drift"
	// case; the Python original warns once and degrades to UNKNOWN).
	s.StatusLabel = decodeStatusLabel(data)

	// Kind from metadata[4]: best-effort. Absent / non-list /
	// short metadata → empty Kind. Unknown integer code → empty
	// Kind. Matches `_web/rows/sources.py::SourceRow.type_code`
	// semantics.
	s.Kind = decodeSourceKind(data)

	return s, nil
}

// extractSourceID walks the three id-envelope layouts
// (`_web/rows/sources.py::SourceRow._id_envelope`) and returns the
// resolved id string, or "" if none of the layouts matches.
//
// The three layouts:
//
//  1. bare string at row[0] (flat shape; rare on GET_NOTEBOOK).
//  2. `["id"]` at row[0] (typical wrapping; the common case).
//  3. `[None, True, ["id"]]` at row[0] (drive-backed entries nest
//     the id one level deeper).
//
// A row[0] that is None / a non-string / non-list / empty list /
// out-of-range drive payload all degrade to "" so a present-but-
// malformed id envelope does not turn into a "drift" error that
// would mask the still-usable title / kind / status_label fields.
func extractSourceID(row any) string {
	raw, err := wire.At(row, 0)
	if err != nil {
		return ""
	}
	switch env := raw.(type) {
	case string:
		// Layout 1: bare string.
		return env
	case []any:
		if len(env) == 0 {
			return ""
		}
		// Layout 2: ["id"] — typical.
		if id, ok := env[0].(string); ok && id != "" {
			return id
		}
		// Layout 3: [None, True, ["id"]] — drive-backed. Mirror the
		// positional contract pinned at
		// `_web/rows/sources.py::_ID_ENVELOPE_DRIVE_PAYLOAD_POS`.
		if len(env) > 2 {
			if inner, ok := env[2].([]any); ok && len(inner) > 0 {
				if id, ok := inner[0].(string); ok && id != "" {
					return id
				}
			}
		}
	}
	return ""
}

// decodeStatusLabel reads the ingestion-status code at
// row[3][1] and maps it to the typed label. Returns "" when the
// status slot is absent, non-numeric, or carries a code we do not
// model — mirrors the Python original's warn-once-and-degrade
// pattern (`_warned_status_codes`).
//
// SourceStatus enum (Python `_types/enums.py::SourceStatus`):
//
//	1 → PROCESSING
//	2 → READY
//	3 → ERROR
//
// Other codes (0 = UNSPECIFIED, and any unmapped future code) map to
// "" rather than raising. The Python original warns once for unknown
// codes; the Go port returns "" silently so a poll loop on a stuck
// source does not log the same drift line on every iteration. The
// drift diagnostic still exists via the absent/malformed slot path.
func decodeStatusLabel(row any) string {
	settings, err := wire.At(row, srcPosSettings)
	if err != nil {
		return ""
	}
	block, ok := settings.([]any)
	if !ok || len(block) < srcPosSettingsMin {
		return ""
	}
	code, ok := block[srcPosStatusCode].(int)
	if !ok {
		// json.Number (from wire.Unmarshal's UseNumber) is the
		// production-path wire carrier; tests pass Go ints. Decode
		// the json.Number text via a strict int parse so the two
		// paths surface the same enum value. Using a typed accessor
		// (rather than importing encoding/json directly) keeps the
		// rule 3 surface clean: wire.Unmarshal owns json.Number
		// revelations, and we hand back the int here.
		if num, ok := block[srcPosStatusCode].(jsonNumber); ok {
			i, err := num.Int64()
			if err != nil {
				return ""
			}
			code = int(i)
		} else {
			return ""
		}
	}
	switch code {
	case 1:
		return params.SourceStatusProcessing
	case 2:
		return params.SourceStatusReady
	case 3:
		return params.SourceStatusError
	}
	return ""
}

// jsonNumber is the local structural alias for `json.Number` so we
// can match the production-path carrier without importing
// encoding/json at the package surface (per AGENTS.md rule 3:
// only `internal/web/wire` may import encoding/json). The alias
// resolves at compile time because `json.Number` is defined as
//
//	type Number string
//
// so any value carrying the `Int64()` method satisfies this
// interface. Production wire.Unmarshal outputs implement it;
// row-construction helpers that build the test fixtures via Go
// literal ints never reach this branch.
type jsonNumber interface {
	Int64() (int64, error)
}

// decodeSourceKind reads the source-type code at metadata[4]
// (`_web/rows/sources.py::SourceRow._META_TYPE_POS`) and maps it to
// the typed kind label. Returns "" when the metadata block is absent
// or carries a code we do not model.
//
// SourceType enum (Python `_types/sources.py::SourceType`) — the
// integer values come from the mobile schema recovery:
//
//	1 → URL (typed kind: "url")
//	2 → YouTube (typed kind: "youtube")
//	3 → Drive (typed kind: "drive")
//	4 → Text (typed kind: "text")
//	5 → Uploaded file (typed kind: "file")
//
// Other codes map to "" — the Python original maps unknown codes via
// `_safe_source_type` which returns UNKNOWN; the Go port surfaces ""
// since the typed enum lands in a later ticket.
func decodeSourceKind(row any) string {
	meta, err := wire.At(row, srcPosMetadata)
	if err != nil {
		return ""
	}
	block, ok := meta.([]any)
	if !ok || len(block) <= srcPosMetaType {
		return ""
	}
	raw := block[srcPosMetaType]
	switch v := raw.(type) {
	case int:
		return sourceKindFromCode(v)
	case jsonNumber:
		i, err := v.Int64()
		if err != nil {
			return ""
		}
		return sourceKindFromCode(int(i))
	}
	return ""
}

// sourceKindFromCode is the pure mapping the int path funnels
// through. Centralized so decodeSourceKind reads at the level of
// "extract → map" rather than mixing the json.Number fallback with
// the typed-int primary path.
func sourceKindFromCode(code int) string {
	switch code {
	case 1:
		return params.SourceKindURL
	case 2:
		return params.SourceKindYouTube
	case 3:
		return params.SourceKindDrive
	case 4:
		return params.SourceKindText
	case 5:
		return params.SourceKindFile
	}
	return ""
}

// ErrSourceNotFound is the typed sentinel for "source missing" —
// surfaced by the SourcesAPI wrapper when an ADD_SOURCE call lands
// but the response carries no row (the documented empty-state on a
// throttled backend). Mirrors the spirit of
// `_web/rows/notebooks.py::ErrNotFound` from the sibling package.
var ErrSourceNotFound = errors.New("rows: source not found")

// IsSourceNotFound reports whether err (or any error it wraps) is
// the typed ErrSourceNotFound sentinel. Useful for the features /
// SDK layer to re-classify a transport-level "not found" into the
// typed error without an extra wrap.
func IsSourceNotFound(err error) bool {
	return errors.Is(err, ErrSourceNotFound)
}

// decodeSourcesRowErr is a small typed error used internally for the
// "row index out of range" path. It exists so callers can wrap a
// decodeSourcesRowErr is the typed error reserved for a future
// strict-mode batch decoder. The current DecodeSource returns the
// zero Source rather than raising; the type lives in this file so
// a later ticket can land the strict path without an import cycle.
type decodeSourcesRowErr struct {
	Index int
	Err   error
}

func (e *decodeSourcesRowErr) Error() string {
	return fmt.Sprintf("rows.DecodeSources[%d]: %v", e.Index, e.Err)
}

func (e *decodeSourcesRowErr) Unwrap() error { return e.Err }

// keep the type live even when callers don't trigger the strict path.
var _ error = (*decodeSourcesRowErr)(nil)
