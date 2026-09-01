# Feature 7 — Workspace and policy

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Move from "one user's `~/.notebooklm/`" to "a team's `.notebooklm-workspace/` checked into git." A workspace declares shared templates, personas, source bundles, pipelines, watches, and a policy file. New team members clone the repo and run `notebooklm workspace sync` to materialize. A policy file enforces team guardrails (no public sharing by default, mandatory source labels, generation caps).

## User stories

1. A team lead creates `.notebooklm-workspace/` in their repo, populates it with the team's templates and bundles, commits it. Every team member runs `notebooklm workspace sync` once; their `~/.notebooklm/` is now in sync.
2. A team sets `policy.sharing.default: restricted` in `.notebooklm-workspace/policy.yaml`. Subsequent `notebooklm share set-public` calls are refused at the CLI unless the operator explicitly passes `--override-policy` with a typed reason.
3. A team sets `policy.generation.max_per_day_per_user: 20`. The 21st `generate audio` call today is refused with `policy_violation`.
4. A team requires source labels for every new source. `policy.sources.required_labels: true`. `notebooklm source add` without `--label` is refused.
5. A team syncs the workspace to S3 for users who don't want to clone git; `notebooklm workspace sync --backend s3 --bucket team-notebooklm`.

## CLI surface

New top-level group:

```
notebooklm workspace
  init [--from <path>] [--scope <user|project>]   # create .notebooklm-workspace/
  status                                            # current state vs workspace
  sync [--backend <git|s3|gcs|local>] [--dry-run]
  diff [--against <remote>]
  show [path]                                       # print effective config
  pin <kind> <name> --to <workspace-path>          # move a local asset into the workspace
```

Plus a new top-level group for policy:

```
notebooklm policy
  show [--effective] [--source <workspace|user|both>]
  set <key> <value>                                 # sets a user-level override
  unset <key>
  explain <key>                                     # prints the rule + origin
```

### Workspace layout

```
.notebooklm-workspace/
├── workspace.yaml                                  # workspace metadata
├── policy.yaml                                     # team policy
├── templates/                                      # shared templates (#4)
│   ├── podcast-deep-dive.md
│   └── study-guide.md
├── personas/                                       # shared personas (#4)
│   └── research-analyst.md
├── bundles/                                        # shared source bundles (#4)
│   └── rfc-set/bundle.yaml
├── pipelines/                                      # shared pipelines (#1)
│   └── research-to-podcast.yaml
├── watches/                                        # shared watch configs (#3)
│   └── competitor-blog.yaml
├── evals/                                          # shared eval specs (#6)
│   └── master-brain-regression.yaml
├── plugins/                                        # plugin manifests (#10)
└── README.md                                       # human-readable usage notes
```

`workspace.yaml` declares workspace-level metadata:

```yaml
version: 1
name: acme-research
description: Acme's NotebookLM workspace.
sync:
  backend: git                      # git | s3 | gcs | local
  remote: https://github.com/acme/notebooklm-workspace
  branch: main
pinned:
  notebooklm_version: ">=1.1.0"
```

### Policy file format

`policy.yaml`:

```yaml
version: 1
defaults:
  sharing:
    default: restricted              # restricted | anyone-with-link
    allow_set_public: false          # refuse `share set-public` unless --override-policy
    require_recipient_for_user_grant: true
  sources:
    required_labels: true            # refuse `source add` without --label
    max_per_notebook: 300            # soft cap; --force to exceed
  generation:
    max_per_day_per_user: 20         # 0 = unlimited
    allowed_kinds: [audio, video, quiz, flashcards, report, data_table, mind_map, slide_deck, infographic]
    refuse_if_no_labels: false
  research:
    max_sources_per_run: 50
    allow_drive_research: false
  sharing_public:
    require_approval_from: [leadership@example.com]
overrides:
  by_role:
    admin:
      generation:
        max_per_day_per_user: 100
        allowed_kinds: [audio, video, quiz, flashcards, report, data_table, mind_map, slide_deck, infographic, file]
        refuse_if_no_labels: false
```

The CLI consults policy on every relevant operation. Policy enforcement is centralized: there is one `internal/app/policy/Enforce()` function that takes an `Op` (typed enum of operation kinds) and returns `Allow | Deny(reason)`. Adapters (CLI / MCP / REST) call it; the user-facing message names the rule and its origin (workspace, user override, default).

Per-user counters (e.g., `max_per_day_per_user: 20`) are stored at `~/.notebooklm/policy-counters.json`, written atomically per operation.

## REST surface

| Method | Path | Status | Limiter |
|---|---|---|---|
| `GET` | `/v1/workspace` | 200 | — |
| `POST` | `/v1/workspace/sync` | 202 | — |
| `GET` | `/v1/policy` | 200 | — |
| `POST` | `/v1/policy/check` | 200 | — |

`POST /v1/policy/check { op, args }` returns `{ allow: true }` or `{ allow: false, reason: "...", rule_origin: "workspace:policy.yaml:generation.max_per_day_per_user" }`. Useful for an agent that wants to test before doing.

## MCP tools

| Name | Purpose | Confirmation |
|---|---|---|
| `workspace_status` | Show workspace state. | no |
| `workspace_sync` | Pull the latest workspace assets. | yes (changes local files) |
| `policy_show` | Show effective policy. | no |
| `policy_check` | Test if an operation would be allowed. | no |

## Public SDK additions

```go
type Workspace struct {
    Version int
    Name, Description string
    Sync WorkspaceSync
    Pinned WorkspacePinned
}
type WorkspaceSync struct { Backend, Remote, Branch string }

type Policy struct { Version int; Defaults, Overrides PolicyBody }
type PolicyEnforcement struct { Allow bool; Reason, RuleOrigin string }

type WorkspaceAPI struct{ client *Client }
func (w *WorkspaceAPI) Init(ctx, opts WorkspaceInitOptions) error
func (w *WorkspaceAPI) Status(ctx) (*WorkspaceStatus, error)
func (w *WorkspaceAPI) Sync(ctx, opts WorkspaceSyncOptions) error
func (w *WorkspaceAPI) Diff(ctx) (*WorkspaceDiff, error)
func (w *WorkspaceAPI) Pin(ctx, kind, name, target string) error

type PolicyAPI struct{ client *Client }
func (p *PolicyAPI) Show(ctx, opts PolicyShowOptions) (*Policy, error)
func (p *PolicyAPI) Check(ctx, op PolicyOp, args map[string]any) (*PolicyEnforcement, error)
func (p *PolicyAPI) Set(ctx, key, value string) error
func (p *PolicyAPI) Unset(ctx, key string) error
```

## Data model

Workspace files live in the user's git repo at `.notebooklm-workspace/`. They are user-owned (not in `~/.notebooklm/`); the workspace is the source of truth, and `~/.notebooklm/` is the materialized view.

| Path | Purpose |
|---|---|
| `./.notebooklm-workspace/` | workspace root (git-managed) |
| `~/.notebooklm/workspace-link.json` | points to the workspace root (resolved at sync time) |
| `~/.notebooklm/policy-counters.json` | per-user daily counters, atomic writes |

The `Sync` operation copies workspace files into the corresponding `~/.notebooklm/<kind>/` folders. Files written by sync are tagged with the `<!-- managed by notebooklm workspace -->` marker so `workspace diff` can show what diverges.

### Sync backends

```go
type SyncBackend interface {
    Pull(ctx, dest string) error
    Push(ctx, src string) error
    Status(ctx) (*SyncStatus, error)
}
```

Bundled implementations: `git` (subprocess over `os/exec` argv, no shell), `local` (filesystem copy), `s3` (`aws-sdk-go-v2`), `gcs` (`cloud.google.com/go/storage`). The `git` backend uses the user's existing SSH / HTTPS credentials; it does not embed a credential manager.

## Protocol implications

**No new RPC.** Every workspace operation is a filesystem operation + the existing SDK calls. Policy enforcement is a thin wrapper around existing operations; it calls `errors.Classify` after the policy check fails so the user sees the standardized error envelope.

The sync backends are pure client-side: `git pull`, `aws s3 sync`, `gsutil rsync`, or `cp -r`. No RPC.

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| 14 | Templates, personas, bundles, pipelines live in the workspace. |
| 15 | Eval specs and bundles live in the workspace; dataset.jsonl files are gitignored by default. |
| **16 (this phase)** | Watches are workspace-managed. Policy is the governance layer over everything. |
| 17 | Plugin manifests come from the workspace by default; user installs override. |
| 18 | The skill teaches the agent that destructive operations need a policy check. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestWorkspaceInit` | `init` creates `.notebooklm-workspace/` with the documented layout. |
| `TestWorkspaceSyncGit` | A `git` backend pulls the latest commit into `~/.notebooklm/<kind>/`. |
| `TestWorkspaceSyncLocal` | A `local` backend copies files; missing source fails with `workspace_not_found`. |
| `TestPolicyEnforceSharing` | `share set-public` is refused under `sharing.allow_set_public: false`. |
| `TestPolicyEnforceGeneration` | The 21st `generate audio` in a day is refused with `policy_violation`. |
| `TestPolicyEnforceSources` | `source add` without `--label` is refused when `required_labels: true`. |
| `TestPolicyOverrides` | A `by_role.admin` rule allows the operation that defaults would refuse. |
| `TestPolicyOverride` | `--override-policy` requires a typed reason; an empty reason is refused. |
| `TestPolicyCountersAtomic` | A crashed counter write leaves the previous count intact. |
| `TestSyncIdempotency` | A second sync with no upstream changes is a no-op. |
| `TestWorkspaceDiff` | `diff` lists every file that has diverged from the workspace. |

### Cassette

A pre-recorded `notebooklm share set-public` against a cassette; the policy wrapper refuses it before any RPC is fired.

### E2E

A live `workspace sync` against a real git remote; `policy show` reflects the workspace's policy; a violating `share set-public` is refused at the CLI.

## Acceptance criteria

1. `notebooklm workspace init` creates `.notebooklm-workspace/` with the documented layout and an example `policy.yaml`.
2. `notebooklm workspace sync` from a git remote materializes the workspace's templates, personas, bundles, pipelines, watches, evals into `~/.notebooklm/`.
3. `notebooklm policy show` reflects the workspace policy and any user overrides.
4. An operation that violates a policy rule is refused at the CLI before any RPC is fired; the error names the rule and its origin.
5. `--override-policy` requires a `--reason`; an empty reason is refused.
6. The MCP `workspace_sync` tool requires `confirm: true`; the `policy_check` tool is read-only.
7. `notebooklm workspace diff` shows every file in `~/.notebooklm/` that has drifted from the workspace.
8. Per-user counters survive `notebooklm upgrade`; a crash mid-counter-write preserves the previous count.
