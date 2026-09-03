# Sprint 2 — Ticket Breakdown (Phases 4–6 vertical slice)

## Sprint goal
Ship the **notebooks vertical slice** end-to-end: a `notebooklm.Client` that talks to
the runtime on master, a `NotebooksAPI` with 17 methods, a `SourcesAPI` minimal
(List + AddURL), an `internal/cli` Cobra skeleton with grouped help + theme + error
handler + table renderer, the first CLI commands (notebook + session + auth check +
profile), the cassette harness with `scrubhar` credential scrubbing, and an end-to-end
test that runs `notebooklm list --json` against a cassette and verifies the envelope
matches the Python CLI. Plus a partial Phase 4 auth acquisition path sufficient for
the vertical slice.

After Sprint 2, every Phase 5 acceptance criterion from `docs/10-implementation-plan.md`
is met; the project can be demoed end-to-end against cassettes.

## Dependency graph

```
        ┌──────────────────────────────────────────────────────────────┐
        │                                                              │
        │  T-P4-1 (tokens + extract)  ─────►  T-P4-2 (refresh L1)       │
        │                                                              │
        │  T-P5-1 (Client) ─┬─► T-P5-2 (serialize) ─► T-P5-3 (errors) │
        │                  ├─► T-P5-4 (params+rows+features)          │
        │                  ├─► T-P5-9 (scrubhar + harness)             │
        │                  │                                          │
        │                  ├─► T-P5-5 (NotebooksAPI) ◄─┐               │
        │                  │                          │               │
        │                  ├─► T-P6-1 (SourcesAPI) ◄──┤               │
        │                  │                          │               │
        │                  ├─► T-P5-6 (resolve) ◄──────┤               │
        │                  │                          │               │
        │                  └─► T-P5-7 (CLI skeleton) ◄─┤               │
        │                                             │               │
        │                  T-P5-8 (CLI commands) ◄─────┤               │
        │                                             │               │
        │                  T-P5-10 (e2e test) ◄────────┴─ T-P5-9       │
        └──────────────────────────────────────────────────────────────┘
```

**Wave 1 (parallel, no inter-dependencies):** T-P4-1, T-P5-1, T-P5-2, T-P5-9
**Wave 2 (parallel, depend on Wave 1):** T-P4-2, T-P5-3, T-P5-4, T-P5-7
**Wave 3 (parallel, depend on Wave 2):** T-P5-5, T-P5-6, T-P6-1
**Wave 4 (depend on Wave 3):** T-P5-8
**Wave 5 (last):** T-P5-10 (needs everything)

## Phase issues

| Phase | Issue | Sprint 2 scope |
|---|---|---|
| Phase 4 — auth + refresh | [#49](https://github.com/raihankhan/notebooklm-go/issues/49) | T-P4-1, T-P4-2 (partial; full ladder Sprint 3) |
| Phase 5 — notebooks vertical slice | [#50](https://github.com/raihankhan/notebooklm-go/issues/50) | T-P5-1 … T-P5-10 (full) |
| Phase 6 — sources | [#51](https://github.com/raihankhan/notebooklm-go/issues/51) | T-P6-1 (minimal; full Phase 6 Sprint 4) |

## Tickets

### Wave 1 — independent foundations (fan out in parallel)

| Ticket | Issue | Depends on | Worktree |
|---|---|---|---|
| T-P4-1 tokens + extract | [#62](https://github.com/raihankhan/notebooklm-go/issues/62) | — | `.worktrees/t-p4-1` |
| T-P5-1 notebooklm.Client | [#52](https://github.com/raihankhan/notebooklm-go/issues/52) | — | `.worktrees/t-p5-1` |
| T-P5-2 serialize envelope | [#53](https://github.com/raihankhan/notebooklm-go/issues/53) | — | `.worktrees/t-p5-2` |
| T-P5-9 scrubhar + harness | [#61](https://github.com/raihankhan/notebooklm-go/issues/61) | — | `.worktrees/t-p5-9` |

### Wave 2 — depend on Wave 1

| Ticket | Issue | Depends on | Worktree |
|---|---|---|---|
| T-P4-2 refresh L1 + profile | [#63](https://github.com/raihankhan/notebooklm-go/issues/63) | T-P4-1 | `.worktrees/t-p4-2` |
| T-P5-3 errors classify | [#54](https://github.com/raihankhan/notebooklm-go/issues/54) | T-P5-2 | `.worktrees/t-p5-3` |
| T-P5-4 params+rows+features | [#55](https://github.com/raihankhan/notebooklm-go/issues/55) | T-P5-1 | `.worktrees/t-p5-4` |
| T-P5-7 CLI skeleton | [#57](https://github.com/raihankhan/notebooklm-go/issues/57) | T-P5-2, T-P5-3 | `.worktrees/t-p5-7` |

### Wave 3 — depend on Wave 2

| Ticket | Issue | Depends on | Worktree |
|---|---|---|---|
| T-P5-5 NotebooksAPI | [#56](https://github.com/raihankhan/notebooklm-go/issues/56) | T-P5-1, T-P5-3, T-P5-4 | `.worktrees/t-p5-5` |
| T-P5-6 resolve | [#58](https://github.com/raihankhan/notebooklm-go/issues/58) | T-P5-5 | `.worktrees/t-p5-6` |
| T-P6-1 SourcesAPI minimal | [#64](https://github.com/raihankhan/notebooklm-go/issues/64) | T-P5-1, T-P5-5 | `.worktrees/t-p6-1` |

### Wave 4 — depend on Wave 3

| Ticket | Issue | Depends on | Worktree |
|---|---|---|---|
| T-P5-8 CLI commands | [#59](https://github.com/raihankhan/notebooklm-go/issues/59) | T-P5-5, T-P5-6, T-P5-7 | `.worktrees/t-p5-8` |

### Wave 5 — last

| Ticket | Issue | Depends on | Worktree |
|---|---|---|---|
| T-P5-10 e2e cassette test | [#60](https://github.com/raihankhan/notebooklm-go/issues/60) | T-P5-7, T-P5-8, T-P5-9 | `.worktrees/t-p5-10` |

## Carry-over rules

- If a Wave 2 ticket fails QA on second consecutive review: defer to Sprint 3 carry-over slot.
- T-P4-2 explicitly leaves L2/L3/L4 ladder as `ErrLadderLevelNotImplemented`; Sprint 3 fills those.
- T-P6-1 explicitly leaves 4/5 source kinds; Sprint 4 fills those.

## Quality gates

Every PR must pass `make check` (fmt + vet + lint + test + boundarycheck + build) before
merge. CI matrix is unchanged from Sprint 1 (golangci-lint v2.5.0 pinned, Go 1.25).
