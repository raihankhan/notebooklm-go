// Command notebooklm is the entry point for github.com/raihankhan/notebooklm-go.
//
// The binary is wired to internal/cli in T-P5-7 (this file replaces
// the version-only placeholder that lived here through Phase 0-4).
// main.go owns the lifecycle; every command, flag, and error path
// lives under internal/cli so main.go stays a single function.
package main

import (
	"context"
	"os"

	"github.com/raihankhan/notebooklm-go/internal/cli"
)

func main() {
	os.Exit(cli.ExecuteContext(context.Background()))
}
