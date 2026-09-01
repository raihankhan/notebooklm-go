// Command notebooklm is the entry point for github.com/raihankhan/notebooklm-go.
//
// In Phase 0 the binary is a placeholder that prints its version and exits.
// Real flags and subcommands land in later phases (see AGENTIC_LOOP plan).
package main

import "fmt"

const version = "0.1.0-dev"

func main() {
	fmt.Println("notebooklm-go", version)
}
