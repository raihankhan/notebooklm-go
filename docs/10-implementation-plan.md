# 10 — Implementation plan

13 phases. **Work one phase to completion — including its tests — before starting the next.**
Each phase ends with `make check` green and its acceptance criteria demonstrably met.

Sequencing rationale: the protocol layer is the only part that cannot be guessed, so it goes
first and gets pinned by cassettes. Auth is next because nothing else can make a real call
without it. Then one vertical slice end-to-end (notebooks) to prove the whole stack before
scaling out horizontally across the remaining ten namespaces.

Effort estimates assume one focused engineer or agent. They are for sequencing, not for
promising a date.

---

## Phase 0 — Repository foundation

**Effort:** 1 day

### Deliverables

- `go.mod` at `github.com/raihankhan/notebooklm-go`, Go 1.25.
- `Makefile`: `build`, `check`, `fmt`, `vet`, `lint`, `test`, `test-e2e`, `cover`,
  `boundarycheck`, `clean`, `release`.
- `.golangci.yml` — enable `errcheck`, `govet`, `staticcheck`, `revive`, `gosec`,
  `bodyclose`, `contextcheck`, `errorlint`, `nilerr`, `unconvert`, `misspell`.
- `internal/tools/boundarycheck` — an import-graph linter reading a declarative rules file
  (`boundaries.yaml`) matching doc AGENTS.md rule 5. Fails CI on a violation.
- `internal/buildinfo` — version, commit, date, injected via `-ldflags`.
- `internal/logging` — `log/slog` handler: level from `NOTEBOOKLM_LOG_LEVEL` / `-v` count,
  **stderr only**, request-id correlation via `context`, and a `slog.ReplaceAttr` hook that
  routes every value through `internal/redact`.
- `internal/redact` — the four credential-regex families ported from `_logging.py`
  (quoted JSON, HTML-escaped JSON, form/prose, bare tokens) plus URL redactors.
- `.github/workflows/ci.yml` — matrix: `{linux, darwin, windows} × {amd64, arm64}` build;
  test on linux/amd64 + darwin/arm64; `make check`.
- Root `AGENTS.md` and `CLAUDE.md` pointing at `docs/AGENTS.md`.
- `README.md` — install, quickstart, pointer to docs.

### Acceptance

`make check` passes on an empty module. `boundarycheck` correctly **rejects** a deliberately
planted bad import (add one, watch it fail, remove it). Redaction has a table test proving
`SNlM0e`, `FdrFJe`, and a cookie value are masked in all four shapes.

---

## Phase 1 — The wire layer

**Effort:** 4 days · **The highest-leverage phase. Do not rush it.**

### Deliverables

`internal/web/wire`:

- `json.go` — `Marshal` (`SetEscapeHTML(false)`, newline-trimmed) and `Unmarshal`
  (`UseNumber()`). The only `encoding/json` importer in the module.
- `escape.go` — `escapeAll`, a Python-`quote(s, safe="")`-equivalent percent-encoder.
  **Space must encode as `%20`, not `+`.**
- `methods.go` — the full `Method` table from doc 03, plus `ResolveID` honoring
  `NOTEBOOKLM_RPC_OVERRIDES` (name=id pairs, or a JSON file path). Log the override set's
  hash once, never the values.
- `encode.go` — `EncodeRequest(method, params, resolvedID) []any`,
  `BuildRequestBody(req, csrf) []byte`, `NestSourceIDs(ids, depth) []any`,
  `TemplateBlock()`, `ArtifactOptions()`.
- `decode.go` — `StripAntiXSSI`, `ParseChunked` (with the three independent 10% malformed-rate
  gates and the `byteCountMismatchTotal` counter), `CollectRPCIDs`, `ExtractResult`,
  `DecodeResponse(raw, rpcID, opts)` with the full null-result classification tree.
- `index.go` — `At`/`Str`/`Int`/`Bool`/`List` + `Opt*` variants, returning
  `*ShapeDriftError`.
- `status.go` — `GrpcStatusCode` labels, `SanitizeStatusMessage` (whitespace-collapsed,
  300-char cap, non-string → warn and degrade), the `UserDisplayableError` depth-capped
  marker scan, the account-routing hint text.
- `urls.go` — endpoint builders + the three-host base-URL allowlist.

`internal/web/policy` — the five-class `IdempotencyRegistry`, keyed `(Method, variant)`, with
a startup assertion that every declared `Method` has an entry.

### Tests

- **Golden encode table**: for each RPC, `params → exact body bytes`. Generate the expected
  values by running the Python builders once and committing the output. This is the single
  most valuable test file in the repo.
- Byte-for-byte comparison of `Marshal` against Python `json.dumps(separators=(",",":"))`
  for payloads containing `<`, `>`, `&`, non-ASCII, emoji, and `null`.
- `escapeAll` table including space, `/`, `:`, `,`, `[`, `]`, `+`, `%`, and UTF-8.
- Chunked-parser fixtures: single frame · multi-frame with a null placeholder first ·
  byte-count mismatch · `\r\n` framing · malformed payload under 10% · malformed payload over
  10% · framing error · `er` frame · bare `[5]` status · `[8, null, [[UserDisplayableError…]]]`
  · absent rpc id · zero rpc ids · deeply nested JSON.
- `NestSourceIDs` for depths 1–3 and the empty/nil cases.
- Registry: every `Method` covered; a `ProbeThenCreate` entry without a rationale fails.

### Acceptance

Every golden encode case matches the Python output byte for byte. Every decode fixture
produces the same result or the same error class as the Python decoder. `make cover` reports
≥90% on `internal/web/wire`.

---

## Phase 2 — Cookie jar and credential storage

**Effort:** 4 days · **Highest-risk single unit. Gets a fuzz test.**

### Deliverables

`internal/auth/cookiejar`:

- The `Cookie` struct and `Jar` from doc 02, implementing `http.CookieJar`.
- RFC 6265 §5.3 storage keyed by `(name, domain, path)`; §5.4 selection with correct
  ordering; `Secure`-awareness; public-suffix rejection.
- `All()`, `Snapshot()`, `HeaderFor(url)`.
- `__Secure-`/`__Host-` prefix enforcement.

`internal/auth/storage`:

- `storage_state.json` read/write with **lossless** attribute round-trip, including
  `expires: -1` ⇄ `nil` and `sameSite`.
- Import normalizers: `expirationDate` → `expires`, `hostOnly`, `sameSite` case forms, bare
  cookie arrays, `origins` dropped.
- The `notebooklm.account` in-band namespace; `context.json` legacy read + in-band promotion.

`internal/auth/policy` — the domain allowlist (required + optional labels + regional ccTLDs),
`MinimumRequiredCookies`, the Tier 2 binding matrix, typed validation reasons
(`missing_cookie`, `psidts_unroutable`, `no_secondary_binding`), and the two-host diagnostic
messages.

`internal/atomicio` — atomic write (temp + chmod 0600 + sync + rename), `0700` directory
creation with the Windows ACL carve-out, `.bak` rollback, and the four `flock` derivations
from one path function plus per-path in-process mutexes.

`internal/paths` — `NOTEBOOKLM_HOME`, profile dirs, legacy fallback for the `default` profile
only, `config.json` with an mtime cache, `PathInfo` for `status --paths`.

### Tests

- **Round-trip fuzz**: generate random cookie sets (weird domains, dotted/undotted, all
  `SameSite` values, session and persistent, prefix cookies) → write → read → assert equal.
- §5.4 selection table, including the `OSID`-on-two-hosts case that must **not** collapse.
- Cross-compatibility: read a `storage_state.json` produced by the Python CLI (commit one as
  a fixture, with fake values) and assert every attribute survives; write one and assert the
  Python loader's expectations hold.
- Atomic write under simulated crash (write temp, do not rename, assert original intact).
- Concurrent `flock` acquisition from two goroutines and two processes.
- Windows path handling (skip-gated in CI where unavailable).

### Acceptance

The fuzz test passes 100k iterations. A Python-written profile is read with zero attribute
loss. Permissions are `0600`/`0700` on POSIX. No credential appears in any log line during
the whole suite (assert with a log-capturing hook).

---

## Phase 3 — Transport and runtime

**Effort:** 5 days

### Deliverables

`internal/runtime`:

- `Lifecycle` — open/close waves, monotonic resource generations (epoch fencing), phased
  participant ordering, rollback on a failed open, deterministic teardown failure precedence.
- `Supervisor` — drain admission → metrics → semaphore; call and operation leases;
  cancellation-safe settlement; race-free admitted child spawning.
- `Metrics` — atomic counters (`rpcCallsStarted/Succeeded/Failed`, `rpcAuthRetries`,
  `rpcDecodeErrors`, `queueWaitSeconds`, `byteCountMismatchTotal`) + the `RPCEvent` callback
  fan-out.
- `deadline.Budget` — aggregate deadline with `Remaining()` and `Expired()`, for the
  sleep-clamping arithmetic `context` alone cannot express.

`internal/web/transport`:

- `Kernel` — owns the `*http.Client` and the `Jar`; `Post` with a response size cap;
  `ActivateEpoch`/`FenceEpoch`/`AssertEpoch`; `Close`.
- `Executor` — one logical RPC: mint request id → consult the idempotency registry → resolve
  the method id → encode → dispatch through the chain → decode → map errors. Owns the
  decode-time auth-refresh-and-retry leg and the shared `RefreshBudget`.
- `Runtime` — authed POST entry: loop-free epoch check, auth snapshot capture, envelope
  materialization, chain dispatch, and the **unconditional pre-POST rebuild** in `terminal`.
- The four middlewares, wired in the pinned order.
- `errors.go` — transport error shapes and `Retry-After` parsing (both delta-seconds and
  HTTP-date forms).
- `stream.go` — streaming POST with the size cap.

`internal/config` — env resolution, the base-URL allowlist, `DEFAULT_BL` + the build-label
regex/staleness helpers, `NOTEBOOKLM_HL`.

### Tests

- `httptest` servers exercising: 429 with `Retry-After` (both forms) · 429 with retries
  disabled · 500 → success on retry · 5xx budget exhaustion · 401 → refresh → success ·
  401 → refresh → 429 → retry with the **rebuilt** envelope (the stale-envelope regression) ·
  response over the size cap · connect timeout · read timeout.
- Chain order pinned by a test that asserts the constructed middleware names in sequence.
- Concurrency: 50 goroutines issuing RPCs while one refresh fires → **exactly one** refresh,
  proven by a counter on a fake refresher.
- Epoch fencing: start 10 RPCs, close mid-flight, assert every late POST is rejected with the
  retired-generation error and none touches a rebuilt jar.
- `RefreshBudget`: a `wire-401 → refresh → decoded-auth-error` sequence performs **one**
  refresh.
- Aggregate deadline: a decode-time retry does not extend the T0-anchored budget.
- An AST test asserting no channel/lock/await-shaped operation sits between the envelope
  rebuild and `Kernel.Post`.

### Acceptance

Every middleware scenario passes. The single-refresh and epoch-fencing tests pass under
`-race` with `-count=20`.

---

## Phase 4 — Auth: acquisition and the refresh ladder

**Effort:** 6 days

### Deliverables

`internal/auth`:

- `tokens.go` — `Tokens` with a redacting `String()`/`LogValue()`; the load-from-storage
  pipeline (resolve source → seed jar → acquire tokens → merge observation → build).
- `extract/` — `WIZ_global_data` extraction for `SNlM0e` and `FdrFJe` across all quoting
  variants (double, single, HTML-escaped), plus the **four-branch failure taxonomy** in the
  correct order: region/anti-abuse gate → cookie-mismatch hop → auth redirect → token
  missing. One shared classifier so the refresh path and the extractor cannot disagree on
  branch order.
- `refresh/` — the L1 → L2.0 → L2.5 → L3 → L4 ladder with the bounded storage-reload attempt
  counts (3 file-backed / 2 inline).
- `singleflight/` — leader/follower coalescing keyed by canonical path, per-path success
  epochs, cancellation-safe follower bridging, plus the cross-process `flock`.
- `keepalive/` — `RotateCookies` with all three stampede guards; the background goroutine and
  its `NOTEBOOKLM_DISABLE_KEEPALIVE_POKE` kill switch; inline PSIDTS recovery on load.
- `profile/` — `ProfileStore`: document read/write, CAS cookie merge against a baseline,
  in-band account read/update/clear, minted-session replacement with the same-lock
  latest-owner gate.
- `account/` — `FormatAuthuserValue`, `authuser` query building, `?authuser=N` probing, active
  email extraction, generation-safe self-heal via exact-document CAS.
- `mastertoken/` — the four steps from doc 05 (D2 exchange, D3 mint, D4 OAuthLogin +
  MergeSession + Rotate, jar verification), the `master_token.json` codec, the bootstrap
  coordinator with its bootstrap lock and session-before-token ordering, and the
  account-mismatch guard.

### Tests

- All extraction variants + all four failure branches, each asserting the *specific*
  remediation message.
- Ladder tests with a fake homepage: L1 success · dead cookies → L2.0 reload success ·
  → L2.5 command success · → L3 headless success · → L4 re-mint success · all exhausted →
  the exact final message.
- Single-flight: 100 concurrent refreshes → 1 execution; a cancelled follower does not cancel
  the leader.
- Keepalive: two goroutines and two processes → one POST; a failed POST still consumes the
  60 s slot.
- Master token: a fake `android.clients.google.com` and `accounts.google.com` covering
  exchange, mint, uberauth (valid, empty, containing a space, non-200), MergeSession, and the
  missing-required-cookie rejection. **Assert no credential appears in any error, log, or
  wrapped cause.**
- Account routing: index vs email; a `NOT_FOUND` gets the hint and does **not** trigger a
  refresh.

### Acceptance

The full ladder works against fakes. A live smoke test authenticates with a real profile and
lists notebooks. No credential in any output, verified by a log-scanning assertion over the
whole suite.

---

## Phase 5 — Vertical slice: notebooks end to end

**Effort:** 3 days

Prove the entire stack on the smallest complete namespace before scaling out.

### Deliverables

- `notebooklm/client.go` — `Client`, `New`, `FromStorage`, `Close`, `Drain`, `RPCCall`,
  `RefreshAuth`, `MetricsSnapshot`, and the `Option` set.
- `notebooklm/notebooks.go` + `notebooklm/types_notebooks.go` — the full `NotebooksAPI`
  (17 methods from doc 12's table).
- `internal/web/params/notebooks.go`, `internal/web/rows/notebooks.go`,
  `internal/web/features/notebooks.go`.
- `internal/backend/backend.go` — the interface seam.
- `internal/app/resolve` — partial-id resolution for all five id kinds.
- `internal/app/errors` — `Classify`.
- `internal/app/serialize` — the canonical `--json` envelope shapes.
- `internal/cli` skeleton: root command, persistent flags, `cobra.Group` bins, the error
  handler with the full exit-code table and the JSON envelope (including parse-time
  interception), `internal/cli/theme` with the CP AXTRA palette, and the table renderer.
- CLI commands: `list`, `create`, `use`, `status`, `clear`, `delete`, `rename`, `summary`,
  `metadata`, `auth check`, `profile *`, `completion`.
- `internal/tools/scrubhar` + the go-vcr harness with the ported match tuple.

### Acceptance

`notebooklm list --json` against a cassette produces the same envelope keys as the Python
CLI. Exit codes verified for success, not-found, auth failure, and SIGINT. `--json` output
is byte-clean with a spinner active. `boundarycheck` green with all layers populated.

---

## Phase 6 — Sources

**Effort:** 6 days · **The largest namespace (~5k Python lines).**

### Deliverables

- `notebooklm/sources.go` — all 18 methods.
- `internal/web/params/sources.go` + the five inline add shapes from doc 04.
- `internal/web/rows/sources.go` — the source row decoder (1385 Python lines): type-code
  mapping, status, both Drive axes, metadata URL/timestamp extraction, revision fields.
- `internal/web/upload/` — the three-step Scotty protocol with upload-URL validation, 64 KiB
  streaming, progress callback, its own client and semaphore, and best-effort cancel.
- `internal/web/sources/` — add (url/youtube/text/file/drive), the probe-then-create wrapper,
  listing with status/label filters, content/fulltext, batch, clean.
- `internal/app/sourceadd` — argument classification (URL vs file vs YouTube vs text vs
  Drive share URL), symlink gate, internal-address gate, MIME inference with `--mime-type`
  override.
- `internal/app/sourcewait` — wait orchestration + the content-sanity warning patterns.
- HTML→Markdown for `fulltext -f markdown`.
- CLI: the full `source` group; `label` and `collection` groups.

### Tests

- Add-argument classification table, including ambiguous cases.
- The probe-then-create matrix: create succeeds · transport fails and the probe finds the new
  id · transport fails and the probe finds nothing · **the probe itself fails → UNRESOLVED is
  raised, not a retry.**
- Upload: happy path · 400 on an unsupported extension · an invalid upload URL in the
  response header · mid-stream abort → cancel POST sent · local teardown → cancel **not**
  sent.
- Drive axes: a row with an id and no health claim (the common case) · a health claim and no
  id · neither · `drive_status: 0` normalized to `nil`.
- `--status preparing` finds an orphaned row.
- Label fieldmask: rename · emoji · add · remove-without-add (asserting slot 1 is `null`).
- Collection membership: add at group slot 3, remove at group slot 4, `group1` empty.

### Acceptance

All five source kinds add against cassettes. A real file uploads end to end in the live smoke
test. Label and collection wire shapes match the golden bytes.

---

## Phase 7 — Chat

**Effort:** 5 days

### Deliverables

- `notebooklm/chat.go` — `Ask`, `GetHistory`, `GetConversationTurns`, `GetConversationID`,
  `DeleteConversation`, `Configure`, `GetSettings`, `SetMode`, `SaveAnswerAsNote`, and the
  conversation cache.
- `internal/web/params/chatstream.go` — the streaming request builder + `ReqidCounter`.
- `internal/web/rows/chatstream.go` — the streaming response parser (1033 Python lines):
  answer deltas, citation markers, reference rows, the chat error envelope.
- `internal/web/rows/chat.go` — history and turn decoding (1199 Python lines).
- `internal/web/params/chatnote.go` — the save-as-note payload with integer render flags and
  UTF-16 offsets.
- `internal/app/studio` — the unified note+artifact projection (needed by MCP, built here).
- CLI: `ask`, `suggest-prompts`, `configure`, `history`.

### Tests

- Streaming parser fixtures: a plain answer · a cited answer · multi-chunk delivery · a
  mid-stream error envelope · an oversized-prompt rejection.
- **UTF-16 offset table** with ASCII, accents, CJK, emoji, and surrogate pairs. Prove
  `utf16Len` differs from both `len(s)` and `len([]rune(s))` on the emoji cases.
- Save-as-note golden bytes, asserting `0` and not `false` for the render flags.
- Configure merge semantics: preset · partial custom preserves the omitted field · bare call
  resets · preset + custom rejected.
- `ask --new` deletes then starts fresh; `--json` implies `--yes`.
- Conversation-id recovery on a fresh conversation.

### Acceptance

A live `ask` returns a cited answer whose references resolve to real sources. Save-as-note
produces a note with working hover anchors in the web UI (manual verification, recorded in
the phase notes).

---

## Phase 8 — Artifacts: generate, poll, download

**Effort:** 7 days

### Deliverables

- `notebooklm/artifacts.go` — 10 `Generate*`, 9 `Download*`, plus list/get/get-prompt/
  rename/delete/poll/wait/retry/export/suggestions and the eight typed `List*` helpers.
- `notebooklm/mindmaps.go` — the unified `MindMapsAPI` over both kinds.
- `internal/web/params/artifacts.go` — all ten `CreateArtifact` builders, `ReviseSlide`,
  `RetryArtifact`, `GenerateMindMap`, `SuggestReports`. Shared `quizOptionPair`.
- `internal/web/rows/artifacts.go` — the artifact row decoder (1244 Python lines): type +
  variant resolution, status, media URLs, slides, infographics, quiz options, user state.
- `internal/web/artifact/polling.go` — leader/follower polling with backoff over a
  target-aware studio projection.
- `internal/web/assets/` — byte download with the **per-hop** SSRF guard: the
  `.google.com`/`.googleusercontent.com`/`.googleapis.com` allowlist, the reject-any-`%`/`\`/`/`
  hostname rule, credential stripping the moment a chain leaves the allowlist, staged temp
  write, atomic publication, and WAV correction.
- `internal/app/downloadspec` — the canonical registry (types × formats × extensions × MIME
  × default dirs).
- `internal/app/download` — target selection (`--all`/`--latest`/`--earliest`/`--name`), the
  three-way collision policy, `--dry-run`.
- `internal/app/generate` — plan building + rate-limit retry with backoff.
- `internal/artifact/formatters` — quiz/flashcard Markdown and HTML renderers.
- CLI: the full `generate`, `artifact`, and `download` groups.

### Tests

- **Golden bytes for all ten generate payloads**, plus the variants: custom video style
  (null style code + prompt at slot 6) · cinematic (5-element config) · report with
  `--append` · report `custom` · quiz vs flashcards option-pair slots · interactive mind map
  with and without instructions.
- Wrong-enum rejection (`QuizDifficulty` where `QuizQuantity` belongs) — a compile error in
  Go, asserted by a `go vet`-style negative example in a doc test.
- **SSRF guard table**: trusted → trusted redirect passes · a non-HTTPS hop rejected · an
  off-allowlist host rejected · `evil%2egoogleapis.com` rejected · credentials stripped after
  leaving the allowlist · `169.254.169.254` rejected.
- Download collision: auto-rename · `--force` · `--no-clobber` (single fails, `--all` skips).
- Polling: completion · failure · timeout · a leader/follower pair sharing one poll loop.
- Mind map: `artifact delete` on a mind map prints `Cleared mind map:`.
- Every download format writes the right extension and MIME.

### Acceptance

All ten types generate and download in the live smoke test. Audio lands as `.m4a`. The SSRF
table passes in full. Quiz Markdown and HTML render correctly.

---

## Phase 9 — Research, sharing, settings, notes

**Effort:** 4 days

### Deliverables

- `notebooklm/research.go` — `Start`, `Poll`, `WaitForCompletion`, `Cancel`,
  `ImportSources`, `ImportSourcesWithVerification`, `ExtractReportURLs`,
  `SelectCitedSources`.
- `internal/web/rows/research.go` + `research_task.go` — run/report/source decoding, the
  `status_code` + `termination_reason` partition, `reason_message`/`hint`.
- `notebooklm/sharing.go`, `notebooklm/settings.go`, `notebooklm/notes.go`,
  `notebooklm/labels.go`, `notebooklm/collections.go`.
- `internal/web/rows/sharing.go`, `notes.go`, `labels.go`, `collections.go`.
- `AccountLimits` with `tier`.
- CLI: `research`, `share`, `note`, `language` groups.

### Tests

- Research state machine: fast and deep · in-progress · completed · `no_results` ·
  `cancelled` · `unknown` · **the both-null no-backend-code case**.
- Import idempotency: URL-addressed entries skip as `already_present`;
  `--allow-duplicate` re-adds; `--cited-only` narrows; `--max-sources` caps.
- `IMPORT_RESEARCH`'s batch-scaled read timeout survives a post-refresh retry.
- Share status omits `view_level`; `is_public_sharing_allowed` nil vs false.
- Language set warns that it is global.

### Acceptance

A live deep-research run completes and imports cited sources. Sharing round-trips
public → restricted → user grant → removal.

---

## Phase 10 — MCP server

**Effort:** 5 days

### Deliverables

- `cmd/notebooklm-mcp` + `internal/mcpsrv`: all 33 tools, matching names and parameter names.
- Transports: stdio and streamable HTTP.
- Bearer auth (`crypto/subtle`), the fail-closed non-loopback bind guard,
  `NOTEBOOKLM_MCP_TRUST_PROXY`.
- Confirmation gating: the `needs_confirmation` preview for every destructive tool.
- `internal/app/studio` wired into `studio_*`.
- Pagination helpers, ref resolution, `detail` projections, the content-sanity warnings, the
  in-channel base64 upload path, and the signed file-link routes for remote mode.
- `internal/app/mcpinstall` + the `mcp install` CLI command.
- Schema enums **generated** from the domain label maps.

### Tests

- One test per tool: happy path, a not-found ref, and a missing `confirm` on the destructive
  ones.
- **stdout purity test**: run the stdio server against a scripted session and assert every
  stdout byte is framed JSON-RPC.
- Fail-closed bind: external bind without a token refuses to start.
- Bearer: correct, wrong, and missing.
- `mcp install` idempotency: unrelated servers preserved across two runs.
- Error truncation at 300 chars keeps the action visible.

### Acceptance

Claude Desktop and Claude Code connect via `mcp install` and successfully call a read tool, a
generate tool, and a gated destructive tool. Tool-name parity with the Python server asserted
by a test reading a committed name list.

---

## Phase 11 — REST server

**Effort:** 3 days

### Deliverables

- `cmd/notebooklm-server` + `internal/restsrv`: all 43 routes.
- Bearer + loopback-`Host` guard; no schema surface; fail-closed external bind.
- Six `semaphore.Weighted` limiters wired from env.
- The poll-the-resource pending registry.
- Multipart upload with `MaxBytesReader` and temp-file streaming.
- Graceful shutdown with a bounded drain.

### Tests

- Route table test: every path/method/status asserted against a committed list.
- Auth: bearer missing/wrong/right; a non-loopback `Host` rejected.
- Limiters: N+1 concurrent generations → the last one queues, and `/healthz` still answers
  immediately.
- Pending lifecycle: 202 → pending → 200 → 410.
- Upload over the size limit → 413.
- `/docs` and `/openapi.json` → 404.

### Acceptance

Route parity asserted. `/healthz` stays responsive under a saturated generation limiter.

---

## Phase 12 — Hardening, parity audit, and cross-compatibility

**Effort:** 5 days

### Deliverables

- **Parity audit script** (`internal/tools/parityaudit`): parse
  `../notebooklm-py/docs/cli-reference.md` and the MCP tool table, diff against the Go
  command tree and tool registry, and fail on a missing command, flag, or tool.
- **Cross-compatibility test**: a fixture `~/.notebooklm/` written by the Python CLI is read
  by the Go binary and vice versa, asserting a byte-identical `storage_state.json` after a
  no-op open/close.
- `doctor` with the full check set and `--fix`.
- `skill install/status/uninstall/show/package` with `go:embed`ed `SKILL.md`.
- `agent show codex|claude`.
- RPC health canary (port `scripts/check_rpc_health.py`): probe every method id, compare the
  pinned `DEFAULT_BL` against the served label, report the staleness gap. Wire to a nightly CI
  job.
- Structured tracing hooks positioned for OpenTelemetry.
- `-race` and `-count=5` in CI for the concurrency-sensitive packages.
- Fuzz targets: the chunked parser, the cookie jar, and `escapeAll`.
- Docs: `README.md`, `CHANGELOG.md`, `SECURITY.md`, `CONTRIBUTING.md`.

### Acceptance

The parity audit reports zero missing commands, flags, or tools. Cross-compatibility passes
in both directions. The nightly canary runs green. All fuzz targets survive 5 minutes with no
crash.

---

## Phase 13 — Packaging and release

**Effort:** 3 days

### Deliverables

- `goreleaser` config: archives for `{linux, darwin, windows} × {amd64, arm64}`, checksums,
  SLSA provenance, and Homebrew + Scoop manifests.
- Multi-arch container: distroless or `FROM scratch` base, all three binaries.
- `deploy/` — `Dockerfile`, `docker-compose.yml`, `docker-compose.build.yml`, `Makefile`,
  `.env.example`, and the Cloudflare + Tailscale Funnel tunnel sidecars behind Compose
  profiles, with `make setup` → one manual tunnel step → `make up`.
- `install.sh` — a one-liner installer detecting OS/arch.
- Release CI: tag → build matrix → sign → publish → update manifests.
- **v1.1 backlog, scoped:** the `utls` impersonation transport, MCP self-hosted OAuth, the
  `.mcpb` desktop-extension bundle.

### Acceptance

A tagged release produces working binaries on all six platform pairs. The container image is
under 40 MB and the remote MCP stack comes up behind a tunnel and answers a tool call from
the claude.ai mobile app.

---

## Summary

| Phase | Focus | Days |
|---|---|---|
| 0 | Foundation, lint, boundaries, redaction | 1 |
| 1 | Wire layer + idempotency registry | 4 |
| 2 | Cookie jar + credential storage | 4 |
| 3 | Transport + runtime + middleware | 5 |
| 4 | Auth acquisition + refresh ladder | 6 |
| 5 | Vertical slice: notebooks + CLI skeleton | 3 |
| 6 | Sources + upload + labels + collections | 6 |
| 7 | Chat + streaming parser | 5 |
| 8 | Artifacts: generate, poll, download | 7 |
| 9 | Research, sharing, settings, notes | 4 |
| 10 | MCP server | 5 |
| 11 | REST server | 3 |
| 12 | Hardening + parity audit | 5 |
| 13 | Packaging + release | 3 |
| | **Total** | **~57 working days** |

Roughly 11–12 focused weeks for one engineer; the phases after 5 parallelize two or three
ways across namespaces if more hands are available. Phases 1–4 are strictly sequential —
they are the foundation everything else stands on, and every one of them is load-bearing for
correctness rather than for features.

---

# v1.1 and beyond — feature-additive phases

Phases 0–13 ship parity with `notebooklm-py` v0.8.1. The five phases below ship the
**eleven net-new features** introduced in [docs/15-features-overview.md](15-features-overview.md).
Every feature obeys the three parity-stability rules ([docs/AGENTS.md](AGENTS.md) § "Feature
additions"): no new RPC, no NotebookLM-side metadata writes, and a `~/.notebooklm/`
layout that round-trips with the Python CLI.

Sequencing rationale: phases 14 and 15 are independent and can run in parallel after
Phase 13. Phase 16 depends on 14 (templates, bundles). Phase 17 depends on 14 (plugin
SDK composes pipeline steps). Phase 18 is always last — the skill bundle is the user-facing
front door for everything earlier.

---

## Phase 14 — Client-side composition (features #1, #2, #4)

**Effort:** 12 days

Spec files: [docs/features/01-pipelines.md](features/01-pipelines.md),
[docs/features/02-cross-notebook.md](features/02-cross-notebook.md),
[docs/features/04-templates-personas-bundles.md](features/04-templates-personas-bundles.md).

### Deliverables

`internal/app/pipeline`:

- YAML schema parser with golden tests for every shape.
- DAG executor: topological sort, cycle detection, dependency resolution.
- `${{vars.X}}` / `${{steps.Y.output}}` / `{{item}}` interpolation engine using `text/template`.
- `for_each` fan-out with per-step `parallel` cap and global `concurrency` cap.
- Per-step retry policy with the existing 5-class idempotency registry from `internal/web/policy`.
- Resume support: read partial state from disk, skip completed steps, retry pending ones.
- Subprocess driver over `os/exec.Cmd` (argv-only, no shell) for `run:` steps.

`internal/app/crossnotebook`:

- `AskAcross`: fan out `Chat.Ask` per notebook under the existing concurrency limits; merge answers + renumber citations globally.
- `Synth`: one-shot create + N × `Sources.AddURL` + wait-for-ready + `Chat.Ask` + optional `Notes.Create`.
- `Query`: thin wrapper around `Chat.Ask` honoring `NOTEBOOKLM_BRAIN_NOTEBOOK`.

`internal/app/templates`:

- Template / persona / bundle parsers (frontmatter + Markdown / YAML).
- `text/template` renderer for templates.
- Atomic write helpers for `~/.notebooklm/templates/`, `personas/`, `bundles/`.

CLI: full `pipeline`, `template`, `persona`, `bundle` groups; `ask --across` and `--max-per-notebook`; `synth`; `query`.

REST: `/v1/pipelines`, `/v1/templates`, `/v1/personas`, `/v1/bundles`, `/v1/notebooks/chat/across`, `/v1/notebooks/synth`.

MCP: 11 new tools (per the per-feature specs).

### Tests

- YAML schema golden table for every pipeline shape.
- DAG topo-sort with cycle detection.
- Interpolation: vars, step outputs, `for_each`, missing-var failures.
- Retry policy: only retries on the documented error codes.
- `for_each` parallel cap honored (mock clock + counting shim).
- Resume from a partial state.
- Shell-injection guard (a malicious `run:` does not execute a shell).
- `AskAcross` merge: citation renumbering, partial failure handling.
- `Synth` end-to-end: create + sources + ask + note.
- Template / persona / bundle round-trip.

### Acceptance

- `notebooklm pipeline validate pipeline.yaml` exits 0 for a valid file, 1 for every known invalid shape.
- `notebooklm pipeline run pipeline.yaml --wait` against a fixture notebook completes; `--json` envelope matches the documented schema.
- `notebooklm query "..."` with `NOTEBOOKLM_BRAIN_NOTEBOOK` set runs a single `Chat.Ask`; without the env var exits 1 with the documented message.
- `notebooklm template apply` produces an artifact whose `generation_prompt` equals the rendered template.
- `~/.notebooklm/{templates,personas,bundles,pipelines}/` round-trip with `notebooklm-py`'s reader (Python does not have these folders, but the Go writer must not interfere with the parity folders).

### Risk

[docs/14-risk-register.md](14-risk-register.md) applies. The pipeline executor's subprocess driver is the highest-blast-radius surface; the shell-injection test is mandatory.

---

## Phase 15 — Local data plane (features #5, #6, #8)

**Effort:** 11 days

Spec files: [docs/features/05-local-search-index.md](features/05-local-search-index.md),
[docs/features/06-eval-harness.md](features/06-eval-harness.md),
[docs/features/08-export-import-bundles.md](features/08-export-import-bundles.md).

### Deliverables

`internal/index`:

- Pure-Go SQLite wrapper using `modernc.org/sqlite` (no CGo).
- FTS5 schema (`sources_fts`, `doc_offsets`); porter + unicode61 tokenizers.
- Index builder that walks `Sources.List` + `Sources.GetFulltext` and writes fragments.
- Incremental update keyed on `(source_id, fragment_id, mtime)`.
- Atomic rename (`corpus.db.tmp` → `corpus.db`) with one-cycle `corpus.db.bak` rollback.
- Schema-versioned migrations; the `manifest.schema_version` field.

`internal/app/eval`:

- JSONL dataset loader / writer; `history --output history.jsonl` reuses the existing `Chat.GetHistory` flow.
- Spec parser (`pass_threshold`, `parallel`, `scoring.weights`).
- Per-row runner: `Chat.Ask` per row, with `-c <conv-id>` continuation when present.
- Scoring engine: `must_contain`, `must_cite`, `must_not_contain`, `must_cite_any_of`, `recall_at_k`.
- Diff reporter (pairwise regression / improvement).

`internal/app/bundle`:

- Manifest writer / reader.
- Ed25519 signing (`crypto/ed25519`).
- Export driver: `Sources.List` + `GetFulltext` + `Notes.List/Get` + `Chat.GetHistory` + `Artifacts.List`.
- Import driver: `Notebooks.Create` + N × `Sources.AddURL` + `Notes.Create` + chat replay.
- Idempotency keyed on `manifest.uuid`.

CLI: `index build/update/status/show/drop`, `search`, `eval export/run/list/show/diff`, `bundle export/import/verify/inspect`.

REST: `/v1/index/*`, `/v1/evals/*`, `/v1/notebooks/{id}/bundle/export`, `/v1/notebooks/bundle/import`, `/v1/bundles/verify`.

MCP: 9 new tools.

### Tests

- FTS5 query precision / recall on a synthetic corpus.
- Incremental update: a single changed source updates only its fragments.
- Snippet reconstruction (`start_char` / `end_char` correct).
- Eval: scoring golden table for every `expected` field type.
- Eval diff: regression vs improvement classification.
- Bundle: round-trip export → import preserves title, source count, note count, chat history.
- Bundle: signed vs unsigned verify paths.
- Bundle: idempotency under repeated import.

### Acceptance

- `notebooklm index build` over 200 sources completes in under 5 minutes; `corpus.db` is the documented size.
- `notebooklm search` returns ranked results in under 100 ms over 10 K fragments.
- `notebooklm eval run` against 20 rows finishes within the timeout; `--json` envelope matches the schema.
- `notebooklm bundle export` produces a zip whose `manifest.json` enumerates every file with sha256 hashes.
- Round-trip export → import preserves the documented invariants.
- Binary remains static (no CGo).

### Risk

SQLite FTS5 availability under `modernc.org/sqlite` on every supported platform. The
Windows-on-ARM64 combination needs explicit testing; fall back to bundled `golang.org/x/text`
tokenizer if FTS5 is unavailable on a target.

---

## Phase 16 — Team governance (features #3, #7)

**Effort:** 9 days

Spec files: [docs/features/03-watchers.md](features/03-watchers.md),
[docs/features/07-workspace-policy.md](features/07-workspace-policy.md).

### Deliverables

`internal/app/watchers`:

- Trigger sources: RSS / Atom, fsnotify-based directory, git-based repository.
- Per-watch state: last-seen cursor (RSS guid, dir mtime, git HEAD commit), persisted under `~/.notebooklm/watches/<id>/state.json`.
- Polling loop with per-kind `Interval`; backpressure via `Batch` cap.
- `Log` tail; `Status` synchronous one-cycle; `Pause` / `Resume` / `Remove`.

`internal/app/workspace`:

- Workspace root detection (walks up to `.notebooklm-workspace/`, falls back to `~/.notebooklm/workspace-link.json`).
- Sync backend interface; bundled implementations: `git`, `local`, `s3` (`aws-sdk-go-v2`), `gcs` (`cloud.google.com/go/storage`).
- `Pin` for moving local assets into the workspace.
- `Diff` against the workspace state; tag-managed files (the `<!-- managed by notebooklm workspace -->` marker).

`internal/app/policy`:

- One central `Enforce(op, args) → Allow | Deny(reason, rule_origin)`.
- Per-user counters (`~/.notebooklm/policy-counters.json`) with atomic write.
- Override mechanism (`--override-policy` with required `--reason`).

CLI: `watch add/list/show/status/pause/resume/remove/log`, `workspace init/status/sync/diff/show/pin`, `policy show/set/unset/explain`.

REST: `/v1/notebooks/{id}/watches`, `/v1/watches/{id}`, `/v1/workspace`, `/v1/policy`, `/v1/policy/check`.

MCP: 8 new tools.

### Tests

- RSS cursor advancement; guid stability.
- fsnotify on POSIX; skip-gated on Windows.
- Git diff against a shallow clone; branch-change resets the cursor.
- Watch pause / resume; idempotency under concurrent cycles.
- Workspace `git` backend: `pull` from a real local clone fixture.
- Workspace `s3` / `gcs` backends: stub the SDK and assert the right calls.
- Policy: every rule is enforced in the right place; one `Enforce` function, no policy logic in adapters.
- Per-user counter atomicity under crash.

### Acceptance

- `notebooklm watch add nb1 --source rss:https://example.com/feed` adds new entries; a second poll with no new entries adds none.
- `notebooklm watch add nb1 --source dir:/tmp/papers --include "*.pdf"` adds new PDFs and refreshes changed ones.
- `notebooklm workspace init` creates the documented layout.
- `notebooklm workspace sync` materializes templates, bundles, pipelines, watches, evals into `~/.notebooklm/`.
- A policy-violating operation is refused before any RPC fires; the error names the rule and origin.
- Per-user counters survive `notebooklm upgrade`.

### Risk

fsnotify behavior varies by platform; the watcher must degrade gracefully on Windows when `ReadDirectoryChangesW` is unavailable. S3 / GCS backends add two new transitive dependencies; the dependency matrix in [docs/02-architecture.md](02-architecture.md) is updated.

---

## Phase 17 — Extensibility (features #9, #10)

**Effort:** 8 days

Spec files: [docs/features/09-repl-completions.md](features/09-repl-completions.md),
[docs/features/10-plugins.md](features/10-plugins.md).

### Deliverables

`internal/app/repl`:

- `bubbletea`-based prompt with `lipgloss` styling; falls back to `chzyer/readline` when stdout is not a TTY.
- Persistent history under `~/.notebooklm/repl_history/<profile>.txt` (atomic-append, 10 000-line rotation).
- Slash commands: `/notebook`, `/clear`, `/history`, `/exit`, `/help`.
- Tab completion via the existing Cobra engines (`GenBashCompletion` / fish / zsh / powershell).

`internal/app/plugin`:

- Manifest parser with golden tests.
- NDJSON contract: `init`, `call`, `call_result`, `call_error`, `event`, cancellation.
- Subprocess driver with env stripping; SIGKILL after 5 s on cancellation.
- Plugin lifecycle: install / enable / disable / uninstall.
- Plugin sandbox: no `NOTEBOOKLM_HOME`, no `NOTEBOOKLM_AUTH_JSON`, no access to `storage_state.json`.
- Routing: plugin-declared CLI commands, MCP tools, pipeline step kinds, watch kinds.

`pkg/plugin` (public, for plugin authors):

- A small Go SDK that hides the NDJSON contract: `plugin.Serve(manifest, handlers)`.

CLI: `repl`, `completion install/uninstall/show/generate`, `plugin list/install/uninstall/enable/disable/info/show/validate`.

REST: `/v1/plugins`, `/v1/plugins/install`, `/v1/plugins/{name}/{enable,disable}`, `DELETE /v1/plugins/{name}`.

MCP: 4 new tools.

### Tests

- REPL: bind notebook; history persistence; slash commands; tab completion (scripted).
- Completion: every existing top-level command and flag is completable for bash / zsh / fish / powershell.
- Plugin: install (local / URL / S3); enable / disable; subprocess contract; isolation; cancellation; env stripping.
- Plugin: declared CLI command routes to the plugin, not the host.
- Plugin: declared MCP tool appears in the tool list only when enabled.
- Plugin: declared pipeline step kind is invocable from a pipeline YAML.
- Plugin: declared watch kind is invocable from `watch add --source <kind>:...`.

### Acceptance

- `notebooklm repl` opens with the documented prompt; history persists across sessions.
- `completion install zsh --path ~/.zsh/completions` writes a valid `_notebooklm`.
- `plugin install --from ./plugin.tar.gz` succeeds for a valid plugin; fails with a specific error for each invalid shape.
- A plugin that panics / deadlocks / prints garbage is killed within 5 s; the host continues.
- A plugin's process has no `NOTEBOOKLM_HOME`, no `NOTEBOOKLM_AUTH_JSON`, no `storage_state.json` access.
- The `pkg/plugin` Go SDK compiles and runs against the documented NDJSON contract.

### Risk

The plugin subprocess driver is the highest-blast-radius new surface. The isolation guarantees are mandatory: env stripping, NDJSON-only access, no host files outside the plugin folder. The bash / zsh / fish / powershell completion scripts must each be tested with the actual shell — virtual-machine CI is required.

---

## Phase 18 — Agent front door (feature #11)

**Effort:** 7 days

Spec files: [docs/features/11-skill.md](features/11-skill.md),
[docs/16-skill-bundle.md](16-skill-bundle.md).

### Deliverables

`internal/app/skill` (extends the Phase 12 scaffold):

- `install` / `uninstall` / `status` / `show` / `package` / `generate` / `check`.
- Target detection (Claude Code, puku-cli, Codex, Cursor, Claude Desktop) per [docs/16-skill-bundle.md](16-skill-bundle.md) § "Target detection".
- Idempotent install; file-managed marker (`<!-- managed by notebooklm-skill-installer -->`).
- Version match check between bundle `VERSION` and CLI version.

`cmd/notebooklm-skill-installer/`:

- Cross-compiled binary for 6 platforms.
- Same target detection; same idempotency; same version check.

`cmd/skill-generate/`:

- Renders `SKILL.md` from `docs/AGENTS.md` + `docs/15-features-overview.md` + `docs/07-cli-spec.md` + `docs/08-mcp-spec.md` + `docs/09-rest-spec.md` + `docs/16-skill-bundle.md`.

`Makefile` targets:

- `skill-generate` — regenerate `docs/skill-generated.md`.
- `skill-check` — assert committed `SKILL.md` equals the generated file.
- `skill-build` — build the 6 platform binaries.
- `skill-package` — assemble `notebooklm-skill.tar.gz`.
- `skill-npm` — assemble the npm package.

npm package `@raihankhan/notebooklm-go-skill`:

- `package.json` with `optionalDependencies` per platform.
- `bin/notebooklm-skill-installer.js` (Node 18+ shim, ~50 lines).

`.mcpb` bundle:

- `manifest.json` per the Claude Desktop bundle format.
- Bundled `run_server.{sh,cmd}` per platform selection.

CLI: full `skill` group, with the new `generate` / `check` subcommands.

REST: `/v1/skill`, `/v1/skill/install`, `/v1/skill/install/preview`, `/v1/skill/uninstall`, `/v1/skill/package`.

MCP: `skill_status`, `skill_install`, `skill_uninstall`.

### Tests

- `DetectTargets` table-driven (each target in / out; env-var override; OS-specific paths).
- `TestPukuSkillsPathDrift` — pins the puku-cli convention; fails fast on drift.
- Install → uninstall round-trip; no managed files left behind.
- Version mismatch refused with exit 2 + actionable message; `--allow-version-mismatch` opts through.
- Idempotent install: second run is a no-op.
- `make skill-generate && make skill-check` exits 0.
- `make skill-build` produces six working binaries under 25 MB each.

### Acceptance

- `npx @raihankhan/notebooklm-go-skill add --target all` exits 0 with the documented `--json` envelope.
- Claude Code, puku-cli, Codex, Cursor, and Claude Desktop each successfully drive at least one read tool and one gated destructive tool after install.
- The committed `SKILL.md` equals the generated file (CI fails on drift).
- `notebooklm upgrade` re-prompts the user to reinstall the skill with the matching new version; mismatch is refused.
- A `tar -xf notebooklm-skill.tar.gz -C ~/.claude/skills/notebooklm/` produces the same on-disk shape as `npx … add`.

### Risk

The puku-cli skill-discovery path may drift between releases. The drift test (`TestPukuSkillsPathDrift`) is mandatory and fails the build on silent convention changes. The npm package layout depends on `optionalDependencies`; the build matrix must be re-verified on every npm change.

---

## Summary

| Phase | Bundles | Days |
|---|---|---|
| 0–13 | Parity (existing) | 57 |
| 14 | #1, #2, #4 — composition | 12 |
| 15 | #5, #6, #8 — data plane | 11 |
| 16 | #3, #7 — governance | 9 |
| 17 | #9, #10 — extensibility | 8 |
| 18 | #11 — agent front door | 7 |
| | **v1.1 + v1.2 total** | **~47 working days** |

Phases 14 and 15 are independent and can run in parallel after Phase 13. Phase 16 depends on 14 (templates live in the workspace); Phase 17 depends on 14 (the plugin SDK composes pipeline steps). Phase 18 is always last.

The full release timeline — parity (Phases 0–13) plus the eleven features (14–18) — is roughly **20–22 focused weeks** for one engineer, or about **5 months** with a small team.
