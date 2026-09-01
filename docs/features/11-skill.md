# Feature 11 — npx-installable skill bundle for Claude Code and puku-cli

> Spec part of [docs/15-features-overview.md](../15-features-overview.md). The artifact layout (tarball structure, npx package shape, target-detection algorithm) lives in [docs/16-skill-bundle.md](../16-skill-bundle.md). This file is the user-facing and SDK-facing spec.

## Goal

Ship one skill bundle — versioned, installable, statically linkable — that teaches an LLM agent (Claude Code, puku-cli, Codex, Cursor, Claude Desktop) to drive every capability of `notebooklm-go` over either of the three transports (local CLI, local MCP, remote REST). The skill is the user-facing front door for everything in [docs/15-features-overview.md](../15-features-overview.md).

## User stories

1. A solo developer runs `npx @raihankhan/notebooklm-go-skill add` after installing `notebooklm`. Claude Code and puku-cli both learn every command / tool / route in under a minute, no manual config.
2. A team admin runs `notebooklm skill install --target all --scope project` in a git repo. Every team member gets the skill on `git pull`, versioned with the repo.
3. An ops engineer points a remote `notebooklm-server` at claude.ai via the MCP remote connector. The agent installs the skill via `npx @raihankhan/notebooklm-go-skill add --remote-server https://notebooklm.example.com` and the same skill works against the remote server.
4. A user upgrades `notebooklm` to a new release. The CLI nudges them to reinstall the skill with the matching version; mismatch is refused with an actionable error.
5. A maintainer runs `make skill-generate` after editing canonical docs. The committed `SKILL.md` is checked byte-for-byte against the regenerated one in CI; drift fails the build.

## CLI surface

Extends the existing `skill` group from [docs/07-cli-spec.md](../07-cli-spec.md) § "skill":

```
notebooklm skill
  install [--target <claude-code|puku|codex|cursor|desktop|all>]
          [--scope <user|project>]
          [--from <path-or-url>]
          [--allow-version-mismatch]
          [--dry-run] [--force|--no-clobber]
  uninstall [--target ...] [--scope ...] [--purge]
  status [--target ...]
  show [--target <source|installed|both>]
  package -o DIR [--include-binaries]
  generate [--output docs/skill-generated.md]
  check
```

`skill install` is the high-traffic path; the others are covered briefly below.

### `skill install`

| Flag | Default | Meaning |
|---|---|---|
| `--target` | `all` | which agent environments to install into (see [docs/16-skill-bundle.md](../16-skill-bundle.md) § "Target detection") |
| `--scope` | `user` | user scope writes to `~/.claude/`, `~/.puku/`, etc.; project scope writes to `./.claude/`, `./.puku/`, etc. relative to CWD |
| `--from` | bundled source | install from a local path or `https://…/notebooklm-skill.tar.gz` URL instead of the bundled version |
| `--allow-version-mismatch` | `false` | skip the `SKILL.md` VERSION ↔ CLI version check |
| `--dry-run` | `false` | print the file tree that would be written; do not write |
| `--force` / `--no-clobber` | `false` (interactive prompt) | overwrite / refuse-overwrite policy |

`--json` envelope on success:

```json
{
  "installed": [
    {"target": "claude-code", "destination": "/home/u/.claude/skills/notebooklm", "files": 7},
    {"target": "puku",        "destination": "/home/u/.puku/skills/notebooklm",   "files": 7},
    {"target": "claude-desktop", "destination": "/home/u/.config/Claude", "files": 1}
  ],
  "version": "0.8.1",
  "scope": "user",
  "success": true
}
```

Exit codes: `0` success; `1` install error (bad target, permission denied); `2` version mismatch (when `--allow-version-mismatch` was not passed); `130` SIGINT.

### `skill uninstall`

Removes every file the installer wrote (tracked by the `<!-- managed by notebooklm-skill-installer -->` header); refuses to touch user files. `--purge` also deletes the destination directory if empty afterwards.

### `skill status`

Prints the version + destination of every detected installation:

```
claude-code   0.8.1  /home/u/.claude/skills/notebooklm  (up to date)
puku          0.8.1  /home/u/.puku/skills/notebooklm    (up to date)
claude-desktop 0.8.1 /home/u/.config/Claude             (MCP server only)
```

`--json` returns the same as an array; missing installs are reported with `installed: false`, never as an error (status is informational, not actionable on its own).

### `skill show`

- `show --target source` — prints the canonical `SKILL.md` and the surrounding bundle layout as it would be installed.
- `show --target installed` — prints whatever is currently on disk at every detected destination, byte-for-byte.
- `show --target both` — diffs the source against the installed bundle and prints a unified diff. Useful for "did my upgrade actually update?" questions.

### `skill package`

Builds `notebooklm-skill.tar.gz` from the source tree. `--include-binaries` puts the per-platform `notebooklm-skill-installer` binaries inside the `tools/` directory so the tarball can be installed without an external download.

### `skill generate` / `skill check`

Makefile target equivalents, exposed for debugging:

- `skill generate --output PATH` — runs the bundle generator against canonical sources.
- `skill check` — asserts the committed `SKILL.md` is byte-identical to the regenerated one; exits 1 on drift.

## REST surface

New routes under `/v1/`, following [docs/09-rest-spec.md](../09-rest-spec.md) conventions.

| Method | Path | Status | Limiter |
|---|---|---|---|
| `GET` | `/v1/skill` | 200 | — |
| `POST` | `/v1/skill/install` | 200 | — |
| `POST` | `/v1/skill/install/preview` | 200 | — |
| `POST` | `/v1/skill/uninstall` | 200 | — |
| `POST` | `/v1/skill/package` | 200 | — |

`GET /v1/skill` returns the source bundle layout and the same `status` array as the CLI, JSON-encoded. `POST /v1/skill/install` requires `confirm: true` if the installation would change existing files. `/install/preview` is the dry-run variant.

Auth: bearer required. Destructive install/uninstall operations require an operator-scoped token (see the operators-vs-readers token split this design proposes for v1.1+).

## MCP tools

Three new tools, all read-only or operator-only. Tool names use the same `snake_case` style as the existing 33 tools in [docs/08-mcp-spec.md](../08-mcp-spec.md):

| Name | Purpose | Confirmation required |
|---|---|---|
| `skill_status` | List detected installations and their versions. | no |
| `skill_install` | Install the skill into one or more targets. | yes |
| `skill_uninstall` | Remove an installation. | yes |

Schema sketch (illustrative):

```json
{
  "name": "skill_install",
  "description": "Install the NotebookLM skill bundle into one or more agent environments.",
  "input": {
    "type": "object",
    "properties": {
      "targets": {"type": "array", "items": {"enum": ["claude-code", "puku", "codex", "cursor", "desktop"]}},
      "scope":   {"enum": ["user", "project"], "default": "user"},
      "from":    {"type": "string"},
      "allow_version_mismatch": {"type": "boolean", "default": false},
      "dry_run":  {"type": "boolean", "default": false},
      "confirm":  {"type": "boolean"}
    },
    "required": ["targets", "confirm"]
  }
}
```

## Public SDK additions

A new `notebooklm` namespace:

```go
// notebooklm/skill.go
package notebooklm

type SkillBundle struct { /* parsed SKILL.md frontmatter + bundle layout */ }

type SkillTarget struct {
    Name        string
    Destination string
    Skill       bool
}

type SkillInstallOptions struct {
    Targets               []string
    Scope                 string
    From                  string
    AllowVersionMismatch  bool
    DryRun                bool
    Force                 bool
    NoClobber             bool
}

type SkillStatus struct {
    Target     string `json:"target"`
    Version    string `json:"version"`
    Destination string `json:"destination"`
    UpToDate   bool   `json:"up_to_date"`
    Installed  bool   `json:"installed"`
}

type Skill struct {
    client *Client
}

func (s *Skill) Install(ctx context.Context, opts SkillInstallOptions) (Result, error)
func (s *Skill) Uninstall(ctx context.Context, targets []string) (Result, error)
func (s *Skill) Status(ctx context.Context) ([]SkillStatus, error)
func (s *Skill) Show(ctx context.Context, target string) (string, error)
func (s *Skill) Package(ctx context.Context, dir string) (string, error)
```

The methods are thin wrappers over the CLI today; once `internal/app/skill` ships, they hit it directly. No new domain types — the wire surface is the bundle, which is bytes on disk.

## Data model under `~/.notebooklm/`

The skill bundle itself writes *into* the user's agent config (`~/.claude/skills/notebooklm/`, `~/.puku/skills/notebooklm/`, etc.), which is not owned by `notebooklm-go`. The SDK-level state under `~/.notebooklm/` is:

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/skill-state.json` | JSON | which (target, destination) pairs `notebooklm skill install` last wrote; used by `uninstall` to know what to remove. `0700` dir, `0600` file. |
| `~/.notebooklm/skill-cache/` | directory | transient: downloaded bundle tarballs awaiting install. Cleared after install completes. |

Atomic-write semantics via `internal/atomicio` (temp + chmod 0600 + sync + rename). No migrations needed for v1 — the state file is the single source of truth and is recreated on first use.

## Protocol implications

**No new RPC.** This feature composes the existing CLI / MCP / REST surfaces and writes files to disk. Every step in `skill install` is either a local operation (filesystem, tarball extraction) or a wrapper over an existing transport. There is no need to add anything to `internal/web/wire/methods.go` or any payload builder.

The MCP server already exposes `auth check`, `notebook_list`, etc.; the new skill tools simply route to the same `notebooklm.Skill` SDK namespace.

Cited reusable RPCs (none new): the skill surface is purely additive over the v1 transport surface; nothing in `../notebooklm-py/src/notebooklm/_web/params/` is reused because no RPC is involved.

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| 14 | None directly; the skill's capability parity table references feature #1 (pipelines), #2 (cross-notebook), #4 (templates), but install + detect work standalone. |
| 15 | The local search index surfaces through MCP in this skill (read-only). |
| 16 | The workspace feature (#7) is referenced by the skill's pointer section. |
| 17 | The plugin feature (#10) is referenced by the skill's pointer section; the REPL (#9) is also surfaced through the skill. |
| **18 (this phase)** | The skill ships in Phase 18. |

The skill is always the last feature to ship because it is the front door for everything else.

## Tests

Three tiers, per [docs/11-testing-strategy.md](../11-testing-strategy.md).

### Unit

| Test | What it asserts |
|---|---|
| `TestDetectTargets` (table) | Each target in / out; env-var override of `NOTEBOOKLM_PUKU_HOME`; `claude-desktop` OS-specific paths (POSIX + Windows skip-gated). |
| `TestPukuSkillsPathDrift` | Locks in the puku skill path against drift; fails loud when puku docs change. |
| `TestSkillInstallRoundTrip` | `Install` then `Uninstall` leaves no `notebooklm-skill-installer`-tagged files behind. |
| `TestSkillVersionMismatch` | Installing a bundle whose `VERSION` ≠ CLI version fails with exit 2 + actionable message; `--allow-version-mismatch` opts through. |
| `TestSkillGenerate` | `skill generate` against a synthetic input set produces an output that matches a golden fixture. |
| `TestSkillCheck` | Committed `SKILL.md` equals regenerated file. |
| `TestIdempotentInstall` | Running install twice is a no-op on the second run. |
| `TestNoClobberRefuses` | `--no-clobber` refuses to change existing files. |
| `TestTarballRoundTrip` | Extract tarball → file tree matches `dist/manifest.json` exactly. |

### Cassette

`notebooklm-mcp` MCP server's `skill_status` and `skill_install` tools exercised against a cassette pair: one for the read path (no auth required beyond bearer), one for a destructive install that requires `confirm: true`. Asserts:

- `status` returns the expected JSON shape with the cassette's recorded status.
- `install` without `confirm` returns the standardized confirmation-required error.
- `install` with `confirm: true` succeeds and writes the expected bundle tarball extraction (verified via a fixture, not against a live agent home).

### E2E

Gated on `NOTEBOOKLM_E2E=1` and a real Google profile. One live run per matrix cell:

| Combination | What it verifies |
|---|---|
| `npx … add` + Claude Code + local CLI | Claude Code drives `notebook_list` via the shell, gets real notebooks. |
| `npx … add` + Claude Code + local MCP | Claude Code drives `notebook_list` via MCP stdio, gets real notebooks. |
| `npx … add --remote-server URL` + Claude Code + remote REST | Claude Code drives `notebook_list` via HTTPS bearer, gets real notebooks. |
| `npx … add` + puku-cli + local MCP | puku-cli drives `notebook_list` via MCP stdio, gets real notebooks. |
| `notebooklm skill install` (no npx, no Node) + Claude Code | Same as the first cell, proving the Go-only path is equivalent. |

A puku-cli drift check: read the puku docs in force at the time of release, run `TestPukuSkillsPathDrift`, fail the release if it does not match.

## Acceptance criteria

A reviewer can verify each of the following from a fresh checkout, against a real Google account:

1. `make skill-generate && make skill-check` exits 0.
2. `npx @raihankhan/notebooklm-go-skill add --target all` exits 0 with the documented `--json` envelope.
3. Claude Code, after install, can drive `notebook_list` and `notebook_create` successfully via the skill.
4. puku-cli, after install, can drive `notebook_list` successfully via the skill.
5. A second `npx … add` is a no-op (no files changed, exit 0).
6. `npx … add` with a `VERSION` ≠ CLI version fails with exit code 2 and the documented error message.
7. `notebooklm skill uninstall --target all` removes every file the installer wrote; user-authored files in the same directories are untouched.
8. The bundled `manifest.json` validates against the Claude Desktop bundle schema (`internal/tools/mcpbcheck`).
9. `notebooklm upgrade` re-prompts the user to reinstall the skill with the matching new version; mismatch is refused with the documented error.
10. `make skill-build` produces six working binaries (`darwin-amd64`, `darwin-arm64`, `linux-amd64`, `linux-arm64`, `windows-amd64`, `windows-arm64`), each under 25 MB.
