# AGENTS.md — rules of engagement

Guidance for Claude Code, Codex, and any other agent (or human) working in
`notebooklm-go`. Read this before your first edit.

## What this repo is

A Go port of `notebooklm-py` — an unofficial client for Google Gemini Notebook
(NotebookLM) over Google's internal `batchexecute` RPC protocol. It ships a library, a
Cobra CLI, an MCP server, and a REST server from one module.

**The single most important constraint:** the obfuscated RPC method IDs and the
positional array shapes are undocumented and can break whenever Google changes them.
They are the #1 breakage class. Never "clean up" a payload shape because it looks odd —
the odd shape *is* the contract.

## The seven rules

### 1. The Python source is normative

`../notebooklm-py/src/notebooklm/` is ground truth for every wire shape, enum value,
default, and error message. Before writing a payload builder or row decoder, open the
Python original and port it position for position. Cite the Python file and symbol in a
Go comment:

```go
// Port of _web/params/artifacts.py::build_audio_artifact_params.
// Positional shape is load-bearing — see docs/04-rpc-payloads.md#audio.
```

If you cannot find the Python original, **stop and ask** rather than inventing a shape.

### 2. Never guess a wire shape

A wrong index does not fail loudly — it silently decodes garbage or gets rejected with a
bare gRPC status the user cannot act on. Three legitimate sources of truth, in order:

1. The Python builder/decoder.
2. `../notebooklm-py/docs/rpc-reference.md`.
3. A fresh capture from a real browser session (see doc 03, "Re-capturing the protocol").

Anything else is a guess.

### 3. Preserve JSON byte-compatibility

Go's `encoding/json` differs from Python's `json.dumps` in ways that break this protocol:

- Go HTML-escapes `<`, `>`, `&` into `<` etc. **by default.** Python does not.
  Always encode through `wire.Marshal`, which sets `SetEscapeHTML(false)`.
- `json.Encoder.Encode` appends a newline. `wire.Marshal` trims it.
- Decoding into `any` turns every number into `float64`, losing precision on large IDs.
  Always decode with `UseNumber()`.

There is exactly one JSON encoder and one JSON decoder in this repo, both in
`internal/web/wire`. Do not call `encoding/json` directly outside that package.

### 4. Credentials never reach a log, an error, or a `String()`

Cookie values, the CSRF token (`SNlM0e`), the session id (`FdrFJe`), the master token,
and OAuth bearers are full-account credentials. Rules:

- Every struct holding one implements `String()`/`LogValue()` that redacts.
- Never `%v` a struct that holds one; use the redacting accessor.
- The redaction regexes are ported in `internal/redact`. Route error text and debug
  response previews through it.
- Credential files are written `0600` inside a `0700` directory, atomically
  (temp + `rename`), never with a partial write visible.
- Never send a credential to a host outside the allowlist. See doc 05.

### 5. Every layer boundary is enforced, not merely documented

The Python original lint-enforces its boundaries; we do too, with
`internal/tools/boundarycheck` run in CI:

| Package | May import | May NOT import |
|---|---|---|
| `internal/app` | `notebooklm`, `internal/web`, `internal/auth` | `internal/cli`, `internal/mcpsrv`, `internal/restsrv`, `spf13/cobra`, any TUI package |
| `internal/cli` | `notebooklm`, `internal/app`, `internal/auth/browser` | `internal/web`, other adapters |
| `internal/mcpsrv` | `notebooklm`, `internal/app` | `internal/cli`, `internal/web`, `internal/restsrv` |
| `internal/restsrv` | `notebooklm`, `internal/app` | `internal/cli`, `internal/web`, `internal/mcpsrv` |
| `internal/web/wire` | stdlib only | everything else in the module |

If you need to cross a boundary, the answer is to move logic **down** into
`internal/app`, not to add an import.

### 6. Destructive things confirm; long things stream progress; failures are typed

- Any command that deletes, overwrites, or widens sharing requires `-y/--yes` (CLI),
  `confirm: true` (MCP), or an explicit request body flag (REST). No exceptions.
- `--json` output on stdout must stay parseable: status lines, spinners, and progress go
  to **stderr**.
- Errors carry a stable machine code (doc 07, exit-code table). Never surface a raw
  obfuscated RPC id or a bare numeric gRPC code in a user-facing message — keep both on
  the error struct for logs.

### 7. Mutations declare their retry safety

Every RPC is registered in `internal/web/policy` with one of five idempotency classes
(doc 03). The transport reads that registry to decide whether its inner retry loop may
replay the call. An unregistered RPC fails a startup assertion — that is deliberate: a
`batchexecute` mutation replayed after a lost response duplicates the write (a duplicate
notebook, a duplicate source, an extra LLM inference, a re-sent invite email).

## Working style

**Build order is the plan.** `docs/10-implementation-plan.md` defines 13 phases with
explicit acceptance criteria. Work a phase to completion, including its tests, before
starting the next. Do not scaffold ten packages of stubs.

**The execution loop is the loop.** Planning is read-only — actual work runs through
the Scrum/Code/QA agent loop defined in [`docs/AGENTIC_LOOP.md`](AGENTIC_LOOP.md).
The loop owns the GitHub Project board, sprint tickets, worktrees, and PRs.

**Every phase ends green.** `make check` (fmt + vet + lint + test + boundarycheck) must
pass before a phase is considered done.

**Cassettes over live calls.** Live API calls are rate-limited and account-bound. Record
once, replay forever. See doc 11.

**Ask about ambiguity in behavior; decide ambiguity in style.** If the Python original is
ambiguous about *what the server expects*, ask. If it is merely un-Go-like in *how it
expresses something*, make it idiomatic and move on.

## Commands

```bash
make build            # all three binaries into ./bin
make check            # fmt, vet, golangci-lint, boundarycheck, test
make test             # unit + cassette tests
make test-e2e         # live tests; requires auth + NOTEBOOKLM_E2E=1
make cover            # coverage report; gate is 80% on internal/web and internal/auth
go run ./cmd/notebooklm --help
```

## Common pitfalls (ported from the Python original's hard-won list)

1. **RPC method IDs change.** Re-capture traffic and update `internal/web/wire/methods.go`.
   `NOTEBOOKLM_RPC_OVERRIDES` lets an operator patch an id without a release.
2. **Source-id nesting depth varies per RPC** — `[id]` / `[[id]]` / `[[[id]]]` /
   `[[[[id]]]]`. Copy the depth from the Python builder; never assume.
3. **CSRF tokens expire.** The refresh ladder handles it; do not add ad-hoc retries.
4. **Rate limiting is real.** Bulk operations need pacing. Honor `Retry-After`.
5. **The two personal hosts stand in for each other** (`notebook.google.com`,
   `notebooklm.google.com`) but `Origin`/`Referer` must name the host the request is
   actually going to, never the other one.
6. **`status` and `drive_status` are different axes.** A Drive file that was deleted keeps
   reading `status: ready` because NotebookLM's own ingestion did finish.
7. **A file add that fails after registering its row leaves the row at `preparing`, not
   `error`.** That is deliberate evidence, and it counts against quota.

## Feature additions (Phases 14–18)

The 11 enterprise features documented under `docs/features/01..11-*.md` and
`docs/15-features-overview.md` are layered on top of the 13-phase parity plan.
They are **post-parity** work; parity is not relaxed to ship them. The seven rules
above still apply to every byte of feature code. The rules below add to them.

### F1. No new RPC. No new wire shape.

Every feature in `docs/features/` runs entirely on the SDK that the parity plan
already exposes (`Sources.AddURL`, `Sources.GetFulltext`, `Chat.Ask`,
`Artifacts.List`, `Notes.Create`, `Sharing.GetStatus`, `Downloads.Get`, etc.).
A feature that needs an RPC the parity SDK does not expose **does not ship** —
it gets deferred until upstream `notebooklm-py` adds the equivalent call, or
the feature is rescoped to use existing calls.

This is checked mechanically in two places:

1. The per-feature spec at `docs/features/NN-*.md` has a "Protocol implications"
   section. If that section does not say "No new RPC" and cite the Python
   originals reused, the spec is incomplete.
2. `internal/tools/boundarycheck` (extended in Phase 14) rejects any new symbol
   in `internal/web/wire/methods.go`, `internal/web/wire/payloads/*.go`, or
   `internal/web/policy/*.go` whose only consumer is a feature package listed
   in F3.

### F2. All new state under `~/.notebooklm/`. Round-trips with the Python CLI.

Each feature owns one folder under `~/.notebooklm/`:

| Feature | Folder |
|---|---|
| #1 Pipelines | `~/.notebooklm/pipelines/` |
| #2 Cross-notebook | `~/.notebooklm/crossnb/` (cache only) |
| #3 Watchers | `~/.notebooklm/watches/`, `~/.notebooklm/outbox/` |
| #4 Templates / personas / bundles | `~/.notebooklm/templates/`, `personas/`, `bundles/` |
| #5 Local search index | `~/.notebooklm/index/` (SQLite FTS5) |
| #6 Eval harness | `~/.notebooklm/evals/` |
| #7 Workspace + policy | `~/.notebooklm/workspace-link.json`, `policy-counters.json` |
| #8 Export/import bundles | `~/.notebooklm/bundles/` |
| #9 REPL + completions | `~/.notebooklm/repl_history/` |
| #10 Plugin system | `~/.notebooklm/plugins/`, `plugin-state/` |
| #11 Skill bundle | (no per-feature state; tarball + npm package only) |

Round-trip rule: a `~/.notebooklm/` directory written by the Go CLI must be
readable by the Python CLI unchanged where parity overlaps (notebook list,
source list, notes, chat history, share status). Feature-local state (pipelines,
watches, indices, evals, bundles, plugins, history) is Go-only; the Python CLI
ignores those folders.

All writes use `internal/atomicio` (temp + `chmod 0600` + `sync` + `rename`).
Directories are `0700`. A crash mid-write leaves the previous version intact.
This rule is enforced by the test `TestAtomicWriteNoPartial` which is run
against every feature folder.

### F3. Boundary map for feature packages

Phase 14 extends the package boundary table in rule 5 with the new
`internal/app/*` and `internal/index` packages:

| Package | May import | May NOT import |
|---|---|---|
| `internal/app/pipeline` | `notebooklm`, `internal/web`, `internal/auth`, `internal/atomicio`, `internal/redact` | `internal/cli`, `internal/mcpsrv`, `internal/restsrv`, `spf13/cobra`, any TUI package, any other `internal/app/*` that is not its declared dependency |
| `internal/app/templates` | same as pipeline | same as pipeline |
| `internal/app/crossnotebook` | same | same |
| `internal/app/watchers` | same | `internal/app/pipeline` (watches invoke SDK directly, not via pipelines) |
| `internal/app/bundle` | same | same |
| `internal/app/eval` | same | same |
| `internal/app/workspace` | same | same |
| `internal/app/repl` | same | `internal/cli` (REPL is its own CLI; shares only types with `internal/cli`) |
| `internal/app/plugin` | same | `internal/cli`, `internal/mcpsrv`, `internal/restsrv` (plugin subprocess IPC is internal) |
| `internal/app/skill` | same | same |
| `internal/index` | `notebooklm`, `internal/atomicio`, `internal/redact`, `modernc.org/sqlite` | anything that imports a TUI, anything that talks RPC directly |

The boundary rules for `internal/cli`, `internal/mcpsrv`, `internal/restsrv`
are unchanged: each adapter may import only the `internal/app/*` packages it
needs. `internal/cli` may import all of them; `internal/mcpsrv` and
`internal/restsrv` import only those whose REST/MCP routes are documented in
the per-feature spec.

If a feature needs logic shared by two `internal/app/*` packages, that logic
moves down into a new shared `internal/app/<concern>` package (e.g.,
`internal/app/templateutil` for template rendering shared by `templates` and
`pipeline`). The shared package has its own boundary row.

### F4. The per-feature spec is normative

For feature N, the file `docs/features/NN-*.md` is the spec of record. Code
review resolves disagreements by reading that file, not by re-debating the
question. The spec must cover all 11 sections from the template:

1. Goal
2. User stories
3. CLI surface
4. REST surface
5. MCP tools
6. Public SDK additions
7. Data model under `~/.notebooklm/`
8. Protocol implications (must say "No new RPC" and cite Python originals)
9. Dependencies on Phases 14–18
10. Tests (unit / cassette / e2e)
11. Acceptance criteria

A feature whose spec is missing one of these sections is not ready for
implementation. `make check-features` (added in Phase 14) parses every
`docs/features/*.md` and fails on missing sections.

### F5. No NotebookLM-side metadata writes

The Python original never writes back to NotebookLM beyond what a regular user
can do via the UI (create notebook, add source, share, etc.). The Go port must
honor that. Features must not invent side effects on the NotebookLM side:

- No new notebook properties, no notebook rename as a side effect of a
  pipeline step (rename only when the user explicitly asked).
- No automatic label/tag application.
- No "background refresh" RPCs that re-write state the user did not ask to
  change.
- No bulk operations that exceed what `notebooklm-py` exposes (e.g., no
  per-source re-upload, no chat-history rewrite).

A feature that needs any of these gets rescoped or deferred.

### F6. Workspace + policy are the governance layer

For #7 (workspace + policy), one centralized function
`internal/app/policy.Enforce(ctx, op, args) PolicyEnforcement` decides every
operation. The CLI, MCP, and REST adapters call it before any work; the
SDK does not bypass it. Adding a new policy-bearing operation requires:

1. Adding the operation to the `PolicyOp` enum.
2. Adding at least one default rule to `policy.yaml` so a default workspace
   produces a meaningful answer (allow or deny with reason).
3. A unit test that the operation is refused when the default rule denies it.
4. A unit test that the override path (`--override-policy --reason ...`)
   succeeds.

A new feature without an entry in the policy registry cannot ship. This is
checked by `TestPolicyCoverage` in Phase 16.

### F7. Plugins are subprocesses. The host is privileged.

For #10 (plugin system), the plugin is a separate process that communicates
over NDJSON. Plugins do not import any package in the `notebooklm-go` module.
The host strips `NOTEBOOKLM_HOME`, `NOTEBOOKLM_AUTH_JSON`, and any other
credential-bearing env var before exec'ing the plugin. The plugin cannot
read `~/.notebooklm/storage_state.json` directly; it must ask the host over
the NDJSON contract.

Enforced by:

- `TestPluginEnvironmentStripped` — `os.Getenv("NOTEBOOKLM_HOME")` inside
  the plugin returns empty.
- `TestPluginCredentialsNeverPassed` — the NDJSON stream from the host to
  the plugin contains no `storage_state.json`, no `at=`, no `SID`, no
  `FdrFJe`, no `SNlM0e`.
- `TestPluginIsolation` — a plugin that panics or prints garbage does not
  crash the host.
- `TestPluginCallCancellation` — closing stdin causes the plugin to exit
  within 5 s; longer-running plugins are SIGKILLed.

A plugin feature that bypasses these tests does not ship.

### F8. Skill bundle content is generated, not hand-written

For #11 (skill bundle), `SKILL.md` and the manifest are generated at release
time from canonical docs by `make skill-generate`. The committed
`SKILL.md` must equal the generated one; `make skill-check` enforces this
in CI. A hand-edited `SKILL.md` does not commit cleanly.

This is the only feature whose primary artifact is generated. If the source
docs (`docs/07-cli-spec.md`, `docs/08-mcp-spec.md`,
`docs/15-features-overview.md`, `docs/16-skill-bundle.md`) drift, the
generated skill bundle drifts with them — there is no second source of
truth to keep in sync.

### F9. Phase boundaries hold

Phase 14–18 each have acceptance criteria in `docs/10-implementation-plan.md`.
A feature is not "done" until its phase's acceptance passes. Carrying
features forward across phase boundaries is allowed only when:

- The carried feature has its full spec, tests, and acceptance written.
- The carry is recorded in the next phase's "Carried over" subsection.
- The carry does not push the next phase past its stated effort estimate
  by more than 20%.

Implicit carries (starting Phase 15 work with unfinished Phase 14) are a
process violation and fail review.

### F10. Cassettes and e2e stay separate from feature logic

Cassette replay lives in `internal/web/policy/testdata/cassettes/`. Feature
code under `internal/app/*` must not import cassettes directly; it consumes
the SDK like any other caller. E2E tests live in `internal/app/<feature>/e2e_test.go`
with the build tag `//go:build e2e` and are run only when
`NOTEBOOKLM_E2E=1` is set.

This is the same separation the parity plan enforces; the rules above do not
relax it.
