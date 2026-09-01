package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
)

// Method is the obfuscated batchexecute RPC id for a single NotebookLM
// backend operation. The string value is the live, server-observed id; the
// Go-side constant name is a reverse-engineered label that is sometimes
// narrower or older than the actual backend method name (e.g.
// “ListNotebooks“ -> “ListRecentlyViewedProjects“). The full mapping
// appears in doc 03, "RPC method table".
//
// Method is a NAMED type (not an alias) so the policy registry and other
// packages can refer to it symbolically; method-name strings ("SomeName")
// remain the lookup key for NOTEBOOKLM_RPC_OVERRIDES so operators can patch
// one entry without recompiling.
type Method string

// Method ids from doc 03, ported from
// notebooklm-py/src/notebooklm/rpc/types.py::RPCMethod. The trailing comment
// on each line names the live /LabsTailwindOrchestrationService.<Method>
// endpoint the id resolves to; that backend name is the authoritative
// semantics, and our Go constant name is the reverse-engineered label.
//
// Keeping every id in one constant block makes the T-P1-2 presence test
// trivial to read: the test fixture is just (constant name, value) pairs
// extracted from this file by a tiny scanner.
const (
	// Notebooks
	MethodListNotebooks        Method = "wXbhsf" // -> ListRecentlyViewedProjects (recency-ordered)
	MethodCreateNotebook       Method = "CCqFvf" // -> CreateProject
	MethodCopyNotebook         Method = "te3DCe" // -> CopyProject
	MethodGetNotebook          Method = "rLM1Ne" // -> GetProject
	MethodRenameNotebook       Method = "s0tc2d" // -> MutateProject (generic mutator; also chat config + share access)
	MethodDeleteNotebook       Method = "WWINqb" // -> DeleteProjects (single id; batch shapes probed & rejected)
	MethodRemoveRecentlyViewed Method = "fejl7e" // -> RemoveRecentlyViewedProject

	// Sources
	MethodAddSource            Method = "izAoDd" // -> AddSources
	MethodAddSourceFile        Method = "o4cbdc" // register an uploaded file (live method unconfirmed)
	MethodDeleteSource         Method = "tGMBJ"  // -> DeleteSources (batch-capable; we send one)
	MethodGetSource            Method = "hizoJc" // -> LoadSource
	MethodRefreshSource        Method = "FLmJqe" // -> RefreshSource
	MethodCheckSourceFreshness Method = "yR9Yof" // -> CheckSourceFreshness
	MethodUpdateSource         Method = "b7Wfje" // -> MutateSource

	// Labels — ALSO used verbatim for account-level Collections
	// (a collection is a type-3 label with a null notebook parent).
	MethodCreateLabel Method = "agX4Bc" // -> CreateLabel (AI auto-group AND manual create)
	MethodListLabels  Method = "I3xc3c" // -> GetLabels
	MethodUpdateLabel Method = "le8sX"  // -> MutateLabel (rename / emoji / add sources, fieldmask)
	MethodDeleteLabel Method = "GyzE7e" // -> DeleteLabels (batch by id)

	// Guides & suggestions
	MethodSummarize           Method = "VfAZjd" // -> GenerateNotebookGuide
	MethodGetSourceGuide      Method = "tr032e" // -> GenerateDocumentGuides
	MethodGetSuggestedReports Method = "ciyUvf" // -> GenerateReportSuggestions

	// Artifacts
	MethodCreateArtifact     Method = "R7cb6c" // -> CreateArtifact (all ten types)
	MethodListArtifacts      Method = "gArtLc" // -> ListArtifacts
	MethodDeleteArtifact     Method = "V5N4be" // -> DeleteArtifact (single id)
	MethodRenameArtifact     Method = "rc3d8d" // -> UpdateArtifact (we only set the title)
	MethodExportArtifact     Method = "Krh3pd" // -> ExportToDrive (Docs/Sheets are Drive destinations)
	MethodShareArtifact      Method = "RGP97b" // -> LabsTailwindSharingService.ShareAudio
	MethodGetInteractiveHTML Method = "v9rmvd" // -> GetArtifact (generic; quiz/flashcard HTML at [0][9][0],
	//                                                       interactive mind-map tree at [0][9][3])
	MethodReviseSlide   Method = "KmcKPe" // -> DeriveArtifact (generic derive; we revise one slide)
	MethodRetryArtifact Method = "Rytqqe" // -> GenerateArtifact (in-place retry, UI "Retry")

	// Research — all backed by Google's DiscoverSources pipeline.
	MethodStartFastResearch Method = "Ljjv0c" // -> DiscoverSourcesManifold
	MethodStartDeepResearch Method = "QA9ei"  // -> DiscoverSourcesAsync
	MethodPollResearch      Method = "e3bVqc" // -> ListDiscoverSourcesJob
	MethodImportResearch    Method = "LBwxtb" // -> FinishDiscoverSourcesRun
	MethodCancelResearch    Method = "Zbrupe" // -> CancelDiscoverSourcesJob

	// Notes & mind maps.
	MethodGenerateMindMap     Method = "yyryJe" // -> ActOnSources (generic; we generate a note-backed mind map)
	MethodCreateNote          Method = "CYK0Xb" // -> CreateNote
	MethodGetNotesAndMindMaps Method = "cFji9"  // -> GetNotes (mind maps come back as JSON-bodied notes)
	MethodUpdateNote          Method = "cYAfTb" // -> MutateNote
	MethodDeleteNote          Method = "AH0mwd" // -> DeleteNotes (batch-capable; we send one)

	// Conversation.
	MethodGetLastConversationID Method = "hPTbtc" // -> ListChatSessions (we read the most recent id)
	MethodGetConversationTurns  Method = "khqZz"  // -> ListChatTurns
	MethodDeleteConversation    Method = "J7Gthc" // -> DeleteChatTurns (UI "Delete history")
	MethodSuggestPrompts        Method = "otmP3b" // -> GeneratePromptSuggestions

	// Sharing.
	MethodShareNotebook  Method = "QDyure" // -> LabsTailwindSharingService.ShareProject
	MethodGetShareStatus Method = "JFMDGd" // -> LabsTailwindSharingService.GetProjectDetails
	// SetShareAccess reuses RenameNotebook (s0tc2d) with different params.

	// Settings.
	MethodGetUserSettings Method = "ZwVcOc" // -> GetOrCreateAccount (may create on first call)
	MethodSetUserSettings Method = "hT54vc" // -> MutateAccount (we only set output language)
)

// methodTable is the (Go constant name, Method value) table, built once at
// init from the constants above. The presence test reads the committed
// fixture and asserts every entry here matches; ResolveID also walks this
// map to validate override keys without importing encoding/json on the
// hot path.
//
// We intentionally rebuild the table here rather than expose a slice of
// methods on the package surface — callers should use the named Method
// constants and let the compiler catch typos.
var methodTable = func() map[string]Method {
	m := map[string]Method{
		"MethodListNotebooks":         MethodListNotebooks,
		"MethodCreateNotebook":        MethodCreateNotebook,
		"MethodCopyNotebook":          MethodCopyNotebook,
		"MethodGetNotebook":           MethodGetNotebook,
		"MethodRenameNotebook":        MethodRenameNotebook,
		"MethodDeleteNotebook":        MethodDeleteNotebook,
		"MethodRemoveRecentlyViewed":  MethodRemoveRecentlyViewed,
		"MethodAddSource":             MethodAddSource,
		"MethodAddSourceFile":         MethodAddSourceFile,
		"MethodDeleteSource":          MethodDeleteSource,
		"MethodGetSource":             MethodGetSource,
		"MethodRefreshSource":         MethodRefreshSource,
		"MethodCheckSourceFreshness":  MethodCheckSourceFreshness,
		"MethodUpdateSource":          MethodUpdateSource,
		"MethodCreateLabel":           MethodCreateLabel,
		"MethodListLabels":            MethodListLabels,
		"MethodUpdateLabel":           MethodUpdateLabel,
		"MethodDeleteLabel":           MethodDeleteLabel,
		"MethodSummarize":             MethodSummarize,
		"MethodGetSourceGuide":        MethodGetSourceGuide,
		"MethodGetSuggestedReports":   MethodGetSuggestedReports,
		"MethodCreateArtifact":        MethodCreateArtifact,
		"MethodListArtifacts":         MethodListArtifacts,
		"MethodDeleteArtifact":        MethodDeleteArtifact,
		"MethodRenameArtifact":        MethodRenameArtifact,
		"MethodExportArtifact":        MethodExportArtifact,
		"MethodShareArtifact":         MethodShareArtifact,
		"MethodGetInteractiveHTML":    MethodGetInteractiveHTML,
		"MethodReviseSlide":           MethodReviseSlide,
		"MethodRetryArtifact":         MethodRetryArtifact,
		"MethodStartFastResearch":     MethodStartFastResearch,
		"MethodStartDeepResearch":     MethodStartDeepResearch,
		"MethodPollResearch":          MethodPollResearch,
		"MethodImportResearch":        MethodImportResearch,
		"MethodCancelResearch":        MethodCancelResearch,
		"MethodGenerateMindMap":       MethodGenerateMindMap,
		"MethodCreateNote":            MethodCreateNote,
		"MethodGetNotesAndMindMaps":   MethodGetNotesAndMindMaps,
		"MethodUpdateNote":            MethodUpdateNote,
		"MethodDeleteNote":            MethodDeleteNote,
		"MethodGetLastConversationID": MethodGetLastConversationID,
		"MethodGetConversationTurns":  MethodGetConversationTurns,
		"MethodDeleteConversation":    MethodDeleteConversation,
		"MethodSuggestPrompts":        MethodSuggestPrompts,
		"MethodShareNotebook":         MethodShareNotebook,
		"MethodGetShareStatus":        MethodGetShareStatus,
		"MethodGetUserSettings":       MethodGetUserSettings,
		"MethodSetUserSettings":       MethodSetUserSettings,
	}
	return m
}()

// AllMethods returns a snapshot of the registered method table, in
// alphabetical order by Go constant name. Callers should not mutate the
// returned slice; the table itself is read-only after init.
func AllMethods() []MethodEntry {
	out := make([]MethodEntry, 0, len(methodTable))
	for name, m := range methodTable {
		out = append(out, MethodEntry{Name: name, Method: m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MethodEntry is one row of the method table — a Go-side name (without the
// "Method" prefix) and its obfuscated Method id. Returned by AllMethods.
type MethodEntry struct {
	Name   string
	Method Method
}

// RPCOverridesEnvVar is the name of the environment variable that lets
// operators patch a changed id without waiting for a release. The variable
// is JSON-encoded: {"MethodName": "newId", ...}. Format matches the Python
// override path (notebooklm-py/src/notebooklm/_web/wire/overrides.py).
const RPCOverridesEnvVar = "NOTEBOOKLM_RPC_OVERRIDES"

// overrideCacheMemo guards the once-per-process override-hash dedupe so a
// long-running daemon with multiple in-flight RPCs cannot spam the log on
// every call (see notebooklm-py::_logged_override_hashes).
var (
	overrideCacheMu sync.Mutex
	overrideCache   = map[string]struct{}{}
)

// ResolveID returns the live RPC id for the Go constant named methodName
// (e.g. "MethodListNotebooks"), honoring the operator escape hatch in
// RPCOverridesEnvVar.
//
// Behavior:
//   - methodName not in the table -> the empty Method. Callers must check
//     the return value before using it on the wire; this is the failure mode
//     a typo introduces, and we want it visible rather than silently
//     substituted with "Unknown".
//   - Override set absent or methodName not in it -> the canonical id.
//   - Override set present -> the override id is returned AND the first
//     call per distinct override set emits exactly one INFO log line
//     containing the SHA-256 hex of the canonicalized override JSON. The
//     values themselves are NEVER logged at info level (they could carry a
//     customer-specific id Google has not yet published).
//
// The hash is computed over the sorted (key, value) pairs so two operators
// with the same logical override set hit the dedupe path even if their JSON
// has different key order or whitespace.
func ResolveID(methodName string) Method {
	canonical, ok := methodTable[methodName]
	if !ok {
		return ""
	}
	overrides := loadOverrides()
	if len(overrides) == 0 {
		return canonical
	}
	override, present := overrides[methodName]
	if !present {
		return canonical
	}
	logOverrideFingerprint(overrides)
	return Method(override)
}

// loadOverrides reads NOTEBOOKLM_RPC_OVERRIDES from the environment and
// returns the parsed (methodName -> rpcId) map. A malformed env value is
// treated as no overrides; we do not panic from a transport-layer helper.
//
// Unknown method names in the override file are silently dropped; the
// operator's intent is to patch known ids, and a typo'd key applying as
// "no override" is the least-bad outcome (the canonical id keeps the
// call working, and the log line still names the override set so the
// operator can see the typo).
func loadOverrides() map[string]string {
	raw := strings.TrimSpace(os.Getenv(RPCOverridesEnvVar))
	if raw == "" {
		return nil
	}
	parsed, err := parseOverridesJSON(raw)
	if err != nil || parsed == nil {
		return nil
	}
	out := make(map[string]string, len(parsed))
	for k, v := range parsed {
		if _, known := methodTable[k]; known && v != "" {
			out[k] = v
		}
	}
	return out
}

// logOverrideFingerprint emits exactly one log line per distinct override
// set, containing the SHA-256 hex of the canonical (sorted) override
// representation. Subsequent calls with the same set are silent.
func logOverrideFingerprint(overrides map[string]string) {
	hashHex := overrideSetHash(overrides)

	overrideCacheMu.Lock()
	_, seen := overrideCache[hashHex]
	if !seen {
		overrideCache[hashHex] = struct{}{}
	}
	overrideCacheMu.Unlock()

	if seen {
		return
	}
	slog.Info("NOTEBOOKLM_RPC_OVERRIDES applied",
		slog.String("override_set_hash", hashHex),
		slog.Int("override_count", len(overrides)),
	)
}

// overrideSetHash returns the SHA-256 hex of the canonical sorted
// (key, value) representation of overrides. Two callers with the same
// logical override set hit the dedupe path even if their input JSON
// differed in whitespace or key order.
func overrideSetHash(overrides map[string]string) string {
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(MarshalString(k))
		b.WriteByte(':')
		b.WriteString(MarshalString(overrides[k]))
	}
	b.WriteByte('}')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// MarshalString returns the JSON-string encoding of s, using this
// package's Marshal (HTML-significant characters unescaped, no trailing
// newline). It exists so overrideSetHash can build a deterministic
// byte representation without duplicating Marshal's behavior.
func MarshalString(s string) string {
	b, err := Marshal(s)
	if err != nil {
		// Marshal only fails on cyclic or unsupported types; a Go string
		// is always marshaled cleanly. Defend against future changes.
		return strconvQuoteASCII(s)
	}
	return string(b)
}

// strconvQuoteASCII quotes s with the same escape rules as encoding/json
// for safe ASCII input. Used only as a fallback when Marshal itself
// fails; in practice it never runs because Go strings are always
// marshalable.
func strconvQuoteASCII(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xF])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// parseOverridesJSON decodes NOTEBOOKLM_RPC_OVERRIDES into a flat map of
// string -> string. We accept top-level arrays (treated as a k/v list by
// position — discouraged but tolerated) and bare strings (treated as one
// override). Anything else is an error.
//
// The wire package wraps encoding/json per AGENTS.md rule 3. This helper
// intentionally lives next to ResolveID because the override path is the
// only consumer — keeping it scoped avoids tempting a future package to
// "just decode JSON" through a different funnel.
func parseOverridesJSON(raw string) (map[string]string, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var first any
	if err := dec.Decode(&first); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		// Anything other than EOF after the first token means the input
		// was a JSON stream; we only ever consume one document here.
		return nil, errTrailingOverrideToken
	}
	switch v := first.(type) {
	case map[string]any:
		out := make(map[string]string, len(v))
		for k, raw := range v {
			if raw == nil {
				continue
			}
			switch t := raw.(type) {
			case string:
				if t != "" {
					out[k] = t
				}
			case json.Number:
				out[k] = t.String()
			default:
				// Marshal the value back through this package's encoder so
				// the on-wire id representation matches the operator's
				// intent for non-string primitives (rare; mostly for tests
				// that pass numeric ids).
				b, err := Marshal(raw)
				if err == nil {
					out[k] = string(b)
				}
			}
		}
		return out, nil
	default:
		return nil, errOverrideShape
	}
}

// errOverrideShape is returned from parseOverridesJSON when the env value
// is not a JSON object. We match the Python implementation's behavior of
// logging a warning and ignoring the value rather than failing the call.
var errOverrideShape = &overrideError{msg: "NOTEBOOKLM_RPC_OVERRIDES must be a JSON object mapping method names to RPC IDs"}

// errTrailingOverrideToken is returned when the env value carries more
// than one JSON document. The Python implementation is more tolerant; we
// are stricter here because a stray second document is almost certainly a
// copy-paste mistake and silent acceptance is the worse failure mode.
var errTrailingOverrideToken = &overrideError{msg: "NOTEBOOKLM_RPC_OVERRIDES must contain a single JSON document"}

// overrideError is a small typed error so we can detect "override ignored"
// at the log-site and emit WARNING — not ERROR — without losing the cause.
type overrideError struct{ msg string }

func (e *overrideError) Error() string { return e.msg }
