// Package internal/cli — exit code table.
//
// The numeric exit codes are the stable contract every scripted
// caller keys on. docs/07-cli-spec.md "Exit codes" enumerates the
// table verbatim; this file is the single Go-side source of truth
// the root error handler (errors.go) and the --json FlagErrorFunc
// (json.go) consult.
//
// Per docs/AGENTS.md rule 6 ("failures are typed"), every error
// carries a stable machine code; the exit code is then derived from
// the code, never free-form. Renaming or moving an exit code is a
// breaking change for CI gates, Make recipes, and shell aliases.
package cli

// Exit codes. The numeric values are part of the public CLI
// contract. The constants below are the single source of truth.
const (
	// ExitOK is the success code. Returned by every command that
	// completed its work and emitted its payload.
	ExitOK = 0

	// ExitUserError is the user / application error code. Returned
	// for validation, auth, rate-limit, network, config, and any
	// library-class error — the bulk of failures a scripted caller
	// can recover from by retrying or fixing the operator's input.
	ExitUserError = 1

	// ExitSystemError is the system / unexpected error code.
	// Returned for parse-time usage errors (a bad flag combination,
	// a missing required argument) and for any panic or programmer
	// error the CLI did not classify.
	ExitSystemError = 2

	// ExitCancelled is the SIGINT exit code (128 + 2). Returned
	// from the root error handler when the user aborts with Ctrl-C.
	ExitCancelled = 130
)

// ExitCodeFromClassified maps an internal/app/errors.Code to its
// numeric exit code. The mapping is fixed by docs/07-cli-spec.md;
// the root error handler consults this function for every non-nil
// error.
//
// When the stub (T-P5-7) is replaced by the real errors package
// (T-P5-3) the code values are unchanged but the imports in
// errors.go flow through here.
func ExitCodeFromClassified(code string) int {
	switch code {
	case "CANCELLED": //nolint:misspell // docs/07-cli-spec.md canonical.
		return ExitCancelled
	case "UNEXPECTED_ERROR":
		return ExitSystemError
	default:
		// Every other code maps to ExitUserError (1). The mapping
		// is dense by design — any future code that should map to
		// 2 (system error) must add a case here so the contract
		// stays explicit.
		return ExitUserError
	}
}

// ExitCodeForParseError maps a parse-time Cobra error to its exit
// code. Per docs/07-cli-spec.md "Parse-time usage error" → exit 2,
// and "Parse-time other flag error" → exit 1. The distinction is
// "the user typed garbage at the command line" (usage error, exit
// 2) versus "the flag value itself failed to parse" (a Cobra
// FlagError wrapping a typed error, exit 1).
//
// The function inspects the error message for the conventional
// "usage" / "unknown command" / "required flag(s) missing" strings
// Cobra uses; anything else falls through to ExitUserError (1) so
// a parse-time typed error (e.g. a bad value for --backend) still
// reports the right code.
func ExitCodeForParseError(err error) int {
	if err == nil {
		return ExitOK
	}
	msg := err.Error()
	for _, hint := range []string{
		"unknown command",
		"unknown shorthand flag",
		"unknown flag",
		"required flag(s) missing",
		"if any flags in the group [",
		"can't be combined with",
		"accepts", // "accepts 1 arg(s), received 0"
	} {
		if contains(msg, hint) {
			return ExitSystemError
		}
	}
	return ExitUserError
}

// contains is a tiny strings.Contains replacement so this file
// does not import strings (and pollute the imports of callers that
// only need the table). The match is byte-equal — callers that
// need case-insensitive matches must lowercase before calling.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
