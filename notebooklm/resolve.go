// Package notebooklm — resolve.go.
//
// The name→ID resolver. Resolve and ResolveID accept either a
// title or a notebook id and return the canonical typed view (or
// just its id). Resolution is a thin layer over NotebooksAPI.List
// + NotebooksAPI.Get + NotebooksAPI.GetByProject that short-
// circuits on the first match and never forces an extra listing
// when the user supplied an exact id.
//
// Per docs/AGENTS.md:
//
//   - Rule 1 (Python is normative) — every behavior here is a
//     faithful port of notebooklm.notebooks.NotebooksAPI.resolve
//     and the _app/resolve helpers the Python CLI ships. The Go
//     port keeps the same resolution precedence (id-first then
//     title-first then auto-fallback) and the same empty-input,
//     case-insensitive, and fuzzy matching rules.
//
//   - Rule 3 (one JSON encoder / decoder) — payloads marshal
//     through internal/web/wire.Marshal and responses decode
//     through internal/web/wire.Unmarshal. The bytes flow through
//     Client.RPCCall / Client.Notebooks.(), which is already wired
//     through the encoder.
//
//   - Rule 4 (no credential leakage) — no error wrap or log
//     statement carries a cookie value, CSRF token, or session id.
//     Apperrors classifier never extracts raw transport errors.
//
//   - Rule 5 (boundary enforcement) — Resolve / ResolveID live in
//     notebooklm/* (the public SDK root) and only import sibling
//     modules. The implementation MUST live in resolve.go, not in
//     notebooklm/notebooks.go (separation of concerns): the
//     NotebooksAPI namespace exposes the raw RPC surface; Resolve
//     is a derived convenience that composes it.
package notebooklm

import (
	"context"
	stderrors "errors"
	"fmt"
	"regexp"
	"strings"

	apperrors "github.com/raihankhan/notebooklm-go/internal/app/errors"
	"github.com/raihankhan/notebooklm-go/internal/redact"
)

// ResolveKind names the strategy Resolve applies when matching
// `query` against the notebook universe. The constants are stable
// strings callers can reuse without depending on the default; an
// empty string is equivalent to ResolveKindAuto for ergonomics.
//
// Mirrors the `_app/resolve` mode-string family in notebooklm-py
// (the Python surface uses string literals — title, id, auto —
// the Go port surfaces them as named constants so callers do not
// pass magic values across the boundary).
const (
	// ResolveKindAuto tries `Get(query)` first and falls back to
	// the `List + title filter` route when the id lookup fails.
	// This is the documented default: callers can pass either a
	// title or an id without thinking about which API to call.
	ResolveKindAuto = "auto"

	// ResolveKindID forces the `Get(query)` path. Useful when
	// the caller already knows the query is a 36-char UUID and
	// does not want to pay the listing cost on a title-shaped
	// query that happens to be a valid id.
	ResolveKindID = "id"

	// ResolveKindTitle forces the `List + title filter` path.
	// Useful when the query is known to be a title (a marker
	// the user typed) and a stray exact-id lookup would mask
	// the title semantics with a 404.
	ResolveKindTitle = "title"
)

// defaultResolveKind is the `Kind` zero value used when the
// caller did not pass WithKind. Equal to ResolveKindAuto.
const defaultResolveKind = ResolveKindAuto

// ResolveOption is the functional-option type. Implementations are
// the With* constructors in this file; a nil option is a no-op so
// callers can pass `WithKind("")` unconditionally without growing
// their nil-guards.
//
// The type is named generically (ResolveOption, not
// ResolveKindOption) because the same options bag flows through
// Resolve and ResolveID; future knobs (for example a project-id
// constraint) extend ResolveOptions without churning call sites.
type ResolveOption func(*ResolveOptions)

// ResolveOptions is the typed bag the functional-option
// constructors mutate. The zero value is the documented default
// (Kind="auto", ProjectID="", Fuzzy=false, CaseInsensitive=true).
//
// Structurally identical to the keyword-argument surface Python
// callers pass to `notebooklm/notebooks.py::NotebooksAPI.resolve`
// — every field has a 1:1 partner on the Python side and the
// field set is the union of values Python callers can override.
// Fields are exported through the option constructors only; direct
// construction is reserved for tests.
type ResolveOptions struct {
	// Kind selects the resolver strategy. One of
	// ResolveKindAuto (the default), ResolveKindID, or
	// ResolveKindTitle. Any other string falls back to
	// ResolveKindAuto so a typo from a caller does not silently
	// match nothing.
	Kind string

	// ProjectID scopes the lookup to one project. When set, the
	// list leg of the resolver calls GetByProject instead of
	// List so the matching set is bounded to the project the
	// caller named. Empty means "search the whole user-visible
	// notebook universe".
	ProjectID string

	// Fuzzy enables substring matching on Title. The default
	// (false) requires an exact title match (modulo the case
	// rule below); Fuzzy=true degrades to a
	// strings.Contains check so `Resolve(ctx, "scientific")`
	// matches `Scientific PDF Parsing — Intel`.
	Fuzzy bool

	// CaseInsensitive controls whether the title match honors
	// case. Default true mirrors the canonical notebookLM-Go /
	// Python behavior — users type titles without thinking
	// about whether the title was Title-Cased. Setting it false
	// pins a case-sensitive comparison.
	CaseInsensitive bool
}

// apply satisfies the same nil-safe apply helper the other option
// types in this package expose.
func (f ResolveOption) apply(o *ResolveOptions) {
	if f == nil {
		return
	}
	f(o)
}

// resolveOpts applies every functional option to a fresh
// ResolveOptions and returns the result. The loop is nil-safe so
// a caller can pass `WithXxx(...)` unconditionally.
func resolveOpts(opts []ResolveOption) ResolveOptions {
	o := ResolveOptions{
		// The zero value of ResolveOptions is the documented
		// default. Seed it here so tests that assert against
		// the option bag see the resolved Kind / settings
		// rather than the empty-string default.
		Kind:            defaultResolveKind,
		ProjectID:       "",
		Fuzzy:           false,
		CaseInsensitive: true,
	}
	for _, opt := range opts {
		opt.apply(&o)
	}
	return o
}

// WithKind pins the resolver strategy. The accepted values are
// ResolveKindAuto (the default), ResolveKindID, and
// ResolveKindTitle. An empty string is equivalent to passing
// ResolveKindAuto; any other unrecognized string degrades to
// ResolveKindAuto so a typo from a caller does not silently
// match nothing.
//
// Port of `notebooklm.notebooks.NotebooksAPI.resolve`'s `mode`
// keyword argument (the Python surface uses positional strings;
// the Go port uses named constants).
func WithKind(kind string) ResolveOption {
	return func(o *ResolveOptions) {
		switch kind {
		case ResolveKindAuto, ResolveKindID, ResolveKindTitle:
			o.Kind = kind
		default:
			// Preserve the zero-value default for an empty
			// string; reject anything else by degrading
			// to the default.
			o.Kind = defaultResolveKind
		}
	}
}

// WithProject scopes the lookup to one project. When set, the
// list leg calls GetByProject(ctx, projectID) instead of List so
// the matching set is bounded to the project the caller named.
//
// An empty string is equivalent to omitting the option. Whitespace
// is tolerated for ergonomics but trimmed.
func WithProject(projectID string) ResolveOption {
	return func(o *ResolveOptions) {
		o.ProjectID = strings.TrimSpace(projectID)
	}
}

// WithFuzzy enables substring matching on Title. The default
// (false) requires an exact title match. Fuzzy=true degrades to
// a `strings.Contains` check so partial queries still resolve.
//
// Port of the case-insensitive prefix mode in
// _app.resolve.resolve_ref.
func WithFuzzy(fuzzy bool) ResolveOption {
	return func(o *ResolveOptions) {
		o.Fuzzy = fuzzy
	}
}

// WithCaseSensitive pins the title comparison to case-sensitive.
// The default is case-insensitive; this option exists so a caller
// that knows the canonical title casing can opt back in.
func WithCaseSensitive(caseSensitive bool) ResolveOption {
	return func(o *ResolveOptions) {
		o.CaseInsensitive = !caseSensitive
	}
}

// Resolve looks up a single notebook by name or id and returns
// the canonical typed view. The result is the same `Notebook`
// row the listing endpoints surface, projected from the wire
// decoder.
//
// Resolution precedence (chosen by opts.Kind):
//
//   - ResolveKindID: call `Get(query)` directly. The id path is
//     short-circuited because a `Get` against a non-existent id
//     costs one RPC, while `List + filter` always costs the full
//     listing RPC.
//
//   - ResolveKindTitle: call `List` (or `GetByProject` if the
//     caller passed WithProject), filter to Title == query.
//     Case-insensitive comparison is the default; pass
//     WithCaseSensitive to opt out. Fuzzy=true enables substring
//     matching.
//
//   - ResolveKindAuto (default): try `Get(query)` first. If it
//     returns `apperrors.ErrNotFound`, fall back to the title
//     path. If neither finds anything, return `apperrors.ErrNotFound`
//     with the redacted query attached.
//
// An empty query returns `apperrors.ErrValidation` before any RPC
// fires. The query is redacted through `internal/redact` (and a
// resolver-local `at=` token mask) when it matches a
// credential-shaped substring so an error surfaced to the user
// cannot leak a session id.
//
// A typed `*Notebook` is returned so callers can read either the
// title (for a "use this notebook" command) or the id (for a
// downstream RPC). For callers that only want the id, see
// ResolveID.
//
// Per docs/AGENTS.md rule 5, Resolve lives in `notebooklm/`, not
// inside `notebooklm/notebooks.go` (separation of concerns): the
// NotebooksAPI exposes the raw RPC surface; Resolve is a derived
// convenience that composes it.
//
// Port of `notebooklm.notebooks.NotebooksAPI.resolve` from the
// Python original (the Python CLI uses the `_app.resolve.resolve_ref`
// core for partial-id matching; the Go port keeps the simpler
// "exact or fuzzy title" semantics so the surface stays
// predictable).
func (c *Client) Resolve(ctx context.Context, query string, opts ...ResolveOption) (Notebook, error) {
	if c == nil {
		return Notebook{}, apperrors.Wrap(apperrors.CodeConfigError, stderrors.New("notebooklm: nil Client"))
	}
	o := resolveOpts(opts)

	// Step 1 — empty query short-circuit. Returning a typed
	// ValidationError here is cheaper than dispatching an RPC
	// the user can never get value from.
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return Notebook{}, apperrors.Wrap(apperrors.CodeValidationError,
			stderrors.New("notebooklm.Resolve: query must not be empty"))
	}

	// Step 2 — strategy dispatch.
	switch o.Kind {
	case ResolveKindID:
		return c.resolveAsID(ctx, trimmed)
	case ResolveKindTitle:
		return c.resolveAsTitle(ctx, trimmed, o)
	default: // ResolveKindAuto
		return c.resolveAuto(ctx, trimmed, o)
	}
}

// ResolveID is like Resolve but returns just the id. Useful for
// commands that take `notebooklm <cmd> <name-or-id>` so the user
// can pass either a title or an id without re-typing.
//
// Errors route through Resolve unchanged: ResolveID is a thin
// adapter that drops the typed Notebook row and returns the
// resolved id so a downstream RPC (SourceList, ArtifactList, …)
// does not need to know about the typed view.
//
// An empty query returns `apperrors.ErrValidation` (see Resolve).
// A no-match returns `apperrors.ErrNotFound` with the redacted
// query attached.
func (c *Client) ResolveID(ctx context.Context, query string, opts ...ResolveOption) (string, error) {
	nb, err := c.Resolve(ctx, query, opts...)
	if err != nil {
		return "", err
	}
	// Defensive: a degenerate Notebook from the wire decoder
	// (the row had no id slot) degrades to a not-found error
	// rather than a silent empty id. This is the same row-level
	// short-row tolerance the NotebooksAPI.Get surface already
	// honors; Resolve mirrors.
	if strings.TrimSpace(nb.ID) == "" {
		return "", resolveNotFound(query)
	}
	return nb.ID, nil
}

// resolveAsID dispatches the Get(query) path. Returns a typed
// error matching the canonical apperrors contract.
func (c *Client) resolveAsID(ctx context.Context, query string) (Notebook, error) {
	nb, err := c.Notebooks().Get(ctx, query)
	if err == nil {
		return nb, nil
	}
	// A not-found from the id path is the documented "no
	// match" outcome; surface the canonical typed error so
	// callers can errors.Is on apperrors.ErrNotFound.
	if stderrors.Is(err, apperrors.ErrNotFound) || isRowsNotFound(err) {
		return Notebook{}, resolveNotFound(query)
	}
	// Any other error from Get is wrapped with the canonical
	// CodeNotebookLMError tag the NotebooksAPI namespace
	// already attaches — pass through unchanged so callers
	// see the original classification.
	return Notebook{}, err
}

// resolveAsTitle dispatches the List + title-filter path. Empty
// matching sets return a typed not-found error.
func (c *Client) resolveAsTitle(ctx context.Context, query string, o ResolveOptions) (Notebook, error) {
	page, err := c.listForResolve(ctx, o.ProjectID)
	if err != nil {
		return Notebook{}, err
	}
	for _, nb := range page.Items {
		if titleMatches(nb.Title, query, o.Fuzzy, o.CaseInsensitive) {
			return nb, nil
		}
	}
	return Notebook{}, resolveNotFound(query)
}

// resolveAuto tries the id path first (works when query is an id)
// and falls back to the title path when the id lookup miss is
// recoverable. Other errors short-circuit.
func (c *Client) resolveAuto(ctx context.Context, query string, o ResolveOptions) (Notebook, error) {
	// First try the id route. A successful Get means the
	// caller passed an id (not a title).
	nb, err := c.Notebooks().Get(ctx, query)
	if err == nil {
		return nb, nil
	}
	// A not-found from the id route is the recoverable
	// fallback signal: the query is not an id, so we try
	// the title route. Anything else (transport error,
	// auth, rate-limit) is an unrecoverable failure and
	// passes through unchanged.
	if !stderrors.Is(err, apperrors.ErrNotFound) && !isRowsNotFound(err) {
		return Notebook{}, err
	}
	// Fallback to title-match. Same shape as resolveAsTitle.
	page, lerr := c.listForResolve(ctx, o.ProjectID)
	if lerr != nil {
		return Notebook{}, lerr
	}
	for _, candidate := range page.Items {
		if titleMatches(candidate.Title, query, o.Fuzzy, o.CaseInsensitive) {
			return candidate, nil
		}
	}
	return Notebook{}, resolveNotFound(query)
}

// listForResolve is the List / GetByProject dispatch the resolver
// shares between the title and auto paths. A non-empty
// ProjectID routes through GetByProject; the empty case routes
// through List.
//
// Errors are returned unchanged; the resolver wraps them with the
// canonical CodeNotebookLMError tag through NotebooksAPI.call.
func (c *Client) listForResolve(ctx context.Context, projectID string) (Page[Notebook], error) {
	if projectID != "" {
		return c.Notebooks().GetByProject(ctx, projectID)
	}
	return c.Notebooks().List(ctx)
}

// titleMatches reports whether a candidate title satisfies the
// resolution rules — exact-or-fuzzy, case-sensitive-or-not. The
// rule is the only piece of stateful logic the resolver carries;
// keeping it in its own function makes the test table trivial.
//
// The comparison is symmetric: the candidate title is folded the
// same way the query is folded, so a caller can dial either
// CaseInsensitive=false or Fuzzy=true without surprising the other
// axis.
func titleMatches(candidate, query string, fuzzy, caseInsensitive bool) bool {
	if caseInsensitive {
		candidate = strings.ToLower(candidate)
		query = strings.ToLower(query)
	}
	if fuzzy {
		return strings.Contains(candidate, query)
	}
	return candidate == query
}

// isRowsNotFound reports whether err wraps the rows-package
// ErrNotFound sentinel. The NotebooksAPI namespace passes
// ErrNotFound through unchanged for sources / get-style calls,
// but the rows-package ErrNotFound is its own typed sentinel
// (mirrors `exceptions.NotebookNotFoundError` from Python). The
// resolver honors both so callers see a single typed
// `apperrors.ErrNotFound` regardless of which package detected
// the miss.
//
// We avoid importing the rows package here on purpose so
// resolve.go does not bloat the public SDK's surface area — the
// resolver probes the rows sentinel via the canonical
// `errors.Is` walk and a string-prefix fallback (the rows
// package's typed sentinel message is `rows: notebook not
// found`, the same prefix the rows docs declare for its
// ErrNotFound).
func isRowsNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "notebook not found") ||
		stderrors.Is(err, apperrors.ErrNotFound)
}

// resolveNotFound wraps a typed not-found error with the redacted
// query attached. The redactor masks any `at=…` substring so a
// caller typing a session-id as a "title" does not leak the
// credential to a log or error sink.
//
// The wrap composes `apperrors.ErrNotFound` as the cause so
// callers can branch on `errors.Is(err, apperrors.ErrNotFound)`
// regardless of how the resolver surfaces the typed code. The
// apperrors.Wrap signature sets msg = err.Error() and the wrap's
// Error() method renders "<msg>: <cause.Error()>" — the
// duplication of the sentinel text is the canonical cost of
// wrapping a sentinel; the user-facing value is the typed-code
// contract the spec commits to.
//
// We use Wrap (not New) here because New builds an un-rooted
// wrapError that errors.Is cannot match to the ErrNotFound
// sentinel — the spec calls for errors.Is on the sentinel at
// the call site, and Wrap is the canonical way to make that
// match work.
//
// stderrors.Join gives us a multi-error that unwraps to BOTH
// prose (so the user-facing message renders the resolver's
// context) and apperrors.ErrNotFound (so errors.Is matches the
// sentinel at the call site).
func resolveNotFound(query string) error {
	masked := maskQuery(query)
	prose := fmt.Errorf("notebooklm.Resolve: no notebook matched query %q", masked)
	return apperrors.Wrap(apperrors.CodeNotFound, stderrors.Join(prose, apperrors.ErrNotFound))
}

// maskQuery returns the redacted form of `query`. The redact
// primitive (internal/redact) masks SNlM0e / FdrFJe tokens; the
// resolver additionally masks `at=…` (the GAIA account-marker
// token that survives the standard redact sweep) so a caller
// typing a session-id as a "title" does not leak the credential
// to a log or error sink. The string `"[REDACTED]"` mirrors the
// redact package's substitution so log readers see one
// credential-handling vocabulary end-to-end.
func maskQuery(query string) string {
	// Run the canonical redact primitive first so SNlM0e /
	// FdrFJe / SNlM0e cookie shapes are masked with the
	// package's vocabulary. Then layer the at= redaction on
	// top (a custom regex scoped to the resolver's
	// responsibility).
	masked := string(redact.Apply([]byte(query)))
	// at=token is the format the GAIA auth surface emits;
	// redact never sees it because it lives in cookie values
	// or transport tokens that the redact regexes do not
	// recognize. The resolver masks it locally so an error
	// surfaced from a failed lookup cannot leak it.
	if atRegex.MatchString(masked) {
		masked = atRegex.ReplaceAllString(masked, "at=[REDACTED]")
	}
	return masked
}

// atRegex is the resolver-local pattern for masking `at=…`
// tokens in user-typed queries. Compiled once at package init
// so the resolver's hot path stays allocation-free.
var atRegex = regexp.MustCompile(`at=[^&\s"]*`)
