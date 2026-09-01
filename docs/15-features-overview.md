# 15 — Features overview: v1.1 and beyond

Phases 0–13 ship **parity** with `notebooklm-py` v0.8.1 — every CLI command, MCP tool, REST route, and on-disk credential format works identically, and a Python-written `~/.notebooklm/` directory is read by the Go binary without modification. That is the v1 contract.

This document introduces the **eleven net-new features** that ship **on top of parity**, in five subsequent phases (14–18). Each feature is purely additive: it composes existing primitives, lives under `~/.notebooklm/`, and never invents a new RPC. They turn the parity baseline into a team-ready, automation-ready, agent-ready engine without breaking the wire contract that makes parity possible.

## Goals

- **One pipeline per concept**, not one CLI invocation per concept — turn "click this, then click that, then poll" into `notebooklm pipeline run pipeline.yaml`.
- **Cross-notebook synthesis** — let a configured "Master Brain" notebook be queried as a single grounded engine, and let ad-hoc queries span N notebooks with merged citations.
- **Always-on ingestion** — watch RSS feeds, local directories, and git repos; auto-add new sources when they appear.
- **Team-shareable assets** — templates, personas, source bundles, and workspaces that live in git, not in a single user's home directory.
- **Local-first retrieval** — a SQLite FTS5 index over your own corpus for instant offline search and hybrid grounded chat.
- **Closed-loop quality** — turn chat history into a JSONL eval set and run regression tests against it.
- **Extensibility** — a plugin system that does not require forking the project, and an REPL that does not require a script.
- **Agent front door** — one installable skill bundle (npx-installable, MCP-bundleable) that teaches Claude Code, puku-cli, and other MCP-aware agents to drive every capability in this overview.

## Non-goals

- **No new RPC methods, no new payload shapes.** The protocol is normative and frozen by `docs/AGENTS.md` rule 1; v1.1+ rides on what `notebooklm-py` already exposes.
- **No NotebookLM-side metadata writes.** Templates, eval runs, watch state, and bundle manifests are stored in `~/.notebooklm/`, not in notebook descriptions or source titles. NotebookLM's UI is never polluted.
- **No replacement of the parity surface.** The 13 phases that ship parity are not refactored. New features extend the SDK and adapters in additive namespaces.
- **No breaking CLI changes.** Every command from `docs/07-cli-spec.md` keeps its flags, exit codes, and `--json` envelope keys. New top-level command groups are net-new (`pipeline`, `template`, `workspace`, `watch`, `eval`, `bundle`, `repl`, `plugin`).

## The eleven features

| # | Feature | One-line value | Spec |
|---|---|---|---|
| 1 | **Pipelines / DAGs** | Declarative YAML runs `notebooklm` invocations as a directed acyclic graph with `${{steps.X}}` interpolation, retries, and concurrency limits. | [features/01-pipelines.md](features/01-pipelines.md) |
| 2 | **Cross-notebook ask + synth** | Query a configured "Master Brain" notebook as a single grounded engine; ad-hoc `ask --across` merges citations from N notebooks. | [features/02-cross-notebook.md](features/02-cross-notebook.md) |
| 3 | **Watchers** | RSS / fsnotify / git pollers auto-add new sources to a notebook when they appear. | [features/03-watchers.md](features/03-watchers.md) |
| 4 | **Templates / personas / source bundles** | Versioned, shareable assets under `~/.notebooklm/templates/`, `personas/`, `bundles/`. | [features/04-templates-personas-bundles.md](features/04-templates-personas-bundles.md) |
| 5 | **Local-first search index** | SQLite FTS5 index over your own corpus; offline `search` and `search --grounded` hybrid mode. | [features/05-local-search-index.md](features/05-local-search-index.md) |
| 6 | **Eval harness** | Export chat history to JSONL; run hit-rate / recall@k / citation-precision regression tests. | [features/06-eval-harness.md](features/06-eval-harness.md) |
| 7 | **Workspace + policy** | `.notebooklm-workspace/` in a git repo; team-wide templates, bundles, watches; workspace-level policy. | [features/07-workspace-policy.md](features/07-workspace-policy.md) |
| 8 | **Export / import bundles** | Signed zip containing sources + fulltext + notes + chat + artifacts + metadata; legal hold and tenant migration. | [features/08-export-import-bundles.md](features/08-export-import-bundles.md) |
| 9 | **CLI REPL + completions** | Persistent-notebook REPL with history, plus first-class shell completions. | [features/09-repl-completions.md](features/09-repl-completions.md) |
| 10 | **Plugin system** | Third-party extensions under `~/.notebooklm/plugins/` with a Go SDK exposing `Client` and `PipelineContext`. | [features/10-plugins.md](features/10-plugins.md) |
| 11 | **npx-installable skill bundle** | One skill, three transports (local CLI / local MCP / remote REST), installable via `npx`, `notebooklm skill install`, or `.mcpb`. | [features/11-skill.md](features/11-skill.md) |

## Skill bundle directory layout

The detailed shape of the skill bundle (`SKILL.md`, `references/`, `examples/`, `tools/`, `manifest.json`, `VERSION`), the npx package layout, and the Claude Code / puku-cli detection logic live in:

- [docs/16-skill-bundle.md](16-skill-bundle.md)

## Phasing

| Phase | Bundles features | Effort | Status |
|---|---|---|---|
| 14 | #1, #2, #4 — client-side composition | 12 days | planned |
| 15 | #5, #6, #8 — local data plane | 11 days | planned |
| 16 | #3, #7 — team governance | 9 days | planned |
| 17 | #9, #10 — extensibility | 8 days | planned |
| 18 | #11 — agent front door | 7 days | planned |

Appended to [docs/10-implementation-plan.md](10-implementation-plan.md) as Phases 14–18. The existing 13-phase plan is untouched.

## Parity-stability statement

Every feature in this overview **must** pass three tests before it ships:

1. **No protocol invention.** A reviewer must be able to grep the feature's source for every `wire.EncodeRequest` call and find a matching Python original in `../notebooklm-py/src/notebooklm/`. The feature may compose existing RPCs in new ways; it may not invent new positional shapes or new method IDs.
2. **No NotebookLM-side metadata writes.** A reviewer must be able to grep the feature's source for the existing `NotebookUpdateMetadata` / `SourceRename` / `NoteCreate` calls and confirm none are used to write templates, eval results, watch state, or bundle manifests. All such state lives in `~/.notebooklm/`.
3. **`~/.notebooklm/` round-trips with the Python CLI.** A reviewer must be able to run `notebooklm-py` against a Go-prepared `~/.notebooklm/` (and vice versa) without conflict on any folder the feature owns. The `python_go_compat_test` from Phase 12 is extended to cover each new folder.

A feature that cannot pass all three is out of scope for v1.1+ and belongs in v2 (with the Backend seam).

## Storage layout under `~/.notebooklm/`

The parity baseline owns `storage_state.json`, `profiles/`, `context.json`, `master_token.json`, and `notebooklm.account`. The eleven features each own one folder, listed below. Folders are created on first use, deleted on uninstall, and never read by the parity baseline.

| Folder | Feature | Owner |
|---|---|---|
| `~/.notebooklm/templates/` | #4 — prompt and report templates | `internal/app/templates` |
| `~/.notebooklm/personas/` | #4 — chat personas | `internal/app/templates` |
| `~/.notebooklm/bundles/` | #4 — source bundles; #8 — export bundles (separate subfolders) | `internal/app/templates`, `internal/app/bundle` |
| `~/.notebooklm/index/` | #5 — local search index | `internal/index` |
| `~/.notebooklm/evals/` | #6 — eval runs, JSONL datasets, regression reports | `internal/app/eval` |
| `~/.notebooklm/watches/` | #3 — watcher state per (kind, source, target notebook) | `internal/app/watchers` |
| `~/.notebooklm/pipelines/` | #1 — pipeline state (history, last-run artifacts) | `internal/app/pipeline` |
| `~/.notebooklm/repl_history/` | #9 — REPL command history | `internal/app/repl` |
| `~/.notebooklm/plugins/` | #10 — installed plugins | `internal/app/plugin` |
| `~/.notebooklm/outbox/` | Persistent retry queue (Phase 14+ companion) | `internal/runtime/outbox` |

Workspaces (#7) do not live in `~/.notebooklm/`; they live in `.notebooklm-workspace/` inside the user's git repo. See [features/07-workspace-policy.md](features/07-workspace-policy.md).

## Versioning

v1.1 ships Phases 14–17 (features #1–#10). v1.2 ships Phase 18 (#11). Each phase ships behind a `NOTEBOOKLM_FEATURE_<NAME>=1` opt-in flag until the parity audit (`internal/tools/parityaudit`) confirms zero regressions, then the flag is removed in the next minor release.

## How to contribute a new feature

A twelfth feature that fits this overview should:

1. Add a new file at `docs/features/12-<name>.md` following the 11-section template.
2. Add a row to the table above.
3. Add a phase to `docs/10-implementation-plan.md` (or fit into an existing phase).
4. Pass the three parity-stability tests.
5. Cover the three testing tiers from `docs/11-testing-strategy.md`.

A feature that breaks any of the three tests belongs in a v2 design doc, not here.