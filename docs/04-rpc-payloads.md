# 04 — Positional RPC payloads

Every payload below is **position-addressed**. An index shift is not a style question; it
is a wire break. `null` means the JSON literal `null` (Go: an untyped `nil` element in
`[]any`), and it is often load-bearing — it holds a later slot in place.

Normative source: `../notebooklm-py/src/notebooklm/_web/params/*.py` plus the inline
literals in `_web/sources/add.py` and `_web/settings.py`. Deeper narrative:
`../notebooklm-py/docs/rpc-reference.md` (3171 lines).

## Conventions used here

| Shorthand | Meaning |
|---|---|
| `TPL` | the shared template block: `[2, null, null, [1, null×8, [1]]]` |
| `ARTOPTS` | the artifact client-options block: `[2, null, null, [1, null×8, [1]], [[1,4,8,2,3,6]]]` |
| `LBLOPTS` | label options — same as `TPL` |
| `COLOPTS` | collection options: `[2, null, null, [1, null×8, [1,3]]]` |
| `SRC(n)` | `NestSourceIDs(sourceIDs, n)` |
| `null×8` | eight consecutive `null` elements |

Build every options block **fresh per call**. Sharing a nested mutable literal across
requests is how one call's mutation corrupts the next.

## Notebooks

| RPC | Params |
|---|---|
| `ListNotebooks` | `[null, 1, null, [2]]` — note the trailing `[2]`, **not** the full `TPL` |
| `CreateNotebook` | `[title, null, null, TPL]` |
| `CopyNotebook` | `[TPL, sourceNotebookID, title]` — note the context **leads** here, unlike create |
| `GetNotebook` | `[notebookID, null, TPL, null, 0]` |
| `RenameNotebook` / set-emoji | `[notebookID, [[null, null, null, changeProperty]]]` where `changeProperty = [null, title]`, or `[null, title, emoji]` when the emoji is also changing. Omit the emoji slot entirely when not changing it. |
| `DeleteNotebook` | single id; batch shapes were probed and rejected |
| `RemoveRecentlyViewed` | `[notebookID]` |
| `Summarize` | notebook guide (summary + suggested questions) |

The response envelope for `ListNotebooks` is a **single-element wrapper** whose first element
is the row list (`[[row1, row2, …]]`). Unwrap through the strict accessor. An empty or `null`
payload and a `null` row-list slot are legitimate "no notebooks" shapes → `[]`; a truthy
payload that does not match the envelope is schema drift → `ErrDecoding`. Do not flow
unrecognized rows into the decoder.

`RenameNotebook` (`s0tc2d`) is a **generic notebook mutator**. It backs three unrelated
features: rename, chat configuration (persona/goal/response length), and share access
level. Distinguish by which mutation variant slot is populated.

### SuggestPrompts

```
[ ctx, notebookID, SRC(1), mode, null, query ]
  0    1           2       3     4     5
```

`ctx` = `[2, null, null, [1, null×8, [1]]]` — the four-element envelope **without**
`ARTOPTS`'s trailing capability projection.

`mode` is a required int in the inclusive range 1–10. `0` or omitted → gRPC INTERNAL; `11+`
→ INTERNAL. Validate client-side. The surface map (browser-verified by opening each Studio
Customize dialog):

| mode | surface | mode | surface |
|---|---|---|---|
| 1 | Audio · Deep Dive | 6 | Audio · Debate |
| 2 | Audio · Brief | 7 | *unidentified — no UI sends it* |
| 3 | Video · Explainer | 8 | Quiz |
| 4 | **Chat / ask (default)** | 9 | Flashcards |
| 5 | Audio · Critique | 10 | Video · Short |

The labels name **which dialog sends the mode**, not a promise about output tone: for
deep-dive / brief / explainer / short, NotebookLM steers format via content direction rather
than format jargon, so those four return prompts that read much like mode 4's.

`query` is a free-text steer. Normalize empty/whitespace-only to `null` so the default
request stays byte-identical and no blank prompt is sent.

## Sources

### AddSource (`izAoDd`)

One RPC, five source kinds, distinguished by which slot of the 11-element **source spec**
is populated. The spec is wrapped in a single-element list.

```
params = [ [sourceSpec], notebookID, TPL ]
```

Source spec slot map:

| kind | spec |
|---|---|
| **URL** | `[null, null, [url], null×7, 1]` — url at slot **2** |
| **YouTube** | `[null×7, [url], null, null, 1]` — url at slot **7** |
| **Pasted text** | `[null, [title, content], null, 2, null×6, 1]` — pair at slot **1**, type marker `2` at slot **3** |
| **Google Drive native** | `[[fileID, mimeType, 1, title], null×9, 1]` — at slot **0** |

The trailing `1` at slot 10 is present on every kind.

**Drive add has not been migrated to `TPL`.** It still sends the old four-element tail:

```
params = [ [sourceSpec], notebookID, [2], [1, null×8, [1]] ]
```

Keep it that way until a live Drive add is captured from the web UI and verified — the
Python original carries the same `TODO(#1546)`.

Operation variants for the idempotency registry: `"url"` (URL + YouTube), `"text"`,
`"drive"`. Text adds are intentionally **non-idempotent** — no stable probe exists, so a
caller wanting dedupe must handle it externally.

The URL and Drive variants run with inner retries **disabled** and a caller-owned
probe-then-create loop: capture the notebook's source-id set before the create, and on a
transport failure list again and diff. Notes on that probe, all ported:

- A URL is **not** unique within a notebook — a live capture added `https://example.com/`
  twice and got two distinct ids. An unfiltered match could hand back a pre-existing copy
  as if it were the one just created, masking a failed create.
- The baseline read is a `GET_NOTEBOOK`, and the backend **writes** `lastViewedTime` when
  answering one. So every `add_url` promotes the notebook to the top of the user's *Recent*
  list. Accepted: source ids are published only inside `GET_NOTEBOOK`, and `LIST_NOTEBOOKS`
  (which does not bump) does not carry them.
- The baseline must be captured **before** the create, not lazily inside the probe — a list
  taken after the create already contains the new source.
- If the probe itself fails with a non-transport error, **raise "UNRESOLVED — do not blindly
  retry"** rather than guess. Returning "did not land" on no evidence silently downgrades a
  `ProbeThenCreate` operation to at-least-once, which only the caller may opt into. Put the
  action first in the message: MCP and REST truncate at 300 characters.

### AddSourceFile (`o4cbdc`) — register an uploaded file

```
[ [[filename]], notebookID, TPL ]
```

### Other source RPCs

| RPC | Params |
|---|---|
| `UpdateSource` (rename) | `[null, [sourceID], [[[newTitle]]]]` |
| `DeleteSource` | batch-capable; we send a single id |
| `GetSource` | `[sourceID, …]` — see `_web/sources/listing.py` |
| `RefreshSource` | `[sourceID, …]` |
| `CheckSourceFreshness` | `[sourceID, …]` |
| `GetSourceGuide` | document guides |

## File upload — the Scotty resumable protocol

Three HTTP steps, none of them a batchexecute RPC. Port of
`_web/params/sources.py::build_resumable_upload_start_request` and
`_web/sources/upload.py`.

**Step 1 — register the source row.** `AddSourceFile` RPC → returns a `sourceID`.
This row now exists and counts against quota even if the upload never completes.

**Step 2 — start the upload session.** `POST {base}/upload/_/?{authuserQuery}`

| Header | Value |
|---|---|
| `Accept` | `*/*` |
| `Content-Type` | `application/x-www-form-urlencoded;charset=UTF-8` |
| `Origin` | derived from **the upload URL**, never from the configured base URL |
| `Referer` | `{origin}/` |
| `x-goog-authuser` | the authuser route value |
| `x-goog-upload-command` | `start` |
| `x-goog-upload-header-content-length` | file size in bytes, as a string |
| `x-goog-upload-header-content-type` | detected MIME type |
| `x-goog-upload-protocol` | `resumable` |

Body is **JSON** (despite the form `Content-Type` — that is what the wire sends):

```json
{"PROJECT_ID": "<notebookID>", "SOURCE_NAME": "<filename>", "SOURCE_ID": "<sourceID>"}
```

Response header `x-goog-upload-url` carries the session URL. Validate it before use
(scheme https, Google host, no credentials) — an unvalidated URL from a response header is
an SSRF sink.

**Step 3 — stream the bytes.** `POST {uploadURL}`

| Header | Value |
|---|---|
| `Accept` | `*/*` |
| `Content-Type` | `application/x-www-form-urlencoded;charset=utf-8` (lowercase charset here — matches the capture) |
| `x-goog-authuser` | the authuser route value |
| `Origin` / `Referer` | derived from the **validated upload URL** |
| `x-goog-upload-command` | `upload, finalize` |
| `x-goog-upload-offset` | `0` |

Stream in 64 KiB chunks with a progress callback. On abort, best-effort POST the same URL
with `x-goog-upload-command: cancel` — local teardown must never emit that cancel, only a
genuine user abort.

Notes: an unsupported extension (e.g. `.pub`) returns HTTP 400; classify it as a typed
`SourceAddError` naming the filename rather than leaking a raw status error. Uploads use
their own HTTP client with epoch-fenced live cookies copied from the kernel jar, and their
own concurrency semaphore — not the RPC semaphore.

## Labels

Two differences from source RPCs: the options wrapper is slot `[0]`, and `notebookID` rides
in the params at slot `[1]` **in addition to** the `source-path` query arg.

| RPC | Params |
|---|---|
| AI-generate | `[LBLOPTS, notebookID, null, null, [scopeIsAll]]` — `[true]` wipes and regenerates every label with new ids (destructive); `[false]` only labels currently-unlabeled sources |
| Manual create | `[LBLOPTS, notebookID, null, null, null, [[name, emoji]]]` |
| List | `[LBLOPTS, notebookID]` |
| Delete | `[LBLOPTS, notebookID, [labelID, …]]` — batch |

**Update** (`le8sX`) uses a fieldmask at slot `[3]` = `[[ nameEmoji, sourcesAdd, sourcesRemove ]]`
— a **three-slot group**:

| group slot | value | effect |
|---|---|---|
| 0 | `[name, emoji]`, or `[name]` for a rename | set name and/or emoji |
| 1 | `[[sourceID]]` | **assign** one source to the label |
| 2 | `[[sourceID]]` | **un-assign** one source (does not delete it from the notebook) |

The wire honors only the **first id per group per call**, so the builder is singular — the
API layer loops one call per id. When removing without adding, slot 1 must be `null` so
`sourcesRemove` keeps positional slot 2.

Whether a length-1 `nameEmoji` preserves or clears an existing emoji is confirmed
**preserve** for collections (live capture) and unverified for labels. The API layer passes
the current emoji explicitly regardless, so `Rename` does not depend on the answer.

## Collections

A collection is a source-`Label` of **type 3 with no notebook parent**, so the same four
label RPCs back it. Three wire differences:

1. The notebook slot `[1]` is `null` — collections are account-level.
2. A type discriminator `3` rides in the **last** slot of every request.
3. The options wrapper's trailing context list is `[1, 3]`, not `[1]`.

| RPC | Params |
|---|---|
| List | `[COLOPTS, null, 3]` |
| Create | `[COLOPTS_CREATE, null, null, null, null, [[name]], 3]` |
| Rename | `[COLOPTS, null, collectionID, [[[name]]], 3]` — or `[[[name, emoji]]]` |
| Delete | `[COLOPTS, null, [collectionID, …], 3]` |

`COLOPTS_CREATE` differs from `COLOPTS` in **one slot**: `[2]` is `[1]` instead of `null`.

```
COLOPTS        = [2, null, null,  [1, null×8, [1,3]]]
COLOPTS_CREATE = [2, null, [1],   [1, null×8, [1,3]]]
```

Without that `[1]`, the create reproducibly left nothing server-side — confirmed on three
independent accounts. There is **no emoji slot** on collection create; the create wire
carries the name at slot `[5]`.

**Notebook membership** (`le8sX`) has a two-element fieldmask `[group0, group1]` where
`group1` is always empty and both add and remove ride in `group0`:

```
add:    [[null, null, null, [[nbID]]],       []]     // id at group slot 3
remove: [[null, null, null, null, [[nbID]]], []]     // id at group slot 4
```

Remove does **not** move to a second group. An earlier (incorrect) inference that it did
made the original `remove_notebooks` a silent wire no-op — a good reminder of why guessing
a shape is forbidden.

## Artifacts — `CreateArtifact` (`R7cb6c`)

One RPC, ten artifact types. Every payload is `[ARTOPTS, notebookID, spec]` where `spec`'s
slot 2 is the `ArtifactTypeCode` and slot 3 is `SRC(2)`. **The per-type options block sits
at a different slot for every type** — that is the whole difficulty.

### Audio — type 1

```
[ARTOPTS, notebookID, [
  null, null, 1, SRC(2), null, null,
  [ null, [ instructions, lengthCode, null, SRC(1), language, null, formatCode ] ],   // slot 6
]]
```

Defaults when unset: format `DEEP_DIVE` (1), length `DEFAULT` (2).

### Video — type 3

```
[ARTOPTS, notebookID, [
  null, null, 3, SRC(2), null, null, null, null,
  [ null, null, videoConfig ],                                                        // slot 8
]]

videoConfig = [ SRC(1), language, instructions, null, formatCode, styleCode ]
```

Defaults: format `EXPLAINER` (1), style `AUTO_SELECT` (1).

`VideoStyle.CUSTOM` is special: its proto value is `0`, and the live web UI serializes a
proto-default as an **omitted/null field**, then carries the custom visual prompt at
`videoConfig` slot **6**. So for `CUSTOM`: `styleCode = null` and append `stylePrompt`.

### Cinematic video — type 3, format 3

Same as video, but `videoConfig` is only five elements and carries no style:

```
videoConfig = [ SRC(1), language, instructions, null, 3 ]
```

### Report — type 2

```
[ARTOPTS, notebookID, [
  null, null, 2, SRC(2), null, null, null,
  [ null, [ title, description, null, SRC(1), language, prompt, null, true ] ],       // slot 7
]]
```

`title`/`description`/`prompt` come from a static table per format. `--append TEXT` is
concatenated onto the built-in prompt as `prompt + "\n\n" + extra`, and has **no effect** for
`custom` (where the caller supplies the whole prompt).

| format | title | description |
|---|---|---|
| `briefing_doc` | Briefing Doc | Key insights and important quotes |
| `study_guide` | Study Guide | Short-answer quiz, essay questions, glossary |
| `blog_post` | Blog Post | Insightful takeaways in readable article format |
| `custom` | Custom Report | Custom format |

Port the three static prompt strings verbatim from
`_web/params/artifacts.py::_STATIC_REPORT_CONFIGS`. They are the product behavior, not
placeholder text. `custom` with no prompt falls back to
`"Create a report based on the provided sources."`

### Quiz — type 4, variant 2

```
[ARTOPTS, notebookID, [
  null, null, 4, SRC(2), null, null, null, null, null,
  [ null, [ 2, null, instructions, null, null, null, null, optionPair ] ],            // slot 9
]]
```

### Flashcards — type 4, variant 1

```
[ARTOPTS, notebookID, [
  null, null, 4, SRC(2), null, null, null, null, null,
  [ null, [ 1, null, instructions, null, null, null, optionPair ] ],                  // slot 9
]]
```

Note the option pair lands at options-slot **7** for quiz and **6** for flashcards.

`optionPair = [quantityCode, difficultyCode]` — **quantity first, for both**. Build it from
one shared helper. Two hand-written literals drifted apart in the original, with the
flashcards one transposed for long enough that the reference docs documented the inversion
as intended.

Defaults are **sent explicitly** (`STANDARD` = 2, `MEDIUM` = 2), not omitted. Omission is
accepted by the server, but what it then picked is **unobservable**: the stored options echo
back as `null`/`[]` and the generated content is not carried in `ListArtifacts`. Sending an
explicit pair trades a value that can be named, echoed, and asserted for one that cannot be
seen at all.

Reject a wrong-enum argument at build time. `QuizQuantity.MORE` and `QuizDifficulty.HARD`
are **both 3**, so passing one where the other belongs used to encode silently.

### Interactive mind map — type 4, variant 4

```
[ARTOPTS, notebookID, [
  null, null, 4, SRC(2), null, null, null, null, null,
  [ null, options ],                                                                  // slot 9
]]

options = [4]                          // no prompt
options = [4, null, instructions]      // with prompt (mirrors quiz's slot-2 prompt)
```

Empty/whitespace instructions must produce the bare `[4]` so the default request stays
byte-identical.

### Infographic — type 7

```
[ARTOPTS, notebookID, [
  null, null, 7, SRC(2), null×10,
  [[ instructions, language, null, orientationCode, detailCode, styleCode ]],          // slot 14
]]
```

Defaults: `LANDSCAPE` (1), `STANDARD` (2), `AUTO_SELECT` (1).

### Slide deck — type 8

```
[ARTOPTS, notebookID, [
  null, null, 8, SRC(2), null×12,
  [[ instructions, language, formatCode, lengthCode ]],                                // slot 16
]]
```

Defaults: `DETAILED_DECK` (1), `DEFAULT` (1).

### Data table — type 9

```
[ARTOPTS, notebookID, [
  null, null, 9, SRC(2), null×14,
  [ null, [ instructions, language ] ],                                                // slot 18
]]
```

### Note-backed mind map — `GenerateMindMap` (`yyryJe` → `ActOnSources`)

A **different RPC**, synchronous, and the result is stored as a JSON-bodied note.

```
[ SRC(2), null, null, null, null,
  [ "interactive_mindmap", [["[CONTEXT]", instructions]], language ],
  null,
  [2, null, [1]] ]
```

`instructions` becomes `""` when unset — not `null`. The `"[CONTEXT]"` literal and the
`"interactive_mindmap"` string are both required. The server may not always act on the
instructions on this path (unlike the interactive kind, which honors them reliably).

### Artifact lifecycle

| RPC | Params |
|---|---|
| `ReviseSlide` | `[[2], artifactID, [[[slideIndex, prompt]]]]` — note the short `[2]` options, not `ARTOPTS` |
| `RetryArtifact` | `[ARTOPTS, artifactID]` |
| `GetSuggestedReports` | `[[2], notebookID]` |
| `ListArtifacts` | includes a server-side filter excluding `ARTIFACT_STATUS_SUGGESTED`. The filter grammar takes the **symbolic enum name string**, not the integer — so `"ARTIFACT_STATUS_SUGGESTED"` cannot be derived from the value `5`. A stale string matches nothing and suggestion rows start appearing in every listing. |
| `GetInteractiveHTML` | generic single-artifact getter: quiz/flashcard HTML at `[0][9][0]`, interactive mind-map node tree at `[0][9][3]` |
| `ExportArtifact` | Drive export; Docs (1) and Sheets (2) are Drive destinations |

## Chat — save answer as note

The most intricate payload in the codebase (`_web/params/chat_note.py`). It reproduces the
web UI's "Save to note" button, preserving hover-anchored citation links.

Critical invariants:

- **Render flags are integers, not booleans.** The trailer inside every text-passage wrapper
  is `(0, 0, 0, null, null, null, null, 0, 0)`. Go's `json.Marshal(false)` emits `false`; the
  captured wire payload uses `0`. A byte-exact golden test guards this.
- **All character offsets are UTF-16 code units**, never bytes and never Go runes. Port a
  `utf16Len(s)` helper: `len(utf16.Encode([]rune(s)))`. One emoji anywhere in a cited
  fragment shifts every subsequent offset if you count wrong, misaligning — or getting the
  server to reject — the saved note.
- Two coordinate spaces coexist: the source-document span (absolute in the source's space)
  and the text-wrapper offsets (**local** to `citedText`, always starting at 0). The captured
  fixture has both starting at 0, masking the distinction; real chat references commonly have
  non-zero source offsets.
- Empty `citedText` collapses the source span to `[0, 0]` rather than emitting an invalid
  `[null, start, 0]`.
- The per-passage UUID slot falls back to `chunkID` when `passageID` is unset — empirically
  accepted, and citation anchors still work.

Answers **without** citations fall back to a plain-text note. The server may apply
smart-title generation on citation-rich saves and override the requested title; report what
the server actually stored, not what was asked for.

## Settings

| RPC | Params |
|---|---|
| `GetUserSettings` (`ZwVcOc` → GetOrCreateAccount) | context block only. **May create the account on first call.** |
| `SetUserSettings` (`hT54vc` → MutateAccount) | generic account mutator; we set only the output language |

Output language is a **global account setting**, not per-notebook. Every notebook in the
account is affected. Surface that in the CLI's `language set` output.

## Sharing

| RPC | Params |
|---|---|
| `GetShareStatus` (`JFMDGd`) | returns is-public, access level, share URL, collaborators, `maxIndividualsShareLimit`, `isPublicSharingAllowed`. **Cannot report `view_level`** — omit it from read output rather than guessing. |
| `ShareNotebook` (`QDyure`) | set notebook visibility |
| `SetShareAccess` | reuses `RenameNotebook` (`s0tc2d`) with a different mutation variant |
| `ShareArtifact` (`RGP97b`) | audio share |

`isPublicSharingAllowed` is a tenant policy gate. It is `null` when the backend made no
claim — so test `== false` for a real denial, never `!= true`.
