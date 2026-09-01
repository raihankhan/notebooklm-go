# 13 — Operations: configuration, packaging, deployment

## Environment variables

Complete set, ported from `../notebooklm-py/docs/configuration.md`. Names must match — a
user's existing systemd unit or Docker env file must keep working.

### Core

| Variable | Default | Purpose |
|---|---|---|
| `NOTEBOOKLM_HOME` | `~/.notebooklm` | base directory for all config and credentials |
| `NOTEBOOKLM_PROFILE` | `default` | active profile name |
| `NOTEBOOKLM_NOTEBOOK` | — | default notebook id for any `-n`-accepting command |
| `NOTEBOOKLM_AUTH_JSON` | — | inline `storage_state` JSON (CI; no file writes) |
| `NOTEBOOKLM_BASE_URL` | `https://notebook.google.com` | app host; **allowlisted to three values** |
| `NOTEBOOKLM_BACKEND` | `web` | namespace backend |
| `NOTEBOOKLM_HL` | `en` | output language for artifacts and the `hl` query param |
| `NOTEBOOKLM_BL` | pinned constant | frontend build label for the chat endpoint |

### Logging and diagnostics

| Variable | Purpose |
|---|---|
| `NOTEBOOKLM_LOG_LEVEL` | `DEBUG` / `INFO` / `WARNING` / `ERROR` |
| `NOTEBOOKLM_DEBUG` | `1` — include untruncated response previews in errors. **Prints credentials-adjacent data; never enable in production.** |
| `NOTEBOOKLM_DEBUG_RPC` | `1` — RPC-level debug logging |
| `NOTEBOOKLM_QUIET_DEPRECATIONS` | `1` — suppress deprecation notices |
| `NOTEBOOKLM_STRICT_DECODE` | reserved; strict is the only mode |
| `NOTEBOOKLM_FUTURE_ERRORS` | `1` — opt into next-major error behavior early |

### Protocol overrides

| Variable | Purpose |
|---|---|
| `NOTEBOOKLM_RPC_OVERRIDES` | `Name=id` pairs (comma/newline separated) or a JSON file path. **The escape hatch when Google changes a method id.** |
| `NOTEBOOKLM_TRANSPORT` | `net` (default) or `utls` (v1.1, JA3 impersonation) |

### Auth

| Variable | Purpose |
|---|---|
| `NOTEBOOKLM_DISABLE_KEEPALIVE_POKE` | `1` — disable the `RotateCookies` keepalive |
| `NOTEBOOKLM_HEADLESS_REAUTH` | `1` — enable the L3 rung |
| `NOTEBOOKLM_HEADLESS_REAUTH_CDP_URL` | attach L3 to a running Chrome instead of the dedicated profile |
| `NOTEBOOKLM_REFRESH_CMD` | operator command for the L2.5 rung |
| `NOTEBOOKLM_REFRESH_CMD_MIDSESSION` | `1` — allow L2.5 mid-session (default: cold start only) |
| `NOTEBOOKLM_REFRESH_CMD_USE_SHELL` | run the refresh command through a shell |
| `NOTEBOOKLM_REFRESH_CMD_LOG_OUTPUT` | log the command's output (**may contain credentials**) |
| `NOTEBOOKLM_REFRESH_PROFILE` | profile the refresh command targets |
| `NOTEBOOKLM_REFRESH_STORAGE_PATH` | storage path the refresh command targets |
| `NOTEBOOKLM_PROMOTION_EXIT_TIMEOUT` | bounded drain for the legacy-account promotion goroutine |

### MCP server

| Variable | Default | Purpose |
|---|---|---|
| `NOTEBOOKLM_MCP_TOKEN` | — | HTTP bearer. **Env-only, no flag** — cannot leak via `ps aux`. |
| `NOTEBOOKLM_MCP_TRANSPORT` | `stdio` | |
| `NOTEBOOKLM_MCP_HOST` | `127.0.0.1` | |
| `NOTEBOOKLM_MCP_PORT` | `9420` | |
| `NOTEBOOKLM_MCP_ALLOW_EXTERNAL_BIND` | unset | required (with a token) for a non-loopback bind |
| `NOTEBOOKLM_MCP_TRUST_PROXY` | unset | honor `X-Forwarded-*` behind a tunnel |
| `NOTEBOOKLM_MCP_PUBLIC_URL` | — | public URL for OAuth metadata (v1.1) |
| `NOTEBOOKLM_MCP_OAUTH_BASE_URL` | — | v1.1 |
| `NOTEBOOKLM_MCP_OAUTH_PASSWORD` | — | v1.1 |
| `NOTEBOOKLM_MCP_STRICT_IDS` | unset | reject partial-prefix ids; require full ids |

### REST server

See doc 09 for the full table: `NOTEBOOKLM_SERVER_TOKEN` (required), `_HOST`, `_PORT`,
`_ALLOW_EXTERNAL_BIND`, and the six `_*_CONCURRENCY` limiters.

## Precedence

```
CLI flag  >  environment variable  >  config.json  >  built-in default
```

Two exceptions, both ported:

- Auth source: `--storage` > `NOTEBOOKLM_AUTH_JSON` > active profile storage.
- Notebook: `-n` > `NOTEBOOKLM_NOTEBOOK` > active context (`use`) > error.

Empty and whitespace-only env values count as **unset**, not as an empty override.

## `config.json`

```json
{ "language": "en", "default_profile": "default" }
```

Read with an mtime cache. Written atomically. A malformed file is a `CONFIG_ERROR` naming the
path and the parse position — never silently ignored, because "my language setting does
nothing" is a miserable thing to debug.

## Build and packaging

### Local

```bash
make build      # → ./bin/{notebooklm,notebooklm-mcp,notebooklm-server}
```

```makefile
LDFLAGS := -s -w \
  -X github.com/raihankhan/notebooklm-go/internal/buildinfo.Version=$(VERSION) \
  -X github.com/raihankhan/notebooklm-go/internal/buildinfo.Commit=$(COMMIT) \
  -X github.com/raihankhan/notebooklm-go/internal/buildinfo.Date=$(DATE)
```

`CGO_ENABLED=0` for every target. `kooky` is pure Go; `chromedp` speaks CDP over a socket
and needs no cgo.

### Release matrix

`goreleaser`, six platform pairs:

| GOOS | GOARCH |
|---|---|
| linux | amd64, arm64 |
| darwin | amd64, arm64 |
| windows | amd64, arm64 |

Plus: `checksums.txt`, SLSA provenance, cosign signatures, a Homebrew tap formula, and a
Scoop manifest.

### Installation paths

```bash
# one-liner
curl -fsSL https://raw.githubusercontent.com/raihankhan/notebooklm-go/main/install.sh | sh

# Homebrew
brew install raihankhan/tap/notebooklm

# Go
go install github.com/raihankhan/notebooklm-go/cmd/notebooklm@latest

# Scoop (Windows)
scoop bucket add raihankhan https://github.com/raihankhan/scoop-bucket
scoop install notebooklm
```

Compare with the Python original's install story — `uv tool install`, `pipx`, PEP 668
externally-managed-environment errors, a 170 MB Chromium download, and a documented Linux
Playwright workaround. Removing all of that is the single largest user-facing win of this
port. Say so in the README.

### Container

```dockerfile
# --- build ---
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev COMMIT=none DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w \
      -X .../buildinfo.Version=${VERSION} \
      -X .../buildinfo.Commit=${COMMIT} \
      -X .../buildinfo.Date=${DATE}" \
      -o /out/ ./cmd/...

# --- runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/notebooklm /out/notebooklm-mcp /out/notebooklm-server /usr/local/bin/
ENV NOTEBOOKLM_HOME=/data \
    NOTEBOOKLM_MCP_TRANSPORT=http \
    NOTEBOOKLM_MCP_HOST=0.0.0.0 \
    NOTEBOOKLM_MCP_ALLOW_EXTERNAL_BIND=1
VOLUME /data
EXPOSE 9420
USER nonroot
ENTRYPOINT ["notebooklm-mcp"]
```

Target: **under 40 MB**, versus roughly 250 MB for the Python image.

`distroless/static` carries CA certificates, which the HTTPS calls need. `nonroot` means the
`/data` volume must be writable by uid 65532 — document that, because a permission error on
the credential directory presents as a confusing auth failure.

## Remote MCP deployment

The reason this matters: with master-token auth the session stays alive unattended, so the
HTTP transport can run as a **remote connector** reachable from Claude Code, Claude Desktop,
claude.ai (including mobile), and ChatGPT in Developer Mode. A tunnel gives HTTPS with no
public IP, no open ports, and no TLS certificate to manage — the tunnel terminates TLS at its
edge.

```
deploy/
├── Dockerfile
├── docker-compose.yml            # notebooklm-mcp + a tunnel sidecar (Compose profiles)
├── docker-compose.build.yml      # build from source instead of pulling
├── Makefile                      # setup / up / down / logs / token / shell
├── .env.example
└── tunnel/
    ├── cloudflare.md             # needs a domain in your Cloudflare account
    └── tailscale.md              # no domain — a free, stable *.ts.net HTTPS hostname
```

Flow: `make setup` → complete the one manual tunnel step → `make up`.

`make setup` must:

1. Generate a strong `NOTEBOOKLM_MCP_TOKEN` and write `.env` with `0600`.
2. Create `./data` with the right ownership for uid 65532.
3. Print the exact `notebooklm login --master-token --account …` command to run **on the
   host** to seed `./data`, and explain why a dedicated Google account is the right choice.
4. Print the tunnel-specific manual step.

Security checklist for the deployment docs — this exposes a full Google account to the
internet:

- [ ] A strong random bearer token, not a memorable one.
- [ ] A **dedicated** Google account, not a personal one.
- [ ] Tunnel access control on top of the bearer (Cloudflare Access policy, or Tailscale ACLs).
- [ ] `./data` is `0700`, `master_token.json` is `0600`.
- [ ] Container is read-only except `/data`; no capabilities; `no-new-privileges`.
- [ ] Log level `INFO` or higher — never `DEBUG` (which widens response previews).
- [ ] A documented revocation path: how to invalidate the master token
      (revoke the app at myaccount.google.com → Security → Third-party access).

## Scheduling

Two patterns worth documenting because they are what the original's users actually built.

**Keepalive** — keeps cookies fresh so an unattended agent never hits an auth wall:

```
# crontab: every 30 minutes
*/30 * * * * /usr/local/bin/notebooklm auth refresh --quiet
```

```ini
# systemd timer (preferred on Linux)
[Unit]
Description=NotebookLM cookie keepalive
[Service]
Type=oneshot
ExecStart=/usr/local/bin/notebooklm auth refresh --quiet
Environment=NOTEBOOKLM_HOME=/var/lib/notebooklm
User=notebooklm
```

**Scheduled audio briefing** — the pattern behind "a fresh personalized podcast every
morning":

```bash
#!/usr/bin/env bash
set -euo pipefail
NB=$(notebooklm create "Briefing $(date +%F)" --json | jq -r .notebook.id)
while read -r url; do notebooklm source add "$url" -n "$NB"; sleep 3; done < feeds.txt
notebooklm source wait --all -n "$NB" --timeout 600
notebooklm generate audio "Focus on what changed since yesterday" -n "$NB" --wait
notebooklm download audio "./briefings/$(date +%F).m4a" -n "$NB"
```

Note the `sleep 3` between adds. Rate limiting is real; pacing bulk operations is not
optional. Mention it wherever a loop appears in the docs.

## Observability

- **Structured logs** via `log/slog`, JSON handler when stdout is not a TTY, text when it is.
  Always stderr.
- **Request-id correlation**: one id per logical RPC, threaded through `context`, present on
  every line of that call including its retries. This is what makes a
  `401 → refresh → 429 → retry` sequence readable as one story.
- **Metrics**: `client.MetricsSnapshot()` exposes `rpcCallsStarted/Succeeded/Failed`,
  `rpcAuthRetries`, `rpcDecodeErrors`, `queueWaitSeconds`, and `byteCountMismatchTotal`. The
  REST server can expose them at `/v1/server/info`.
- **The two drift signals worth alerting on**, both rate-of-change rather than absolute:
  - `rpcDecodeErrors` rising → Google reshaped a response.
  - `byteCountMismatchTotal` jumping → Google changed the chunk framing unit, or a proxy
    stopped preserving counts.
- **The nightly canary** (`internal/tools/rpchealth`) probes every method id and reports the
  build-label staleness gap. This is the early-warning system for the #1 breakage class, and
  it is worth wiring to a real notification rather than a CI badge nobody reads.

## Migrating from `notebooklm-py`

For the README, because this should be a two-line story:

```bash
# 1. Install the Go binary.
brew install raihankhan/tap/notebooklm

# 2. That's it. Your existing ~/.notebooklm/ works unchanged.
notebooklm auth check --test --json     # expect "status": "ok"
```

Known differences to state plainly:

| Difference | Detail |
|---|---|
| Interactive login | Uses your **system** Chrome/Edge over CDP instead of a bundled Chromium. No 170 MB download, but Chrome must be installed. `--browser-cookies` needs no browser launch at all and is the recommended path. |
| Optional extras | Gone. Browser-cookie import, markdown conversion, MCP, and REST are all in the one binary. No `[browser]`, `[cookies]`, `[mcp]`, `[server]`, `[headless]`. |
| Android backend | `--backend android` is unavailable in v1 and returns a typed error. |
| TLS impersonation | `NOTEBOOKLM_TRANSPORT=curl_cffi` becomes `=utls` in v1.1. Default is stdlib `net/http`. |
| Python API | No Python API. Go SDK at `github.com/raihankhan/notebooklm-go/notebooklm`. |
| MCP launch command | `notebooklm-mcp` directly, not `uvx --from notebooklm-py[mcp]`. Re-run `notebooklm mcp install <client>` once to update the config block. |

Everything else — commands, flags, `--json` keys, exit codes, MCP tool names, REST routes,
and the on-disk credential format — is unchanged.
