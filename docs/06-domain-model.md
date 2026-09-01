# 06 — Domain model: types and enums

Normative source: `../notebooklm-py/src/notebooklm/_types/`.

## Naming conventions

| Python | Go |
|---|---|
| `snake_case` fields | `PascalCase` exported fields with `json:"snake_case"` tags |
| `str` enum (`SourceType`) | `type SourceType string` + typed constants + `String()`/`Parse` |
| `int` enum (`AudioFormat`) | `type AudioFormat int` + typed constants + `String()`/`Parse` |
| `X \| None` | pointer (`*string`, `*time.Time`) when absent must be distinguishable from zero; value otherwise |
| `_private` dataclass field | unexported struct field + method accessor |
| `tuple[X, ...]` | `[]X` |

**When to use a pointer.** Only when `nil` carries meaning distinct from the zero value.
This matters more here than in ordinary Go code because the protocol distinguishes three
states in several places:

- `DriveStatus *DriveSourceStatus` — `nil` means "the row made no Drive-health claim at
  all", which is a *different answer* from `DriveSourceStatusUnknown` ("present but
  unmappable").
- `IsPublicSharingAllowed *bool` — `nil` means the backend made no claim; only an explicit
  `false` is a real denial.
- `Expires *int64` on a cookie — `nil` is a session cookie.

## Str enums

### SourceType

```go
type SourceType string

const (
    SourceGoogleDocs        SourceType = "google_docs"
    SourceGoogleSlides      SourceType = "google_slides"
    SourceGoogleSpreadsheet SourceType = "google_spreadsheet"
    SourcePDF               SourceType = "pdf"
    SourcePastedText        SourceType = "pasted_text"
    SourceWebPage           SourceType = "web_page"
    SourceGoogleDriveAudio  SourceType = "google_drive_audio"
    SourceGoogleDriveVideo  SourceType = "google_drive_video"
    SourceYouTube           SourceType = "youtube"
    SourceMarkdown          SourceType = "markdown"
    SourceDocx              SourceType = "docx"
    SourcePowerPoint        SourceType = "powerpoint"
    SourceCSV               SourceType = "csv"
    SourceEPUB              SourceType = "epub"
    SourceImage             SourceType = "image"
    SourceMedia             SourceType = "media"
    SourceUnknown           SourceType = "unknown"
)
```

Mapped from an integer wire code via a lookup table (port `_SOURCE_TYPE_CODE_MAP`). An
unmapped code warns **once per code per process** and degrades to `SourceUnknown` — never
panics, never drops the row. One code (14) is remapped by MIME type (PDF → 3).

### ArtifactType

```go
type ArtifactType string

const (
    ArtifactAudio       ArtifactType = "audio"
    ArtifactVideo       ArtifactType = "video"
    ArtifactReport      ArtifactType = "report"
    ArtifactQuiz        ArtifactType = "quiz"
    ArtifactFlashcards  ArtifactType = "flashcards"
    ArtifactMindMap     ArtifactType = "mind_map"
    ArtifactInfographic ArtifactType = "infographic"
    ArtifactSlideDeck   ArtifactType = "slide_deck"
    ArtifactDataTable   ArtifactType = "data_table"
    ArtifactFantasyMap  ArtifactType = "fantasy_map"
    ArtifactFile        ArtifactType = "file"
    ArtifactUnknown     ArtifactType = "unknown"
)
```

This is the **user-facing** axis; it deliberately hides variant complexity. Quiz,
flashcards, and interactive mind maps are all wire type **4**, distinguished by a variant
code.

### Others

`ReportFormat` (`briefing_doc`, `study_guide`, `blog_post`, `concept_explanation`,
`custom`), `DriveMimeType` (the four `application/vnd.google-apps.*` + `application/pdf`),
`MindMapKind` (`interactive`, `note_backed`).

## Int enums — the wire codes

Every value below is a wire integer. **Do not renumber, do not sort, do not "fix" a gap.**

### Artifacts

```go
type ArtifactTypeCode int
const (
    TypeAudio       ArtifactTypeCode = 1
    TypeReport      ArtifactTypeCode = 2   // briefing doc, study guide, blog post, white paper, …
    TypeVideo       ArtifactTypeCode = 3
    TypeQuiz        ArtifactTypeCode = 4   // ALSO flashcards and interactive mind maps
    TypeMindMap     ArtifactTypeCode = 5   // the genuine backend mind-map code (note-backed adaptation)
    TypeFantasyMap  ArtifactTypeCode = 6
    TypeInfographic ArtifactTypeCode = 7
    TypeSlideDeck   ArtifactTypeCode = 8
    TypeDataTable   ArtifactTypeCode = 9
    TypeFile        ArtifactTypeCode = 10
)

// Variant codes at artifactData[9][1][0], distinguishing sub-kinds of type 4.
const (
    VariantFlashcards         = 1
    VariantQuiz               = 2
    VariantInteractiveMindMap = 4
)
```

```go
type ArtifactStatus int
const (
    StatusUnknown       ArtifactStatus = 0  // ARTIFACT_STATUS_UNKNOWN
    StatusPending       ArtifactStatus = 1  // ARTIFACT_STATUS_INITIALIZED — row created, worker not started
    StatusProcessing    ArtifactStatus = 2  // actively generating
    StatusCompleted     ArtifactStatus = 3  // ARTIFACT_STATUS_READY
    StatusFailed        ArtifactStatus = 4
    StatusSuggested     ArtifactStatus = 5  // a suggestion, not a real artifact; filtered out of listings
    StatusPendingReview ArtifactStatus = 6  // ARTIFACT_PENDING_REVIEW — never observed on this transport
)
```

String projection: `unknown`, `pending`, `in_progress`, `completed`, `failed`,
`suggested`, `pending_review`. Note `StatusProcessing` → `"in_progress"`, not
`"processing"` — that string is part of the CLI/MCP/REST contract.

Two traps:

- **1 and 2 were swapped** in the original until a late fix, which made `IsPending` and
  `IsProcessing` answer each other's question. The member *names* describe the lifecycle
  phase and are stable; only the integers moved. Copy the integers from this table.
- `StatusPendingReview` shares a name prefix with `StatusPending` but is **unrelated** — that
  is the backend's own spelling. Its semantics are unconfirmed (0 of 42 live artifacts, 0 of
  301 recorded rows). Model it so a caller can *detect* it distinctly rather than have it
  collapse into `unknown`; do not infer a workflow from the name.

### Generation options

```go
type AudioFormat int
const (AudioDeepDive AudioFormat = 1; AudioBrief = 2; AudioCritique = 3; AudioDebate = 4)

type AudioLength int
const (AudioShort AudioLength = 1; AudioLengthDefault = 2; AudioLong = 3)

type VideoFormat int
const (VideoExplainer VideoFormat = 1; VideoBrief = 2; VideoCinematic = 3; VideoShort = 4)

type VideoStyle int
const (
    VideoStyleCustom     VideoStyle = 0   // proto default → serialized as null; prompt rides slot 6
    VideoStyleAutoSelect VideoStyle = 1
    VideoStyleClassic    VideoStyle = 2
    VideoStyleWhiteboard VideoStyle = 3
    VideoStyleHeritage   VideoStyle = 4
    VideoStylePaperCraft VideoStyle = 5
    VideoStyleWatercolor VideoStyle = 6
    VideoStyleAnime      VideoStyle = 7
    VideoStyleRetroPrint VideoStyle = 8
    VideoStyleKawaii     VideoStyle = 9
)

type QuizQuantity int
const (QuantityFewer QuizQuantity = 1; QuantityStandard = 2; QuantityMore = 3)

type QuizDifficulty int
const (DifficultyEasy QuizDifficulty = 1; DifficultyMedium = 2; DifficultyHard = 3)

type InfographicOrientation int
const (OrientLandscape InfographicOrientation = 1; OrientPortrait = 2; OrientSquare = 3)

type InfographicDetail int
const (DetailConcise InfographicDetail = 1; DetailStandard = 2; DetailDetailed = 3)

type InfographicStyle int
const (
    InfoStyleAutoSelect    InfographicStyle = 1
    InfoStyleSketchNote    InfographicStyle = 2
    InfoStyleProfessional  InfographicStyle = 3
    InfoStyleBentoGrid     InfographicStyle = 4
    InfoStyleEditorial     InfographicStyle = 5
    InfoStyleInstructional InfographicStyle = 6
    InfoStyleBricks        InfographicStyle = 7
    InfoStyleClay          InfographicStyle = 8
    InfoStyleAnime         InfographicStyle = 9
    InfoStyleKawaii        InfographicStyle = 10
    InfoStyleScientific    InfographicStyle = 11
)

type SlideDeckFormat int
const (SlideDetailedDeck SlideDeckFormat = 1; SlidePresenterSlides = 2)

type SlideDeckLength int
const (SlideLengthDefault SlideDeckLength = 1; SlideLengthShort = 2)
```

⚠️ `VideoStyle` and `InfographicStyle` share member *names* (`ANIME`, `KAWAII`) with
**different codes**. They are separate types precisely so the compiler catches a mix-up.

⚠️ `QuizQuantity.MORE` and `QuizDifficulty.HARD` are **both 3**. Distinct Go types make
passing one where the other belongs a compile error — a strict improvement over Python,
where it encoded silently.

### Chat

```go
type ChatGoal int
const (
    GoalDefault       ChatGoal = 1  // general research and brainstorming
    GoalCustom        ChatGoal = 2  // custom prompt, up to 10,000 characters
    GoalLearningGuide ChatGoal = 3  // educational, learning-oriented responses
)

type ChatResponseLength int
const (
    LengthDefault ChatResponseLength = 1
    LengthLonger  ChatResponseLength = 4   // note the gap: 2 and 3 are not used
    LengthShorter ChatResponseLength = 5
)
```

The four CLI/MCP mode presets map onto these: `default` → `(GoalDefault, LengthDefault)`,
`learning-guide` → `(GoalLearningGuide, LengthDefault)`, `concise` →
`(GoalDefault, LengthShorter)`, `detailed` → `(GoalDefault, LengthLonger)`.

A preset is **mutually exclusive** with a custom persona/length. Setting just one of
persona/response-length **merges** with current settings — the omitted field is preserved,
not reset. Only a bare call with no preset and neither field resets everything to defaults.

### Sources

```go
type SourceStatus int
const (
    SrcUnknown    SourceStatus = -1  // client sentinel: absent, malformed, or unmapped
    SrcProcessing SourceStatus = 1
    SrcReady      SourceStatus = 2
    SrcError      SourceStatus = 3
    SrcPreparing  SourceStatus = 5   // being prepared/uploaded — note the gap at 4
)

type DriveSourceStatus int
const (
    DriveUnknown            DriveSourceStatus = -1  // client sentinel
    DriveInaccessible       DriveSourceStatus = 1
    DriveSyncing            DriveSourceStatus = 2
    DriveActive             DriveSourceStatus = 3   // the ONLY value observed live
    DriveDeleted            DriveSourceStatus = 4
    DriveGenAIAccessDenied  DriveSourceStatus = 5
)
```

**`SourceStatus` and `DriveSourceStatus` are different axes and must never be conflated.**
`SourceStatus` reports NotebookLM's own ingestion, which completes and *stays* complete after
the Drive file is deleted or unshared. So a row legitimately reads `status: ready` while
`drive_status: deleted` — answers grounded on it may be stale.

Their code spaces collide adversarially: `2`/`3` mean ready/error for ingestion but
syncing/active for Drive. A consumer reasoning by analogy selects exactly the wrong rows.
Distinct Go types are the fix.

The backend's `DRIVE_SOURCE_STATUS_UNSPECIFIED` (0) is deliberately **not** modeled: proto3
omits zero-valued fields, so it arrives as an absent slot and means what an absent slot
means. The decoder normalizes an explicit `0` to `nil`. Modeling it would give one state two
representations.

`SrcPreparing` is how you find **orphaned rows**: a file add that fails *after* registering
its source row leaves that row at `preparing`, not `error` — and it still counts against
quota. `source list --status preparing` is the reconciliation query. Rows genuinely
mid-upload also read `preparing`, so the filter cannot by itself tell "abandoned" from "in
flight"; re-run a minute apart and act only on rows that persist. Nothing is deleted
automatically — that posture is deliberate.

### Research

```go
type DiscoveryMode int
const (
    DiscoveryUnknown         DiscoveryMode = -1  // client sentinel
    DiscoveryDefaultLLMSearch DiscoveryMode = 1  // sent + observed for mode=fast
    DiscoveryRawSearch        DiscoveryMode = 2  // never sent by this client
    DiscoveryCuriousSearch    DiscoveryMode = 3  // never sent
    DiscoveryCuriousRawSearch DiscoveryMode = 4  // never sent
    DiscoveryDeepResearch     DiscoveryMode = 5  // sent + observed for mode=deep
    DiscoveryLiteLLMSearch    DiscoveryMode = 6  // never sent
)
```

Only 1 and 5 are ever sent; the rest are decode-only forward compatibility. Same
unspecified-is-nil rule as `DriveSourceStatus`.

### Sharing

```go
type ShareAccess int
const (AccessRestricted ShareAccess = 0; AccessAnyoneWithLink = 1)

type ShareViewLevel int
const (ViewFullNotebook ShareViewLevel = 0; ViewChatOnly = 1)

type SharePermission int
const (
    PermOwner   SharePermission = 1  // proto: OWNER  — read-only, cannot be assigned
    PermEditor  SharePermission = 2  // proto: WRITER
    PermViewer  SharePermission = 3  // proto: READER
    permRemove  SharePermission = 4  // unexported: write-only sentinel (proto: NOT_SHARED)
)
```

`SharePermission` covers **both halves** of the sharing story under two proto names — a
collaborator's `SharedUser.permission`, and the calling account's own `Notebook.role`
(`ProjectMetadata.userRole`). Integers are identical, so one type serves both rather than a
value-identical twin.

`permRemove` (4) is a write-only sentinel with no display meaning; it is deliberately absent
from the label map and degrades to `"unknown"` like any unmapped code.

### Export and status

```go
type ExportType int
const (ExportDocs ExportType = 1; ExportSheets ExportType = 2)

type GrpcStatusCode int   // google.rpc.Code — the canonical 0–16
const (
    GrpcOK GrpcStatusCode = 0; GrpcCancelled = 1; GrpcUnknown = 2
    GrpcInvalidArgument = 3; GrpcDeadlineExceeded = 4; GrpcNotFound = 5
    GrpcAlreadyExists = 6; GrpcPermissionDenied = 7; GrpcResourceExhausted = 8
    GrpcFailedPrecondition = 9; GrpcAborted = 10; GrpcOutOfRange = 11
    GrpcUnimplemented = 12; GrpcInternal = 13; GrpcUnavailable = 14
    GrpcDataLoss = 15; GrpcUnauthenticated = 16
)
```

`GrpcStatusCode` is **deliberately distinct** from the HTTP-style `RPCErrorCode`
(`NotFound = 404`). They share member names but not values, so a distinct Go type forces the
call site to say which namespace it means. `GrpcNotFound` is `5`.

`MagicArtifactType` (0–15) covers the `NextStepSuggestions` surface codes; port verbatim
from `_types/enums.py`.

## Core structs

```go
type Notebook struct {
    ID            string              `json:"id"`
    Title         string              `json:"title"`
    Emoji         *string             `json:"emoji,omitempty"`
    CreatedAt     *time.Time          `json:"created_at,omitempty"`
    ModifiedAt    *time.Time          `json:"modified_at,omitempty"`
    LastViewedAt  *time.Time          `json:"last_viewed_at,omitempty"`
    SourcesCount  int                 `json:"sources_count"`
    IsOwner       bool                `json:"is_owner"`
    Role          *SharePermission    `json:"role,omitempty"`
    ChatSessions  []ChatSession       `json:"chat_sessions,omitempty"`
    ChatSettings  *ChatSettings       `json:"chat_settings,omitempty"`
    Premium       *PremiumFeatureInfo `json:"premium_features,omitempty"`
}

func (n Notebook) ShareURL() string   // derived, not stored

type Source struct {
    ID                string             `json:"id"`
    Title             string             `json:"title"`
    URL               *string            `json:"url"`
    Kind              SourceType         `json:"type"`
    Status            SourceStatus       `json:"-"`
    CreatedAt         *time.Time         `json:"created_at,omitempty"`
    LastModifiedAt    *time.Time         `json:"last_modified_at,omitempty"`
    DriveDocumentID   *string            `json:"drive_document_id"`
    DriveStatus       *DriveSourceStatus `json:"-"`
    ContentMIME       *string            `json:"content_mime,omitempty"`
    WordCount         *int               `json:"word_count,omitempty"`
    RevisionID        *string            `json:"revision_id,omitempty"`
    RevisionTimestamp *time.Time         `json:"revision_timestamp,omitempty"`

    typeCode    int     // wire code, for round-trip fidelity
    downloadURL string  // never serialized
    viewerURL   string  // never serialized
}

// The JSON surface exposes label+code pairs, matching the Python envelope:
//   "status": "ready", "status_id": 2, "drive_status": "deleted", "is_drive_degraded": true
func (s Source) IsDriveDegraded() bool  // true ONLY on an explicit non-healthy Drive state

type Artifact struct {
    ID               string          `json:"id"`
    Title            string          `json:"title"`
    Kind             ArtifactType    `json:"type"`
    Status           ArtifactStatus  `json:"-"`
    CreatedAt        *time.Time      `json:"created_at,omitempty"`
    LastModifiedAt   *time.Time      `json:"last_modified_at,omitempty"`
    URL              *string         `json:"url,omitempty"`
    GenerationPrompt *string         `json:"generation_prompt"`
    SourceIDs        []string        `json:"source_ids,omitempty"`
    DurationSeconds  *float64        `json:"duration_seconds,omitempty"`
    MediaURLs        []ArtifactMedia `json:"-"`
    Slides           []ArtifactSlide `json:"-"`
    Infographics     []ArtifactInfographic `json:"-"`
    ReportKind       *string         `json:"report_kind,omitempty"`
    ETag             *string         `json:"etag,omitempty"`
    QuizOptions      *QuizOptionPair `json:"quiz_options,omitempty"`

    typeCode int
    variant  *int
}

func (a Artifact) StatusString() string
func (a Artifact) IsPending() bool
func (a Artifact) IsProcessing() bool
func (a Artifact) IsCompleted() bool
func (a Artifact) IsFailed() bool
```

`Status` is exposed through `StatusString()` and the `Is*` predicates, never as an integer
comparison at a call site. Same reason as the Python original: when the wire integers moved,
every call site that compared integers would have needed auditing.

Other structs to port: `AskResult` (+ `ChatReference`, `ChatTurn`), `Note`, `MindMap` (+
`MindMapNode` tree), `Label`, `Collection`, `ShareStatus` (+ `SharedUser`), `ResearchRun` (+
`ResearchSource`, `ResearchReport`), `AccountLimits` (+ `tier`), `SourceFulltext` (+
`StructuredDocument`), `SourceGuide`, `NotebookDescription`, `PromptSuggestion`,
`ArtifactGenerationStatus`, `DownloadResult`, `MetricsSnapshot`.

## Two encoding invariants

### UTF-16 offsets

Every character offset in a TailwindDoc — chat citations, note passages, cited text spans —
is a count of **UTF-16 code units**. Not bytes, not runes.

```go
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }
```

Go's `len(s)` (bytes) and `len([]rune(s))` (runes) are **both wrong**. One emoji anywhere in
a cited fragment makes every subsequent offset wrong by one, which either misaligns the
citation anchors or gets the note rejected.

### Timestamps

Wire timestamps are Unix seconds, sometimes as `[seconds, nanos]` pairs, sometimes absent,
sometimes `0`. Port `_datetime_from_timestamp` semantics exactly:

- Absent slot or `0` → `nil`, not the Unix epoch.
- Decode from `json.Number`, not `float64` — millisecond-precision values lose fidelity
  through a `float64` round trip.
- Always UTC. Format `--json` output as RFC 3339 without a timezone suffix to match the
  Python `isoformat()` output the existing envelope produces
  (`"2026-01-23T18:42:00"`).
