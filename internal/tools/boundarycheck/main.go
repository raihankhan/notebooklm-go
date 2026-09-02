// Package main implements the boundarycheck tool: a tiny regex-based linter
// that reads declarative rules from boundaries.yaml and rejects any Go
// import that violates them.
//
// Boundary rules — the rows of the table in docs/AGENTS.md rule 5 — are the
// single most load-bearing invariant in this module. A misrule would block
// every later PR from passing CI, so the tool is deliberately small and
// dependency-free: it does not invoke go/types, does not shell out to
// `go list`, and adds no third-party modules to go.mod. The planted-failure
// test in main_test.go proves that a violation is detected and a clean tree
// is accepted.
//
// Note on meta-rules: this tool lives under internal/tools/, not internal/app/,
// so the boundary rules do not apply to it. It may import regexp and any
// other stdlib package without violating the table.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const currentSchemaVersion = 2

// rule is one row of the boundary table.
type rule struct {
	Path     string
	Mode     string // "internal" | "stdlib" | "external"
	External []string
	// ListKey is the most-recently-assigned list-shaped rule field (e.g.
	// "external") so a subsequent "- item" line under the rule knows
	// where to append. Empty when the rule has no current list field
	// open, in which case a dash entry is rejected.
	ListKey string
}

// config is the in-memory shape of boundaries.yaml.
type config struct {
	SchemaVersion int
	Packages      []rule
}

// violation describes a single rejected import.
type violation struct {
	PackagePath string // governed package that imported the bad path
	ImportPath  string // the offending import
	Mode        string // rule mode that was violated ("internal" | "stdlib")
	Reason      string // human-readable explanation
}

// stdlibPrefixes are the small set of top-level path prefixes the Go
// standard library uses. Anything starting with one of these is treated as
// stdlib. Anything else is either an internal package (under our module)
// or a third-party dependency.
var stdlibPrefixes = []string{
	"archive/", "bufio", "bytes", "cmp", "compress/", "container/",
	"context", "crypto/", "database/", "debug/", "embed", "encoding/",
	"errors", "expvar", "flag", "fmt", "go/", "hash/", "html/", "image/",
	"index/", "io", "io/fs", "log/", "maps", "math/", "mime/", "net/",
	"os", "os/exec", "os/signal", "os/user", "path/", "plugin", "reflect",
	"regexp/", "runtime/", "slices", "sort", "strconv", "strings", "sync/",
	"syscall/", "testing/", "text/", "time", "unicode/", "unsafe",
}

// importRe extracts double-quoted import paths.
var importRe = regexp.MustCompile(`"([^"]+)"`)

// loadConfig reads and validates a boundaries.yaml file.
//
// We hand-parse the file rather than pulling in a YAML library to keep
// go.mod pristine — the schema is intentionally tiny and fixed.
func loadConfig(path string) (*config, error) {
	f, err := os.Open(path) // #nosec G304 -- path is operator-controlled CLI flag.
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	cfg := &config{}
	seen := make(map[string]bool)
	var cur *rule

	scanner := bufio.NewScanner(f)
	flush := func() error {
		if cur == nil {
			return nil
		}
		if seen[cur.Path] {
			return fmt.Errorf("duplicate rule for %s", cur.Path)
		}
		if err := validateRule(*cur); err != nil {
			return err
		}
		seen[cur.Path] = true
		cfg.Packages = append(cfg.Packages, *cur)
		cur = nil
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t")
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(stripped, "- "):
			// `- item` has two meanings depending on context:
			//   - at the top of `packages:` (cur == nil): start a new rule
			//     with `- path: <path>` (the existing behavior).
			//   - under a rule whose most-recent key is list-shaped
			//     (cur != nil && cur.ListKey != ""): append one entry to
			//     that list (currently only `external`).
			//   - under a rule whose most-recent key is NOT list-shaped
			//     (cur != nil && cur.ListKey == ""): treat as a sibling
			//     rule — flush the previous one and start a new rule.
			//     This matches the YAML pattern of two
			//     `- path: …` entries at the same indent under
			//     `packages:`, separated by their own `mode: …`
			//     children.
			//
			// The third ambiguity (cur != nil && cur.ListKey != "" but
			// the item looks like `key: value` of a known non-list field)
			// must also resolve to a sibling rule. Otherwise a block
			// ending in `external: [items]` followed by `- path: …`
			// would feed `path: …` into parseRuleList as a malformed
			// allowlist entry and leave the previous rule open so the
			// subsequent `mode: …` collides on duplicate-mode.
			isSiblingKey := false
			if cur != nil {
				rest := strings.TrimSpace(stripped[2:])
				k, _, ok := splitKV(rest)
				if ok && !isListField(k) {
					isSiblingKey = true
				}
			}
			if cur == nil || (cur.ListKey == "" || isSiblingKey) {
				if err := flush(); err != nil {
					return nil, err
				}
				cur = &rule{}
				rest := strings.TrimSpace(stripped[2:])
				if rest != "" {
					k, v, ok := splitKV(rest)
					if !ok {
						return nil, fmt.Errorf("malformed list item: %q", line)
					}
					if err := assignRuleField(cur, k, v); err != nil {
						return nil, err
					}
				}
				continue
			}
			rest := strings.TrimSpace(stripped[2:])
			if rest == "" {
				return nil, fmt.Errorf("empty list item under rule %q", cur.Path)
			}
			if err := parseRuleList(cur, cur.ListKey, rest); err != nil {
				return nil, err
			}
		case cur != nil:
			k, v, ok := splitKV(stripped)
			if !ok {
				return nil, fmt.Errorf("malformed line under rule %q: %q",
					cur.Path, line)
			}
			if err := assignRuleField(cur, k, v); err != nil {
				return nil, err
			}
			// Track the most-recently-set list-capable key so a subsequent
			// `- item` line knows where to append. Default "" rejects stray
			// dash entries under rules that have no list-shaped fields.
			if k == "external" {
				cur.ListKey = k
			} else {
				cur.ListKey = ""
			}
			continue
		default:
			if strings.HasPrefix(stripped, "packages:") {
				continue
			}
			k, v, ok := splitKV(stripped)
			if !ok {
				return nil, fmt.Errorf("malformed top-level line: %q", line)
			}
			if k != "schema_version" {
				return nil, fmt.Errorf("unknown top-level key %q", k)
			}
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
				return nil, fmt.Errorf("schema_version not an int: %q", v)
			}
			cfg.SchemaVersion = n
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = currentSchemaVersion
	}
	if cfg.SchemaVersion != currentSchemaVersion {
		return nil, fmt.Errorf(
			"boundaries.yaml schema_version=%d, want %d",
			cfg.SchemaVersion, currentSchemaVersion,
		)
	}
	if len(cfg.Packages) == 0 {
		return nil, fmt.Errorf("boundaries.yaml declares no packages")
	}
	return cfg, nil
}

// splitKV splits "key: value" into (key, value, true). If there is no
// colon, ok is false.
func splitKV(s string) (string, string, bool) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:]), true
}

func assignRuleField(r *rule, k, v string) error {
	switch k {
	case "path":
		if r.Path != "" {
			return fmt.Errorf("duplicate path field")
		}
		r.Path = v
	case "mode":
		if r.Mode != "" {
			return fmt.Errorf("duplicate mode field")
		}
		r.Mode = v
	case "external":
		// `external:` is a list-opener: the next lines (each `- …`)
		// feed parseRuleList via the caller. We treat the empty-value
		// form as a no-op success (the field shape is set up by the
		// caller; this branch's job is to mark ListKey so the list-
		// item branch knows where to append).
		if v != "" {
			// The `external: <single-string>` shorthand form is
			// intentionally not supported — callers must use the
			// block form. Rejecting the inline form prevents an
			// accidental `external: foo` from silently allowing a
			// bare path with no allowlist enforcement.
			return fmt.Errorf("external: requires a block list (use `- …` lines below)")
		}
		return nil
	default:
		return fmt.Errorf("unknown rule field %q", k)
	}
	return nil
}

// parseRuleList is invoked when the parser encounters a list entry like
// `- github.com/foo/bar` directly under a rule (an alternative to
// `key: [a, b, c]` shorthand which the hand-rolled parser below does not
// support). The first call seeds the rule field and subsequent calls append.
func parseRuleList(r *rule, k, v string) error {
	switch k {
	case "external":
		r.External = append(r.External, v)
	default:
		return fmt.Errorf("unknown rule list field %q", k)
	}
	return nil
}

// isListField reports whether a key may appear as a `- item` directly
// under a rule (as opposed to opening a sibling rule). Today only
// `external` is list-shaped — `path` and `mode` always live on the rule
// itself and any `- path: …` / `- mode: …` under `packages:` is a
// sibling rule.
func isListField(k string) bool {
	return k == "external"
}

func validateRule(r rule) error {
	if r.Path == "" {
		return fmt.Errorf("rule has empty path")
	}
	switch r.Mode {
	case "internal", "stdlib", "external":
		// ok
	case "":
		return fmt.Errorf("rule for %s: missing mode", r.Path)
	default:
		return fmt.Errorf(
			"rule for %s: unknown mode %q (want internal, stdlib, or external)",
			r.Path, r.Mode,
		)
	}
	if r.Mode == "external" && len(r.External) == 0 {
		return fmt.Errorf("rule for %s: mode=external requires at least one entry in `external`", r.Path)
	}
	return nil
}

// isStdlib reports whether path is a Go standard-library package.
func isStdlib(path string) bool {
	for _, p := range stdlibPrefixes {
		if path == strings.TrimSuffix(p, "/") {
			return true
		}
		if strings.HasSuffix(p, "/") && strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// isInternal reports whether path belongs to this module (i.e. starts with
// the module root).
func isInternal(moduleRoot, path string) bool {
	return strings.HasPrefix(path, moduleRoot+"/") || path == moduleRoot
}

// matchesExternalAllowlist reports whether path is permitted under an
// `external:` allowlist entry. The entry syntax is "prefix" or "prefix/*"
// (the latter matches any subpackage of prefix). An entry without a
// trailing "/*" matches the path itself only, so callers typically
// list both "foo" and "foo/*" to permit both the root and its children.
func matchesExternalAllowlist(path string, entries []string) bool {
	for _, e := range entries {
		if strings.HasSuffix(e, "/*") {
			prefix := strings.TrimSuffix(e, "/*")
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
			continue
		}
		if path == e {
			return true
		}
	}
	return false
}

// extractImports walks every .go file under pkgDir and returns the list of
// fully-qualified import paths used by that package.
func extractImports(pkgDir string) ([]string, error) {
	var imports []string
	err := filepath.Walk(pkgDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if (name == "testdata" || name == "vendor" || name == ".git" ||
				strings.HasPrefix(name, ".")) && path != pkgDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := os.Open(path) // #nosec G304 -- path comes from filepath.Walk under pkgDir.
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		scanner := bufio.NewScanner(f)
		inImportBlock := false
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if !inImportBlock {
				if strings.HasPrefix(trimmed, "import (") {
					inImportBlock = true
					if strings.Count(trimmed, "\"") >= 2 {
						for _, m := range importRe.FindAllStringSubmatch(line, -1) {
							imports = append(imports, m[1])
						}
					}
					continue
				}
				if strings.HasPrefix(trimmed, "import ") {
					for _, m := range importRe.FindAllStringSubmatch(line, -1) {
						imports = append(imports, m[1])
					}
					continue
				}
			} else {
				if trimmed == ")" {
					inImportBlock = false
					continue
				}
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				for _, m := range importRe.FindAllStringSubmatch(line, -1) {
					imports = append(imports, m[1])
				}
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return nil, err
	}
	return imports, nil
}

// pkgDirToRel converts a fully-qualified Go package path to a relative
// directory under moduleRoot. Returns "" if the path is outside the module.
func pkgDirToRel(moduleRoot, pkgPath string) string {
	if pkgPath == moduleRoot {
		return "."
	}
	if !strings.HasPrefix(pkgPath, moduleRoot+"/") {
		return ""
	}
	return strings.TrimPrefix(pkgPath, moduleRoot+"/")
}

// checkPackage walks pkgDir and returns every violation against the rule.
func checkPackage(r rule, moduleRoot, pkgDir string) ([]violation, error) {
	imports, err := extractImports(pkgDir)
	if err != nil {
		return nil, err
	}
	var out []violation
	for _, imp := range imports {
		switch r.Mode {
		case "internal":
			if isInternal(moduleRoot, imp) || isStdlib(imp) {
				continue
			}
			out = append(out, violation{
				PackagePath: r.Path,
				ImportPath:  imp,
				Mode:        r.Mode,
				Reason: fmt.Sprintf(
					"%s is mode=internal and may only import stdlib or other packages under %s",
					r.Path, moduleRoot,
				),
			})
		case "stdlib":
			if isStdlib(imp) {
				continue
			}
			out = append(out, violation{
				PackagePath: r.Path,
				ImportPath:  imp,
				Mode:        r.Mode,
				Reason: fmt.Sprintf(
					"%s is mode=stdlib and may not import %s",
					r.Path, imp,
				),
			})
		case "external":
			if isInternal(moduleRoot, imp) || isStdlib(imp) {
				continue
			}
			if matchesExternalAllowlist(imp, r.External) {
				continue
			}
			out = append(out, violation{
				PackagePath: r.Path,
				ImportPath:  imp,
				Mode:        r.Mode,
				Reason: fmt.Sprintf(
					"%s is mode=external and may not import %s (allowlist: %s)",
					r.Path, imp, strings.Join(r.External, ", "),
				),
			})
		}
	}
	return out, nil
}

func main() {
	root := flag.String("root", ".", "module root directory to lint")
	cfgPath := flag.String("config", "boundaries.yaml", "path to boundaries.yaml")
	moduleRoot := flag.String("module",
		"github.com/raihankhan/notebooklm-go",
		"module root import path (must match go.mod)")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "boundarycheck: %v\n", err)
		os.Exit(2)
	}

	var allViolations []violation
	for _, r := range cfg.Packages {
		rel := pkgDirToRel(*moduleRoot, r.Path)
		if rel == "" {
			continue
		}
		pkgDir := filepath.Join(*root, rel)
		if _, err := os.Stat(pkgDir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			fmt.Fprintf(os.Stderr, "boundarycheck: stat %s: %v\n", pkgDir, err)
			os.Exit(2)
		}
		vs, err := checkPackage(r, *moduleRoot, pkgDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "boundarycheck: %s: %v\n", r.Path, err)
			os.Exit(2)
		}
		allViolations = append(allViolations, vs...)
	}

	if len(allViolations) == 0 {
		fmt.Fprintf(os.Stderr, "boundarycheck: OK (%d packages checked)\n", len(cfg.Packages))
		return
	}

	fmt.Fprintf(os.Stderr, "boundarycheck: %d violation(s):\n", len(allViolations))
	for _, v := range allViolations {
		fmt.Fprintf(os.Stderr,
			"  - %s: forbidden import %q\n      rule: %s\n",
			v.PackagePath, v.ImportPath, v.Reason,
		)
	}
	os.Exit(1)
}
