// Command trylockchild attempts atomicio.TryExclusive on a path provided
// as argv[1]. Exits 0 on success, 1 on TryExclusive not acquiring (i.e.
// the path is locked by someone else), and 2 on a real error.
//
// This file lives under testdata so that it is skipped by the
// boundarycheck tool. It is built and exec'd by
// internal/atomicio/flock_test.go::TestFlock_TwoProcesses_ProcessTries.
//
//go:build testflockchild

package main

import (
	"fmt"
	"os"

	"github.com/raihankhan/notebooklm-go/internal/atomicio"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: trylockchild <op> <path>")
		os.Exit(2)
	}
	op, path := os.Args[1], os.Args[2]
	switch op {
	case "tryexclusive":
		_, ok, err := atomicio.TryExclusive(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if !ok {
			os.Exit(1)
		}
		os.Exit(0)
	case "exclusive":
		r, err := atomicio.Exclusive(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := r(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown op:", op)
		os.Exit(2)
	}
}
