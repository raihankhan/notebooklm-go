# 16 — Skill bundle directory layout

The skill bundle is the user-facing front door for every feature in [docs/15-features-overview.md](15-features-overview.md). It is the document an agent (Claude Code, puku-cli, Codex, Cursor, Claude Desktop) reads to know what `notebooklm-go` can do and how to drive it. This document specifies the on-disk shape of that artifact, the npx-installable npm package that ships it, and the target-detection logic that decides where the bundle lands on a given machine.

The user-facing surface (CLI commands, MCP tools, REST routes) is described in [docs/features/11-skill.md](features/11-skill.md). This document is the **artifact spec** — what files exist, what their contents are, and how the install path decides what to do.

## Bundle layout

Every skill bundle ships as a single tarball with the following layout. The tarball is identical regardless of how it is installed (npx, `notebooklm skill install`, `.mcpb`, or manual `tar -xf`):

```
notebooklm-skill/
├── SKILL.md                           # the canonical skill doc (generated at release)
├── VERSION                            # semver, matches notebooklm-go release tag
├── manifest.json                      # MCP-bundle manifest (manifest_version 0.3)
├── references/
│   ├── cli-cheatsheet.md              # generated from `notebooklm --help` at build time
│   ├── mcp-tools.md                   # 33 existing tools + new ones, names + signatures
│   ├── rest-api.md                    # 43 existing routes + new ones, with curl examples
│   ├── workflows.md                   # recipes (research→podcast, bulk-import, eval loop, …)
│   └── troubleshooting.md
├── examples/
│   ├── pipeline.yaml                  # example pipelines (#1)
│   ├── eval-spec.yaml                 # example eval harness spec (#6)
│   ├── workspace/                     # example workspace layout (#7)
│   └── templates/
│       ├── podcast-deep-dive.md       # example templates (#4)
│       └── study-guide.md
├── tools/
│   ├── install.sh                     # POSIX installer
│   ├── install.ps1                    # Windows installer
│   └── detect.go                      # env-detection logic (also built into the binary)
└── README.md                          # human-readable installation guide
```

### SKILL.md

Generated from canonical sources by a Makefile target. Inputs and ordering:

1. `docs/AGENTS.md` (the seven rules + the new "Feature additions" section)
2. `docs/15-features-overview.md` (this overview)
3. `docs/07-cli-spec.md` (CLI command tree, exit codes, theme)
4. `docs/08-mcp-spec.md` (MCP tool reference)
5. `docs/09-rest-spec.md` (REST route reference)
6. `docs/16-skill-bundle.md` (this document)

The generator (`cmd/skill-generate/main.go`) renders these into a single Markdown file with four top-level sections, in this order — agents must encounter the safety preamble before any command list:

1. **Transport selection** — teach the agent to pick CLI vs MCP vs REST.
2. **Safety rules** — destructive ops need `--yes` / `confirm: true`; JSON stdout is parseable; auth lives in `~/.notebooklm/`; etc.
3. **Capability parity table** — every CLI command, MCP tool, and REST route, by name.
4. **Feature index** — pointers to the eleven features in [docs/15-features-overview.md](15-features-overview.md), with one-line summaries.

The Makefile target `make skill-generate` writes `docs/skill-generated.md`; `make skill-check` asserts that the committed `SKILL.md` is byte-identical. CI fails if the committed file drifts from the generated file.

### VERSION

Semver, matching the `notebooklm-go` release tag (e.g., `0.8.1`, `1.0.0`, `1.1.0`). The installer refuses to install a bundle whose `VERSION` does not match the running `notebooklm` CLI's reported version, with an actionable error message.

### manifest.json

MCP bundle manifest. Normative source: `../notebooklm-py/desktop-extension/manifest.json`. Required fields:

- `manifest_version`: `"0.3"` (Claude Desktop's bundle format)
- `name`: `"notebooklm-mcp"`
- `display_name`: `"NotebookLM (notebooklm-go)"`
- `version`: matches `VERSION`
- `description`: one-line summary
- `long_description`: multi-paragraph feature list with prerequisites
- `author`: from `LICENSE` headers
- `server.type`: `"go"` (new — Python original uses `"python"`)
- `server.entry_point`: `"run_server.sh"` / `"run_server.cmd"` / `"run_server.exe"` (per-platform selection by the installer)
- `server.mcp_config`: per-platform command + args, with `platform_overrides.win32`
- `tools`: hand-curated list of the high-traffic tools (the rest are discovered at runtime); mirrors the Python manifest's `tools` array
- `tools_generated`: `true`
- `keywords`: includes `"notebooklm"`, `"google"`, `"podcast"`, `"research"`, `"notebook"`, `"ai"`, plus `"mcp-bundle"`
- `license`: `"MIT"`
- `compatibility.claude_desktop`: `">=0.10.0"`
- `compatibility.platforms`: `["darwin", "win32", "linux"]`
- `compatibility.runtimes`: `{"go": ">=1.22"}` (replaces `python: ">=3.10"`)

### references/

Generated Markdown files. The `cli-cheatsheet.md` is regenerated from `notebooklm --help`; the `mcp-tools.md` and `rest-api.md` are generated from the source files (`internal/mcpsrv/*` and `internal/restsrv/*` schema annotations) at build time. `workflows.md` and `troubleshooting.md` are hand-edited.

### examples/

Hand-curated YAML / Markdown that demonstrates idiomatic use of each feature. Versioned with the bundle (not the docs) so that an installed bundle always has working examples even if the agent's docs view is stale.

### tools/install.sh / install.ps1

Cross-shell installers. Each accepts `--target <claude-code|puku|codex|cursor|desktop|all>` and `--scope <user|project>`. The Go binary `cmd/notebooklm-skill-installer` is the canonical implementation; the shell scripts are thin shims that invoke it (see "npx package layout" below).

### tools/detect.go

The env-detection logic. Lives in the bundle as Go source so a user can read it without invoking the installer; the same code is compiled into the `notebooklm-skill-installer` binary for production installs.

The detector (detailed below in "Target detection") walks the filesystem and a few env vars to decide which agent environments are present.

## npx package layout

The npm package `@raihankhan/notebooklm-go-skill` ships a Node.js shim that **does not run any Node code at runtime** — it resolves to a per-platform static binary that does the actual work. This keeps the install footprint small (the binary, not the Node runtime) and lets the package target macOS / Linux / Windows across amd64 and arm64.

```
notebooklm-go-skill/
├── package.json
├── README.md
├── bin/
│   └── notebooklm-skill-installer.js       # the npx entrypoint
└── dist/                                   # populated by the GitHub release
    ├── notebooklm-skill-installer-darwin-amd64
    ├── notebooklm-skill-installer-darwin-arm64
    ├── notebooklm-skill-installer-linux-amd64
    ├── notebooklm-skill-installer-linux-arm64
    ├── notebooklm-skill-installer-windows-amd64.exe
    └── notebooklm-skill-installer-windows-arm64.exe
```

### package.json

The shape follows the standard "npx downloads a binary" pattern. Highlights:

```json
{
  "name": "@raihankhan/notebooklm-go-skill",
  "version": "0.8.1",
  "description": "Install the NotebookLM (notebooklm-go) skill for Claude Code, puku-cli, Codex, and other MCP-aware agents.",
  "bin": {
    "notebooklm-skill-installer": "bin/notebooklm-skill-installer.js"
  },
  "scripts": { "postinstall": "node ./bin/notebooklm-skill-installer.js --self-check" },
  "engines": { "node": ">=18" },
  "os": ["darwin", "linux", "win32"],
  "cpu": ["x64", "arm64"],
  "optionalDependencies": {
    "@raihankhan/notebooklm-go-skill-darwin-amd64":  "0.8.1",
    "@raihankhan/notebooklm-go-skill-darwin-arm64":  "0.8.1",
    "@raihankhan/notebooklm-go-skill-linux-amd64":   "0.8.1",
    "@raihankhan/notebooklm-go-skill-linux-arm64":   "0.8.1",
    "@raihankhan/notebooklm-go-skill-win32-amd64":   "0.8.1",
    "@raihankhan/notebooklm-go-skill-win32-arm64":   "0.8.1"
  }
}
```

Each per-platform optional dependency is a single-file package containing one binary. The Node entrypoint picks the right one based on `process.platform` + `process.arch` and execs it.

### bin/notebooklm-skill-installer.js

~50 lines of Node. Reads `process.platform` and `process.arch`, requires the matching optional dependency to obtain its binary path, sets the binary executable on POSIX, and `child_process.spawn`s it with `stdio: 'inherit'` so the installer's progress, prompts, and error messages pass through unchanged. Diagnostic output goes to stderr; stdout stays byte-clean.

The shim **never** parses CLI args or touches the filesystem itself — all logic lives in the Go binary. This keeps the entire npm package auditable in two files.

### Why this shape

The Python original uses `npx --package "notebooklm-py[mcp]" -- notebooklm-mcp` — uvx resolves the package and runs its console script. The Go port cannot do that (no equivalent of a single-binary console script entrypoint in npm). The `optionalDependencies` pattern is the npm-idiomatic equivalent: `npm` already knows how to download the right binary for the user's platform; the Node shim just execs it.

The result: `npx @raihankhan/notebooklm-go-skill` works on macOS, Linux, and Windows across amd64 and arm64 with **zero** Node runtime dependency beyond the shim. No Python, no uv, no extra runtime on the user's machine.

## Target detection

The `detect.go` (and the compiled `notebooklm-skill-installer`) decides which agent environments to install into. Detection is best-effort: a missing directory does not fail the install; it skips that target.

### Detection table

| Target | Marker(s) | Install destination | Notes |
|---|---|---|---|
| **Claude Code** | `~/.claude/` exists (file or dir) | `~/.claude/skills/notebooklm/SKILL.md` (and the rest of the bundle) | The primary path. |
| **puku-cli** | `~/.puku/` exists, **or** `NOTEBOOKLM_PUKU_HOME` env var, **or** a `~/.config/puku/` fallback | `~/.puku/skills/notebooklm/` (or `${NOTEBOOKLM_PUKU_HOME}/skills/notebooklm/`) | The exact path puku reads is verified at implementation time against the puku docs in force then; see "puku-cli convention" below. |
| **Codex** | `~/.codex/` exists | `~/.codex/skills/notebooklm/` (project scope only when run from a repo root with a `.agents/` dir; also writes `.agents/skills/notebooklm/` if `--scope project`) | Mirrors the Python original's `~/.agents/skills/notebooklm/` dual-target behaviour. |
| **Cursor** | `~/.cursor/` exists | `~/.cursor/skills/notebooklm/` | Cursor scans its own directory tree. |
| **Claude Desktop** | OS-specific path exists (macOS: `~/Library/Application Support/Claude/`, Linux: `~/.config/Claude/`, Windows: `%APPDATA%\Claude\`) | That directory (the installer writes `claude_desktop_config.json` snippets into the existing config and **does not** drop a skill directory — Claude Desktop is MCP-only, not skill-aware) | Only the MCP server is wired, never the SKILL.md. |

### Detection algorithm

```go
// internal/app/skillinstall/detect.go
package skillinstall

import (
    "os"
    "path/filepath"
    "runtime"
)

type Target struct {
    Name        string  // "claude-code" | "puku" | "codex" | "cursor" | "claude-desktop"
    Destination string  // absolute path to write into
    Skill       bool    // true for Claude Code / puku / Codex / Cursor; false for Claude Desktop
}

func DetectTargets(home string, env []string) []Target {
    var out []Target

    if exists(filepath.Join(home, ".claude")) {
        out = append(out, Target{Name: "claude-code",
            Destination: filepath.Join(home, ".claude", "skills", "notebooklm"),
            Skill:       true})
    }

    pukuHome := os.Getenv("NOTEBOOKLM_PUKU_HOME")
    if pukuHome == "" {
        for _, kv := range env {
            if strings.HasPrefix(kv, "NOTEBOOKLM_PUKU_HOME=") {
                pukuHome = strings.TrimPrefix(kv, "NOTEBOOKLM_PUKU_HOME=")
                break
            }
        }
    }
    pukuBase := pukuHome
    if pukuBase == "" {
        pukuBase = filepath.Join(home, ".puku")
    }
    if exists(pukuBase) {
        out = append(out, Target{Name: "puku",
            Destination: filepath.Join(pukuBase, "skills", "notebooklm"),
            Skill:       true})
    }

    if exists(filepath.Join(home, ".codex")) {
        out = append(out, Target{Name: "codex",
            Destination: filepath.Join(home, ".codex", "skills", "notebooklm"),
            Skill:       true})
    }
    if exists(filepath.Join(home, ".cursor")) {
        out = append(out, Target{Name: "cursor",
            Destination: filepath.Join(home, ".cursor", "skills", "notebooklm"),
            Skill:       true})
    }

    if cd := claudeDesktopConfigDir(); cd != "" {
        out = append(out, Target{Name: "claude-desktop",
            Destination: cd,
            Skill:       false})
    }

    return out
}

func claudeDesktopConfigDir() string {
    switch runtime.GOOS {
    case "darwin":
        p := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Claude")
        if exists(p) {
            return p
        }
    case "windows":
        if app := os.Getenv("APPDATA"); app != "" {
            p := filepath.Join(app, "Claude")
            if exists(p) {
                return p
            }
        }
    default: // linux + bsd
        if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
            p := filepath.Join(x, "Claude")
            if exists(p) {
                return p
            }
        }
        if h := os.Getenv("HOME"); h != "" {
            p := filepath.Join(h, ".config", "Claude")
            if exists(p) {
                return p
            }
        }
    }
    return ""
}
```

Each target gets a unit test with a fixture directory tree (using `t.TempDir()` to simulate `~/.claude`, `~/.puku`, etc.) asserting that `DetectTargets` returns the expected slice.

### `--target` flag

The installer's `--target` flag narrows the auto-detected set:

```bash
notebooklm-skill-installer --target claude-code
notebooklm-skill-installer --target puku
notebooklm-skill-installer --target all      # install everywhere detected
```

When `--target` is omitted, the installer installs into every detected target. When `--target` is given and the target is not detected, the installer fails with an actionable error suggesting how to create the marker directory (or pointing at the `--force` flag).

### `--scope` flag

```bash
notebooklm-skill-installer --scope user      # ~/.claude/, ~/.puku/, etc. (default)
notebooklm-skill-installer --scope project   # ./.claude/, ./.puku/, etc., relative to CWD
```

Project scope requires write access to the CWD. The installer refuses to walk up the directory tree in search of a repo root (matches the existing `agent show codex` behaviour from `docs/07-cli-spec.md`).

## puku-cli convention

The exact path puku-cli reads for skills **must** be verified at Phase 18 implementation time. The puku-cli documentation in force at that time is read in full, the path is locked into `internal/app/skillinstall/detect.go` as the constant `pukuSkillsPath`, and a unit test pins the convention:

```go
// TestPukuSkillsPathDrift asserts that puku's documented skill-discovery
// path matches what the installer writes to. If puku changes its convention,
// this test must be updated consciously, not silently.
func TestPukuSkillsPathDrift(t *testing.T) {
    pukuHome := t.TempDir()
    os.Setenv("NOTEBOOKLM_PUKU_HOME", pukuHome)
    defer os.Unsetenv("NOTEBOOKLM_PUKU_HOME")

    targets := skillinstall.DetectTargets(pukuHome, nil)
    var got string
    for _, t := range targets {
        if t.Name == "puku" {
            got = t.Destination
        }
    }
    want := filepath.Join(pukuHome, "skills", "notebooklm")
    if got != want {
        t.Fatalf("puku skills path drifted: got %q, want %q (puku docs may have changed)", got, want)
    }
}
```

The test fails fast when puku changes its convention. Updating the test is an explicit, reviewable action — not a silent acceptance.

## Version matching

The installer refuses to install a bundle whose `VERSION` field does not match the running `notebooklm` CLI's version:

```bash
$ notebooklm-skill-installer
Error: bundle version 0.8.1 does not match notebooklm CLI version 0.8.0.
Run `notebooklm upgrade` or download the matching bundle from
https://github.com/raihankhan/notebooklm-go/releases.
```

This catches the case where a user updates their `notebooklm` CLI without re-installing the skill (or vice versa). The check is opt-out via `--allow-version-mismatch` for advanced workflows (e.g., testing a beta bundle against a stable CLI).

## Idempotency

The installer is idempotent. A second invocation:

- Overwrites files whose content has changed (byte-comparison, not mtime).
- Leaves untouched files whose content is identical.
- Reports `--dry-run` output under the `--dry-run` flag.
- Fails under `--no-clobber` if any file would change.

Uninstall (`notebooklm-skill-installer uninstall` or `notebooklm skill uninstall`) is also idempotent and refuses to remove files the installer did not write (signature comment at the top of every file: `<!-- managed by notebooklm-skill-installer -->`).

## Build / release wiring

| Step | Output |
|---|---|
| `make skill-build` | `dist/notebooklm-skill-installer-<goos>-<goarch>[.exe]` (6 platform binaries) |
| `make skill-package` | `notebooklm-skill.tar.gz` (the bundle, without the binaries) |
| `make skill-npm` | the npm package directory, ready to publish |
| `make skill-generate` | regenerates `docs/skill-generated.md` from canonical sources |
| `make skill-check` | asserts the committed `SKILL.md` equals the generated one |

Release CI:

- On every release tag, build the 6 platform binaries, sign them, attach to the GitHub release.
- Publish the per-platform npm `@scope/name-<os>-<arch>` packages.
- Publish the umbrella `@raihankhan/notebooklm-go-skill` package.
- Run `make skill-check` as a CI step before publishing; the release is blocked if the generated `SKILL.md` does not match the committed one.

## Tests

Three tiers, per `docs/11-testing-strategy.md`:

| Tier | Test |
|---|---|
| Unit | `DetectTargets` table-driven test (each target on / off, env var override, Windows / POSIX paths). `TestPukuSkillsPathDrift` (above). Round-trip install → uninstall: every file removed, no extras left behind. Bundle extraction test: tarball → on-disk tree matches `dist/manifest.json`. |
| Cassette | `notebooklm-mcp` stdio transcript against a scripted agent session; assert every stdout byte is framed JSON-RPC; install path produces a bundle the cassette replays cleanly. |
| E2E | Live smoke: `npx @raihankhan/notebooklm-go-skill add --target claude-code`, launch Claude Code, drive the `notebook_list` MCP tool, verify it returns real notebooks. Same for puku-cli. Same against a remote `notebooklm-server` over HTTPS. |

## Acceptance criteria

- `npx @raihankhan/notebooklm-go-skill add --target all` installs into every detected environment and exits 0.
- The same command run twice is a no-op (no files changed, exit 0).
- `npx @raihankhan/notebooklm-go-skill add` against an incompatible bundle version prints the version-mismatch error and exits non-zero.
- `SKILL.md` is regenerated by `make skill-generate` from canonical sources; the committed file is byte-identical to the regenerated one (CI fails on drift).
- Claude Code, puku-cli, Codex, Cursor, and Claude Desktop each successfully drive at least one read tool and one gated destructive tool after install.
- A user can `tar -xf notebooklm-skill.tar.gz -C ~/.claude/skills/notebooklm/` and get the same on-disk shape as `npx` would have produced.
- A `notebooklm upgrade` run after a CLI version bump re-prompts the user to reinstall the skill with the new version.