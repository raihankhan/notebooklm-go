# Sprint 3 — Re-split tickets (single-day chunks)

> Per the Wave 1 carry-over note in
> [`docs/sprint-reports/2026-09-03-heartbeat.md`](2026-09-03-heartbeat.md),
> the 12 mega-tickets in
> [`docs/sprint-reports/S03-tickets.md`](S03-tickets.md) exceeded the
> AGENTIC_LOOP.md §6.3 autonomous inner-loop ceiling (~30 minutes per
> ticket) by ~24x. Each ticket is a 5-6 day effort; parallel agents
> ran for 6+ hours, generated partial code, and timed out without
> reaching `make check` green.

This document re-splits each mega-ticket into **single-day subtickets**
labeled `T-S3-001a`, `T-S3-001b`, … that fit the autonomous inner-loop
envelope. Each subticket is one PR, one worktree, one PR body that
`Refs T-S3-001` (the parent mega-ticket) so the deliverable group stays
visible at the board level.

Total: **~30 tickets** (the 12 mega-tickets become 30 subtickets).
Wave assignments below group the subtickets so each parallel fan-out
remains 3 PRs at a time.

## Subticket numbering convention

`T-S3-{MEGA}-{LOWER}` where:
- `{MEGA}` is the mega-ticket number (`001` through `012`)
- `{LOWER}` is a sequential letter (`a`, `b`, `c`, …) within the mega

The board item titles follow `T-S3-{MEGA}-{LOWER}: <one-line>`.

## Parent issues (unchanged from S03)

| Mega | Parent phase | Issue |
|---|---|---|
| T-S3-001 / 002 / 003 | Phase 4 (ladder completion) | #50 |
| T-S3-004 / 005 / 006 | Phase 6 (sources full) | #51 |
| T-S3-007 / 008 | Phase 7 (chat) | #82 |
| T-S3-009 / 010 | Phase 8 (artifacts) | #83 |
| T-S3-011 | Phase 9 (research/sharing/notes) | #84 |
| T-S3-012 | Phase 11 + 12 (REST + skill) | #85 + #86 |

## Phase 4 ladder — T-S3-001 re-split

### T-S3-001a: L2.0 file-backed reload

- **Files**: `internal/auth/refresh/l2_0.go`, `internal/auth/refresh/l2_0_test.go`
- **AC**: ReloadL2_0 reads storage_state.json under a 3-attempt bounded loop; surfaces the file-touched timestamp; returns typed error on exhaust.
- **Tests**: 3-attempt loop (success-on-attempt-1, success-on-attempt-3, all-exhausted); no-cred log-scan.
- **Depends on**: T-P4-2 (S02 PR #72) L1 step.
- **Effort**: ~1 day.

### T-S3-001b: L2.5 inline command

- **Files**: `internal/auth/refresh/l2_5.go`, `internal/auth/refresh/l2_5_test.go`
- **AC**: ReloadL2_5 reads `NOTEBOOKLM_REFRESH_CMD` env var, runs it under `os/exec`, parses stdout for the refreshed tokens; 2-attempt bounded loop; gated behind `NOTEBOOKLM_REFRESH_CMD_MIDSESSION=1`.
- **Tests**: 2-attempt loop; env-var unset rejection; mid-session gate; command-failure error wrapping; no-cred log-scan.
- **Depends on**: T-S3-001a.
- **Effort**: ~1 day.

### T-S3-001c: L3 headless mint

- **Files**: `internal/auth/refresh/l3.go`, `internal/auth/refresh/l3_test.go`, fake-homepage fixture under `internal/auth/refresh/testdata/homepage.html`
- **AC**: ReloadL3 issues a headless mint against `accounts.google.com` (via the fake homepage in tests), parses the response into Tokens; surfaces typed error on auth redirect.
- **Tests**: fake-homepage mint; redirect rejection; CSRF/SessionID populated; no-cred log-scan.
- **Depends on**: T-S3-001a + T-S3-001b.
- **Effort**: ~1 day.

### T-S3-001d: Ladder wiring + full-sequence test

- **Files**: `internal/auth/refresh/ladder.go` (extend DefaultLadder.Step), `internal/auth/refresh/ladder_test.go` (full sequence)
- **AC**: DefaultLadder.Step dispatches to the new reloaders; ErrLadderLevelNotImplemented returns ONLY from L4 stub; full-sequence test exercises L1 → L2.0 reload → L2.5 command → L3 headless → L4 re-mint.
- **Tests**: 5-step ladder test; all-exhausted final message; sentinel-only-on-L4 assertion.
- **Depends on**: T-S3-001c.
- **Effort**: ~0.5 day.

## Phase 4 cross-cutting — T-S3-002 re-split

### T-S3-002a: singleflight leader/follower coalescing

- **Files**: `internal/auth/singleflight/{singleflight.go,flock.go,followers.go}`, `internal/auth/singleflight/singleflight_test.go`
- **AC**: 100 concurrent refreshes against same canonical path → 1 execution; cancelled follower does NOT cancel leader; per-path epoch fencing.
- **Tests**: 100-goroutine race; cancellation propagation table; epoch-fencing table.
- **Depends on**: T-S3-001d.
- **Effort**: ~1 day.

### T-S3-002b: keepalive RotateCookies + stampede guards

- **Files**: `internal/auth/keepalive/{keepalive.go,rotate.go,psidts.go}`, `internal/auth/keepalive/keepalive_test.go`
- **AC**: RotateCookies runs at most once per 60 s under contention; two-goroutine + two-process race → one POST wins; failed POST still consumes the 60 s slot; PSIDTS recovery rewrites the profile on expiry.
- **Tests**: stampede-guard table; cross-process lock test; PSIDTS recovery table.
- **Depends on**: T-S3-002a.
- **Effort**: ~1 day.

### T-S3-002c: mastertoken D2 exchange + D3 mint

- **Files**: `internal/auth/mastertoken/{mastertoken.go,d2_exchange.go,d3_mint.go}`, `internal/auth/mastertoken/d2_d3_test.go`
- **AC**: D2 exchange against `android.clients.google.com` fakes; D3 mint against `accounts.google.com` fakes; bootstrap coordinator rejects session-before-token ordering.
- **Tests**: D2 success/failure table; D3 success/failure table; bootstrap-ordering sentinel.
- **Depends on**: T-S3-002b.
- **Effort**: ~1 day.

### T-S3-002d: mastertoken D4 OAuthLogin + MergeSession + Rotate

- **Files**: `internal/auth/mastertoken/{d4_oauth.go,bootstrap.go}`, `internal/auth/mastertoken/d4_test.go`
- **AC**: D4 OAuthLogin + MergeSession + Rotate against fakes; account-mismatch guard rejects D4 when bootstrapped account does not match; no-cred log-scan.
- **Tests**: D4 full-path table; account-mismatch table; credential-redaction scan.
- **Depends on**: T-S3-002c.
- **Effort**: ~1 day.

## Phase 4 profile + account — T-S3-003 re-split

### T-S3-003a: ProfileStore CAS + minted-session gate

- **Files**: `internal/auth/profile/{cas.go,merge.go,minted_session.go}`, `internal/auth/profile/cas_test.go`
- **AC**: ProfileStore round-trip; CAS merge preserves merged state; minted-session replacement under same-lock prevents stale-owner eviction.
- **Tests**: write-read-CAS round-trip; minted-session gate table.
- **Depends on**: T-S3-002d (singleflight provides the per-path locking).
- **Effort**: ~1 day.

### T-S3-003b: Account routing + self-heal

- **Files**: `internal/auth/account/{account.go,format.go,probe.go,self_heal.go}`, `internal/auth/account/account_test.go`
- **AC**: FormatAuthuserValue(index) + FormatAuthuserValue(email) canonical; `?authuser=N` probe returns active user; generation-safe CAS self-heal; NOT_FOUND returns hint without refresh.
- **Tests**: format table; probe table; self-heal table; NOT_FOUND-no-refresh table.
- **Depends on**: T-S3-003a.
- **Effort**: ~1 day.

### T-S3-003c: auth policy completion + boundarycheck

- **Files**: `internal/auth/policy/policy.go` (final allowlist), `boundaries.yaml` updates, `internal/auth/policy/policy_test.go`
- **AC**: Every internal/auth import passes `make boundarycheck`; auth policy allowlist finalized.
- **Tests**: full boundarycheck sweep green; policy decision table.
- **Depends on**: T-S3-003b.
- **Effort**: ~0.5 day.

## Phase 6 sources — T-S3-004 re-split

### T-S3-004a: sourceadd classify + gate

- **Files**: `internal/app/sourceadd/{classify.go,gate.go}`, `internal/app/sourceadd/classify_test.go`
- **AC**: Argument classifier routes URL / YouTube / Text / File / Drive share URL; ambiguous cases rejected with VALIDATION_ERROR; symlink gate rejects paths inside `~/.notebooklm/`; internal-address gate rejects `127.0.0.1` / `169.254.169.254`.
- **Tests**: classification table (10+ cases incl. ambiguous); symlink-gate table; internal-address table.
- **Depends on**: S02 SourcesAPI shape.
- **Effort**: ~1 day.

### T-S3-004b: sourceadd mime + URL/YouTube extensions

- **Files**: `internal/app/sourceadd/mime.go`, `notebooklm/sources.go` (AddURL extended, AddYouTube added), `internal/web/params/sources/...` (URL + YouTube param builders)
- **AC**: MIME inference for URL/YouTube; `--mime-type` override wins; AddURL extended with all URL options; AddYouTube accepts YouTube URL/ID.
- **Tests**: MIME table; override-wins; AddURL full-path cassette; AddYouTube cassette.
- **Depends on**: T-S3-004a.
- **Effort**: ~1 day.

### T-S3-004c: Text + File + Drive extensions

- **Files**: `notebooklm/sources.go` (AddText + AddFile + AddDrive), `internal/web/params/sources/...` (Text + File + Drive param builders), cassettes per kind
- **AC**: AddText accepts raw text + optional title; AddFile accepts local path + MIME; AddDrive accepts Drive share URL.
- **Tests**: AddText cassette; AddFile cassette (5 MiB happy path + 400 unsupported ext); AddDrive cassette.
- **Depends on**: T-S3-004b.
- **Effort**: ~1.5 days.

### T-S3-004d: 5 cassettes + TestNoCredentialInCassettes

- **Files**: 5 cassettes under `internal/web/policy/testdata/cassettes/source_add_*.yaml`; `TestNoCredentialInCassettes` green
- **AC**: All 5 kinds have happy-path cassettes; scrubber passes every cassette.
- **Tests**: TestNoCredentialInCassettes table; each cassette replays cleanly.
- **Depends on**: T-S3-004c.
- **Effort**: ~0.5 day.

## Phase 6 upload + Drive — T-S3-005 re-split

### T-S3-005a: upload streaming (Scotty 3-step)

- **Files**: `internal/web/upload/{upload.go,progress.go,semaphore.go}`, `internal/web/upload/upload_test.go`
- **AC**: 3-step Scotty handshake; 64 KiB streaming; progress callback; semaphore admission; happy-path 5 MiB upload.
- **Tests**: 5 MiB happy path; 400 unsupported ext; invalid upload URL rejection.
- **Depends on**: T-S3-004d.
- **Effort**: ~1 day.

### T-S3-005b: upload cancel semantics + Drive axes decoder

- **Files**: `internal/web/upload/cancel.go`, `internal/web/rows/sources.go` (Drive axes), `internal/web/upload/cancel_test.go`
- **AC**: Mid-stream abort sends cancel POST; local teardown does NOT send cancel; Drive axes 4-row table (id+no-health, health+no-id, neither, drive_status:0 → nil).
- **Tests**: cancel table; Drive axes 4-row table.
- **Depends on**: T-S3-005a.
- **Effort**: ~1 day.

### T-S3-005c: CLI label + collection groups

- **Files**: `internal/cli/cmd/label.go`, `internal/cli/cmd/collection.go`, label fieldmask + collection group-slot tests
- **AC**: label fieldmask table (rename / emoji / add / remove-without-add slot 1 = null); collection group-slot membership (add at slot 3, remove at slot 4, group1 empty); `--status preparing` finds orphaned row.
- **Tests**: label fieldmask table; collection group-slot table; orphan finder.
- **Depends on**: T-S3-005b.
- **Effort**: ~1 day.

## Phase 6 probe + CLI source — T-S3-006 re-split

### T-S3-006a: probe-then-create wrapper

- **Files**: `internal/web/sources/{probe.go,create.go}`, `internal/web/sources/probe_test.go`
- **AC**: Probe-then-create 4-outcome matrix (create succeeds; transport fails, probe finds new id; transport fails, probe finds nothing; probe itself fails → UNRESOLVED, no retry).
- **Tests**: probe-then-create 4-outcome table; UNRESOLVED-on-probe-failure path.
- **Depends on**: T-S3-005c.
- **Effort**: ~1 day.

### T-S3-006b: sourcewait + content/fulltext + Markdown renderer

- **Files**: `internal/app/sourcewait/{wait.go,sanity.go}`, `internal/web/sources/{content.go,fulltext.go}`, `internal/app/htmltomd/render.go`
- **AC**: sourcewait completes / fails / timeouts; content reads source content; fulltext returns extracted text; fulltext -f markdown produces valid Markdown.
- **Tests**: wait table; content round-trip; fulltext round-trip; HTML→Markdown renderer table.
- **Depends on**: T-S3-006a.
- **Effort**: ~1 day.

### T-S3-006c: full CLI source group

- **Files**: `internal/cli/cmd/source.go` + child commands (list, get, content, fulltext, add, wait, delete)
- **AC**: Every command replays against cassette; clean removes the right rows; batch add applies each kind in sequence.
- **Tests**: per-command round-trip; clean table; batch table.
- **Depends on**: T-S3-006b.
- **Effort**: ~1 day.

## Phase 7 chat — T-S3-007 re-split

### T-S3-007a: UTF-16 offset table + escape helpers

- **Files**: `internal/web/rows/utf16.go`, `internal/web/wire/escape.go`, `internal/web/rows/utf16_test.go`
- **AC**: utf16Len differs from len and runeCount on emoji/surrogate-pair cases; escape helpers round-trip.
- **Tests**: UTF-16 offset table (ASCII / accents / CJK / emoji / surrogate pairs).
- **Depends on**: Phase 4 ladder for the auth surface.
- **Effort**: ~0.5 day.

### T-S3-007b: chatstream request builder + ReqidCounter

- **Files**: `internal/web/params/chatstream.go`, `internal/web/params/chatstream_test.go`
- **AC**: Streaming request builder produces the canonical wire shape; ReqidCounter increments per request.
- **Tests**: golden-bytes request shape; ReqidCounter increments.
- **Depends on**: T-S3-007a.
- **Effort**: ~0.5 day.

### T-S3-007c: chatstream parser (~1033 lines)

- **Files**: `internal/web/rows/chatstream.go`, `internal/web/rows/chatstream_test.go`
- **AC**: Streaming parser handles plain / cited / multi-chunk / mid-stream error envelope / oversized-prompt rejection.
- **Tests**: streaming parser fixture table (5 outcomes).
- **Depends on**: T-S3-007b.
- **Effort**: ~2 days.

### T-S3-007d: chat history decoder (~1199 lines)

- **Files**: `internal/web/rows/chat.go`, `internal/web/rows/chat_test.go`
- **AC**: History/turn decoder matches Python source shape; UTF-16 offsets used for answer slice.
- **Tests**: history round-trip; per-turn offset table.
- **Depends on**: T-S3-007c.
- **Effort**: ~2 days.

### T-S3-007e: ChatAPI 10 SDK methods + conversation cache

- **Files**: `notebooklm/chat.go`, `notebooklm/chat_test.go`
- **AC**: ChatAPI exposes Ask / GetHistory / GetConversationTurns / GetConversationID / DeleteConversation / Configure / GetSettings / SetMode / SaveAnswerAsNote + conversation cache.
- **Tests**: SDK method stubs; configure merge semantics (preset / partial / bare / preset+custom-rejected); save-as-note golden bytes (render flags = 0 not false); ask --new + ask --json-implies-yes.
- **Depends on**: T-S3-007d.
- **Effort**: ~1 day.

## Phase 7 chat CLI — T-S3-008 re-split

### T-S3-008a: chatnote payload + save-as-note golden bytes

- **Files**: `internal/web/params/chatnote.go`, `internal/web/params/chatnote_test.go`
- **AC**: save-as-note payload uses int 0 for render flags (not bool false); UTF-16 offsets from T-S3-007a.
- **Tests**: golden-bytes test (render flags int 0).
- **Depends on**: T-S3-007e.
- **Effort**: ~0.5 day.

### T-S3-008b: internal/app/studio projection (boundarycheck external)

- **Files**: `internal/app/studio/{projection.go,note.go,artifact.go}`, `boundaries.yaml` (add studio rule), `internal/app/studio/studio_test.go`
- **AC**: Studio projection unifies note + artifact metadata; boundarycheck green for the new external-mode package.
- **Tests**: projection table; boundarycheck sweep.
- **Depends on**: T-S3-008a.
- **Effort**: ~1 day.

### T-S3-008c: CLI ask / suggest-prompts / configure / history

- **Files**: `internal/cli/cmd/{ask,suggest_prompts,configure,history}.go`, leaf tests
- **AC**: ask --json implies --yes; cited answers resolve references; suggest-prompts returns canonical list; configure round-trips; history returns conversation.
- **Tests**: per-command round-trip.
- **Depends on**: T-S3-008b.
- **Effort**: ~1 day.

## Phase 8 artifacts — T-S3-009 re-split

### T-S3-009a: artifact params + 10 generate golden bytes

- **Files**: `internal/web/params/artifacts.go`, `internal/web/params/artifacts_test.go`
- **AC**: 10 Generate* builders + ReviseSlide + RetryArtifact + GenerateMindMap + SuggestReports + shared quizOptionPair; wrong-enum rejection compile error.
- **Tests**: 10 golden-bytes payloads; wrong-enum compile-error doc test.
- **Depends on**: T-S3-008c (studio projection).
- **Effort**: ~1.5 days.

### T-S3-009b: artifact row decoder (~1244 lines)

- **Files**: `internal/web/rows/artifacts.go`, `internal/web/rows/artifacts_test.go`
- **AC**: Decoder matches Python source shape for type + variant, status, media URLs, slides, infographics, quiz options, user state.
- **Tests**: per-kind decoder table; variant table (custom video, cinematic, report --append, report custom, quiz vs flashcards slots, interactive mind map with/without instructions).
- **Depends on**: T-S3-009a.
- **Effort**: ~2 days.

### T-S3-009c: ArtifactsAPI + MindMapsAPI + polling

- **Files**: `notebooklm/artifacts.go`, `notebooklm/mindmaps.go`, `internal/web/artifact/polling.go`, tests
- **AC**: 10 Generate* + 9 Download* + list/get/get-prompt/rename/delete/poll/wait/retry/export/suggestions; eight typed List* helpers; MindMapsAPI unifies both kinds; polling completion/failure/timeout + leader/follower pair sharing one poll loop; artifact delete on mind map prints "Cleared mind map:".
- **Tests**: SDK method stubs; polling table; mind-map delete assertion.
- **Depends on**: T-S3-009b.
- **Effort**: ~2 days.

## Phase 8 download + SSRF — T-S3-010 re-split

### T-S3-010a: SSRF guard (load-bearing security boundary)

- **Files**: `internal/web/assets/ssrf.go`, `internal/web/assets/ssrf_test.go`
- **AC**: SSRF guard table — trusted→trusted redirect passes; non-HTTPS hop rejected; off-allowlist host rejected; `evil%2egoogleapis.com` rejected; credentials stripped after leaving allowlist; `169.254.169.254` rejected.
- **Tests**: 6-outcome SSRF guard table.
- **Depends on**: T-S3-009c.
- **Effort**: ~1 day.

### T-S3-010b: download pipeline + collision handling

- **Files**: `internal/web/assets/{download.go,temp_write.go,wav.go}`, `internal/app/downloadspec/registry.go`, `internal/app/download/{target.go,collision.go,dry_run.go}`, `internal/app/download/download_test.go`
- **AC**: Byte download with per-hop SSRF guard; download collision auto-rename / `--force` / `--no-clobber` (single fails, `--all` skips); every format writes the right extension + MIME; audio lands as `.m4a`; `--dry-run` outputs target plan.
- **Tests**: collision table; format-extension-MIME table; WAV correction; dry-run output.
- **Depends on**: T-S3-010a.
- **Effort**: ~1.5 days.

### T-S3-010c: generate retry + formatters + CLI groups

- **Files**: `internal/app/generate/{plan.go,retry.go}`, `internal/artifact/formatters/{quiz.go,flashcard.go,markdown.go,html.go}`, `boundaries.yaml`, CLI `generate` / `artifact` / `download` groups
- **AC**: generate --plan outputs plan without executing; rate-limit retry with backoff (max 3) succeeds against flaky test server; quiz Markdown + HTML render correctly; internal/artifact/formatters registers boundarycheck rule; CLI groups wire all three.
- **Tests**: retry test; render table; boundarycheck sweep; CLI round-trip.
- **Depends on**: T-S3-010b.
- **Effort**: ~1.5 days.

## Phase 9 — T-S3-011 re-split

### T-S3-011a: research state machine + import idempotency

- **Files**: `notebooklm/research.go` (Start, Poll, WaitForCompletion, Cancel, ImportSources, ImportSourcesWithVerification, ExtractReportURLs, SelectCitedSources), `internal/web/rows/{research.go,research_task.go}`, tests
- **AC**: 8-outcome research state machine; import idempotency table (URL-addressed skip, --allow-duplicate, --cited-only, --max-sources); IMPORT_RESEARCH batch-scaled read timeout survives post-refresh retry.
- **Tests**: state-machine table; import idempotency table.
- **Depends on**: Phase 4 + Phase 6 + Phase 7 + Phase 8.
- **Effort**: ~1.5 days.

### T-S3-011b: sharing + settings + AccountLimits

- **Files**: `notebooklm/{sharing.go,settings.go}`, `internal/web/rows/sharing.go`, `internal/account/limits.go`, AccountLimits test
- **AC**: Share status omits view_level; is_public_sharing_allowed nil vs false; share round-trip public → restricted → user grant → removal; AccountLimits.tier distinguishes quota.
- **Tests**: share round-trip table; AccountLimits table.
- **Depends on**: T-S3-011a.
- **Effort**: ~1 day.

### T-S3-011c: notes + labels + collections + CLI groups

- **Files**: `notebooklm/{notes.go,labels.go,collections.go}`, `internal/web/rows/{notes.go,labels.go,collections.go}`, CLI `note` / `language` groups; CLI `share` group extension
- **AC**: Notes CRUD; labels CRUD; collections CRUD; language set warns that it is global; CLI groups wire all four.
- **Tests**: per-CRUD round-trip; language warning assertion.
- **Depends on**: T-S3-011b.
- **Effort**: ~1.5 days.

## Phase 11 + 12 (REST + skill) — T-S3-012 re-split

### T-S3-012a: REST server skeleton + auth + limiters

- **Files**: `cmd/notebooklm-server/main.go`, `internal/restsrv/{server.go,auth.go,limiters.go}`, route table test
- **AC**: Server boots; bearer auth (missing/wrong/right); non-loopback Host rejected; 6 semaphore.Weighted limiters wired from env; route table test asserts 43 paths/methods/statuses.
- **Tests**: auth matrix; limiters table; route table.
- **Depends on**: every other Phase lands first.
- **Effort**: ~1.5 days.

### T-S3-012b: REST upload + pending lifecycle + shutdown

- **Files**: `internal/restsrv/{pending.go,upload.go,shutdown.go}`, multipart tests
- **AC**: N+1 generations → last queues, /healthz answers immediately; pending lifecycle 202 → pending → 200 → 410; upload over limit → 413; graceful shutdown with bounded drain.
- **Tests**: pending lifecycle table; upload-over-limit; graceful shutdown.
- **Depends on**: T-S3-012a.
- **Effort**: ~1 day.

### T-S3-012c: skill installer + REST skill routes + MCP tools + CLI group

- **Files**: `cmd/notebooklm-skill-installer/main.go`, `internal/app/skill/{detect.go,install.go,marker.go,package.go}`, SKILL.md (go:embed), REST routes /v1/skill{,/install,/install/preview,/uninstall,/package}, MCP tools skill_status / skill_install / skill_uninstall, CLI skill group
- **AC**: Idempotent install for every target (Claude Code / puku-cli / Codex / Cursor / Claude Desktop); file-managed marker survives re-install; uninstall clean; go:embed SKILL.md matches committed file; REST skill routes return canonical envelopes; MCP tools work against fake client; /docs + /openapi.json → 404.
- **Tests**: idempotent install per target; marker survival; uninstall cleanliness; SKILL.md equality; REST skill route table; MCP tool smoke; /docs 404.
- **Depends on**: T-S3-012b.
- **Effort**: ~1.5 days.

## Wave assignments (after re-split)

Each wave now runs 6 PRs in parallel (subticket-level parallelism), where the parent mega-tickets become the wave groupings.

| Wave | Mega-tickets | Subticket count | Notes |
|---|---|---|---|
| W1 | T-S3-001 (a–d) + T-S3-004 (a–d) + T-S3-007 (a–e) | 13 subtickets in 3 waves of 4-5 | Largest wave — split into W1a (a/b only), W1b (c/d only), W1c (007 a/b/c) |
| W2 | T-S3-002 + T-S3-005 + T-S3-008 | 10 subtickets | 007 d/e land between W1 and W2 |
| W3 | T-S3-003 + T-S3-006 + T-S3-009 | 10 subtickets | |
| W4 | T-S3-010 + T-S3-011 + T-S3-012 | 10 subtickets | |

### Recommended sub-waves

- **W1a** (4 parallel): T-S3-001a, T-S3-001b, T-S3-004a, T-S3-007a
- **W1b** (4 parallel): T-S3-001c, T-S3-001d, T-S3-004b, T-S3-007b
- **W1c** (4 parallel): T-S3-004c, T-S3-004d, T-S3-007c, T-S3-007d
- **W1d** (1): T-S3-007e (depends on 007d)
- **W2a** (3 parallel): T-S3-002a, T-S3-005a, T-S3-008a
- **W2b** (3 parallel): T-S3-002b, T-S3-005b, T-S3-008b
- **W2c** (3 parallel): T-S3-002c, T-S3-005c, T-S3-008c
- **W2d** (1): T-S3-002d (depends on 002c)
- **W3a** (3 parallel): T-S3-003a, T-S3-006a, T-S3-009a
- **W3b** (3 parallel): T-S3-003b, T-S3-006b, T-S3-009b
- **W3c** (3 parallel): T-S3-003c, T-S3-006c, T-S3-009c
- **W4a** (3 parallel): T-S3-010a, T-S3-011a, T-S3-012a
- **W4b** (3 parallel): T-S3-010b, T-S3-011b, T-S3-012b
- **W4c** (3 parallel): T-S3-010c, T-S3-011c, T-S3-012c

13 sub-waves total. Each sub-wave is 1-4 parallel PRs, each PR within the 30-minute autonomous ceiling (per AGENTIC_LOOP.md §6.3).

## PR template for subtickets

Every subticket PR body uses:

```
Refs T-S3-{MEGA} (Closes #{phase-issue})

- [ ] <AC1>
- [ ] <AC2>
- [ ] ...
- [ ] make check is green
- [ ] intermediate commits: each subticket commit prefix is `WIP:` if not yet green
```

Co-Authored-By footer preserved.

## Files modified by this re-split

- **Created**: `docs/sprint-reports/S03-split-tickets.md` (this file).
- **No changes** to `docs/sprint-reports/S03-tickets.md` — the mega-tickets remain as the high-level contract.
- **No changes** to the S3 project board (#4) — re-filing subtickets is the next-session's job; the current 12 mega-tickets stay on the board as parent items.

## Carry-over for the next session

1. File 30 subtickets on the S3 board (project #4) under their parent mega-tickets.
2. Mark Wave 1 mega-tickets as "parent of T-S3-001a..d" etc. for board-level visibility.
3. Dispatch W1a (4 parallel PRs) — T-S3-001a, T-S3-001b, T-S3-004a, T-S3-007a.
4. After W1a merges, dispatch W1b (4 parallel PRs).
5. Continue through W4c, then live smoke + S3 retro.