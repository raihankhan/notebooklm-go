// Command scrubhar rewrites credential material out of a recorded VCR
// cassette (YAML, the format go-vcr.v4 emits) in place, atomically.
//
// A cassette recorded from a live session contains full-account credentials:
// the Cookie request header, the Set-Cookie response headers, the CSRF
// token (SNlM0e), the session id (FdrFJe), the at= query parameter, the
// f.sid session id, the account email, and signed download URLs. The
// scrubber replaces each known credential shape with a fixed placeholder
// (SCRUBBED) so the file is safe to commit and to share with a contractor.
//
// The script is idempotent by construction: running it twice produces a
// byte-identical file the second time. Every replacement target has a
// stable placeholder, and no replacement touches a placeholder it itself
// would emit.
//
// Usage:
//
//	scrubhar path/to/cassette.yaml          # rewrite in place
//	scrubhar -check path/to/cassette.yaml   # exit 1 if any credential remains
//	scrubhar path1.yaml path2.yaml ...      # rewrite every argument
//
// Exit codes:
//
//	0 — every file written (or already clean in -check mode)
//	1 — a credential pattern still matched after rewriting
//	2 — a file could not be read or written
//
// The byte-level redaction lives in the parent package so the same
// primitive is callable from the cassette harness (see
// internal/tools/cassette). This command is the operator-facing driver.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/raihankhan/notebooklm-go/internal/atomicio"
	"github.com/raihankhan/notebooklm-go/internal/tools/scrubhar"
)

// processFile reads path, rewrites credentials, and writes the result
// back atomically. When checkOnly is true, the file is left untouched
// and the function reports whether a rewrite would have changed it.
func processFile(path string, checkOnly bool) (changed bool, err error) {
	orig, err := os.ReadFile(path) // #nosec G304 -- path is operator-supplied.
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	rewritten := scrubhar.ScrubBytes(orig)
	if bytes.Equal(orig, rewritten) {
		return false, nil
	}
	if checkOnly {
		return true, nil
	}
	if err := atomicio.WriteFile(path, rewritten, 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func main() {
	check := flag.Bool("check", false, "exit 1 if any file would change; do not write")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: scrubhar [-check] path1.yaml [path2.yaml ...]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	anyChanged := false
	for _, arg := range flag.Args() {
		path, err := filepath.Abs(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scrubhar: %s: %v\n", arg, err)
			os.Exit(2)
		}
		changed, err := processFile(path, *check)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scrubhar: %v\n", err)
			os.Exit(2)
		}
		if changed {
			anyChanged = true
			fmt.Fprintf(os.Stderr, "scrubhar: rewrote %s\n", path)
		}
	}

	if *check && anyChanged {
		os.Exit(1)
	}
}
