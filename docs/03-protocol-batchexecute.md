# 03 — The `batchexecute` protocol

Normative source: `../notebooklm-py/src/notebooklm/_web/wire/{encoder,decoder}.py`,
`rpc/types.py`, `_web/transport/executor.go` (`build_url`).

Everything in this document is reverse-engineered from live traffic and can change without
notice. Treat it as a snapshot pinned by cassettes, not as a specification.

## Hosts

| Purpose | Host |
|---|---|
| Personal app (default) | `https://notebook.google.com` |
| Personal app (pre-rebrand, still served) | `https://notebooklm.google.com` |
| Enterprise | `https://notebooklm.cloud.google.com` |

Selected by `NOTEBOOKLM_BASE_URL`, which is **allowlisted** to exactly those three hosts and
requires `https`, no port, no userinfo, no path, no query, no fragment. Any other value is a
configuration error, because the value is used for authenticated requests.

The two personal hosts dual-serve `batchexecute` and stand in for each other. But
`Origin`/`Referer` on the upload and download paths must name **the host the request is
actually going to** — an `Origin` naming the other personal host fails Google's
origin-bound auth checks.

## Endpoints

| Name | Path |
|---|---|
| batchexecute RPC | `{base}/_/LabsTailwindUi/data/batchexecute` |
| Streamed chat | `{base}/_/LabsTailwindUi/data/google.internal.labs.tailwind.orchestration.v1.LabsTailwindOrchestrationService/GenerateFreeFormStreamed` |
| Resumable upload | `{base}/upload/_/` |
| Cookie rotation | `https://accounts.google.com/RotateCookies` |
| OAuth login (master token) | `https://accounts.google.com/OAuthLogin` |
| Session merge (master token) | `https://accounts.google.com/MergeSession` |
| Android auth (master token) | `https://android.clients.google.com/auth` |

Streamed chat is **not** a batchexecute RPC — it has its own body shape and error mapping.
It is the single sanctioned exception to routing everything through the RPC executor.

## Request encoding

### Body

Three steps. Port of `encoder.py`.

```
1. inner   = [rpcID, jsonCompact(params), null, "generic"]
2. request = [[inner]]                                      // triple-nested
3. body    = "f.req=" + urlencode(jsonCompact(request))
             + "&at=" + urlencode(csrfToken)                // omitted if csrf empty
             + "&"                                          // trailing & is real
```

`jsonCompact` = no spaces (Python `separators=(",", ":")`), **no HTML escaping**.
`urlencode` = percent-encode with an empty safe set (Python `quote(s, safe="")`), i.e.
encode `/`, `:`, `,`, `[`, `]`, everything.

Go:

```go
func BuildRequestBody(req []any, csrf string) ([]byte, error) {
    fReq, err := Marshal(req)              // wire.Marshal — SetEscapeHTML(false)
    if err != nil { return nil, err }
    var b strings.Builder
    b.WriteString("f.req=")
    b.WriteString(escapeAll(string(fReq)))
    if csrf != "" {
        b.WriteString("&at=")
        b.WriteString(escapeAll(csrf))
    }
    b.WriteString("&")
    return []byte(b.String()), nil
}
```

`escapeAll` must not use `url.QueryEscape` verbatim: `QueryEscape` encodes a space as `+`
whereas Python's `quote` encodes it as `%20`. Write the escaper explicitly and unit-test it
against a table of Python-produced fixtures.

### URL

Port of `executor.py::build_url`. Query params, in this order:

| Param | Value |
|---|---|
| `rpcids` | the resolved RPC id — **must match the id inside `f.req`** |
| `source-path` | `/` by default; `/notebook/{id}` for notebook-scoped calls |
| `f.sid` | session id (`FdrFJe`) |
| `hl` | output language (`NOTEBOOKLM_HL`, default `en`) |
| `rt` | `c` (chunked response mode) |
| `authuser` | present only when an account email or non-zero authuser index is known |

`authuser` value formatting: if an account email is known, the email is sent; otherwise the
integer index. Port `_auth/account.go::FormatAuthuserValue`. Google account **indices change
when other accounts sign out**, which is why the email is preferred.

A mismatch between `rpcids=` and the id inside `f.req` reaches the wire as a malformed
request. When `NOTEBOOKLM_RPC_OVERRIDES` patches an id, both must be resolved from the same
lookup, once per logical call.

### Headers

| Header | Value |
|---|---|
| `Content-Type` | `application/x-www-form-urlencoded;charset=UTF-8` |
| `Cookie` | domain-correct selection from the jar, per RFC 6265 §5.4 |

Set on the HTTP client, not per request. Cookies come from the jar via the `http.Client`'s
`Jar`; the `at=` CSRF token travels in the body, not a header.

### Source-id nesting

The single most error-prone part of the protocol. `NestSourceIDs(ids, depth)` wraps each id
in `depth` inner lists, then collects:

| depth | shape |
|---|---|
| 1 | `[[id1], [id2]]` |
| 2 | `[[[id1]], [[id2]]]` |
| 3 | `[[[[id1]]], [[[id2]]]]` |

Empty or nil input → `[]` (not `null`). `depth < 1` is a programming error.

**The depth varies per RPC and sometimes per slot within one RPC.** Audio generation sends
depth 2 in one slot and depth 1 in another, in the same payload. Never infer; copy from the
Python builder.

```go
func NestSourceIDs(ids []string, depth int) []any {
    if depth < 1 { panic("wire: NestSourceIDs depth must be >= 1") }
    if len(ids) == 0 { return []any{} }
    out := make([]any, len(ids))
    for i, id := range ids { out[i] = id }
    for d := 0; d < depth; d++ {
        next := make([]any, len(out))
        for i, v := range out { next[i] = []any{v} }
        out = next
    }
    return out
}
```

### The shared template block

`CREATE_NOTEBOOK`, every `ADD_SOURCE` variant, `ADD_SOURCE_FILE`, `GET_NOTEBOOK`, and the
label RPCs all carry the same request-options wrapper:

```
[2, null, null, [1, null, null, null, null, null, null, null, null, null, [1]]]
```

Google's Gemini-3.5 rollout made this **mandatory**; the older degenerate tails (`[2], [1]`
for create, `[2], null, null` for sources) are now rejected with gRPC status 3/5/9. Build a
fresh slice on every call — never share a mutable nested literal across requests.

Artifact RPCs carry a longer sibling with a trailing capability projection:

```
[2, null, null, [1, null, null, null, null, null, null, null, null, null, [1]], [[1, 4, 8, 2, 3, 6]]]
```

## Response decoding

Port of `decoder.py`. Four stages.

### 1. Strip the anti-XSSI prefix

Google prefixes responses with `)]}'` followed by a newline (`\n` or `\r\n`). Strip it if
present; pass through unchanged if not.

### 2. Parse the chunked `rt=c` framing

Alternating lines of a decimal byte count and a JSON payload:

```
)]}'

3129
[["wrb.fr","wXbhsf","[[…]]",null,null,null,"generic"]]
25
[["di",42],["af.httprm",42,"…",5]]
```

Rules, all load-bearing:

- A line that parses as an integer is a byte count; the **next** line is its payload.
- A byte count with no following line is a **framing** error.
- **A byte-count mismatch is expected and tolerated.** Live Google responses count in a
  different unit (likely UTF-16 code units) than UTF-8 bytes. Log at DEBUG and increment a
  process-wide `byteCountMismatchTotal` counter — that counter's *rate of change* is the
  honest drift signal. Never warn: it would fire on essentially every multi-chunk response.
- A line that does not parse as an integer is tried directly as JSON.
- Malformed payloads are skipped with a WARNING, not fatal — **unless** the malformed rate
  exceeds 10%, computed separately for payload records, framing records, and the aggregate.
  Over 10% on any of the three → hard error (Google reshaped the framing).
- Pathologically deep JSON must not blow the stack. Go's `encoding/json` has a nesting
  limit and returns an error rather than crashing, so this is handled for free — but assert
  it with a test (the Python original had to catch `RecursionError` explicitly).

### 3. Extract the frame for our RPC id

Each chunk is a list of items. An item is `[tag, rpcID, resultData, …]`:

| tag | meaning |
|---|---|
| `wrb.fr` | a result frame |
| `er` | an error frame — item[2] is an error code |
| `di`, `af.httprm` | framing/telemetry noise; ignore |

For our `rpcID`:

- An `er` frame → immediate typed error. Integer codes map through a table
  (400/401/403/404/429/500 → message + retryability); non-integer codes stringify.
- A `wrb.fr` frame's `resultData` (index 2) is usually a **JSON string** that must be
  parsed again. If it is not a string, use it as-is.
- **In `rt=c` mode the backend can emit more than one `wrb.fr` frame for one id** — a null
  placeholder followed by the real payload. Iterate all frames and return the **last
  non-null** result. Never stop at the first.
- When `resultData` is null and index 5 exists, index 5 is a JSON-array-encoded
  `google.rpc.Status`:

  | index | proto field |
  |---|---|
  | 0 | `code` (gRPC canonical, 0–16) |
  | 1 | `message` — **never observed populated**; read defensively, cap at 300 chars, collapse whitespace |
  | 2 | `details` — where a `UserDisplayableError` marker appears |

  A `UserDisplayableError` marker anywhere in index 5 (search depth-capped at 20) → a
  rate-limit / quota error.

### 4. Classify a null result

This is where a stale method id gets diagnosed, so the branch order matters:

```
if rpcID not in foundIDs and len(foundIDs) > 0:
    → ErrUnknownRPCMethod  "the RPC method ID may have changed" + the ids actually present
      (fires even when allowNull is set — this is drift, not a benign null)

if len(foundIDs) == 0:
    → ErrRPC  "response contained no RPC data"
      (an anti-bot wall, a redirect to HTML, a signed-out shell — never a benign null)

if allowNull and not (raiseOnNullStatus and statusIsNonOK):
    → nil
      (log at DEBUG when a non-OK status was swallowed; log at WARNING when
       raiseOnNullStatus was set but index 5 held an unrecognizable payload)

if a recognized non-OK status is present:
    NOT_FOUND (5) / PERMISSION_DENIED (7) → ErrClient + account-routing hint
    anything else                          → ErrRPC
    message: "The server rejected this request (<label>)." + server message if any

otherwise:
    → ErrRPC "The server returned an empty result (possible server error or parameter mismatch)."
```

The account-routing hint text is deliberately worded to avoid the substrings the
auth-error detector matches on, so a `NOT_FOUND` cannot trigger a spurious token refresh:

> If you have multiple Google accounts signed in, this is commonly an account-routing
> mismatch — the request defaults to account index 0 when no authuser is set.

**Three RPCs are recorded returning a non-OK status on a flow the client reports as
successful**: `REMOVE_RECENTLY_VIEWED` (13), `SHARE_NOTEBOOK` (3), `SHARE_ARTIFACT` (3).
This is why the swallow logs at DEBUG rather than WARNING — warning would fire on ordinary
`share add` traffic and assert a benign-vs-broken judgement nobody has evidence for.

## Strict positional access

Every decoder navigates nested positional lists. A single index shift must produce a typed,
actionable error — never a panic, never a silent zero value.

```go
// internal/web/wire/index.go
func At(v any, idx int, method, source string) (any, error)   // out of range → *ShapeDriftError
func Str(v any, idx int, method, source string) (string, error)
func Int(v any, idx int, method, source string) (int64, error)   // handles json.Number
func Bool(v any, idx int, method, source string) (bool, error)
func List(v any, idx int, method, source string) ([]any, error)

// Optional variants: absent slot → zero value, nil error. Present-but-wrong-type → error.
func OptStr(v any, idx int) (string, bool)
func OptInt(v any, idx int) (int64, bool)
func OptList(v any, idx int) ([]any, bool)
```

`ShapeDriftError` wraps `ErrDecoding`, so `errors.Is(err, notebooklm.ErrDecoding)` is the
"Google reshaped a response" test. This is counted under a dedicated `rpcDecodeErrors`
metric — separately from semantic errors like a decoded rate limit, which must not inflate
the drift signal.

Strict is the only mode. The Python original retired its soft-mode opt-out in v0.7.0; do
not reintroduce one.

**Beware Go's `bool`/number conflation traps:**

- Python guards `type(code) is not int` because `bool` subclasses `int` there. In Go a JSON
  `true` decodes to `bool`, so a type switch handles it — but a `json.Number` must be
  range-checked before `int64` conversion.
- An empty list must short-circuit before index access, or the strict accessor raises on a
  structurally valid "nothing here" case.

## RPC method table

Port to `internal/web/wire/methods.go`. Comments name the live backend method the
obfuscated id resolves to; **that name is the authoritative semantics**, and our Go constant
name is a reverse-engineered label that is sometimes narrower or older.

```go
type Method string

const (
    // Notebooks
    ListNotebooks       Method = "wXbhsf"  // → ListRecentlyViewedProjects (recency-ordered)
    CreateNotebook      Method = "CCqFvf"  // → CreateProject
    CopyNotebook        Method = "te3DCe"  // → CopyProject
    GetNotebook         Method = "rLM1Ne"  // → GetProject
    RenameNotebook      Method = "s0tc2d"  // → MutateProject (generic mutator; also chat config + share access)
    DeleteNotebook      Method = "WWINqb"  // → DeleteProjects (single id; batch shapes probed & rejected)
    RemoveRecentlyViewed Method = "fejl7e" // → RemoveRecentlyViewedProject

    // Sources
    AddSource           Method = "izAoDd"  // → AddSources
    AddSourceFile       Method = "o4cbdc"  // register an uploaded file (live method unconfirmed)
    DeleteSource        Method = "tGMBJ"   // → DeleteSources (batch-capable; we send one)
    GetSource           Method = "hizoJc"  // → LoadSource
    RefreshSource       Method = "FLmJqe"  // → RefreshSource
    CheckSourceFreshness Method = "yR9Yof" // → CheckSourceFreshness
    UpdateSource        Method = "b7Wfje"  // → MutateSource

    // Labels — ALSO used verbatim for account-level Collections
    // (a collection is a type-3 label with a null notebook parent)
    CreateLabel         Method = "agX4Bc"  // → CreateLabel (AI auto-group AND manual create)
    ListLabels          Method = "I3xc3c"  // → GetLabels
    UpdateLabel         Method = "le8sX"   // → MutateLabel (rename / emoji / add sources, fieldmask)
    DeleteLabel         Method = "GyzE7e"  // → DeleteLabels (batch by id)

    // Guides & suggestions
    Summarize           Method = "VfAZjd"  // → GenerateNotebookGuide
    GetSourceGuide      Method = "tr032e"  // → GenerateDocumentGuides
    GetSuggestedReports Method = "ciyUvf"  // → GenerateReportSuggestions

    // Artifacts
    CreateArtifact      Method = "R7cb6c"  // → CreateArtifact (all ten types)
    ListArtifacts       Method = "gArtLc"  // → ListArtifacts
    DeleteArtifact      Method = "V5N4be"  // → DeleteArtifact (single id)
    RenameArtifact      Method = "rc3d8d"  // → UpdateArtifact (we only set the title)
    ExportArtifact      Method = "Krh3pd"  // → ExportToDrive (Docs/Sheets are Drive destinations)
    ShareArtifact       Method = "RGP97b"  // → LabsTailwindSharingService.ShareAudio
    GetInteractiveHTML  Method = "v9rmvd"  // → GetArtifact (generic; quiz/flashcard HTML at [0][9][0],
                                           //   interactive mind-map tree at [0][9][3])
    ReviseSlide         Method = "KmcKPe"  // → DeriveArtifact (generic derive; we revise one slide)
    RetryArtifact       Method = "Rytqqe"  // → GenerateArtifact (in-place retry, UI "Retry")

    // Research — all backed by Google's DiscoverSources pipeline
    StartFastResearch   Method = "Ljjv0c"  // → DiscoverSourcesManifold
    StartDeepResearch   Method = "QA9ei"   // → DiscoverSourcesAsync
    PollResearch        Method = "e3bVqc"  // → ListDiscoverSourcesJob
    ImportResearch      Method = "LBwxtb"  // → FinishDiscoverSourcesRun
    CancelResearch      Method = "Zbrupe"  // → CancelDiscoverSourcesJob

    // Notes & mind maps
    GenerateMindMap     Method = "yyryJe"  // → ActOnSources (generic; we generate a note-backed mind map)
    CreateNote          Method = "CYK0Xb"  // → CreateNote
    GetNotesAndMindMaps Method = "cFji9"   // → GetNotes (mind maps come back as JSON-bodied notes)
    UpdateNote          Method = "cYAfTb"  // → MutateNote
    DeleteNote          Method = "AH0mwd"  // → DeleteNotes (batch-capable; we send one)

    // Conversation
    GetLastConversationID Method = "hPTbtc" // → ListChatSessions (we read the most recent id)
    GetConversationTurns  Method = "khqZz"  // → ListChatTurns
    DeleteConversation    Method = "J7Gthc" // → DeleteChatTurns (UI "Delete history")
    SuggestPrompts        Method = "otmP3b" // → GeneratePromptSuggestions

    // Sharing
    ShareNotebook       Method = "QDyure"  // → LabsTailwindSharingService.ShareProject
    GetShareStatus      Method = "JFMDGd"  // → LabsTailwindSharingService.GetProjectDetails
    // SetShareAccess reuses RenameNotebook (s0tc2d) with different params.

    // Settings
    GetUserSettings     Method = "ZwVcOc"  // → GetOrCreateAccount (may create on first call)
    SetUserSettings     Method = "hT54vc"  // → MutateAccount (we only set output language)
)
```

### Runtime id overrides

`NOTEBOOKLM_RPC_OVERRIDES` lets an operator patch a changed id without waiting for a
release — the escape hatch for the #1 breakage class. Format: `NAME=id` pairs, comma or
newline separated, or a path to a JSON object.

```
NOTEBOOKLM_RPC_OVERRIDES='ListNotebooks=abc123,CreateArtifact=def456'
```

Resolve once per logical call and thread the resolved string into **both** the URL and the
body. Log the override set's hash once per process, never the values themselves at INFO.

### Re-capturing the protocol

When an id or shape breaks:

1. Open the app in Chrome with DevTools → Network, filter `batchexecute`.
2. Perform the action. Find the request whose `rpcids=` you do not recognize.
3. Copy the request as cURL; decode `f.req` (URL-decode, then read the nested JSON).
4. Compare against the Go builder position by position.
5. Update the builder + the cassette. Add a comment naming the capture date.

`../notebooklm-py/docs/rpc-development.md` documents the HAR-scrubbing tooling; port
`scripts/scrub_rpc_har.py` to `internal/tools/scrubhar` so captures can be committed safely.

## Idempotency taxonomy

`batchexecute` runs over HTTPS, so every mutating call is exposed to a **commit-lost**
failure: the server commits the write, then the response is lost in transit. A naive retry
then duplicates the write — a duplicate notebook, a duplicate source, an extra LLM
inference, a re-sent invite email. The transport's inner retry loop is *correct* for reads
and *dangerous* for mutations.

Retry safety is therefore a property of the **RPC**, declared once in a registry — not a
property of the call site, re-derived every time someone touches the code.

```go
type Policy int
const (
    Unclassified       Policy = iota // placeholder for tests only; never for an active RPC
    ProbeThenCreate                  // caller owns a probe loop → force-disable inner retries
    IdempotentSetOp                  // replay-safe read/delete/rename/set-state → retries stay on
    AtLeastOnceAccepted              // caller accepted duplicate side effects (email/billing) → retries on + rate-limited WARN
    NonIdempotentNoRetry             // no dedupe key, no probe → first failure must surface
)
```

Rules:

- The registry is keyed by `(Method, operationVariant)`. Some methods need per-variant
  policy (`AddSource` and `CreateNote` have several wire shapes).
- **Every active method must have an entry**, including read-only ones (registered as
  `IdempotentSetOp`). A missing entry fails a startup assertion.
- An explicit caller `DisableInternalRetries` always wins over the registry default —
  caller intent beats policy.
- Every `ProbeThenCreate` entry carries a documented rationale string describing how that
  mutation recovers. A test rejects a new one without it.
- The axis is **closed at five**. A sixth needs a design change in lockstep with the
  executor; the cap exists so a reviewer can hold the whole taxonomy in mind.

Side-effect probing (`IdempotentCreate`, the probe-then-create wrapper used by source add
and Drive import) is a separate mechanism, not owned by the registry.

## Response size cap

The terminal POST streams the response with a hard byte cap (default a few MB, configurable).
An uncapped read is how a redirected HTML wall or a runaway payload turns into memory
exhaustion. On exceeding the cap, error out with the bytes read so far available as a
truncated, redacted preview.

## Streamed chat

Different body shape, own error mapping. Port of `_web/params/chat_stream.py`.

**Body:**

```
params  = [
  nestSourceIDs(sourceIDs, 2),   // 0
  question,                      // 1
  conversationHistory,           // 2  (nil for a fresh turn)
  [2, null, [1], [1]],           // 3  capability envelope
  conversationID,                // 4  nil = "use/create the current conversation"
  null,                          // 5
  null,                          // 6
  notebookID,                    // 7
  1,                             // 8
]
fReq    = [null, jsonCompact(params)]     // NOTE: two-element, not triple-nested
body    = "f.req=" + urlencode(jsonCompact(fReq)) + "&at=" + urlencode(csrf) + "&"
```

**URL query:** `bl` (frontend build label), `hl`, `_reqid` (monotonic per client), `rt=c`,
`f.sid`, `authuser`.

The `bl` build label is pinned to a captured constant
(`boq_labs-tailwind-frontend_20260802.02_p0` at time of writing) and overridable via
`NOTEBOOKLM_BL`. Measured: **the server does not validate it** — a fabricated
`…_19700101.00_p0` still returns a complete cited answer. The risk is that it is accepted
silently right up until it isn't, so a nightly canary watches how far the pin trails the
served label (staleness threshold: 90 days, comparing label dates, never the wall clock).

`_reqid` is a monotonic counter per client, mutex-protected. Port `ReqidCounter`.

The response is a streamed chunked body carrying answer deltas, citation markers, and
reference rows; the parser lives in `internal/web/rows/chatstream.go` (port of
`_web/rows/chat_stream.py`, 1033 lines — the largest single decoder). Chat's own error
envelope is modeled separately from the batchexecute one, but both call the same
`SanitizeStatusMessage` so users cannot see two different wordings for one server status.
