// Package sourceadd — gate_test.go: focused tests for the
// safety gates in gate.go.
//
// The classify_test.go table exercises the gates through the
// Validate entry point; this file adds tests that target each
// gate's internal helpers directly so a regression in the
// gate-specific logic (e.g. IP-range detection, symlink
// resolution) is caught even when the higher-level Validate
// path still produces the right answer by accident.
//
// Each test sets up its own sandbox via t.TempDir +
// t.Setenv("NOTEBOOKLM_HOME", …) so the storage root can be
// placed anywhere without touching the operator's real
// ~/.notebooklm/. Tests run sequentially (no t.Parallel) —
// the env-var mutation is process-global and the testing
// package forbids combining Setenv with Parallel.
package sourceadd

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIsInsideDir pins the lexical "is path inside dir" check.
// The table covers the four cases that matter:
//
//   - exact match (path == dir) → inside
//   - direct child (path = dir/file) → inside
//   - sibling (path = dir-sibling) → outside
//   - prefix-only match (path = dir-sibling, lexically)
//     → outside (the famous /foo/bar vs /foo/barbaz trap)
func TestIsInsideDir(t *testing.T) {
	// Not parallel — file-system state is real but the
	// helper is pure; this comment exists for symmetry with
	// the env-mutating tests in this file.
	cases := []struct {
		name string
		path string
		dir  string
		want bool
	}{
		{"exact_match", "/foo/bar", "/foo/bar", true},
		{"direct_child", "/foo/bar/baz", "/foo/bar", true},
		{"deep_child", "/foo/bar/baz/qux", "/foo/bar", true},
		{"sibling", "/foo/bar2", "/foo/bar", false},
		{"prefix_only_trap", "/foo/barbaz", "/foo/bar", false},
		{"parent", "/foo", "/foo/bar", false},
		{"unrelated", "/other/x", "/foo/bar", false},
		// Trailing-separator normalization: filepath.Clean
		// collapses trailing separators before the prefix
		// check, so dir "/foo/bar/" matches path "/foo/bar".
		{"trailing_sep_dir", "/foo/bar/", "/foo/bar", true},
		// Dot-segment resolution: "/foo/bar/./baz" and
		// "/foo/bar/baz" are equivalent after Clean.
		{"dot_segment_path", "/foo/bar/./baz", "/foo/bar", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isInsideDir(tc.path, tc.dir)
			if got != tc.want {
				t.Errorf("isInsideDir(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
			}
		})
	}
}

// TestIsBlockedIP pins the internal-address gate's IP-range
// check. The function is private (lowercase); we test it here
// rather than through Validate so a regression in the IP-range
// logic is caught without depending on the URL parser.
//
// The table covers each blocked range, the unblocked ranges
// (public IPs), and the edge cases that IsLoopback / IsPrivate
// sometimes surprise people on (IPv4-in-IPv6, IPv4-mapped).
func TestIsBlockedIP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		// Loopback v4.
		{"loopback_127_0_0_1", "127.0.0.1", true},
		{"loopback_127_0_0_2", "127.0.0.2", true},
		{"loopback_127_255_255_254", "127.255.255.254", true},
		// IPv6 loopback.
		{"ipv6_loopback", "::1", true},
		// Link-local (cloud IMDS lives here).
		{"link_local_169_254_169_254", "169.254.169.254", true},
		{"link_local_low", "169.254.0.1", true},
		{"link_local_high", "169.254.255.254", true},
		// RFC1918.
		{"rfc1918_10", "10.0.0.1", true},
		{"rfc1918_10_high", "10.255.255.255", true},
		{"rfc1918_172_16", "172.16.0.1", true},
		{"rfc1918_172_31", "172.31.255.254", true},
		{"rfc1918_192_168", "192.168.1.1", true},
		// IPv6 ULA (fc00::/7).
		{"ipv6_ula", "fc00::1", true},
		{"ipv6_ula_alt", "fd00::1", true},
		// IPv4 wildcard.
		{"ipv4_wildcard", "0.0.0.0", true},
		// Public — accepted.
		{"public_8_8_8_8", "8.8.8.8", false},
		{"public_1_1_1_1", "1.1.1.1", false},
		{"public_ip_alt_20_0_0_1", "20.0.0.1", false},
		// IPv6 public.
		{"public_ipv6_google", "2001:4860:4860::8888", false},
		// 172.32 — outside RFC1918.
		{"non_rfc1918_172_32", "172.32.0.1", false},
		// 172.15 — outside RFC1918.
		{"non_rfc1918_172_15", "172.15.0.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("net.ParseIP(%q) returned nil", tc.ip)
			}
			got := isBlockedIP(ip)
			if got != tc.want {
				t.Errorf("isBlockedIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// TestCheckInternalAddress is a thin wrapper around the IP
// gate that exercises the URL-parsing path end-to-end. The
// table mirrors TestClassify_InternalAddressGate in
// classify_test.go but goes through checkInternalAddress
// directly so URL parser changes are caught independently of
// Validate's classify-then-gate composition.
func TestCheckInternalAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"loopback_v4", "http://127.0.0.1/x", true},
		{"loopback_v4_alt", "http://127.0.0.42/x", true},
		{"imds", "http://169.254.169.254/latest/meta-data/", true},
		{"link_local", "http://169.254.10.20/x", true},
		{"rfc1918_10", "http://10.0.0.5/x", true},
		{"rfc1918_172_16", "http://172.16.0.1/x", true},
		{"rfc1918_192_168", "http://192.168.1.1/x", true},
		{"ipv6_loopback", "http://[::1]/x", true},
		{"ipv4_wildcard", "http://0.0.0.0/x", true},
		{"public_dns_name", "http://example.com/x", false},
		{"public_ip_literal", "http://8.8.8.8/x", false},
		// Non-blocked but inside the unusual 172.20 range
		// (still RFC1918 — 172.16/12 covers 172.16-172.31).
		{"rfc1918_172_20", "http://172.20.0.1/x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkInternalAddress(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("checkInternalAddress(%q) returned no error; want internal-address rejection", tc.url)
				}
				if !errors.Is(err, ErrInternalAddress) {
					t.Errorf("checkInternalAddress(%q) error = %v, want wraps ErrInternalAddress", tc.url, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkInternalAddress(%q) returned error: %v", tc.url, err)
			}
		})
	}
}

// TestCheckInternalAddress_BadURL pins the URL-parse failure
// branch of checkInternalAddress. A URL that parses to a
// non-routable shape (empty host) must surface a typed
// validation error, not panic.
func TestCheckInternalAddress_BadURL(t *testing.T) {
	t.Parallel()
	err := checkInternalAddress("not a url at all")
	if err == nil {
		t.Fatalf("checkInternalAddress on non-URL returned no error")
	}
	// The exact error category depends on whether the
	// argument was already rejected as path-shaped by the
	// caller; here we only check that checkInternalAddress
	// surfaces some error (the caller — Validate — gates on
	// Kind so a non-URL never reaches here in production).
}

// TestCheckFilePath pins the symlink gate's behavior on the
// two real failure modes (file inside storage root, symlink
// chain pointing inside storage root) and the two real
// success modes (file outside, symlink chain pointing
// outside).
//
// The test sets NOTEBOOKLM_HOME to the temp dir so the
// storage root equals the sandbox and the operator's real
// ~/.notebooklm/ is never touched.
func TestCheckFilePath(t *testing.T) {
	// Not parallel — env mutation.
	tmp := t.TempDir()
	t.Setenv("NOTEBOOKLM_HOME", tmp)
	t.Setenv("HOME", tmp)

	// Build the sandbox: storage root == tmp, plus a sibling
	// directory that lives outside tmp.
	insideDir := filepath.Join(tmp, "sub")
	if err := os.MkdirAll(insideDir, 0o700); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}
	insideFile := filepath.Join(insideDir, "secret.yaml")
	if err := os.WriteFile(insideFile, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed inside: %v", err)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "report.pdf")
	if err := os.WriteFile(outsideFile, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed outside: %v", err)
	}

	t.Run("inside_rejected", func(t *testing.T) {
		err := checkFilePath(insideFile)
		if err == nil {
			t.Fatalf("checkFilePath(%q) returned no error; want ErrSymlinkInsideProfile", insideFile)
		}
		if !errors.Is(err, ErrSymlinkInsideProfile) {
			t.Errorf("checkFilePath(%q) error = %v, want wraps ErrSymlinkInsideProfile", insideFile, err)
		}
	})

	t.Run("outside_accepted", func(t *testing.T) {
		if err := checkFilePath(outsideFile); err != nil {
			t.Errorf("checkFilePath(%q) returned error: %v; want nil", outsideFile, err)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("symlink_inside_rejected", func(t *testing.T) {
			linkDir := t.TempDir()
			linkPath := filepath.Join(linkDir, "via.yaml")
			if err := os.Symlink(insideFile, linkPath); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			err := checkFilePath(linkPath)
			if err == nil {
				t.Fatalf("checkFilePath(%q) returned no error; want ErrSymlinkInsideProfile", linkPath)
			}
			if !errors.Is(err, ErrSymlinkInsideProfile) {
				t.Errorf("checkFilePath(%q) error = %v, want wraps ErrSymlinkInsideProfile", linkPath, err)
			}
		})

		t.Run("symlink_outside_accepted", func(t *testing.T) {
			linkPath := filepath.Join(tmp, "via.yaml")
			if err := os.Symlink(outsideFile, linkPath); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			if err := checkFilePath(linkPath); err != nil {
				t.Errorf("checkFilePath(%q) returned error: %v; want nil", linkPath, err)
			}
		})

		t.Run("deep_symlink_chain_inside_rejected", func(t *testing.T) {
			// Build a two-hop chain: link1 -> link2 -> insideFile.
			// The chain lives outside tmp, but link2 resolves to
			// insideFile which is inside tmp.
			linkDir := t.TempDir()
			link2 := filepath.Join(linkDir, "link2.yaml")
			link1 := filepath.Join(linkDir, "link1.yaml")
			if err := os.Symlink(insideFile, link2); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			if err := os.Symlink(link2, link1); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			err := checkFilePath(link1)
			if err == nil {
				t.Fatalf("checkFilePath(%q) returned no error; want ErrSymlinkInsideProfile", link1)
			}
			if !errors.Is(err, ErrSymlinkInsideProfile) {
				t.Errorf("checkFilePath(%q) error = %v, want wraps ErrSymlinkInsideProfile", link1, err)
			}
		})
	}
}

// TestStorageRoot_ResolvesEnv pins the StorageRoot helper's
// NOTEBOOKLM_HOME precedence. The helper must honor the env
// var; otherwise the symlink gate's "storage root" would be
// the wrong path in production where the operator overrides
// the home directory.
//
// The test sets NOTEBOOKLM_HOME to a temp dir and verifies
// the helper returns that exact value (cleaned but otherwise
// unchanged).
func TestStorageRoot_ResolvesEnv(t *testing.T) {
	// Not parallel — env mutation.
	tmp := t.TempDir()
	t.Setenv("NOTEBOOKLM_HOME", tmp)
	got, err := StorageRoot()
	if err != nil {
		t.Fatalf("StorageRoot() returned error: %v", err)
	}
	// filepath.Clean may strip a trailing separator; the
	// test asserts the cleaned form matches.
	want := filepath.Clean(tmp)
	if got != want {
		t.Errorf("StorageRoot() = %q, want %q", got, want)
	}
}

// TestCheckFilePath_PathMissing covers the
// filepath.EvalSymlinks error branch. The gate propagates
// missing-target / permission-denied errors verbatim so the
// CLI can format a sensible message; the test pins that
// propagation.
func TestCheckFilePath_PathMissing(t *testing.T) {
	// Not parallel — env mutation.
	tmp := t.TempDir()
	t.Setenv("NOTEBOOKLM_HOME", tmp)
	missing := filepath.Join(tmp, "no-such-file.pdf")
	if err := checkFilePath(missing); err == nil {
		t.Fatalf("checkFilePath(%q) returned no error; want error", missing)
	}
}
