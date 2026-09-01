# Feature 10 — Plugin system

> Spec part of [docs/15-features-overview.md](../15-features-overview.md).

## Goal

Open the project to third-party extensions without forking. A plugin is a directory under `~/.notebooklm/plugins/` containing a `plugin.yaml` and a single Go binary (or a Python script with a documented shim). Plugins can register new CLI subcommands, new MCP tools, new pipeline step kinds, and new watch kinds. The host process spawns the plugin in a subprocess and communicates over NDJSON over stdin/stdout — the plugin cannot crash the host.

## User stories

1. A community member writes `nb-plugin-jupyter`, a plugin that turns Jupyter notebook cells into sources. They publish it as a Go binary; users install it with `notebooklm plugin install nb-plugin-jupyter`.
2. A team writes an internal plugin that registers a custom pipeline step (`transform-pdf`) used only in their workflows. The plugin is installed via a private S3 bucket via `plugin install --from s3://acme-plugins/nb-pdf.tar.gz`.
3. An operator runs `notebooklm plugin list` and sees every installed plugin with its version, source, and registered commands.

## CLI surface

New top-level group:

```
notebooklm plugin
  list                                            # installed plugins
  install <name> [--from <path-or-url>] [--version <semver>]
  uninstall <name> [--yes]
  enable <name>
  disable <name>
  info <name>
  show <name>                                     # print the manifest
  validate <path>                                 # validate a plugin before install
```

### Plugin manifest

```yaml
# ~/.notebooklm/plugins/<name>/plugin.yaml
name: nb-plugin-jupyter
version: 0.1.0
description: Convert Jupyter notebooks to NotebookLM sources.
author: Jane Developer <jane@example.com>
license: MIT
homepage: https://github.com/jane/nb-plugin-jupyter
min_notebooklm_version: ">=1.1.0"
max_notebooklm_version: "<2.0.0"

entry_point:
  kind: go                       # go | python | shell
  binary: ./bin/nb-plugin-jupyter
  # for kind: python
  # interpreter: python3
  # script: ./plugin.py
  # for kind: shell
  # command: ./run.sh

# what the plugin contributes
contributes:
  cli_commands:
    - name: jupyter
      description: "Convert Jupyter notebooks to sources"
  mcp_tools:
    - name: jupyter_convert
      description: "Convert a Jupyter notebook to a NotebookLM source"
      input_schema: { ... }        # JSON Schema
  pipeline_steps:
    - kind: jupyter-cell          # usable in pipeline YAML as `kind: jupyter-cell`
      description: "Extract a single cell's content as a source"
  watch_kinds:
    - kind: jupyter
      description: "Watch a directory for Jupyter notebooks"
```

### Plugin contract

The host invokes the plugin binary as a subprocess with stdin/stdout pipes. The protocol:

- **NDJSON** — one JSON object per line on each stream.
- **Lifecycle**: host writes `{"op":"init","version":"1.1.0","config":{...}}` to stdin; plugin writes `{"op":"init_ok","manifest":{...}}` to stdout.
- **Per-call**: host writes `{"op":"call","call_id":"...","method":"<method>","args":{...}}`; plugin writes `{"op":"call_result","call_id":"...","result":{...}}` or `{"op":"call_error","call_id":"...","error":{"code":"...","message":"..."}}`.
- **Streaming**: plugin can emit `{"op":"event","call_id":"...","event":"progress","data":{...}}` between `call` and `call_result`.
- **Cancellation**: host closes stdin; plugin must exit within 5 s; if it doesn't, the host SIGKILLs the process.

The plugin cannot import the host's Go packages. Plugins are pure subprocesses; the host exposes everything the plugin needs over the NDJSON contract. This is the **isolation guarantee**: a misbehaving plugin cannot crash the host, cannot leak credentials, and cannot corrupt `~/.notebooklm/`.

### What a plugin can do

| Capability | Mechanism |
|---|---|
| Add new CLI subcommands | Declared in `contributes.cli_commands`; the host routes invocations to the plugin. |
| Add new MCP tools | Declared in `contributes.mcp_tools`; the MCP server exposes them alongside the 33 built-in tools. |
| Add new pipeline step kinds | Declared in `contributes.pipeline_steps`; the pipeline executor calls the plugin at the right DAG position. |
| Add new watch kinds | Declared in `contributes.watch_kinds`; the watcher scheduler calls the plugin on each cycle. |
| Read the user's existing notebooks / sources / chat history | The plugin calls the host via the same NDJSON contract; the host invokes the existing SDK methods on the plugin's behalf. |
| Make its own HTTP calls | Yes, the plugin runs in its own process. |

### What a plugin cannot do

- Modify the host's binary or its files.
- Read `~/.notebooklm/storage_state.json` directly. Plugins must use the NDJSON contract to access any data they need; credentials are never passed through to the plugin.
- Persist state under `~/.notebooklm/` outside its own plugin folder.
- Spawn other plugins.

The NDJSON contract is the only path. The host enforces this by stripping the environment (no `NOTEBOOKLM_HOME`, no `NOTEBOOKLM_AUTH_JSON`) before exec'ing the plugin; the plugin must ask the host for everything it needs.

### Plugin install

`plugin install <name> --from <path-or-url>`:

- `--from ./nb-plugin-jupyter.tar.gz` — local tarball
- `--from https://github.com/jane/nb-plugin-jupyter/releases/download/v0.1.0/nb-plugin-jupyter_0.1.0_linux_amd64.tar.gz` — direct URL
- `--from s3://acme-plugins/nb-pdf/0.2.0/` — S3 (uses the workspace sync backend)
- `--from ~/.notebooklm/plugins/cache/` — local cache from a previous install

Install validates the manifest, checks `min_notebooklm_version`, downloads the binary, places it under `~/.notebooklm/plugins/<name>/`, and runs `plugin validate <path>` against the live binary before recording the install.

### `plugin list` output

```json
{
  "plugins": [
    {
      "name": "nb-plugin-jupyter",
      "version": "0.1.0",
      "enabled": true,
      "source": "https://github.com/jane/..",
      "installed_at": "2026-01-01T10:00:00",
      "contributes": {
        "cli_commands": ["jupyter"],
        "mcp_tools": ["jupyter_convert"],
        "pipeline_steps": ["jupyter-cell"],
        "watch_kinds": ["jupyter"]
      }
    }
  ],
  "count": 1
}
```

## REST surface

| Method | Path | Status | Limiter |
|---|---|---|---|
| `GET` | `/v1/plugins` | 200 | — |
| `POST` | `/v1/plugins/install` | 201 | — |
| `POST` | `/v1/plugins/{name}/enable` | 200 | — |
| `POST` | `/v1/plugins/{name}/disable` | 200 | — |
| `DELETE` | `/v1/plugins/{name}` | 204 | — |

`POST /v1/plugins/install` takes the tarball via `multipart/form-data` or a `from_url` field. Server-side, plugins are scoped per tenant: each tenant sees only its own installs.

## MCP tools

| Name | Purpose | Confirmation |
|---|---|---|
| `plugin_list` | List installed plugins. | no |
| `plugin_install` | Install a plugin. | yes |
| `plugin_enable` | Enable a plugin. | yes |
| `plugin_disable` | Disable a plugin. | yes |
| `plugin_remove` | Uninstall a plugin. | yes |

The MCP server discovers plugins at startup. The 33 built-in tools are always present; the N plugin tools are added by walking `~/.notebooklm/plugins/*/plugin.yaml`. A disabled plugin's tools are hidden from the tool list.

## Public SDK additions

A new namespace `notebooklm.Plugins`:

```go
type PluginManifest struct {
    Name, Version, Description, Author, License, Homepage string
    MinVersion, MaxVersion string
    EntryPoint PluginEntryPoint
    Contributes PluginContributes
}

type Plugins struct{ client *Client }
func (p *Plugins) List(ctx) ([]PluginInfo, error)
func (p *Plugins) Install(ctx, opts PluginInstallOptions) (*PluginInfo, error)
func (p *Plugins) Uninstall(ctx, name string) error
func (p *Plugins) Enable(ctx, name string) error
func (p *Plugins) Disable(ctx, name string) error
func (p *Plugins) Info(ctx, name string) (*PluginInfo, error)
```

The host's internal `PluginManager` is not exposed; only `Plugins.List/Install/...` are. Plugin subprocess invocation is an internal detail.

### Plugin authors: a tiny Go SDK

For plugin authors writing in Go, a `pkg/plugin` package makes the NDJSON contract invisible:

```go
// import "github.com/raihankhan/notebooklm-go/pkg/plugin"

func main() {
    plugin.Serve(&plugin.Manifest{ /* ... */ }, map[string]plugin.Handler{
        "jupyter_convert": func(ctx context.Context, args json.RawMessage) (any, error) {
            // plugin logic
        },
    })
}
```

The SDK handles the init handshake, NDJSON framing, error mapping, and lifecycle. Plugin authors write business logic; the contract is invisible.

## Data model under `~/.notebooklm/`

| Path | Format | Purpose |
|---|---|---|
| `~/.notebooklm/plugins/` | dir | one folder per plugin |
| `~/.notebooklm/plugins/<name>/plugin.yaml` | YAML | the manifest |
| `~/.notebooklm/plugins/<name>/bin/` | dir | the plugin binary |
| `~/.notebooklm/plugins/<name>/state.json` | JSON | per-plugin state (atomic-write) |
| `~/.notebooklm/plugins/<name>/logs/` | dir | per-call logs (redacted) |
| `~/.notebooklm/plugins/index.json` | JSON | `{ plugins: [...] }` |

A disabled plugin stays on disk; `enable`/`disable` toggles a flag in the index. `uninstall --yes` removes the folder.

Atomic writes per `internal/atomicio`. The plugin's state is the plugin's responsibility; the host never modifies it.

## Protocol implications

**No new RPC.** Plugins run in their own subprocesses and communicate with the host over NDJSON. The host uses the existing SDK to answer plugin requests. No new method IDs, no new payload shapes.

The protocol contract is one-directional: the host is the privileged side. Plugins must request access to user data through the contract; they cannot directly hit the network or read files outside their folder.

## Dependencies on Phases 14–18

| Phase | What this feature needs |
|---|---|
| 14–16 | Independent. The plugin contract composes existing operations. |
| **17 (this phase)** | Ships in Phase 17 alongside the REPL. |
| 18 | The skill's pointer section notes that plugins can extend the surface; the agent is not required to know about specific plugins. |

## Tests

### Unit

| Test | What it asserts |
|---|---|
| `TestManifestParse` | Valid manifests parse; invalid shapes are rejected. |
| `TestVersionRange` | `min_notebooklm_version: ">=1.1.0"` is checked at install; a too-old host refuses. |
| `TestPluginInstallLocal` | `install --from ./plugin.tar.gz` extracts, validates, registers. |
| `TestPluginInstallURL` | `install --from https://...` downloads via the configured transport. |
| `TestPluginInstallAtomic` | A crashed install leaves `plugins/index.json` unchanged. |
| `TestPluginEnableDisable` | Disabled plugins do not contribute CLI commands, MCP tools, pipeline steps, or watch kinds. |
| `TestPluginSubprocessContract` | The host speaks NDJSON; a misbehaving plugin (panics, prints garbage) is killed within 5 s. |
| `TestPluginIsolation` | A plugin that crashes does not crash the host; a subsequent operation succeeds. |
| `TestPluginEnvironmentStripped` | `os.Getenv("NOTEBOOKLM_HOME")` returns empty inside the plugin's process. |
| `TestPluginCredentialsNeverPassed` | The plugin's NDJSON stream never contains `storage_state.json`, `at=`, `SID`, or any other credential marker. |
| `TestPluginCallCancellation` | Closing stdin on the host causes the plugin to exit within 5 s; longer-running plugins are SIGKILLed. |
| `TestPluginConcurrency` | 10 concurrent calls to the same plugin run in 10 separate subprocesses (no shared state). |
| `TestPluginCLIRouting` | A user-typed `notebooklm jupyter convert foo.ipynb` is routed to the plugin, not the host. |
| `TestPluginMCPRouting` | An MCP `jupyter_convert` tool call is routed to the plugin. |
| `TestPluginPipelineStep` | A pipeline step `kind: jupyter-cell` invokes the plugin via the NDJSON contract. |
| `TestPluginWatchKind` | A watch with `--source jupyter:/path` invokes the plugin per cycle. |

### Cassette

A scripted plugin binary (a small fixture) is exercised via the NDJSON contract. The host's plugin manager is unit-tested against the fixture; the cassette is irrelevant here because plugins don't speak the RPC protocol.

### E2E

Live install of a real community plugin (`nb-plugin-jupyter` or equivalent). Live invocation via CLI, MCP, pipeline, and watch. Live uninstall + reinstall. Asserts no host state is leaked into the plugin process.

## Acceptance criteria

1. `notebooklm plugin install --from ./plugin.tar.gz` succeeds for a valid plugin and fails with a specific error for each known invalid shape.
2. `notebooklm plugin list` shows every installed plugin with its version, source, and contributed surface.
3. A plugin can register a CLI subcommand, a MCP tool, a pipeline step kind, and a watch kind — each tested end-to-end.
4. A plugin that panics, deadlocks, or prints garbage to stdout is killed within 5 s; the host continues.
5. A plugin's process has no `NOTEBOOKLM_HOME`, no `NOTEBOOKLM_AUTH_JSON`, and no access to `~/.notebooklm/storage_state.json`.
6. Disabling a plugin hides its CLI subcommand, MCP tool, pipeline step kind, and watch kind.
7. The MCP `plugin_list` tool is read-only; `plugin_install`/`plugin_enable`/`plugin_disable`/`plugin_remove` all require `confirm: true`.
8. The Go plugin SDK (`pkg/plugin`) compiles and runs against the documented NDJSON contract.
