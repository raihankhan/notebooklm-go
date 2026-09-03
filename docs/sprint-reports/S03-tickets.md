# Sprint 3 — Ticket Breakdown (Consolidated: Phases 4-completion + 6 + 7 + 8 + 9 + 11 + 12-skill-subset)

> Sprint 3 collapses the canonical "Adapters II" (Phase 7–9) + "Polish"
> (Phase 10–13) sprints from `docs/AGENTIC_LOOP.md` into one mega-sprint
> per the user's directive ("merge Sprint 3 and Sprint 4 into one sprint,
> bigger tickets, wrap it up faster"). Twelve tickets, ~32 days compressed
> into one execution cycle. Per AGENTIC_LOOP.md §2, the Scrum-master must
> log mid-sprint carry-over notes if any single ticket exceeds its stated
> effort by more than 20%.

## Sprint goal

Ship **Phase 4 (full ladder)**, **Phase 6 (full sources namespace)**,
**Phase 7 (chat streaming + history)**, **Phase 8 (artifacts generate /
poll / download)**, **Phase 9 (research / sharing / notes)**,
**Phase 11 (REST server)**, and the **skill subset of Phase 12** in one
loop cycle. At end of S3 the project is feature-complete against
`notebooklm-py` plus a REST + skill surface — only Phase 10 (MCP), the
rest of Phase 12 (parity audit / fuzz / docs), Phase 13 (Python↔Go
storage cross-compat), and Phases 14–18 (enterprise) remain.

## Phase parent issues

| Phase | Issue | Status going in |
|---|---|---|
| Phase 4 | #50 | Open — scope expanded to "full ladder" |
| Phase 6 | #51 | Open — scope expanded to "full namespace" |
| Phase 7 | #82 | Filed |
| Phase 8 | #83 | Filed |
| Phase 9 | #84 | Filed |
| Phase 11 | #85 | Filed |
| Phase 12 (subset) | #86 | Filed |

## Sprint board

Project: **Sprint 3 — Consolidated Adapters + Ladder Completion** (project #4)

## Tickets

### T-S3-001: refresh ladder L2.0 + L2.5 + L3

- **Parent phase issue:** #50 (Phase 4)
- **One-line description:** Extend `internal/auth/refresh` with L2.0 (file-backed reload, 3 attempts), L2.5 (inline command, 2 attempts), and L3 (headless mint). Removes the L4-stub sentinel error path so the full ladder works against fakes.
- **Files touched:** `internal/auth/refresh/l2_0.go`, `l2_5.go`, `l3.go`, `internal/auth/refresh/refresh_test.go`, fake-homepage fixture.
- **Acceptance criteria:**
  - [ ] L2.0 attempts the file-backed reload up to 3 times before exhausting.
  - [ ] L2.5 attempts the inline command up to 2 times before exhausting.
  - [ ] L3 performs a headless mint against `accounts.google.com` and returns fresh tokens.
  - [ ] `ErrLadderLevelNotImplemented` is returned only by the L4 stub.
  - [ ] Fake-homepage test exercises every step in order: L1 → L2.0 reload → L2.5 command → L3 headless → L4 re-mint.
  - [ ] All-exhausted path returns the canonical final message.
  - [ ] No credential-shaped substring in any log line, error, or wrapped cause (regression-tested).
- **Tests required:** unit tests per step + end-to-end ladder test + no-cred log-scan assertion.
- **Risk:** high (live network dependency in fixtures; L2.5 calls an inline shell command).
- **Dependencies:** T-P4-2 (S02 PR #72) provides L1 + the `RefreshFunc` interface.
- **Suggested commit message:** `auth: complete refresh ladder L2.0 + L2.5 + L3 (#50)`
- **Suggested PR title:** `auth: complete refresh ladder L2.0 + L2.5 + L3`

### T-S3-002: singleflight + keepalive + mastertoken (the auth-runtime trio)

- **Parent phase issue:** #50 (Phase 4)
- **One-line description:** Land three cross-cutting auth-runtime packages: singleflight (leader/follower coalescing + flock), keepalive (`RotateCookies` + stampede guards + bg goroutine + PSIDTS recovery), and mastertoken (D2-D4 + bootstrap lock + account-mismatch guard).
- **Files touched:** `internal/auth/singleflight/{singleflight.go,flock.go,followers.go}`, `internal/auth/keepalive/{keepalive.go,rotate.go,psidts.go}`, `internal/auth/mastertoken/{mastertoken.go,d2_exchange.go,d3_mint.go,d4_oauth.go,bootstrap.go}`, tests + fixtures.
- **Acceptance criteria:**
  - [ ] 100 concurrent refreshes against the same canonical path → 1 execution; the other 99 followers return the leader's result.
  - [ ] A cancelled follower does not cancel the leader.
  - [ ] `RotateCookies` runs at most once per 60 s under contention (stampede guard).
  - [ ] Two goroutines and two processes racing on the keepalive POST → one POST wins.
  - [ ] A failed keepalive POST still consumes the 60 s slot.
  - [ ] `inline PSIDTS recovery` rewrites a profile whose PSIDTS has expired.
  - [ ] D2 exchange, D3 mint, D4 OAuthLogin + MergeSession + Rotate all work against fakes for `android.clients.google.com` and `accounts.google.com`.
  - [ ] The bootstrap coordinator rejects session-before-token ordering with a typed sentinel.
  - [ ] Account-mismatch guard rejects D4 when the bootstrapped account does not match.
  - [ ] No credential in any error, log, or wrapped cause.
- **Tests required:** singleflight table (4 outcomes); keepalive table; mastertoken D2-D4 fixtures; cross-package integration test.
- **Risk:** high (concurrency + cross-process `flock` + credential safety).
- **Dependencies:** T-S3-001 provides the L2.0/L2.5/L3 ladder surface.
- **Suggested commit message:** `auth: add singleflight + keepalive + mastertoken (#50)`
- **Suggested PR title:** `auth: add singleflight + keepalive + mastertoken`

### T-S3-003: profile CAS + account routing + auth policy completion

- **Parent phase issue:** #50 (Phase 4)
- **One-line description:** Extend `internal/auth/profile` (S02 read path) with CAS cookie merge + minted-session same-lock latest-owner gate, and add `internal/auth/account` for the authuser index/email routing + generation-safe self-heal.
- **Files touched:** `internal/auth/profile/{cas.go,merge.go,minted_session.go}`, `internal/auth/account/{account.go,format.go,probe.go,self_heal.go}`, tests + fixtures.
- **Acceptance criteria:**
  - [ ] ProfileStore round-trip: write → read → CAS merge → re-read preserves the merged state.
  - [ ] Minted-session replacement under same-lock prevents stale-owner eviction.
  - [ ] `FormatAuthuserValue(index)` and `FormatAuthuserValue(email)` produce the canonical strings.
  - [ ] `?authuser=N` probe returns the active user.
  - [ ] Generation-safe self-heal: a stale CAS document self-heals on next read without throwing.
  - [ ] `NOT_FOUND` returns the typed hint and does NOT trigger a refresh.
  - [ ] Auth policy allowlist (`internal/auth/policy`) finalizes — every internal/auth import passes `boundarycheck`.
  - [ ] No credential-shaped substring in any log line, error, or wrapped cause.
- **Tests required:** CAS round-trip; minted-session gate; index vs email routing; generation-self-heal; auth policy table.
- **Risk:** medium (no live network).
- **Dependencies:** T-S3-001 + T-S3-002 provide the L2-L4 ladder + singleflight.
- **Suggested commit message:** `auth: add profile CAS + account routing + auth policy completion (#50)`
- **Suggested PR title:** `auth: add profile CAS + account routing + auth policy completion`

### T-S3-004: source kinds + add pipeline (URL/YouTube/Text/File/Drive)

- **Parent phase issue:** #51 (Phase 6)
- **One-line description:** Extend `notebooklm/sources.go` (S02 minimal List+AddURL) with the remaining 16 source methods, and land `internal/app/sourceadd` (argument classification + symlink gate + internal-address gate + MIME inference).
- **Files touched:** `notebooklm/sources.go` (extended), `internal/app/sourceadd/{classify.go,gate.go,mime.go}`, fixtures per source kind.
- **Acceptance criteria:**
  - [ ] All five source kinds add against cassettes (URL, YouTube, Text, File, Drive share URL).
  - [ ] Ambiguous argument cases (e.g. a string that's both a YouTube URL and a regular URL) are rejected with `VALIDATION_ERROR`.
  - [ ] Symlink gate: a path inside `~/.notebooklm/` is rejected unless `--allow-symlink` is passed.
  - [ ] Internal-address gate: a `127.0.0.1` or `169.254.169.254` URL is rejected.
  - [ ] MIME inference picks the right MIME for each kind; `--mime-type` override wins.
  - [ ] Each of the 5 kinds has a happy-path cassette in `internal/web/policy/testdata/cassettes/`.
  - [ ] `TestNoCredentialInCassettes` passes after every cassette is added.
- **Tests required:** classification table (including ambiguous cases); symlink / internal-address gates; MIME inference; cassette per kind.
- **Risk:** high (5 source kinds × upload paths × argument ambiguity).
- **Dependencies:** `notebooklm/sources.go` (S02 PR #80) + the S02 SourcesAPI shape.
- **Suggested commit message:** `sources: extend to all five kinds + sourceadd classification pipeline (#51)`
- **Suggested PR title:** `sources: extend to all five kinds + sourceadd classification pipeline`

### T-S3-005: upload streaming + Drive axes + label/collection groups

- **Parent phase issue:** #51 (Phase 6)
- **One-line description:** Land `internal/web/upload` (3-step Scotty, 64 KiB streaming, progress, semaphore, cancel-mid-stream), the Drive axes row decoder (`internal/web/rows/sources.go`), and the CLI `label` + `collection` groups.
- **Files touched:** `internal/web/upload/{upload.go,cancel.go,progress.go,semaphore.go}`, `internal/web/rows/sources.go`, `internal/cli/cmd/label.go`, `internal/cli/cmd/collection.go`, tests + fixtures.
- **Acceptance criteria:**
  - [ ] Happy-path upload streams a 5 MiB file with progress callbacks.
  - [ ] 400 on an unsupported extension (`-race` testing).
  - [ ] An invalid upload URL in the response header is rejected with `INVALID_REQUEST`.
  - [ ] Mid-stream abort sends a cancel POST.
  - [ ] Local teardown (cancel handler exit) does NOT send a cancel POST.
  - [ ] Drive axes 4-row table: id + no health, health + no id, neither, `drive_status: 0` normalized to `nil`.
  - [ ] Label fieldmask: rename / emoji / add / remove-without-add (slot 1 must be `null`).
  - [ ] Collection membership: add at group slot 3, remove at group slot 4, `group1` empty.
  - [ ] `--status preparing` finds an orphaned row.
- **Tests required:** upload table (4 outcomes); cancel semantics; Drive axes 4-row table; label fieldmask table; collection group-slot table.
- **Risk:** high (network I/O + cancel semantics).
- **Dependencies:** T-S3-004 provides the extended source kinds.
- **Suggested commit message:** `sources: add upload streaming + Drive axes + label/collection CLI groups (#51)`
- **Suggested PR title:** `sources: add upload streaming + Drive axes + label/collection CLI groups`

### T-S3-006: probe-then-create + content/fulltext + CLI source group

- **Parent phase issue:** #51 (Phase 6)
- **One-line description:** Land `internal/web/sources` (probe-then-create wrapper + listing + content/fulltext + batch + clean), `internal/app/sourcewait`, an HTML→Markdown renderer, and the full CLI `source` group.
- **Files touched:** `internal/web/sources/{probe.go,list.go,content.go,fulltext.go,batch.go,clean.go}`, `internal/app/sourcewait/{wait.go,sanity.go}`, `internal/app/htmltomd/render.go`, `internal/cli/cmd/source.go` + child commands.
- **Acceptance criteria:**
  - [ ] Probe-then-create matrix (4 outcomes): create succeeds; transport fails, probe finds new id; transport fails, probe finds nothing; **probe itself fails → `UNRESOLVED` raised, no retry**.
  - [ ] Listing with `--status preparing` finds an orphaned row.
  - [ ] Listing with `--label` filters correctly.
  - [ ] `content` reads the source content; `fulltext` returns extracted text.
  - [ ] `fulltext -f markdown` produces valid Markdown (HTML→Markdown renderer).
  - [ ] Batch add applies each source kind in sequence.
  - [ ] `clean` removes the right rows.
  - [ ] `sourcewait` orchestrator waits for completion / failure / timeout with the canonical content-sanity warning patterns.
- **Tests required:** probe-then-create table (4 outcomes); listing filters; content/fulltext round-trip; HTML→Markdown table; batch; clean.
- **Risk:** medium.
- **Dependencies:** T-S3-005 provides upload + Drive + label/collection.
- **Suggested commit message:** `sources: add probe-then-create + content/fulltext + CLI source group (#51)`
- **Suggested PR title:** `sources: add probe-then-create + content/fulltext + CLI source group`

### T-S3-007: chat streaming parser + history decoder (the 2k-line Python port)

- **Parent phase issue:** #82 (Phase 7)
- **One-line description:** Port the 1033-line Python streaming parser + the 1199-line history decoder to Go, and add `notebooklm/chat.go` (Ask, GetHistory, GetConversationTurns, GetConversationID, DeleteConversation, Configure, GetSettings, SetMode, SaveAnswerAsNote, conversation cache).
- **Files touched:** `internal/web/params/chatstream.go`, `internal/web/rows/chatstream.go`, `internal/web/rows/chat.go`, `notebooklm/chat.go`, tests + UTF-16 offset table test + streaming fixtures.
- **Acceptance criteria:**
  - [ ] Streaming parser fixture table: plain · cited · multi-chunk · mid-stream error envelope · oversized-prompt rejection.
  - [ ] UTF-16 offset table (ASCII / accents / CJK / emoji / surrogate pairs); `utf16Len` differs from `len` and `runeCount` on emoji cases.
  - [ ] Save-as-note golden bytes: render flags are `0` (int), not `false` (bool).
  - [ ] Configure merge semantics: preset · partial custom preserves omitted · bare call resets · preset + custom rejected.
  - [ ] `notebooklm/chat.go` exposes all 10 SDK methods + the conversation cache.
  - [ ] `Ask` returns a cited answer; `GetConversationID` returns the conversation's id.
  - [ ] `ask --new` deletes then starts fresh; `ask --json` implies `--yes`.
  - [ ] Conversation-id recovery on a fresh conversation.
- **Tests required:** streaming parser table; UTF-16 offset table; golden-bytes save-as-note; configure merge table; SDK method stubs.
- **Risk:** high (1033 + 1199 lines of ported Python logic).
- **Dependencies:** Phase 4 ladder (S02 + T-S3-001..003) provides the auth surface.
- **Suggested commit message:** `chat: port streaming parser + history decoder + ChatAPI (#82)`
- **Suggested PR title:** `chat: port streaming parser + history decoder + ChatAPI`

### T-S3-008: chatnote + studio projection + chat CLI group

- **Parent phase issue:** #82 (Phase 7)
- **One-line description:** Land `internal/web/params/chatnote.go`, `internal/app/studio` (unified note+artifact projection; new boundarycheck `mode=external` package), and the CLI `ask` / `suggest-prompts` / `configure` / `history` commands.
- **Files touched:** `internal/web/params/chatnote.go`, `internal/app/studio/{projection.go,note.go,artifact.go}`, `internal/cli/cmd/{ask,suggest_prompts,configure,history}.go`, `boundaries.yaml` (add studio rule), tests.
- **Acceptance criteria:**
  - [ ] `ask --json` implies `--yes`.
  - [ ] Cited answers resolve references to real source IDs.
  - [ ] Save-as-note works end-to-end against a cassette.
  - [ ] `suggest-prompts` returns the canonical prompt list.
  - [ ] `configure` round-trips a config (preset / partial / bare / preset+custom-rejected).
  - [ ] `history` returns the conversation history.
  - [ ] `internal/app/studio` registers a boundarycheck rule; `make boundarycheck` is green.
  - [ ] The studio projection unifies note + artifact metadata.
- **Tests required:** ask/suggest/configure/history round-trips; studio projection table; boundarycheck green.
- **Risk:** medium.
- **Dependencies:** T-S3-007 provides the streaming parser + history decoder + ChatAPI.
- **Suggested commit message:** `chat: add chatnote + studio projection + chat CLI group (#82)`
- **Suggested PR title:** `chat: add chatnote + studio projection + chat CLI group`

### T-S3-009: artifacts generate + rows + polling (the 1244-line decoder port)

- **Parent phase issue:** #83 (Phase 8)
- **One-line description:** Port the 1244-line Python artifact row decoder, add `notebooklm/artifacts.go` (10 Generate*, 9 Download*, list/get/get-prompt/rename/delete/poll/wait/retry/export/suggestions, eight typed List* helpers) + `notebooklm/mindmaps.go`, and land `internal/web/artifact/polling.go`.
- **Files touched:** `internal/web/params/artifacts.go`, `internal/web/rows/artifacts.go`, `internal/web/artifact/polling.go`, `notebooklm/artifacts.go`, `notebooklm/mindmaps.go`, tests + 10 generate golden-bytes fixtures.
- **Acceptance criteria:**
  - [ ] Golden bytes for all ten generate payloads.
  - [ ] Variants: custom video (null style code + slot-6 prompt); cinematic (5-element config); report with `--append`; report `custom`; quiz vs flashcards option-pair slots; interactive mind map with and without instructions.
  - [ ] Wrong-enum rejection (e.g. `QuizDifficulty` where `QuizQuantity` belongs) is a compile error — asserted by a `go vet`-style negative example in a doc test.
  - [ ] Polling: completion · failure · timeout · a leader/follower pair sharing one poll loop.
  - [ ] `artifact delete` on a mind map prints `Cleared mind map:`.
  - [ ] Eight typed `List*` helpers each return their kind-specific slice.
  - [ ] `MindMapsAPI` unifies both mind-map kinds under one surface.
- **Tests required:** golden-bytes table (10 payloads); variants table; wrong-enum compile-error test; polling table; mind-map delete assertion.
- **Risk:** high (1244-line decoder + 10 golden fixtures).
- **Dependencies:** T-S3-007 + T-S3-008 provide the chat + studio surface.
- **Suggested commit message:** `artifacts: add generate + rows + polling + ArtifactsAPI + MindMapsAPI (#83)`
- **Suggested PR title:** `artifacts: add generate + rows + polling + ArtifactsAPI + MindMapsAPI`

### T-S3-010: download pipeline + SSRF guard + generate/download CLI groups

- **Parent phase issue:** #83 (Phase 8)
- **One-line description:** Land `internal/web/assets/` (byte download with per-hop SSRF guard), `internal/app/downloadspec`, `internal/app/download`, `internal/app/generate`, `internal/artifact/formatters` (boundarycheck `mode=external`), and the CLI `generate` / `artifact` / `download` groups.
- **Files touched:** `internal/web/assets/{download.go,ssrf.go,temp_write.go,wav.go}`, `internal/app/downloadspec/registry.go`, `internal/app/download/{target.go,collision.go,dry_run.go}`, `internal/app/generate/{plan.go,retry.go}`, `internal/artifact/formatters/{quiz.go,flashcard.go,markdown.go,html.go}`, CLI groups, `boundaries.yaml`.
- **Acceptance criteria:**
  - [ ] **SSRF guard table**: trusted→trusted redirect passes · non-HTTPS hop rejected · off-allowlist host rejected · `evil%2egoogleapis.com` rejected · credentials stripped after leaving allowlist · `169.254.169.254` rejected.
  - [ ] Download collision: auto-rename · `--force` · `--no-clobber` (single fails, `--all` skips).
  - [ ] Every download format writes the right extension and MIME.
  - [ ] Audio lands as `.m4a`.
  - [ ] Quiz Markdown + HTML render correctly.
  - [ ] `generate --plan` outputs the artifact plan without executing.
  - [ ] Rate-limit retry with backoff (max 3 retries) succeeds against a flaky test server.
  - [ ] `internal/artifact/formatters` registers a boundarycheck rule; `make boundarycheck` green.
- **Tests required:** SSRF guard table; download collision table; format-extension-MIME table; quiz/flashcard render table; rate-limit retry test.
- **Risk:** high (SSRF guard is the load-bearing security boundary).
- **Dependencies:** T-S3-009 provides the ArtifactsAPI + MindMapsAPI + polling.
- **Suggested commit message:** `artifacts: add download pipeline + SSRF guard + generate/download CLI groups (#83)`
- **Suggested PR title:** `artifacts: add download pipeline + SSRF guard + generate/download CLI groups`

### T-S3-011: research + sharing + settings + notes + labels + collections + CLI groups

- **Parent phase issue:** #84 (Phase 9)
- **One-line description:** Land `notebooklm/research.go` (8 methods), `notebooklm/sharing.go` + `settings.go` + `notes.go` + `labels.go` + `collections.go`, the five row decoders under `internal/web/rows/`, `AccountLimits` with `tier`, and the CLI `research` / `share` / `note` / `language` groups.
- **Files touched:** `notebooklm/{research,sharing,settings,notes,labels,collections}.go`, `internal/web/rows/{research,research_task,sharing,notes,labels,collections}.go`, `internal/account/limits.go`, CLI groups.
- **Acceptance criteria:**
  - [ ] Research state machine (8 outcomes): fast + deep · in-progress · completed · `no_results` · `cancelled` · `unknown` · **the both-null no-backend-code case**.
  - [ ] Import idempotency: URL-addressed entries skip as `already_present`; `--allow-duplicate` re-adds; `--cited-only` narrows; `--max-sources` caps.
  - [ ] `IMPORT_RESEARCH`'s batch-scaled read timeout survives a post-refresh retry.
  - [ ] Share status omits `view_level`; `is_public_sharing_allowed` is `nil` vs `false`.
  - [ ] Share round-trip: public → restricted → user grant → removal.
  - [ ] Language set warns that it is global.
  - [ ] `AccountLimits.tier` distinguishes the available quota.
  - [ ] All 5 row decoders match their Python-source shape.
- **Tests required:** research state-machine table (8 outcomes); import idempotency table; share round-trip; language warning; `AccountLimits` table.
- **Risk:** medium.
- **Dependencies:** Phase 4 ladder + Phase 6 sources + Phase 7 chat + Phase 8 artifacts all provide shared primitives.
- **Suggested commit message:** `phase-9: add research + sharing + settings + notes + labels + collections + CLI (#84)`
- **Suggested PR title:** `phase-9: add research + sharing + settings + notes + labels + collections + CLI`

### T-S3-012: REST server + skill installer (Phase 11 + Phase 12 subset)

- **Parent phase issue:** #85 (Phase 11) + #86 (Phase 12 subset)
- **One-line description:** Land `cmd/notebooklm-server` + `internal/restsrv` (43 routes), `cmd/notebooklm-skill-installer` + `internal/app/skill` (target detection + idempotent install + go:embed SKILL.md), the 5 REST skill routes, the 3 MCP skill tools, and the CLI `skill` group.
- **Files touched:** `cmd/notebooklm-server/main.go`, `internal/restsrv/{server.go,auth.go,limiters.go,pending.go,upload.go,shutdown.go,routes.go}`, `cmd/notebooklm-skill-installer/main.go`, `internal/app/skill/{detect.go,install.go,marker.go,package.go}`, `SKILL.md` (go:embed), CLI group, `boundaries.yaml`.
- **Acceptance criteria:**
  - [ ] Route table test asserts every path/method/status against a committed list (43 routes).
  - [ ] Auth: bearer missing → 401, wrong bearer → 401, right bearer → 200; non-loopback `Host` rejected.
  - [ ] N+1 concurrent generations → last one queues, `/healthz` still answers immediately.
  - [ ] Pending lifecycle: 202 → pending → 200 → 410.
  - [ ] Upload over the size limit → 413.
  - [ ] `/docs` and `/openapi.json` → 404 (no schema surface).
  - [ ] Six `semaphore.Weighted` limiters wired from env.
  - [ ] Graceful shutdown with bounded drain.
  - [ ] Skill install is idempotent for every target (Claude Code / puku-cli / Codex / Cursor / Claude Desktop).
  - [ ] File-managed marker `<!-- managed by notebooklm-skill-installer -->` survives re-install.
  - [ ] Uninstall leaves no managed marker behind.
  - [ ] `go:embed`ed `SKILL.md` matches the committed file.
  - [ ] REST routes `/v1/skill{,/install,/install/preview,/uninstall,/package}` all return the canonical envelopes.
  - [ ] MCP tools `skill_status`, `skill_install`, `skill_uninstall` work against a fake client.
- **Tests required:** route table; auth matrix; limiters table; pending lifecycle; upload-over-limit; `/docs`/`/openapi.json` 404; idempotent install per target; marker survival; uninstall cleanliness.
- **Risk:** medium (no live network; just contract tests).
- **Dependencies:** every other Phase lands first — chat / sources / artifacts all need to be wired through the SDK before REST routes can call them.
- **Suggested commit message:** `rest+skill: add notebooklm-server (43 routes) + skill installer (#85 #86)`
- **Suggested PR title:** `rest+skill: add notebooklm-server (43 routes) + skill installer`

## Wave assignments (parallel fan-out)

- **Wave 1**: T-S3-001, T-S3-004, T-S3-007 (3 PRs in parallel)
- **Wave 2**: T-S3-002, T-S3-005, T-S3-008 (3 PRs in parallel — depend on Wave 1's seams)
- **Wave 3**: T-S3-003, T-S3-006, T-S3-009 (3 PRs in parallel — depend on Wave 2)
- **Wave 4**: T-S3-010, T-S3-011, T-S3-012 (3 PRs in parallel — depend on Wave 3)

Each wave's PRs are squash-merged in dependency order; the next wave
doesn't start until the previous wave's PRs are green on master.
