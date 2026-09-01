// Package buildinfo exposes build-time metadata injected via -ldflags.
//
// The variables below are intentionally package-level (not constants) so the
// Go linker can overwrite them at link time using:
//
//	go build -ldflags "-X github.com/raihankhan/notebooklm-go/internal/buildinfo.Version=v1.2.3 \
//	                   -X github.com/raihankhan/notebooklm-go/internal/buildinfo.Commit=$(git rev-parse --short HEAD) \
//	                   -X github.com/raihankhan/notebooklm-go/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// When the binary is built without -ldflags (e.g. `go test`, `go run`), the
// values fall back to "dev" / "unknown" / "unknown" — see the test in
// buildinfo_test.go for the documented defaults.
package buildinfo

// Version is the human-readable semantic version of the built binary
// (e.g. "0.1.0-dev", "v1.2.3"). Overwritten by -ldflags at link time.
var Version = "dev"

// Commit is the short git SHA the binary was built from. Overwritten by
// -ldflags at link time. "unknown" when the binary is built from a tree that
// is not a git checkout (tarball release, `go run`, etc.).
var Commit = "unknown"

// Date is the UTC build timestamp in RFC 3339 form. Overwritten by -ldflags
// at link time. "unknown" when unset so log lines stay greppable.
var Date = "unknown"
