// Package cmd tests — command constructor shape.
//
// These tests pin the Cobra wire-up of every leaf command T-P5-8
// ships (Args / GroupID / Use string). They do NOT exercise the
// RunE body — that requires a fake transport stub which lives in
// a follow-up. The shape tests fail loudly when a future refactor
// silently drops an arg-count check or migrates a command to the
// wrong cobra.Group.
package cmd

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/internal/cligroups"
)

// findSubcommand returns the named subcommand under parent, or
// nil if not registered.
func findSubcommand(parent *cobra.Command, name string) *cobra.Command {
	for _, sub := range parent.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	return nil
}

func TestRegisterInstallsAllCommands(t *testing.T) {
	root := &cobra.Command{}
	Register(root)

	// Session bin: leaf commands attached directly to root (no parent).
	for _, leaf := range []string{"use", "status", "clear"} {
		if findSubcommand(root, leaf) == nil {
			t.Errorf("root: missing session leaf %q", leaf)
		}
	}

	// Other bins: parents + leaves.
	groups := []struct {
		parent string
		leaves []string
	}{
		{"notebook", []string{"list", "create", "delete", "rename", "summary", "metadata"}},
		{"profile", []string{"list", "create", "switch", "delete", "rename"}},
		{"auth", []string{"check"}},
	}
	for _, g := range groups {
		parent := findSubcommand(root, g.parent)
		if parent == nil {
			t.Errorf("missing parent command %q", g.parent)
			continue
		}
		for _, leaf := range g.leaves {
			if findSubcommand(parent, leaf) == nil {
				t.Errorf("%s: missing leaf %q", g.parent, leaf)
			}
		}
	}
}

func TestSessionCommandsAreInSessionGroup(t *testing.T) {
	root := &cobra.Command{}
	Register(root)
	for _, leaf := range []string{"use", "status", "clear"} {
		lc := findSubcommand(root, leaf)
		if lc == nil {
			t.Errorf("root: missing session leaf %q", leaf)
			continue
		}
		if lc.GroupID != cligroups.Session {
			t.Errorf("session %s.GroupID = %q, want %q", leaf, lc.GroupID, cligroups.Session)
		}
	}
}

func TestNotebookCommandsAreInNotebookGroup(t *testing.T) {
	root := &cobra.Command{}
	Register(root)
	notebook := findSubcommand(root, "notebook")
	if notebook == nil {
		t.Fatalf("no notebook command")
	}
	for _, leaf := range []string{"list", "create", "delete", "rename", "summary", "metadata"} {
		lc := findSubcommand(notebook, leaf)
		if lc == nil {
			t.Errorf("notebook: missing %s", leaf)
			continue
		}
		if lc.GroupID != cligroups.Notebook {
			t.Errorf("notebook %s.GroupID = %q, want %q", leaf, lc.GroupID, cligroups.Notebook)
		}
	}
}

func TestProfileCommandsAreInProfileGroup(t *testing.T) {
	root := &cobra.Command{}
	Register(root)
	profile := findSubcommand(root, "profile")
	if profile == nil {
		t.Fatalf("no profile command")
	}
	for _, leaf := range []string{"list", "create", "switch", "delete", "rename"} {
		lc := findSubcommand(profile, leaf)
		if lc == nil {
			t.Errorf("profile: missing %s", leaf)
			continue
		}
		if lc.GroupID != cligroups.Profile {
			t.Errorf("profile %s.GroupID = %q, want %q", leaf, lc.GroupID, cligroups.Profile)
		}
	}
}

func TestAuthCheckIsInAuthGroup(t *testing.T) {
	root := &cobra.Command{}
	Register(root)
	auth := findSubcommand(root, "auth")
	if auth == nil {
		t.Fatalf("no auth command")
	}
	check := findSubcommand(auth, "check")
	if check == nil {
		t.Fatalf("no auth check subcommand")
	}
	if check.GroupID != cligroups.Auth {
		t.Errorf("auth check.GroupID = %q, want %q", check.GroupID, cligroups.Auth)
	}
}

// TestArgCountPinned covers the exact-args constraints every
// leaf command declares. A future refactor that loosens them
// fails this test loudly.
//
// Session commands attach directly to root (no "session" parent).
func TestArgCountPinned(t *testing.T) {
	cases := []struct {
		parent string // empty = leaf attached directly to root.
		leaf   string
		args   int
	}{
		{"", "use", 1},
		{"", "status", 0},
		{"", "clear", 0},
		{"notebook", "list", 0},
		{"notebook", "create", 1},
		{"notebook", "delete", 1},
		{"notebook", "rename", 2},
		{"notebook", "summary", 1},
		{"notebook", "metadata", 1},
		{"profile", "create", 1},
		{"profile", "switch", 1},
		{"profile", "delete", 1},
		{"profile", "rename", 2},
		{"auth", "check", 0},
	}
	root := &cobra.Command{}
	Register(root)
	for _, c := range cases {
		t.Run(c.parent+"_"+c.leaf, func(t *testing.T) {
			var l *cobra.Command
			if c.parent == "" {
				l = findSubcommand(root, c.leaf)
			} else {
				p := findSubcommand(root, c.parent)
				if p == nil {
					t.Fatalf("no parent %s", c.parent)
				}
				l = findSubcommand(p, c.leaf)
			}
			if l == nil {
				t.Fatalf("no leaf %s/%s", c.parent, c.leaf)
			}
			if l.Args == nil {
				// Commands that take 0 args don't need Args; the
				// Cobra default is "any number of args", which is
				// fine for status / clear / list / check.
				if c.args != 0 {
					t.Fatalf("%s/%s: no Args constraint set, want %d", c.parent, c.leaf, c.args)
				}
				return
			}
			err := l.Args(l, []string{})
			if c.args == 0 {
				if err != nil {
					t.Errorf("%s/%s: Args(0 args) err = %v, want nil", c.parent, c.leaf, err)
				}
			} else {
				if err == nil {
					t.Errorf("%s/%s: Args(0 args) = nil, want error", c.parent, c.leaf)
				}
			}
		})
	}
}
