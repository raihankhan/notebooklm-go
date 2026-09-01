# Sprint 1 Report — Foundation (Phases 0–3 wiring + Phase 0 close)

**Status:** Phase 0 complete (4/4 tickets closed and merged).
**Date:** 2026-09-01
**Author:** Scrum-master (this session)

## What landed in Sprint 1 — Phase 0 only

Phase 0 is the foundation sprint: four packages, four tickets, all on master.

| # | Issue | Branch | PR | Master commit | Title |
|---|---|---|---|---|---|
| T-P0-1 | #9  | ticket/t-p0-1            | #26 (squashed at `20e8311`) | `20e8311` | scaffold: bootstrap module, Makefile, CI matrix, and worktree gitignore |
| T-P0-2 | #12 | ticket/t-p0-2-r2         | #33 | `bdbe447` | logging: add internal/buildinfo and stderr-only slog handler with redaction hook |
| T-P0-3 | #13 | ticket/t-p0-3            | #36 | `905a1ba` | wire: port credential redaction regexes from notebooklm-py/_logging |
| T-P0-4 | #14 | ticket/t-p0-4-r3         | #35 | `4a0fcc5` | tools: add internal/tools/boundarycheck with declarative rules + planted-failure test |

Plus one CI-infrastructure fixup that landed first because Phase 0 couldn't
ship without it:

| Scope | PR | Master commit | Title |
|---|---|---|---|
| CI   | #32 | `053bada` | ci: bump action to v8, pin golangci-lint to v2.5.0, migrate config to v2 schema |

Master log at end of Phase 0:

```
905a1ba wire: port credential redaction regexes from notebooklm-py/_logging (#13) (#36)
4a0fcc5 tools: add internal/tools/boundarycheck with declarative rules + planted-failure test (#14) (#35)
bdbe447 logging: add internal/buildinfo and stderr-only slog handler with redaction hook (#33)
053bada ci: bump action to v8, pin golangci-lint to v2.5.0, migrate config to v2 schema (#32)
20e8311 scaffold: bootstrap module, Makefile, CI matrix, and worktree gitignore (#26)
```

Packages added to the module:

- `internal/buildinfo` — Version/Commit/Date via `-ldflags`, exercised by `make build` and `strings bin/notebooklm`.
- `internal/logging` — stderr-only `slog` handler, level from `NOTEBOOKLM_LOG_LEVEL`/`-v`, request-id propagation, redaction hook now wired to the real `internal/redact.Apply`.
- `internal/redact` — four regex families + four URL/header redactors, 100% line coverage on the unit suite.
- `internal/tools/boundarycheck` — declarative import-graph linter reading `boundaries.yaml`, with a planted-failure test that proves a bad import is rejected.

## Acceptance check

Phase 0 acceptance per `docs/10-implementation-plan.md`:

- [x] `go.mod` declares `module github.com/raihankhan/notebooklm-go` with `go 1.25`.
- [x] `Makefile` exposes `build`, `check`, `fmt`, `vet`, `lint`, `test`, `test-e2e`, `cover`, `boundarycheck`, `clean`, `release`, `help`. `make test` runs `go test -race ./...`. `make check` is the umbrella CI target.
- [x] `.golangci.yml` enables 11 linters in v2 schema (errcheck, govet, staticcheck, revive, gosec, bodyclose, contextcheck, errorlint, nilerr, unconvert, misspell).
- [x] CI matrix covers 6 platforms × 3 test jobs; golangci-lint pinned to v2.5.0 via `@v8` action.
- [x] `.gitignore` contains `.worktrees/`.
- [x] `go run ./cmd/notebooklm` exits 0 with the injected version string.
- [x] `internal/redact` masks `SNlM0e` / `FdrFJe` / `Cookie:` values in all four shapes; URL `?secret=` / `;session=` / `Cookie:` header / `Authorization:` header redactors all pass.
- [x] `internal/buildinfo` populates from `-ldflags` (confirmed in PR #33's CI build).
- [x] `internal/logging` writes to stderr only; context-carried request id propagates; `ReplaceAttr` runs `redact.Apply` on every string attribute.
- [x] `internal/tools/boundarycheck` exits 0 on the current tree (7 governed packages); the planted-failure test asserts both the positive and the negative case.
- [x] `make check` is green on `master` (CI run for #36: 9/9 cells pass).

## What went well

- **T-P0-2 stub-seam pattern paid off.** The stub `redactor` interface in `internal/logging/redact_hook.go` let T-P0-2 and T-P0-3 ship in parallel branches and merge cleanly: T-P0-2 landed the seam, T-P0-3 replaced it with the real `internal/redact` import in one surgical diff. No merge conflict in `internal/logging`.
- **T-P0-4 fully disjoint.** The boundarycheck linter never once had to touch `internal/logging` or `internal/redact`. The two PRs (#33 and #35) only conflicted on `Makefile` `test:` target, which was a one-line resolution in favor of the race-detector flag.
- **Both sub-agents finished in <15 min each** (T-P0-2: 795s, T-P0-3: 714s). They were scoped, had full bodies committed to `/tmp`, and had a clear seam between their work.

## What went poorly (and what to fix in Sprint 2)

### 1. T-P0-1 silently broke the CI lint gate (1 PR + 4 fixup PRs to repair)

T-P0-1 set `golangci/golangci-lint-action@v6` with `version: latest`. The
"latest" tag resolved to `v1.64.8`, which is built with the Go 1.24
toolchain and **refuses to parse a `go.mod` declaring `go 1.25`**:

```
Error: can't load config: the Go language version (go1.24) used to
build golangci-lint is lower than the targeted Go version (1.25)
```

The error fires before any build/test runs, so EVERY subsequent PR
(T-P0-2, T-P0-4) failed at the lint step on every CI matrix cell. The
sub-agents reported "make check PASS" locally and shipped, not knowing
the CI environment was running a different lint binary.

Repair path: four fixup PRs (#29, #30, #31, #32) to discover and apply
the right combination — none of `v1.66.x` exists (project jumped
straight to `v2`); `golangci-lint-action@v6` rejects `v2.x` lint
binaries (action must be `v7+`); and `.golangci.yml` was in v1 schema
(`linters-settings:` top-level) which v2's jsonschema rejects.

**Lesson for Sprint 2+:** the Scrum-master must verify CI green on the
**first** PR after any toolchain or lint-config change. Don't trust the
sub-agent's local-pass report; trust the GitHub Actions run. Add this
gate to AGENTIC_LOOP.md §6 (QA).

### 2. Misspell linter caught British spellings in T-P0-2

The sub-agent wrote doc-comments with British forms (`Unrecognised`,
`Behaviour`, `honoured`). `misspell` (locale: US) caught all five. One
follow-up commit (`7b52ab8`) fixed them. Cosmetic, but worth pre-empting
in the next agent prompt — explicitly say "US spellings only in code
AND comments".

### 3. Force-push is blocked at the tool layer

The local guardrail denies `git push --force-with-lease` even for
legitimate rebases onto master after a fast-forward. This means each
"rebase and re-PR" cycle costs: close old PR, create new branch
`ticket/<id>-r<N>`, push, open new PR, close old. The git history on
master is clean (squash merges), but the PR list grew by 6 stale
closed PRs (#27, #28, #29, #30, #31, #34). 

The clean fix is to allow `--force-with-lease` for non-protected
branches in `.puku-cli/settings.json`. The Scrum-master can keep using
the same `ticket/t-<phase>-<n>` branch name after each fast-forward of
master only if force-push is permitted.

### 4. Sprint 1 Phases 1, 2, 3 not yet started

Per `docs/sprint-reports/S01-tickets.md`, Sprint 1 covers Phases 0–3.
Phase 0 is done; Phases 1 (4 tickets), 2 (4 tickets), 3 (3 tickets) are
all still open. The Scrum-master's next action is to begin Phase 1
fan-out from the dependency graph (T-P1-1 first; T-P1-2 + T-P1-4
parallel; T-P1-3 last). Phases 2 and 3 can start as soon as their
Phase-1 dependencies land.

## Carry-over to Sprint 2 (the rest of Sprint 1)

Tickets still on the Sprint 1 board, in `Todo`:

| Issue | Title |
|---|---|
| #15 | T-P1-1: wire/json + wire/escape |
| #16 | T-P1-2: wire/methods + wire/urls + wire/status |
| #17 | T-P1-3: wire/encode + wire/decode + wire/index |
| #18 | T-P1-4: policy — five-class IdempotencyRegistry |
| #19 | T-P2-1: cookiejar |
| #20 | T-P2-2: storage |
| #21 | T-P2-3: auth policy |
| #22 | T-P2-4: atomicio + paths |
| #23 | T-P3-1: runtime |
| #24 | T-P3-2: transport kernel |
| #25 | T-P3-3: transport Executor + Runtime + chain |

Phase 1 ticket bodies are in `/tmp/t-p1-1.body.md` etc. (created during
Sprint 1 setup, never used because Phase 0 took longer than expected).

Updated lessons for the agent brief:

1. **CI verification is mandatory before claiming a PR is ready.** Do
   not trust local `make check`. Wait for at least one CI run to go
   green on the PR branch.
2. **Spell in US English.** Add a `Spell: US` line to every agent
   prompt.
3. **LDFLAGS resolution.** When the Makefile already declares
   `LDFLAGS`, just confirm `make build` actually injects the symbols
   with `strings bin/notebooklm`. Do not duplicate the LDFLAGS string.

The Scrum-master proceeds to Phase 1 fan-out next.

## Board state at end of Phase 0

| Project | #2 (Sprint 1: Foundation) |
|---|---|
| Items in `Done` | #9 (T-P0-1), #12 (T-P0-2), #13 (T-P0-3), #14 (T-P0-4) |
| Items in `Todo` | #15–#25 (11 Phase 1–3 tickets) |
| Items in `In Progress` | 0 |
