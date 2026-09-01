# 09 — REST server specification

Normative source: `../notebooklm-py/src/notebooklm/server/`.

Binary: `notebooklm-server`. **Single-tenant, experimental, loopback-first.** It exists so
local automation can hit guarded HTTP routes instead of spawning a CLI process per call.

Stdlib `net/http` with Go 1.22+ `ServeMux` method-and-wildcard patterns. No web framework.

## Launch and configuration

```
notebooklm-server                      # 127.0.0.1:9430, requires NOTEBOOKLM_SERVER_TOKEN
notebooklm-server --port 9500
notebooklm-server --profile work
```

| Env | Default | Purpose |
|---|---|---|
| `NOTEBOOKLM_SERVER_TOKEN` | — | **required.** Static bearer for every `/v1` request. |
| `NOTEBOOKLM_SERVER_HOST` | `127.0.0.1` | bind address |
| `NOTEBOOKLM_SERVER_PORT` | `9430` | bind port |
| `NOTEBOOKLM_SERVER_ALLOW_EXTERNAL_BIND` | unset | required to bind non-loopback |
| `NOTEBOOKLM_SERVER_GENERATION_CONCURRENCY` | small | artifact generation limiter |
| `NOTEBOOKLM_SERVER_DOWNLOAD_CONCURRENCY` | small | download limiter |
| `NOTEBOOKLM_SERVER_SOURCE_MUTATION_CONCURRENCY` | small | source add/update/delete limiter |
| `NOTEBOOKLM_SERVER_SOURCE_WAIT_CONCURRENCY` | small | source wait limiter |
| `NOTEBOOKLM_SERVER_RESEARCH_CONCURRENCY` | small | research limiter |
| `NOTEBOOKLM_SERVER_CHAT_CONCURRENCY` | small | blocking chat limiter |

The limiters are the point: without them, five concurrent audio generations starve
`/healthz` and every cheap read. Each is a `semaphore.Weighted` owned by the server's
lifetime and acquired with the request context, so a client disconnect releases the slot.

## Security posture

Four guards, all mandatory:

1. **Static bearer on every `/v1` route**, compared with
   `crypto/subtle.ConstantTimeCompare`. Missing or wrong → 401.
2. **Loopback `Host` literal check** — a DNS-rebinding guard. The `Host` header must be a
   loopback literal (`127.0.0.1[:port]`, `[::1][:port]`, `localhost[:port]`). A browser page
   on any origin can resolve an attacker-controlled hostname to `127.0.0.1` and reach a
   loopback-bound server; checking the literal blocks it.
3. **No schema surface.** `/docs`, `/redoc`, and `/openapi.json` are **not** served. Do not
   add them "for convenience" — this server fronts a full Google account.
4. **Non-loopback bind requires** `NOTEBOOKLM_SERVER_ALLOW_EXTERNAL_BIND=1` **and** a token.
   Fail closed at startup.

`/healthz` is the **only** public route. It reports liveness and nothing about the account.

## Lifecycle

One `*notebooklm.Client` opened once at server start and closed on shutdown. Graceful
shutdown: stop accepting, drain in-flight with a bounded timeout, then `client.Close`.

```go
func main() {
    cfg := mustLoadConfig()                  // fails closed on a bad bind/token combo
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    client, err := notebooklm.FromStorage(ctx, notebooklm.WithProfile(cfg.Profile))
    if err != nil { fatal(err) }

    srv := &http.Server{
        Addr:              cfg.Addr,
        Handler:           routes.New(client, cfg),
        ReadHeaderTimeout: 10 * time.Second,
    }
    go func() { <-ctx.Done(); shutdown(srv, client) }()
    if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        fatal(err)
    }
}
```

## Error envelope

```json
{ "error": { "category": "not_found", "message": "…" } }
```

`category` comes from `internal/app/errors.Classify`, projected onto an HTTP status per doc
02's table. `429` carries `Retry-After`.

## Long-running work — poll the resource

The server never blocks a request on a multi-minute generation. The create call returns
immediately; the matching `GET` reports progress:

| Status | Meaning |
|---|---|
| `200` | done, body is the resource |
| `202` (on create) | accepted, body carries the id to poll |
| `200` + `{"status": "pending"}` | still running |
| `404` | no such task |
| `409` | conflicting state (e.g. retry on a non-failed artifact) |
| `410` | the task existed but its result is gone |

`internal/restsrv/pending` owns the task registry (port `server/_pending.py`).

## Routes

All under `/v1`. **43 routes.** Paths, methods, and status codes must match the Python
server exactly.

### Notebooks

| Method | Path | Status |
|---|---|---|
| `GET` | `/v1/notebooks` | 200 |
| `POST` | `/v1/notebooks` | 201 |
| `GET` | `/v1/notebooks/{notebook_id}` | 200 |
| `PATCH` | `/v1/notebooks/{notebook_id}` | 200 |
| `DELETE` | `/v1/notebooks/{notebook_id}` | 204 |
| `GET` | `/v1/notebooks/{notebook_id}/suggested-prompts` | 200 |

### Sources

| Method | Path | Status | Limiter |
|---|---|---|---|
| `GET` | `/v1/notebooks/{notebook_id}/sources` | 200 | — |
| `GET` | `/v1/notebooks/{notebook_id}/sources/{source_id}` | 200 | — |
| `GET` | `/v1/notebooks/{notebook_id}/sources/{source_id}/content` | 200 | — |
| `POST` | `…/sources/url` | 201 | source-mutation |
| `POST` | `…/sources/text` | 201 | source-mutation |
| `POST` | `…/sources/file` | 201 | — (multipart) |
| `POST` | `…/sources/drive` | 201 | source-mutation |
| `POST` | `…/sources/batch` | 201 | source-mutation |
| `POST` | `…/sources/wait` | 200 | source-wait |
| `PATCH` | `…/sources/{source_id}` | 200 | source-mutation |
| `DELETE` | `…/sources/{source_id}` | 204 | source-mutation |

The `file` route takes `multipart/form-data`. Enforce a max body size with
`http.MaxBytesReader` and stream to a temp file — never buffer an upload in memory.

The `batch` route sends **repeated URL entries in one `AddSource` RPC** — a true batch,
deliberately bypassing the single-item SDK method and its per-item baseline probe.

### Chat

| Method | Path | Limiter |
|---|---|---|
| `POST` | `/v1/notebooks/{notebook_id}/chat` | chat |
| `POST` | `/v1/notebooks/{notebook_id}/chat/configure` | — |

### Notes

| Method | Path | Status |
|---|---|---|
| `GET` | `/v1/notebooks/{notebook_id}/notes` | 200 |
| `POST` | `/v1/notebooks/{notebook_id}/notes` | 201 |
| `GET` | `…/notes/{note_id}` | 200 |
| `PUT` | `…/notes/{note_id}` | 200 |
| `DELETE` | `…/notes/{note_id}` | 204 |

### Artifacts

| Method | Path | Status | Limiter |
|---|---|---|---|
| `GET` | `/v1/notebooks/{notebook_id}/artifacts` | 200 | — |
| `POST` | `/v1/notebooks/{notebook_id}/artifacts` | **202** | generation |
| `GET` | `…/artifacts/{task_id}` | 200 / pending / 404 / 410 | — |
| `GET` | `…/artifacts/{artifact_id}/prompt` | 200 | — |
| `PATCH` | `…/artifacts/{artifact_id}` | 200 | — |
| `POST` | `…/artifacts/{artifact_id}/retry` | 200 / 409 | generation |
| `DELETE` | `…/artifacts/{artifact_id}` | 204 | — |
| `POST` | `…/artifacts/download` | 200 | download |

`download` is a `POST` because it takes a selection body (type, format, target selector,
path policy) rather than being addressable by a URL alone.

### Research

| Method | Path | Status | Limiter |
|---|---|---|---|
| `POST` | `/v1/notebooks/{notebook_id}/research` | **202** | research |
| `GET` | `…/research/{run_id}` | 200 | — |
| `POST` | `…/research/{run_id}/import` | 201 | research |
| `DELETE` | `…/research/{run_id}` | 200 | research |

### Sharing

| Method | Path | Status |
|---|---|---|
| `GET` | `/v1/notebooks/{notebook_id}/share` | 200 |
| `POST` | `…/share/public` | 200 |
| `POST` | `…/share/view-level` | 200 |
| `POST` | `…/share/users` | 201 |
| `PATCH` | `…/share/users/{email}` | 200 |
| `DELETE` | `…/share/users/{email}` | 204 |

An email in a path segment must be URL-escaped by the client and unescaped server-side.
Reject one containing a path separator.

### Meta

| Method | Path | Auth |
|---|---|---|
| `GET` | `/healthz` | **public** |
| `GET` | `/v1/server/info` | bearer |

`/v1/server/info` reports version plus auth health: `storage_exists`, `json_valid`,
`cookies_present`, `sid_cookie`, and an optional `account` block (same opt-in PII rule as the
MCP `server_info` tool).

## Cross-surface naming difference — do not "fix" it

The Drive-health axis is spelled differently on purpose, and both spellings are load-bearing
contracts:

| Surface | Raw code key | Label key |
|---|---|---|
| **CLI** | `status_id` (ingestion only) | `status`, `drive_status` — **strings** |
| **MCP / REST** | `drive_status` — **integer** | `drive_status_label` — string |

So `select(.drive_status == "deleted")` is correct for the CLI and **wrong** for MCP/REST,
where that key holds an integer. Both surfaces resolve the label through the same mapping
helper, so the vocabulary never diverges — only the key name. Document it in the route
comments; a well-meaning unification breaks every existing consumer of one surface.
