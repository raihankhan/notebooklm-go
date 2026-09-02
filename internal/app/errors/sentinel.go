package errors

// Sentinel error values. These are the package-level roots that the rest of
// the module wraps with `%w`; adapters use `errors.Is` to detect them
// without coupling to a specific concrete type. The four sentinels cover
// the categories the spec calls out by name in docs/07-cli-spec.md §"Exit
// codes": auth, rate-limit, not-found, and notebook-limit.
//
// The names follow Go convention — `Err` prefix, past tense — and stay
// distinct from the typed error structs in the transport package so a
// caller can branch on either (the typed struct unwraps to the sentinel
// and the sentinel matches the matching `Code` constant via Classify).
//
// Port of the role played by notebooklm.exceptions.AuthError,
// notebooklm.exceptions.RateLimitError, notebooklm.exceptions.NotFoundError,
// and notebooklm.exceptions.NotebookLimitError in
// notebooklm-py/src/notebooklm/cli/error_handler.py::handle_errors.
//
//nolint:errname // The package is called `errors`, so `errors.ErrAuth` is the natural spelling; the linter's preferErrName rule fires on the suffix, not the prefix.
var (
	// ErrAuth is the canonical "authentication or authorization
	// failure" sentinel. Wrapped by transport-layer auth errors and by
	// any future adapter that surfaces a login-required condition.
	ErrAuth = New(CodeAuthError, "notebooklm: authentication error")

	// ErrRateLimited is the canonical "rate limit exceeded" sentinel.
	// The wrapped error typically carries a Retry-After hint (in
	// seconds) on the envelope's `retry_after` field.
	ErrRateLimited = New(CodeRateLimited, "notebooklm: rate limited")

	// ErrNotFound is the canonical "resource missing" sentinel. Wrapped
	// by every concrete NotFoundError regardless of domain (notebook,
	// source, artifact, note, mind map, label, collection) so the
	// umbrella catches them all.
	ErrNotFound = New(CodeNotFound, "notebooklm: resource not found")

	// ErrQuota is the canonical "notebook quota exhausted" sentinel.
	// Wrapped by NotebookLimitError and any adapter that surfaces a
	// per-account ceiling. Renamed from the typed
	// notebooklm.exceptions.NotebookLimitError to keep the sentinel
	// short while still mapping to the NOTEBOOK_LIMIT code.
	ErrQuota = New(CodeNotebookLimit, "notebooklm: notebook quota exhausted")
)
