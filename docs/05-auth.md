# 05 — Authentication

Normative source: `../notebooklm-py/src/notebooklm/_auth/` (40 modules, ~15k lines) and
`../notebooklm-py/docs/auth-cookie-lifecycle.md`.

Everything in this document handles **full-account Google credentials**. A cookie set
exported from a signed-in browser grants the same access as being logged in. Read doc
`AGENTS.md` rule 4 before touching this subsystem.

## What a session actually is

Three things, all required:

| Piece | Source | Lifetime |
|---|---|---|
| **Google auth cookies** | a signed-in browser, or minted from a master token | days–weeks, with `__Secure-1PSIDTS` rotating every few minutes |
| **CSRF token** (`SNlM0e`) | scraped from the app shell HTML's `WIZ_global_data` object | short — expires within a session |
| **Session id** (`FdrFJe`) | same place | same |

The CSRF token travels in the request **body** as `at=`; the session id travels in the URL
as `f.sid`. Both are re-extracted by fetching `{base}/` and regex-matching the shell.

## On-disk layout

Must be byte-compatible with the Python original so a user can run either binary against
the same directory.

```
$NOTEBOOKLM_HOME                      (default ~/.notebooklm, mode 0700)
├── config.json                       { "language": "...", "default_profile": "..." }
├── profiles/
│   ├── default/
│   │   ├── storage_state.json        cookies + in-band account metadata   (0600)
│   │   ├── storage_state.json.bak    one-step rollback of an import        (0600)
│   │   ├── storage_state.json.lock   ) advisory locks — see below
│   │   ├── storage_state.rotate.lock )
│   │   ├── storage_state.refresh.lock)
│   │   ├── storage_state.lock.bootstrap )
│   │   ├── master_token.json         durable Google master token          (0600)
│   │   ├── context.json              LEGACY: active notebook/conversation
│   │   └── browser_profile/          CDP user-data-dir (contains .notebooklm-owned)
│   └── work/…
└── (legacy pre-profile fallback for the "default" profile only:)
    storage_state.json, context.json, browser_profile/ at the home root
```

`storage_state.json` is a **Playwright storage-state document** extended with a
`notebooklm` namespace:

```json
{
  "cookies": [
    { "name": "SID", "value": "…", "domain": ".google.com", "path": "/",
      "expires": 1798761234, "httpOnly": true, "secure": true, "sameSite": "None" }
  ],
  "origins": [],
  "notebooklm": { "account": { "authuser": 0, "email": "you@example.com" } }
}
```

Round-trip requirements:

- `expires: -1` means a **session cookie**. Map to `nil` in the Go `Cookie` struct, never
  to `0` and never to "expired".
- `sameSite` must survive. It is not recoverable from a Go/Python cookie jar, which is the
  whole reason `internal/auth/cookiejar` exists (doc 02).
- `origins` (localStorage/sessionStorage) is written as `[]` and dropped on import.
- The `notebooklm.account` block is the **in-band** account metadata. `context.json` is its
  pre-v0.5.0 legacy source: read it, promote the value in-band, and never write it again.

### Locks

Four advisory `flock` files, all derived from one path function so they cannot drift:

| Lock | Guards |
|---|---|
| `.lock` | any cookie transaction on `storage_state.json` |
| `.rotate.lock` | the `RotateCookies` keepalive POST (prevents a stampede from parallel CLI invocations) |
| `.refresh.lock` | the L2.5 refresh-command rung |
| `.lock.bootstrap` | master-token bootstrap / re-mint |

Go: `github.com/gofrs/flock`, plus an in-process `sync.Map` of per-exact-raw-path mutexes so
goroutines in one process serialize before contending on the OS lock.

### Atomic writes

Every credential write: create a temp file in the **same directory**, `Chmod(0600)`, write,
`Sync()`, `Rename()` over the target. Never write in place — a crash mid-write must leave the
previous session intact, not a truncated file. On import, copy the existing file to `.bak`
first (one step; each run overwrites the previous `.bak`).

## Required cookies

Two tiers. Validated locally before any network call, so a bad import fails fast with an
actionable message instead of an opaque 401.

**Tier 1 — hard requirement.** Both must be present:

- `SID`
- `__Secure-1PSIDTS` — and it must be **live and RFC 6265-routable** to
  `accounts.google.com/RotateCookies`. Present-by-name but unroutable is its own failure
  reason (`psidts_unroutable`), because rotation is what keeps the session alive.

**Tier 2 — secondary binding**, warn-only for compatibility with unablated account flows.
Accepted forms:

| Binding | Accepted? |
|---|---|
| `OSID` present | ✅ sufficient on its own |
| `APISID` + `SAPISID` + bare `LSID` | ✅ |
| `APISID` + `SAPISID`, no `LSID` | ❌ |
| none of the above | ⚠️ warn and proceed (a completely absent Tier 2 set is preserved) |

`OSID` is **app-host-scoped**; `APISID`/`SAPISID` live on `.google.com`. The binding spans
hosts because the auth flow crosses them — which is why the diagnostic for a missing binding
must name *both* personal hosts. Telling a user to "re-login" when the real problem is a
missing per-product `OSID` on the host they are actually talking to wastes their time.

`__Secure-`/`__Host-` prefixed cookies are forced `Secure` on import, per the cookie-prefix
spec.

## Cookie-domain allowlist

Extraction and persistence both filter through this list. It is a security boundary: cookies
outside it are not requested, not stored, and not sent.

**Required** (exercised by real flows — login, token refresh, source add, chat, download):

```
.google.com            google.com
.notebook.google.com   notebook.google.com
.notebooklm.google.com notebooklm.google.com
.notebooklm.cloud.google.com  notebooklm.cloud.google.com
accounts.google.com    .accounts.google.com     ← token refresh + RotateCookies
drive.google.com       .drive.google.com        ← Drive ingest follows redirects here
.googleusercontent.com                          ← authenticated media downloads
+ every regional .google.<ccTLD> variant        ← users in those regions carry SID there
```

Both dotted and undotted variants are listed deliberately: cookie-jar normalization can add
or drop a leading dot, and a dropped variant means a silently missing cookie next extraction.

**Optional**, opt-in only via `--include-domains`:

| label | domains |
|---|---|
| `youtube` | `.youtube.com`, `youtube.com`, `accounts.youtube.com` |
| `docs` | `docs.google.com` |
| `myaccount` | `myaccount.google.com` |
| `mail` | `mail.google.com` |
| `all` | every label above |

None of these appear in any traced flow. They exist because early versions requested them
"for symmetry with a logged-in browser."

## Acquiring credentials — four paths

### A. Browser cookie import (`login --browser-cookies`) — the default recommendation

Reads cookies directly from an installed browser's cookie store. No browser launch, no
Node, no Chromium download.

Go implementation: **`github.com/zellyn/kooky`**, which handles the OS-specific decryption
the Python original delegated to `rookiepy`:

| Platform | Chromium key source | Firefox |
|---|---|---|
| macOS | Keychain (`Chrome Safe Storage`) → PBKDF2 → AES-128-CBC | `cookies.sqlite`, plaintext |
| Windows | DPAPI-protected key in `Local State` → AES-256-GCM (`v10`/`v11` prefixes) | same |
| Linux | libsecret / gnome-keyring, falling back to the literal `peanuts` password | same |

Selector grammar to preserve exactly:

| Selector | Meaning |
|---|---|
| `chrome` | all populated Chromium user profiles, fanned out |
| `chrome::Profile 1` | one profile, by **directory** name (stable across UI renames) |
| `chrome::Work` | one profile, by display name |
| `firefox` | all containers merged — **emit a warning** when this is what is happening |
| `firefox::Work` | one Multi-Account Container |
| `firefox::none` | the no-container default |
| `auto` | auto-detect any supported browser |

Supported browsers: chrome, chromium, edge, brave, arc, opera, vivaldi, firefox, safari.

Multi-account: `--account EMAIL` selects one signed-in account; `--all-accounts` fans every
signed-in account out into separate profiles named from the account email
(`alice@gmail.com` → profile `alice`). `--update` lets an existing metadata-less profile be
adopted in place instead of creating a suffixed `alice-2`; a profile already bound to a
*different* email always gets the suffix, so account B can never silently clobber account A.

`auth inspect --browser <browser>` previews the accounts a store holds, read-only, with `-v`
showing which Chromium profile directory each came from.

### B. Cookie JSON import (`auth import-cookies`)

Accepts a Playwright `storage_state` object **or** a bare JSON array of cookie objects (the
shape most browser export extensions produce). `-` reads stdin.

Normalizations to port: `expirationDate` → `expires`, `sameSite` case forms,
`hostOnly` → domain without leading dot, `__Secure-`/`__Host-` forced `Secure`, `origins`
dropped. Then: allowlist filter → Tier 1/Tier 2 validation → `.bak` → atomic `0600` write.

Invalid input **never** overwrites an existing session. Incompatible with
`NOTEBOOKLM_AUTH_JSON` — error out rather than silently falling back to the env auth.

### C. Interactive browser login (`login`)

The Python original launches Playwright's bundled Chromium. Go replaces that with
**`chromedp`** driving a **system** browser:

```
notebooklm login                     # launch system Chrome, headed, persistent profile
notebooklm login --browser msedge    # for orgs requiring Edge for SSO
notebooklm login --cdp-url http://localhost:9222   # attach to an already-running Chrome
```

Flow: launch/attach → navigate to `{base}/` → poll for a landing on either personal host →
read the cookie store over CDP (`Network.getAllCookies`) → filter by allowlist → heal → persist.

Details to carry over:

- **5-minute default wait window** for human interaction (`--browser-timeout`).
- Classify the landing URL: on a personal app host → capture; redirected to Google sign-in →
  a typed "login required" outcome, not a generic failure.
- Log every main-frame navigation at DEBUG so `-vv login` is self-diagnosing when a login
  never lands. Redact those trace URLs to **scheme + host only** — stricter than the general
  URL redactor, because this traces arbitrary SSO redirects and a federated IdP can carry a
  one-time assertion in the path.
- Classify launch failures into actionable messages: browser not installed, or a Windows
  `spawn UNKNOWN` execution veto from AppLocker/WDAC/Defender.
- Never let a failed post-capture heal discard a completed sign-in. Heal is best-effort:
  return `(state, error)` and keep the state.
- `--fresh` deletes the cached browser profile (to switch Google accounts). With an explicit
  `--storage`, only delete a sidecar directory that carries the `.notebooklm-owned` marker or
  matches the canonical layout — **refuse to delete an arbitrary unowned directory.**

### D. Master token (`login --master-token`) — the headless/server path

The credential model for servers, CI, cron, and the remote MCP connector. One durable
credential mints fresh web cookies **on demand**, with no browser per session, so it
self-heals expired sessions unattended.

> ⚠️ A master token is a **full-account, durable** credential — strictly more powerful than
> a cookie set, and it does not expire on sign-out. Use a dedicated or throwaway Google
> account. Store `0600`. Never log it, never put it in a URL, never include it in a bug report.

Four steps. No Go equivalent of `gpsoauth` exists, so implement it directly — it is plain
form POSTs; the only crypto in `gpsoauth` is password encryption, which this flow does not use.

**D1 — capture a single-use `oauth_token`.** Google's EmbeddedSetup page issues it. Either
open a browser to it via `chromedp` (`--cdp-url` to attach to a running Chrome instead), or
have the operator paste it with `--oauth-token` on a headless box.

**D2 — exchange it for a durable master token.**

> ⚠️ **The exact form-field set below must be verified against `gpsoauth`'s source before
> implementation** (`gpsoauth/__init__.py`, `exchange_token` and `_perform_auth_request`).
> The Python original delegates entirely to that library, so the field names, the GMS client
> signature, and the device fields are *its* contract, not something recoverable from
> `notebooklm-py`. This is exactly the case doc `AGENTS.md` rule 2 covers: read the source,
> do not trust a recollection. Pin the verified set in a Go comment with the gpsoauth version
> it was read from.

```
POST https://android.clients.google.com/auth
Content-Type: application/x-www-form-urlencoded

Email=<email>
Token=<oauth_token>
service=ac2dm
androidId=<16 hex chars, generated once and persisted with the token>
app=com.google.android.gms
client_sig=<the GMS client signature gpsoauth uses>
device_country=us
operatorCountry=us
lang=en
sdk_version=<per gpsoauth>
```

The response is a `key=value` per line body; the master token is the `Token=` value
(prefixed `aas_et/`). A rejection returns an `Error=` line — surface that value, never the
whole body. Persist as `master_token.json`:

```json
{"version": 1, "email": "…", "android_id": "…", "token": "aas_et/…"}
```

**D3 — mint an OAuth bearer from the master token.** (Same verification caveat as D2 for the
generic field set; the three values below **are** confirmed from
`_auth/mint_service.py::_CHROMECAST_OAUTH_SPEC`.)

```
POST https://android.clients.google.com/auth
Email=<email>
EncryptedPasswd=<master_token>              # the master token goes in this field, unencrypted
service=oauth2:https://www.google.com/accounts/OAuthLogin   ← confirmed
app=com.google.android.apps.chromecast.app                  ← confirmed
client_sig=24bb24c05e47e0aefa68a58a766179d9b613a600         ← confirmed
androidId=<same android id as D2>
… same device fields as D2
```

Response `Auth=` is the bearer; `Expiry=` is an optional server-owned expiry. The
Chromecast app identity is deliberate: it is the OAuth client whose scope grant covers
`OAuthLogin`.

Failure handling: **discard the raw response, the wrapped cause, and any
credential-bearing local variables** before raising. Preserve distinct dependency errors but
never the payload. A malformed response and a rejected token are different errors with
different remediations; a leaked response body is neither.

**D4 — trade the bearer for web cookies.**

```
GET https://accounts.google.com/OAuthLogin?source=ChromiumBrowser&issueuberauth=1
    Authorization: Bearer <bearer>
    → body is the "uberauth" token (a single whitespace-free line; reject a body
      containing a space, or a non-200)

GET https://accounts.google.com/MergeSession?service=mail
        &continue=https://www.google.com&uberauth=<uberauth>
    Authorization: Bearer <bearer>
    → follow redirects; the cookie jar is now populated

POST https://accounts.google.com/RotateCookies    (best-effort; failure is non-fatal)
```

Then **verify** the jar carries `SID`, `APISID`, and `SAPISID`. Missing any means
MergeSession changed shape and the session would fail PSIDTS recovery later — fail now, with
that explanation, rather than persisting a jar that will break mysteriously.

Persist **session before token**: write `storage_state.json` first, then the master token.
If the process dies between them, the user has a working session and can re-bootstrap; the
reverse order leaves a durable credential with no evidence of what it is for.

**Account-mismatch guard:** without `--force`, refuse to bootstrap into a profile that
already holds a session for a **different** account. Direct the user to `-p <profile>`
instead. This prevents account B's mint from silently clobbering account A.

## The refresh ladder

When a request fails with an auth-shaped error, or the CSRF token has expired, five rungs
run in order. Each rung, on success, reloads cookies and retries the homepage GET once.

```
L1   Token refresh
     GET {base}/?authuser=…  → re-extract SNlM0e + FdrFJe from WIZ_global_data
     Always on. Fixes the common case: valid cookies, expired CSRF.

     ↓ if the GET 302s to Google sign-in, first-party cookies are dead:

L2.0 Storage cookie reload                                        [default, always on]
     A sibling process (another CLI invocation, the MCP server) may have already
     refreshed the same storage_state.json. Re-read it before invoking anything
     credential-bearing or operator-configured.
     Bounded attempts: 3 with a file-backed profile, 2 for inline env auth.
       attempt 0: retry once on a live-jar change
       attempt 1: force a disk sample, preserving a new auth-bearing live candidate
       attempt 2: if that candidate is also rejected, use the final sample

L2.5 NOTEBOOKLM_REFRESH_CMD                                       [opt-in mid-session]
     Run an operator-supplied command that refreshes credentials out of band.
     Mid-session use is gated by NOTEBOOKLM_REFRESH_CMD_MIDSESSION=1 (default OFF);
     cold-start use is always available. Single-flight coalesced, per-path flock.
     Profile is derived from the storage path, so a client built for profile "work"
     refreshes "work" — not "default".

L3   Headless re-auth                                             [opt-in]
     Drive a headless browser against the persistent profile to silently re-mint.
     Enabled by RefreshAuth(WithAllowHeadless()) or NOTEBOOKLM_HEADLESS_REAUTH=1.
     Local-unattended only — NEVER the remote/MCP auth path.
     Alternative source: NOTEBOOKLM_HEADLESS_REAUTH_CDP_URL attaches to a running
     Chrome instead of the dedicated profile (freshness mitigation).
     Returns a typed outcome: UNAVAILABLE | FAILED | SUCCESS — never a silent nil.
     Also exposes a credential-free, browser-free readiness probe for `doctor`.

L4   Master-token re-mint                                         [automatic if present]
     When master_token.json sits beside this profile's storage, re-mint a fresh
     session from the durable token. This is the "run notebooklm login" replacement.

     ↓ all rungs exhausted:

     "Authentication expired. Run 'notebooklm login' to re-authenticate."
```

**Coalescing is mandatory.** N concurrent failing RPCs must trigger **at most one** refresh —
and therefore at most one browser. `singleflight.Group` keyed by the canonical storage path,
plus per-path success epochs so a follower that arrives after a successful refresh returns
immediately instead of triggering another.

Cancellation safety: a follower's cancellation must never cancel the shared work. Use
`context.WithoutCancel` for the leader's work and let followers select on their own ctx.

## The keepalive — `__Secure-1PSIDTS` rotation

`__Secure-1PSIDTS` is short-lived and Google mints it **only** in response to a dedicated
`RotateCookies` POST — not on passive navigation. This is why a Playwright login can produce
a `storage_state.json` with `SID` and a secondary binding but **no** `__Secure-1PSIDTS`,
which the Tier 1 preflight then rejects. Inline recovery (POST `RotateCookies` and re-read)
runs on load for exactly that case.

```
POST https://accounts.google.com/RotateCookies
Content-Type: application/json
Origin: https://accounts.google.com

[000,"-0000000000000000000"]
```

Timeout 15 s, follow redirects, failure non-fatal.

Three layered guards against stampeding `accounts.google.com` — all required, since ten
sequential `notebooklm` shell invocations would otherwise fire ten POSTs:

1. **In-process claim** — a per-loop, per-canonical-path monotonic attempt stamp. Claims are
   stamped **before** the POST, so a failure or a cancellation still consumes the 60-second
   slot.
2. **Cross-process flock** on `.rotate.lock`, non-blocking: if another process holds it,
   skip rather than queue.
3. **Re-read after acquiring** — if storage was refreshed while waiting for the lock, skip.

The background keepalive goroutine is disabled by `NOTEBOOKLM_DISABLE_KEEPALIVE_POKE=1`.

## Account routing (`authuser`)

Google routes multi-account requests by an `authuser` value. Two forms:

- An **integer index** (0 = the first signed-in account). Fragile: indices shift when other
  accounts sign out.
- The **account email**, which is stable. Preferred whenever known.

A wrong `authuser` presents as a gRPC `NOT_FOUND` (5) or `PERMISSION_DENIED` (7) on a
notebook the user can plainly see in their browser. That is why the decoder appends the
account-routing hint to exactly those two codes, and why it routes them through a
non-auth error type — so `is_auth_error` cannot misclassify them and fire a pointless refresh.

Resolution: match the persisted identity against the authoritative live cookie route; probe
`{base}/?authuser=N` and extract the active email when needed; self-heal the persisted value
with an exact-document compare-and-swap that never crosses profile-session generations.

## Auth snapshot discipline

An `AuthSnapshot` is an immutable point-in-time view — `{csrfToken, sessionID, authuser,
accountEmail}` — captured once per HTTP attempt under the auth mutex. Rules:

1. The URL and body for one attempt are built from **one** snapshot. Never mix.
2. On retry, capture a **new** snapshot so refreshed credentials are picked up.
3. Before every terminal POST, rebuild the envelope from a fresh snapshot, with **no
   blocking operation between the rebuild and the POST**. A concurrent refresh in that
   window produces a request whose `at=` token does not match its cookies.
4. Cookies, `authuser`, and `accountEmail` advance **together** under the same mutex. A
   snapshot must never see cookies from one stored generation with routing from another.

## `NOTEBOOKLM_AUTH_JSON` — inline auth for CI

The whole `storage_state` document as an env var. No file, no writes. Skips cookie sync
entirely (there is no writable backing store), so PSIDTS recovery is declined with a DEBUG
note rather than attempted. Precedence: `--storage` > `NOTEBOOKLM_AUTH_JSON` > active
profile.

## `auth check` and `doctor`

`auth check` reports: storage exists · JSON valid · cookies present · `SID` present ·
`__Secure-1PSIDTS` present and routable · secondary binding form · account email and
authuser · expiry horizon. `--test` adds one live `ListNotebooks` call. `--json` emits the
same fields as a machine envelope.

`doctor` adds environment health: home directory permissions (`0700`), credential file
permissions (`0600`), profile inventory, config validity, clock skew, headless-reauth
readiness (profile present + browser available — credential-free and browser-free),
`NOTEBOOKLM_*` env sanity, and the build-label staleness gap. `--fix` repairs permissions and
prunes stale locks; nothing that touches credentials is auto-fixed.

## Threat notes carried from the original

- **Never send a credential to a host outside the allowlist**, including across a redirect.
  Both the download path and the upload path re-validate every hop and strip credentials
  the moment a chain leaves the allowlist.
- **Redact in two places, differently.** The general URL redactor keeps the path outside a
  Google-OAuth allowlist (useful for diagnostics); the login-trace redactor keeps only
  scheme + host. They are deliberately distinct — do not "unify" them.
- **Anchor the `net::ERR_*` extractor** so it cannot return arbitrary text following a stray
  `net::`. Browser navigation error messages embed the credential-bearing URL, so log the
  extracted error token, never the message.
- **Two navigation-failure predicates, deliberately disagreeing.** Where *we* navigate, treat
  only `ERR_ABORTED` and prose interruptions as a race (so `ERR_CONNECTION_REFUSED` still
  surfaces). During the interactive login wait we are watching a *human* navigate, so treat
  the whole `net::ERR_*` family as benign — a failed hop there says nothing about their
  sign-in.
- **Redact `SNlM0e` / `FdrFJe` in every shape** they appear: JSON (`"SNlM0e":"…"`),
  HTML-escaped JSON (`&quot;SNlM0e&quot;:…`), form bodies (`SNlM0e=…`), and diagnostic prose
  (`SNlM0e value is …`). Port all four regex families from `_logging.py`.
