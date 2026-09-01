# Feature 1 — Pipelines / DAGs

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Turn a sequence of `notebooklm` invocations into a single declarative YAML file that can be replayed, scheduled, diffed, and shared. A pipeline is a directed acyclic graph of steps; each step is one CLI subcommand (or one MCP tool call) with typed inputs, typed outputs, and `${{steps.X.output}}` interpolation between them.

## User stories

1. A researcher writes `pipeline.yaml` containing `create → source.add (×30) → ask → generate audio → download audio`. They run `notebooklm pipeline run pipeline.yaml --wait` and walk away.
2. A team keeps `pipelines/` in a git repo. CI runs `notebooklm pipeline run` on every PR; failures block merge.
3. An agent loops a single notebook through a 12-step content-repurposing pipeline without human intervention.
4. A user runs `notebooklm pipeline run pipeline.yaml --resume` after a laptop sleep. Completed steps are skipped, pending steps retry, and failed steps restart.

## CLI surface

New top-level group:

```
notebooklm pipeline
  run <file> [--wait] [--resume] [--timeout <duration>] [--concurrency <n>]
             [--dry-run] [--vars KEY=VAL ...] [--var-file <yaml>]
  validate <file>                                   # parse + dry-run, no side effects
  list [--state pending|running|completed|failed]
  show <run-id> [--steps] [--logs]
  cancel <run-id> [--confirm]
  graph <file> [--format dot|mermaid|json]
  template list|show|create                         # scaffolds common shapes
```

`pipeline run` is the high-traffic path.

### Pipeline YAML schema (v1)

```yaml
version: 1
name: research-to-podcast
description: "Bulk import, grounded ask, generate audio, download."
vars:                          # default values; CLI --vars override
  voice: "en"
  topic: "AI safety"
notebook:                      # optional; reused across steps
  title: "Research: {{vars.topic}}"
  use: true                   # set as active notebook for the run
defaults:
  timeout: 10m
  retry:                      # apply to every step unless overridden
    max_attempts: 2
    on: [RATE_LIMITED, NETWORK_ERROR]
concurrency: 4                # global cap on parallel steps

steps:
  create-notebook:
    run: notebook create "{{vars.topic}}" --json
    out: notebook.id           # capture the printed field into a step output

  add-sources:
    depends_on: [create-notebook]
    parallel: 8
    for_each: "{{vars.sources}}"   # list of inputs
    run: |
      notebooklm source add -n {{steps.create-notebook.notebook.id}} \
        "{{item}}" --json
    out: source.id

  wait-sources:
    depends_on: [add-sources]
    run: |
      notebooklm source wait "{{steps.add-sources.source.id}}" \
        -n {{steps.create-notebook.notebook.id}} --json

  grounded-ask:
    depends_on: [wait-sources]
    run: |
      notebooklm ask "Summarize the consensus on {{vars.topic}}" \
        -n {{steps.create-notebook.notebook.id}} --json
    out: answer.text, answer.references

  generate-audio:
    depends_on: [grounded-ask]
    run: |
      notebooklm generate audio "Focus on {{steps.grounded-ask.answer.text}}" \
        -n {{steps.create-notebook.notebook.id}} --wait --json
    out: artifact.id, artifact.url

  download-audio:
    depends_on: [generate-audio]
    run: |
      notebooklm download audio ./out/podcast.m4a \
        -a {{steps.generate-audio.artifact.id}} \
        -n {{steps.create-notebook.notebook.id}} --json
    retry:
      max_attempts: 3
```

A step is one of:

- `run: <string>` — a single command line, executed via `os/exec` (no shell). The first token must be `notebooklm` (or a path the operator has explicitly allowlisted via `--binary-prefix`); everything else is positional/flag args. `${{ … }}` is interpolated before exec.
- `for_each: <list> | {{steps.X.output}}` + `run:` — fan-out; the step runs N times in parallel up to `parallel`.
- `parallel: <int>` — cap on fan-out concurrency for this step.
- `depends_on: [<step-ids>]` — DAG edges.
- `out: <field-path>, …` — capture specific fields from `--json` stdout into step outputs.
- `retry: { max_attempts, on: [codes], backoff: exponential|linear }` — per-step retry policy. `on` is restricted to error codes the existing RPCs already document; nothing new.
- `when: <jq-expression>` — skip the step if the expression is false.

`pipeline validate` parses + DAG-sorts + type-checks the interpolation graph without executing. `pipeline run --dry-run` adds a final "would execute" report.

### `--json` envelope (success)

```json
{
  "run_id": "pl-20260101-abcd1234",
  "pipeline": "research-to-podcast",
  "started_at": "2026-01-01T10:00:00",
  "finished_at": "2026-01-01T10:18:42",
  "duration_seconds": 1122,
  "state": "completed",
  "step_count": 7,
  "steps": [
    {"id": "create-notebook", "state": "completed", "duration_seconds": 1.2, "outputs": {"notebook.id": "abc"}},
    {"id": "add-sources", "state": "completed", "attempts": 30, "succeeded": 30, "failed": 0, "duration_seconds": 47},
    {"id": "grounded-ask", "state": "completed", "outputs": {"answer.text": "..."}},
    {"id": "generate-audio", "state": "completed", "outputs": {"artifact.id": "xyz", "artifact.url": "..."}},
    {"id": "download-audio", "state": "completed"}
  ],
  "outputs": {"notebook.id": "abc", "artifact.id": "xyz"},
  "success": true
}
```

Exit codes: `0` all steps completed; `1` any step failed; `2` timeout; `130` SIGINT.

## REST surface

| Method | Path | Status | Limiter |
|---|---|---|---|
| `POST` | `/v1/pipelines/run` | **202** | — |
| `POST` | `/v1/pipelines/validate` | 200 | — |
| `GET` | `/v1/pipelines/runs` | 200 | — |
| `GET` | `/v1/pipelines/runs/{run_id}` | 200 | — |
| `POST` | `/v1/pipelines/runs/{run_id}/cancel` | 200 | — |
| `GET` | `/v1/pipelines/runs/{run_id}/events` | 200 (SSE) | — |

The events endpoint emits one SSE event per step transition (`step.started`, `step.completed`, `step.failed`, `step.retried`, `run.completed`). Matches the existing artifact-job pattern from [docs/09-rest-spec.md](../09-rest-spec.md) § "Long-running work — poll the resource".

## MCP tools

| Name | Purpose | Confirmation |
|---|---|---|
| `pipeline_run` | Run a pipeline from a YAML string or path. | no (reads); yes when destructive steps are present (the SDK auto-flags them) |
| `pipeline_validate` | Validate without executing. | no |
| `pipeline_status` | Status of one or all runs. | no |
| `pipeline_cancel` | Cancel a running pipeline. | yes |

## Public SDK additions

A new namespace `notebooklm.Pipelines`:

```go
type PipelineRun struct { ID, State, StartedAt, FinishedAt, Outputs, Steps []PipelineStep }
type PipelineStep struct { ID, State, Attempts, Succeeded, Failed, DurationSeconds int; Outputs map[string]any }
type Pipelines struct{ client *Client }

func (p *Pipelines) Run(ctx, yaml []byte, opts PipelineRunOptions) (*PipelineRun, error)
func (p *Pipelines) Validate(ctx, yaml []byte) (*PipelinePlan, error)
func (p *Pipelines) List(ctx, filter PipelineState) ([]PipelineRun, error)
func (p *Pipelines) Get(ctx, runID string) (*PipelineRun, error)
func (p *Pipelines) Cancel(ctx, runID string) error
```

`PipelineRunOptions` carries `Wait`, `Resume`, `Timeout`, `Concurrency`, `Vars`, `VarFile`. Execution composes the existing SDK methods (no new RPCs).

## Data model under `~/.notebooklm/`

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/pipelines/` | dir | one folder per run |
| `~/.notebooklm/pipelines/<run-id>/` | dir | `plan.yaml` (the parsed DAG), `state.json` (per-step outputs + status), `logs/<step>.log` (combined stdout/stderr, redacted via `internal/redact`) |
| `~/.notebooklm/pipelines/index.json` | JSON | `{ runs: [...] }`, latest-first |

Atomic writes per `internal/atomicio`. The state file is updated on every step transition so `pipeline show <run-id>` works against the latest snapshot, and `--resume` can read partial progress from disk if the host process died.

A persisted run is recoverable across restarts when `--resume` is passed; without it, a previous run is reported as `state: crashed` and the user must cancel + re-run.

## Protocol implications

**No new RPC.** Every step is a CLI invocation that already maps to an existing RPC. The executor is a `child_process` driver over `os/exec.Cmd` (no shell, to keep injection out of the picture). `${{ }}` interpolation is local string templating.

Step types map to existing Python originals:

| Step kind | Python source backing the CLI command |
|---|---|
| `notebook create` | `cli/notebook_cmd.py::create` → `notebooks._notebooks.create` → `_web/notebooks.py::create_notebook` |
| `notebooklm source add` | `cli/source_cmd.py::add` → `sources._sources.add_url/add_file/…` |
| `notebooklm ask` | `cli/ask_cmd.py::ask` → `_chat.ask` → `_web/chat.py` |
| `notebooklm generate audio` | `cli/generate_cmd.py::audio` → `_artifacts.generate_audio` → `_web/artifacts.py` |
| `notebooklm download audio` | `cli/download_cmd.py::audio` → `_artifacts.download_audio` → `_web/assets/download.py` |

The pipeline executor is **transport-neutral** — the same `Plan` runs whether the underlying transport is CLI (a sub-process), MCP (an in-process call), or REST (an HTTP round-trip).

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| **14 (this phase)** | Independent of all later phases. |
| 15 | `pipeline run` can reference `search` (offline retrieval) and `bundle create` (export) as steps. |
| 16 | `pipeline run` can reference `watch` and `workspace sync` as steps. |
| 17 | The REPL has a `pipeline run` sub-mode; plugins can contribute new step kinds via the plugin SDK. |
| 18 | The skill's capability parity table points at pipelines. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestParsePipelineYAML` | Schema validation golden table; valid YAML passes, every invalid shape is rejected with a specific message. |
| `TestDAGTopoSort` | Cycle detection; `depends_on` resolution; missing dep = typed error. |
| `TestInterpolation` | `${{vars.X}}`, `${{steps.Y.output}}`, `${{item}}` in `for_each`. Missing var = typed error, never panic. |
| `TestRetryPolicy` | `on: [RATE_LIMITED]` only retries rate-limit errors; other errors fail fast. |
| `TestForEachParallel` | `for_each` of 100 items with `parallel: 8` runs exactly 8 at a time. |
| `TestResume` | Killed mid-run → `--resume` skips completed steps, retries pending ones. |
| `TestStepOutputCapture` | `--json` stdout is parsed; only the documented `out:` paths are kept. |
| `TestShellInjectionGuard` | `run:` is exec'd via argv (not shell); a malicious `; rm -rf /` is a literal arg, not a shell command. |
| `TestConcurrencyLimit` | Global `concurrency: 4` is enforced across `for_each` fan-outs. |
| `TestIdempotencyPolicy` | A retried step calls the SDK's idempotent method (e.g., `source.add` skips `already_present`); the retry does not double-add. |

### Cassette

A pipeline whose `add-sources` step is replayed against a cassette for each of the 30 sources. Asserts: plan parses, DAG sorts, step outputs match the cassette's recorded `--json` envelopes.

### E2E

Live `pipeline run` against a real notebook: `create` + one `source add` + `ask` + `generate audio --wait` + `download audio`. Asserts: every step's recorded outputs are replayable via `pipeline show <run-id>`.

## Acceptance criteria

1. `notebooklm pipeline validate pipeline.yaml` exits 0 on a valid file, exits 1 with a specific error on each known invalid shape.
2. `notebooklm pipeline run pipeline.yaml --wait` against a fixture notebook completes every step; the `--json` envelope matches the documented schema.
3. Killing the runner mid-pipeline then running `pipeline run pipeline.yaml --resume` finishes without re-executing the already-completed steps.
4. A pipeline with a `for_each` of 100 items and `parallel: 8` invokes `notebooklm source add` at most 8 times concurrently (verified with a wrapped `notebooklm` binary that counts concurrent processes).
5. A pipeline that fails on step 3 of 5 reports `state: failed` with the failing step's error code, log tail, and partial outputs.
6. `pipeline graph pipeline.yaml --format mermaid` produces a renderable Mermaid diagram.
7. `pipeline list` survives `notebooklm upgrade` (state file format is versioned).
8. A malicious `run: "rm -rf /tmp/foo; true"` does not execute `rm`; the `;` is a literal argument.
9. The REST events endpoint emits one SSE event per step transition in the documented order.
10. The MCP `pipeline_run` tool, when the plan contains a destructive step, refuses to run without `confirm: true`.
