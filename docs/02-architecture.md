# 02 — Architecture

## Layering

Same five-layer shape as the Python original: three thin transport adapters fan into one
neutral application layer, over one client, over one RPC stack.

```
        ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
        │ CLI          │  │ MCP          │  │ REST         │
        │ internal/cli │  │ internal/    │  │ internal/    │
        │ (cobra)      │  │ mcpsrv       │  │ restsrv      │
        └──────┬───────┘  └──────┬───────┘  └──────┬───────┘
               └─────────────────┼─────────────────┘
                                 ▼
   ┌──────────────────────────────────────────────────────────┐
   │ internal/app — transport-neutral business logic          │
   │ id resolution · plan building · status projection ·      │
   │ wait/retry orchestration · errors.Classify (the single   │
   │ failure-category source) · diagnostics · download specs  │
   │ Imports no cobra, no mcp, no net/http server.            │
   └──────────────────────────────────────────────────────────┘
                                 ▼
   ┌──────────────────────────────────────────────────────────┐
   │ notebooklm — public SDK                                  │
   │ Client + namespaces: Notebooks Sources Artifacts Chat    │
   │ Notes MindMaps Research Settings Sharing Labels          │
   │ Collections · types · enums · error hierarchy            │
   └──────────────────────────────────────────────────────────┘
                                 ▼
   ┌──────────────────────────────────────────────────────────┐
   │ internal/runtime — Supervisor · Lifecycle · Metrics      │
   │ internal/web/transport — Executor · Chain · Kernel       │
   │ internal/auth — cookies · refresh ladder · master token  │
   └──────────────────────────────────────────────────────────┘
                                 ▼
   ┌──────────────────────────────────────────────────────────┐
   │ internal/web/wire — encode · decode · method ids ·       │
   │                     safe positional access               │
   └──────────────────────────────────────────────────────────┘
```

## Package map

```
notebooklm-go/
├── cmd/
│   ├── notebooklm/         main.go — Cobra root, wires internal/cli
│   ├── notebooklm-mcp/     main.go — MCP server entrypoint
│   └── notebooklm-server/  main.go — REST server entrypoint
│
├── notebooklm/             ── PUBLIC SDK (the only package users import)
│   ├── client.go              Client: composition root, lifecycle, RPCCall escape hatch
│   ├── options.go             Option functors (WithTimeout, WithProfile, WithBackend, …)
│   ├── notebooks.go           NotebooksAPI
│   ├── sources.go             SourcesAPI
│   ├── artifacts.go           ArtifactsAPI (10 generate_*, 9 download_*, lifecycle)
│   ├── chat.go                ChatAPI (streaming ask, history, configure)
│   ├── notes.go               NotesAPI
│   ├── mindmaps.go            MindMapsAPI (unified interactive + note-backed)
│   ├── research.go            ResearchAPI
│   ├── sharing.go             SharingAPI
│   ├── labels.go              LabelsAPI
│   ├── collections.go         CollectionsAPI
│   ├── settings.go            SettingsAPI
│   ├── types_*.go             Domain structs (Notebook, Source, Artifact, …)
│   ├── enums.go               Every wire enum + String()/parse helpers
│   ├── errors.go              Error hierarchy + errors.Is/As sentinels
│   └── metrics.go             MetricsSnapshot, RPCEvent callback
│
├── internal/
│   ├── app/                ── transport-neutral cores
│   │   ├── errors/            Classify(err) → Category (the ONE decision)
│   │   ├── resolve/           partial-id → full-id resolution for all 5 id kinds
│   │   ├── generate/          generate plan building + retry orchestration
│   │   ├── download/          download plan, target selection, path safety
│   │   ├── downloadspec/      the canonical download registry (types × formats × MIME)
│   │   ├── sourceadd/         argument classification (url / file / youtube / text / drive)
│   │   ├── sourcewait/        wait orchestration + content-sanity warnings
│   │   ├── research/          run orchestration, import verification
│   │   ├── studio/            unified note+artifact projection (for MCP studio_* tools)
│   │   ├── authcheck/         auth diagnostics
│   │   ├── doctor/            environment health checks
│   │   ├── serialize/         canonical --json envelope shapes
│   │   ├── skill/             agent-skill install/status/package
│   │   └── mcpinstall/        MCP client config writer
│   │
│   ├── web/                ── the batchexecute backend
│   │   ├── wire/
│   │   │   ├── methods.go        RPCMethod table + NOTEBOOKLM_RPC_OVERRIDES
│   │   │   ├── json.go           Marshal/Unmarshal — the ONLY encoding/json users
│   │   │   ├── encode.go         encode_rpc_request, build_request_body, NestSourceIDs
│   │   │   ├── decode.go         strip anti-XSSI, chunked parser, result extraction
│   │   │   ├── index.go          At/Str/Int/Bool/List — strict positional access
│   │   │   └── urls.go           batchexecute / query / upload endpoints
│   │   ├── policy/               idempotency registry (5 classes, keyed method+variant)
│   │   ├── transport/
│   │   │   ├── kernel.go         *http.Client owner, cookie jar owner, epoch fence
│   │   │   ├── executor.go       one logical RPC: encode → chain → decode → map errors
│   │   │   ├── runtime.go        authed POST entry, snapshot capture, materialize
│   │   │   ├── chain.go          middleware chain builder + host
│   │   │   ├── mw_retry.go       429/5xx + Retry-After, aggregate deadline
│   │   │   ├── mw_authrefresh.go refresh-on-auth-error, shared refresh budget
│   │   │   ├── mw_errorinject.go test harness, no-op in prod
│   │   │   ├── mw_tracing.go     structured logging boundary
│   │   │   ├── stream.go         streaming POST with response size cap
│   │   │   └── errors.go         transport error shapes, Retry-After parsing
│   │   ├── params/               positional request builders, one file per domain
│   │   ├── rows/                 positional response decoders, one file per domain
│   │   ├── features/             concrete web impl of each SDK namespace
│   │   ├── upload/               Scotty resumable upload (start → stream → finalize)
│   │   └── assets/               artifact byte download + SSRF hop guard
│   │
│   ├── auth/
│   │   ├── tokens.go             AuthTokens (redacting), load-from-storage
│   │   ├── cookiejar/            RFC 6265 jar: introspectable, attribute-preserving
│   │   ├── storage/              storage_state.json read/write, atomic, locked
│   │   ├── profile/              ProfileStore: document, CAS merge, in-band account
│   │   ├── extract/              WIZ_global_data SNlM0e/FdrFJe extraction + failure taxonomy
│   │   ├── refresh/             L1 token refresh + the L2.5/L3/L4 recovery ladder
│   │   ├── keepalive/            RotateCookies + __Secure-1PSIDTS rotation policy
│   │   ├── mastertoken/          android.clients.google.com/auth exchange + cookie mint
│   │   ├── browser/              CDP login (chromedp) + browser cookie import (kooky)
│   │   ├── account/              authuser routing, account enumeration, repair
│   │   ├── policy/               cookie domain allowlist, required-cookie validation
│   │   └── singleflight/         cross-goroutine + cross-process refresh coalescing
│   │
│   ├── runtime/
│   │   ├── supervisor.go         drain → metrics → semaphore; call & operation leases
│   │   ├── lifecycle.go          open/close waves, resource generations, drain
│   │   ├── metrics.go            atomic counters + event fan-out
│   │   └── deadline.go           aggregate deadline helper
│   │
│   ├── cli/                   Cobra commands (one file per group) + rendering + theme
│   ├── mcpsrv/                MCP tools (one file per domain) + auth + confirm gating
│   ├── restsrv/               routes (one file per domain) + auth + limiters + pending
│   ├── paths/                 NOTEBOOKLM_HOME, profiles, legacy fallback, path info
│   ├── config/                config.json, env resolution, base-URL allowlist
│   ├── redact/                credential redaction regexes (log + error text)
│   ├── logging/               slog handler, request-id correlation, level from env
│   ├── atomicio/              atomic write, 0600/0700, .bak rollback, flock
│   └── tools/boundarycheck/   CI import-boundary linter
│
├── docs/                      this directory
├── testdata/cassettes/        go-vcr recordings
└── deploy/                    Dockerfile, compose, tunnel sidecars
```

## Concurrency model — the biggest deliberate divergence

The Python original carries a **loop-affinity contract** (its ADR-0004): one client binds
to the event loop it was opened on, and cross-loop or cross-thread reuse raises. That
contract exists because `asyncio` primitives bind to a loop on first await; it is a
mitigation for a Python-runtime hazard, not a property of the protocol.

**Go has no such hazard.** We replace the contract with a stronger guarantee:

> A `*notebooklm.Client` is safe for concurrent use by multiple goroutines. Every method
> takes a `context.Context` as its first argument. Cancellation propagates to the
> in-flight HTTP request. A client is still **single-tenant**: one client, one Google
> account — because chat conversation cache and auth state are per-account.

Mapping of the Python primitives:

| Python | Go |
|---|---|
| `asyncio.Lock` on auth snapshot | `sync.Mutex` |
| `asyncio.Semaphore` for RPC admission | `golang.org/x/sync/semaphore.Weighted` (supports ctx-aware `Acquire`) |
| Single-flight refresh task | `golang.org/x/sync/singleflight.Group` |
| Cross-process refresh coalescing (`flock`) | `github.com/gofrs/flock` |
| `asyncio.to_thread` for blocking disk I/O | direct call (Go I/O does not block a thread of execution meaningfully) |
| Keepalive `Task` | goroutine + `time.Ticker`, stopped via `context.CancelFunc` |
| `TransportDrainTracker` | `sync.WaitGroup` + generation counter |
| `RuntimeDeadline` | `context.WithDeadline`, plus an explicit `deadline.Budget` for the sleep-clamping arithmetic the retry layers need |
| `_loop_affinity.assert_bound_loop` | **deleted** |
| Cancellation-safe "settle before propagate" | `context.WithoutCancel` for shielded teardown |

### What we keep from the Python runtime, unchanged in spirit

**Resource generations (epoch fencing).** `Lifecycle` allocates a monotonic epoch on open
and retires it synchronously before teardown's first blocking call. Every admitted
operation carries its epoch; the kernel rejects a POST whose epoch has been retired. This
is what makes "close while 20 RPCs are in flight" deterministic rather than racy. Keep it —
`context` cancellation alone does not prevent a late goroutine from reading a rebuilt
cookie jar from the *next* generation.

**Single refresh budget per logical call.** Two independent layers can decide "this is an
auth error and I should refresh": the HTTP-status layer (`mw_authrefresh`) and the decode
layer (`executor`). Without a shared budget they both refresh on one call. `RefreshBudget`
is a one-shot token threaded through the call context.

**Aggregate deadline anchored at T0.** The retry budget must not restart when a
decode-time refresh retries the call. `deadline.Budget` is minted once per logical call
and threaded through; post-refresh sleeps are clamped to what remains.

**Unconditional pre-POST envelope rebuild.** Before every terminal POST — including
retries — the request URL/body/headers are rebuilt from a freshly captured auth snapshot,
with **no blocking operation between the rebuild and the POST**. This is load-bearing: a
concurrent refresh that swapped the cookie jar between materialization and send produces a
request whose `at=` CSRF token does not match its cookies. In Go, enforce it with a comment
plus a test that inspects the function's AST for an intervening channel/lock operation
(port of the Python original's AST guard).

## Client composition

```go
type Client struct {
    // public namespaces
    Notebooks   *NotebooksAPI
    Sources     *SourcesAPI
    Artifacts   *ArtifactsAPI
    Chat        *ChatAPI
    Notes       *NotesAPI
    MindMaps    *MindMapsAPI
    Research    *ResearchAPI
    Settings    *SettingsAPI
    Sharing     *SharingAPI
    Labels      *LabelsAPI
    Collections *CollectionsAPI

    auth      *auth.Tokens        // the one authoritative mutable token holder
    backend   backend.Backend     // web today; android seam for v2
    lifecycle *runtime.Lifecycle
    sup       *runtime.Supervisor
    metrics   *runtime.Metrics
    exec      *transport.Executor
}

func New(ctx context.Context, opts ...Option) (*Client, error)
func FromStorage(ctx context.Context, opts ...Option) (*Client, error)  // mirrors from_storage
func (c *Client) Close(ctx context.Context) error
func (c *Client) Drain(ctx context.Context) error
func (c *Client) RPCCall(ctx context.Context, m wire.Method, params []any, opts ...CallOption) (any, error)
func (c *Client) RefreshAuth(ctx context.Context, opts ...RefreshOption) error
func (c *Client) MetricsSnapshot() MetricsSnapshot
```

`Close` is required (the keepalive goroutine and the HTTP transport own resources), so the
canonical usage is:

```go
client, err := notebooklm.FromStorage(ctx)
if err != nil { return err }
defer client.Close(context.WithoutCancel(ctx))
```

## The Backend seam (for v2 Android)

Each SDK namespace is a thin struct over an interface, so the concrete implementation can
be swapped by `--backend web|android` without touching the public surface or `internal/app`:

```go
// internal/backend/backend.go
type Backend interface {
    Notebooks() NotebooksBackend
    Sources() SourcesBackend
    Artifacts() ArtifactsBackend
    Chat() ChatBackend
    // …
    Close(ctx context.Context) error
}
```

v1 ships exactly one implementation, `internal/web`. `--backend android` returns a typed
`ErrBackendUnavailable`. Designing the seam now costs a day; retrofitting it costs a
rewrite of eleven namespaces.

## Error model

Two axes, both ported.

**1. A typed hierarchy** in the public `notebooklm` package, wrapping with `%w` so
`errors.Is`/`errors.As` work:

```go
type Error struct {          // base: every library error embeds this
    Code     string          // stable machine code, e.g. "RATE_LIMITED"
    Message  string          // human text; may change between releases
    MethodID string          // obfuscated RPC id — logs only, never user-facing
    RPCCode  any             // gRPC status int, HTTP status int, or a label string
    FoundIDs []string        // rpc ids actually present in a drifted response
    Raw      string          // truncated, redacted response preview
    Err      error           // wrapped cause
}

// Sentinels for errors.Is:
var (
    ErrAuth        = &Error{Code: "AUTH_ERROR"}
    ErrRateLimit   = &Error{Code: "RATE_LIMITED"}
    ErrValidation  = &Error{Code: "VALIDATION_ERROR"}
    ErrNetwork     = &Error{Code: "NETWORK_ERROR"}
    ErrNotFound    = &Error{Code: "NOT_FOUND"}
    ErrRPC         = &Error{Code: "RPC_ERROR"}
    ErrDecoding    = &Error{Code: "DECODE_ERROR"}   // shape drift — Google reshaped a response
    ErrServer      = &Error{Code: "SERVER_ERROR"}
    ErrClient      = &Error{Code: "CLIENT_ERROR"}
    ErrTimeout     = &Error{Code: "TIMEOUT"}
    ErrConfig      = &Error{Code: "CONFIG_ERROR"}
    ErrQuota       = &Error{Code: "NOTEBOOK_LIMIT"}
    ErrUnknownRPC  = &Error{Code: "UNKNOWN_RPC_METHOD"} // method id likely changed
)
```

Domain-specific types carry extra fields: `SourceAddError{SourceID}` (so a caller can
delete the orphaned `preparing` row), `ArtifactTimeoutError{TaskID, Elapsed}`,
`RateLimitError{RetryAfter}`.

**2. One classifier.** `internal/app/errors.Classify(err) → Category` is the single place
that decides a failure's category. Each adapter projects `Category` onto its own vocabulary:

| Category | CLI exit | MCP | REST |
|---|---|---|---|
| `CategoryUser` | 1 | `CODE: message` | 400/404/409 |
| `CategoryAuth` | 1 | `AUTH_ERROR: …` | 401 |
| `CategoryRateLimit` | 1 | `RATE_LIMITED: …` | 429 + `Retry-After` |
| `CategoryNotFound` | 1 | `NOT_FOUND: …` | 404 |
| `CategoryServer` | 1 | `SERVER_ERROR: …` | 502 |
| `CategoryCancelled` | 130 | — | 499 |
| `CategoryUnexpected` | 2 | `UNEXPECTED_ERROR: …` | 500 |

Adapters must not re-derive the category from the error type. That duplication is exactly
what the Python original's ADR-0021 removed.

## Middleware chain

Order is load-bearing (outermost first). Pinned by a test.

```
Supervisor            drain admission → metrics → semaphore   (protocol-neutral)
   ↓
RetryMiddleware       429 / 5xx, honors Retry-After, aggregate deadline
   ↓
AuthRefreshMiddleware refresh-on-auth-error, consumes shared RefreshBudget
   ↓
ErrorInjection        synthetic failures for tests; no-op in prod
   ↓
Tracing               innermost; structured log boundary (OTel export later)
   ↓
terminal              rebuild envelope from fresh snapshot → Kernel.Post → http.Client
```

Signature:

```go
type Request struct {
    URL     string
    Body    []byte
    Headers http.Header
    ctx     *callContext   // build func, snapshot, epoch, budgets, labels
}
type Response struct { HTTP *http.Response; Body []byte }
type Handler func(ctx context.Context, r *Request) (*Response, error)
type Middleware func(next Handler) Handler
```

## Third-party dependencies

Deliberately small. Every one earns its place.

| Dependency | Used for | Alternative rejected |
|---|---|---|
| `github.com/spf13/cobra` | CLI command tree, help, completion | urfave/cli — Cobra's completion + grouped help match Click's `SectionedGroup` |
| `github.com/spf13/pflag` | (transitive) POSIX flags | — |
| `github.com/modelcontextprotocol/go-sdk` | MCP server, stdio + streamable HTTP | hand-rolled JSON-RPC |
| `golang.org/x/sync` | `errgroup`, `semaphore`, `singleflight` | hand-rolled |
| `github.com/gofrs/flock` | cross-process credential lock | `syscall.Flock` directly (no Windows) |
| `github.com/zellyn/kooky` | browser cookie import (Chrome/Firefox/Safari/Edge/Brave/Arc, OS keyring decryption) | reimplementing DPAPI + Keychain + libsecret |
| `github.com/chromedp/chromedp` | CDP: headed login, `oauth_token` capture, attach to running Chrome | playwright-go (needs Node driver) |
| `github.com/charmbracelet/lipgloss` | CLI color/table styling (Rich equivalent) | fatih/color (no layout) |
| `github.com/JohannesKaufmann/html-to-markdown` | `source fulltext -f markdown` | markdownify has no Go port |
| `gopkg.in/dnaeon/go-vcr.v4` | *(test only)* cassette record/replay | hand-rolled httptest fixtures |
| `github.com/refraction-networking/utls` | *(v1.1, optional)* JA3 impersonation | curl_cffi has no Go equivalent |

Everything else — HTTP, JSON, TLS, cookies, atomic writes, logging (`log/slog`), CSV,
zip/pptx passthrough — is stdlib.

## Two Go-specific hazards, called out

### JSON encoding is not Python-compatible by default

`encoding/json` HTML-escapes `<`, `>`, `&` and appends a newline from `Encoder`. Both break
byte-compatibility with the Chrome-shaped payloads Google expects. Decoding into `any`
turns numbers into `float64`, silently mangling 19-digit session ids and millisecond
timestamps.

`internal/web/wire/json.go` is the **only** file in the repo permitted to import
`encoding/json`:

```go
func Marshal(v any) ([]byte, error) {
    var buf bytes.Buffer
    enc := json.NewEncoder(&buf)
    enc.SetEscapeHTML(false)
    if err := enc.Encode(v); err != nil { return nil, err }
    return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func Unmarshal(data []byte, v *any) error {
    dec := json.NewDecoder(bytes.NewReader(data))
    dec.UseNumber()
    return dec.Decode(v)
}
```

`boundarycheck` enforces the exclusivity.

### Go's cookie jar cannot round-trip `storage_state.json`

`net/http/cookiejar` exposes only `Cookies(*url.URL)` — you cannot enumerate the jar, and
it discards `HttpOnly`, `SameSite`, and the distinction between session and persistent
cookies. This project needs all of it: `storage_state.json` is a Playwright-format document
that must round-trip losslessly, `__Secure-1PSIDTS` rotation needs per-cookie CAS against a
baseline, and the `same_site="None"` attribute is load-bearing for the master-token mint.

`internal/auth/cookiejar` therefore implements `http.CookieJar` from scratch:

```go
type Cookie struct {
    Name, Value      string
    Domain, Path     string
    Expires          *int64   // unix seconds; nil = session cookie (Playwright's -1)
    Secure, HTTPOnly bool
    SameSite         string   // "Strict" | "Lax" | "None" | ""
}

type Jar struct { /* RFC 6265 §5.3 keyed by (name, domain, path) */ }

func (j *Jar) SetCookies(u *url.URL, cs []*http.Cookie)   // http.CookieJar
func (j *Jar) Cookies(u *url.URL) []*http.Cookie          // http.CookieJar, §5.4 ordering
func (j *Jar) All() []Cookie                              // enumeration — the whole point
func (j *Jar) Snapshot() Jar                              // immutable baseline for CAS
func (j *Jar) HeaderFor(u *url.URL) string                // domain-correct Cookie: header
```

Required semantics, each with a test:

- Keyed by the RFC 6265 §5.3 triple `(name, domain, path)` — **not** by name. `OSID` exists
  on both the app host and `accounts.google.com` with different values; collapsing them
  picks an arbitrary winner (the Python original's issue #2054).
- §5.4 selection: domain-match, path-match, `Secure` only over https, `HttpOnly` irrelevant
  for our own sends, sorted longest-path-first then oldest-creation-first.
- Attribute preservation on round-trip: a cookie read from `storage_state.json`, sent, and
  re-serialized must be byte-identical unless the server actually changed it.
- `__Secure-`/`__Host-` prefix cookies are forced `Secure` on import.
- Reject a cookie whose domain is a public suffix or does not domain-match the request host.

This package is the highest-risk, highest-value single unit of work in the port. It gets
its own phase (doc 10, Phase 2) and its own fuzz test.
