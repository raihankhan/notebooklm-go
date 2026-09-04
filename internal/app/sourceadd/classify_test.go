// Package sourceadd — classify_test.go: table-driven tests for the
// source-add argument classifier.
//
// The test suite covers three rule families:
//
//  1. URL classification — Drive share URLs, YouTube watch /
//     short URLs, and generic http(s) URLs.
//  2. File path classification — absolute, relative, tilde
//     prefixed, and the inside-the-storage-root rejection that
//     the symlink gate enforces. The inside-storage-root case
//     uses a t.TempDir + os.Symlink (or, when symlink creation
//     is forbidden by the test runner, a direct file) so the
//     table runs without NOTEBOOKLM_HOME side effects.
//  3. Text fallback — anything that does not match a more
//     specific rule lands on KindText.
//
// Each subtest runs in isolation; t.TempDir() is used for any
// file-system state so tests are parallel-safe.
package sourceadd

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestClassify_URL covers the generic-URL, Drive, and YouTube
// rule families. The table is intentionally exhaustive — adding
// a new kind means adding a row here.
func TestClassify_URL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want Kind
	}{
		// Generic URL — accepted as KindURL by Classify.
		{"generic_https", "https://example.com/page", KindURL},
		{"generic_http", "http://example.com/page", KindURL},
		{"generic_https_with_query", "https://example.com/page?id=42&q=foo", KindURL},
		{"generic_https_with_path", "https://example.com/a/b/c", KindURL},
		{"generic_https_with_port", "https://example.com:8443/page", KindURL},

		// YouTube — KindYouTube wins over KindURL.
		{"youtube_watch", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", KindYouTube},
		{"youtube_no_www", "https://youtube.com/watch?v=dQw4w9WgXcQ", KindYouTube},
		{"youtube_short", "https://youtu.be/dQw4w9WgXcQ", KindYouTube},
		{"youtube_short_with_query", "https://youtu.be/abc123?t=42", KindYouTube},
		{"youtube_watch_extra_query", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10s", KindYouTube},

		// Drive — KindDrive wins over KindURL.
		{"drive_file", "https://drive.google.com/file/d/abc123/view", KindDrive},
		{"drive_document", "https://docs.google.com/document/d/abc123/edit", KindDrive},
		{"drive_spreadsheet", "https://docs.google.com/spreadsheets/d/abc123/edit", KindDrive},
		{"drive_presentation", "https://docs.google.com/presentation/d/abc123/edit", KindDrive},
		// drive.google.com can also serve documents via the
		// /document/d/... path; both shapes are accepted.
		{"drive_via_drive_host_doc", "https://drive.google.com/document/d/abc123/view", KindDrive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Classify(tc.in)
			if err != nil {
				t.Fatalf("Classify(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestClassify_File covers the file-path rule family. The
// table relies on t.TempDir() so the tests are independent of
// the operator's $HOME and run on every platform without
// NOTEBOOKLM_HOME side effects.
//
// The test sets the working directory to the temp root via
// t.Chdir so relative paths (./foo, ../foo) resolve inside the
// sandbox rather than against the operator's actual CWD.
// Because t.Chdir mutates process-global state the test runs
// sequentially (no t.Parallel).
func TestClassify_File(t *testing.T) {
	// Not parallel — t.Chdir and the table's HOME mutation via
	// raw os.Setenv both touch process-global state.

	tmp := t.TempDir()
	// Also create a sibling temp dir so we can resolve "../sibling/file.pdf".
	sibling := t.TempDir()

	// Pre-create three files for the absolute / relative / tilde
	// variants. The tilde case points inside tmp so we can
	// construct a HOME-relative path without depending on the
	// real $HOME.
	absFile := filepath.Join(tmp, "report.pdf")
	if err := os.WriteFile(absFile, []byte("hi"), 0o600); err != nil {
		t.Fatalf("seed abs file: %v", err)
	}
	subdir := filepath.Join(tmp, "sub")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatalf("seed subdir: %v", err)
	}
	relFile := filepath.Join(subdir, "notes.md")
	if err := os.WriteFile(relFile, []byte("hi"), 0o600); err != nil {
		t.Fatalf("seed rel file: %v", err)
	}

	// Place a sibling file under sibling so a "../sibling/foo"
	// relative path resolves outside tmp. Resolve the path
	// relative to tmp's parent so the test works regardless of
	// how Go's t.TempDir names sibling directories.
	siblingFile := filepath.Join(sibling, "sib.pdf")
	if err := os.WriteFile(siblingFile, []byte("hi"), 0o600); err != nil {
		t.Fatalf("seed sibling file: %v", err)
	}
	parent := filepath.Dir(tmp)
	siblingBase := filepath.Base(sibling)
	relSibling := "../" + siblingBase + "/sib.pdf"
	// The parent of the sibling dir is the same as the parent
	// of tmp — confirm before using the relative path.
	if filepath.Clean(filepath.Join(tmp, relSibling)) != siblingFile {
		t.Fatalf("sibling layout changed: tmp=%s sibling=%s rel=%s",
			tmp, sibling, relSibling)
	}
	if parent != filepath.Dir(sibling) {
		t.Fatalf("sibling layout: tmp.parent=%s sibling.parent=%s",
			parent, filepath.Dir(sibling))
	}

	// Save and restore $HOME so the tilde test does not leak
	// into sibling tests. Each subtest that touches $HOME does
	// the swap inside its own scope.
	origHome := os.Getenv("HOME")
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })

	// Chdir into tmp so ./ and ../ resolve inside the sandbox.
	// t.Chdir restores the original CWD on test cleanup.
	t.Chdir(tmp)

	cases := []struct {
		name string
		in   string
		home string
		want Kind
	}{
		{"absolute_path", absFile, "", KindFile},
		{"relative_dot_slash", "./report.pdf", "", KindFile},
		{"relative_parent_slash", relSibling, "", KindFile},
		{"relative_subdir_dot_slash", "./sub/notes.md", "", KindFile},
		// Tilde-expanded paths require HOME to point at tmp
		// so ~ resolves there. The tilde-expansion is exact —
		// we do not need to rebuild the full path.
		{"tilde_path", "~/report.pdf", tmp, KindFile},
		{"tilde_only_resolves_to_home", "~", tmp, KindFile},
		{"tilde_subdir", "~/sub/notes.md", tmp, KindFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.home != "" {
				_ = os.Setenv("HOME", tc.home)
			} else {
				_ = os.Setenv("HOME", origHome)
			}
			got, err := Classify(tc.in)
			if err != nil {
				t.Fatalf("Classify(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestClassify_Text covers the text fallback rule. Anything
// that is not URL-shaped and not path-shaped lands on KindText.
func TestClassify_Text(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"plain_text", "hello world"},
		{"paragraph_text", "This is a paragraph of plain text the user pasted."},
		{"text_with_punctuation", "Buy milk, eggs, and bread!"},
		{"text_with_url_inside", "Check https://example.com later"},
		{"single_word", "notebook"},
		// Empty string is treated as text by Classify; the
		// CLI rejects it as a usage error before reaching the
		// classifier. The classification result is therefore
		// KindText (the fallback) without error.
		{"empty_string", ""},
		// Whitespace-only string is treated the same way —
		// the classifier returns the fallback kind and lets
		// the adapter surface a "blank input" error if it
		// cares.
		{"whitespace_only", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Classify(tc.in)
			if err != nil {
				t.Fatalf("Classify(%q) returned error: %v", tc.in, err)
			}
			if got != KindText {
				t.Errorf("Classify(%q) = %q, want %q", tc.in, got, KindText)
			}
		})
	}
}

// TestClassify_Ambiguous covers the cases that should produce
// KindUnknown + ErrAmbiguousSource. The table pins the "must
// fail" behavior of the classifier on inputs that either look
// path-shaped but point at a non-existent file, or look like
// both a path and a URL.
func TestClassify_Ambiguous(t *testing.T) {
	// Not parallel — t.Setenv mutates process-global state and
	// the testing package forbids combining Setenv with Parallel.

	// Use a temp HOME so the tilde path resolves somewhere
	// predictable; t.TempDir() gives us an isolated root.
	tmp := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
	t.Setenv("HOME", tmp)

	cases := []struct {
		name string
		in   string
	}{
		// Path-shaped but missing on disk — the gate refuses
		// to silently treat this as text because that would
		// upload the path string itself as a text source.
		{"path_does_not_exist", "/no/such/file.pdf"},
		{"relative_path_missing", "./no-such-file.pdf"},
		{"tilde_path_missing", "~/no-such-file.pdf"},
		// Bare host with no scheme — neither URL-shaped nor
		// path-shaped, treated as text (no error). This case
		// documents the "do not raise on edge input" behavior
		// rather than the "raise on ambiguous" behavior, so
		// it lives in the dedicated TestClassify_Text table.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Classify(tc.in)
			if err == nil {
				t.Fatalf("Classify(%q) returned no error; want ErrAmbiguousSource (got kind=%q)", tc.in, got)
			}
			if !errors.Is(err, ErrAmbiguousSource) {
				t.Errorf("Classify(%q) error = %v, want wraps ErrAmbiguousSource", tc.in, err)
			}
			if got != KindUnknown {
				t.Errorf("Classify(%q) kind = %q, want %q", tc.in, got, KindUnknown)
			}
		})
	}
}

// TestClassify_MalformedURL covers inputs that start with a
// URL scheme but parse to something unusable (empty host, or
// a non-IP non-name token). These should NOT be classified as
// URLs; they fall through to the path / text branches.
//
// We pin behavior for two important shapes:
//
//   - "http://" alone (scheme + nothing) — net.ParseIP returns
//     nil for the empty host, so isGenericURL returns false.
//     The string is then path-shaped by neither rule (it does
//     not start with "/", "./", or "~"), so it lands on
//     KindText. That is the documented fallback: a user who
//     types "http://" by mistake gets inline-text behavior
//     rather than a URL-validation error.
//   - "https://" alone — same as above.
func TestClassify_MalformedURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want Kind
	}{
		{"empty_host_http", "http://", KindText},
		{"empty_host_https", "https://", KindText},
		// Scheme + host + no path is still a valid URL — net/url
		// accepts it. We classify it as KindURL.
		{"scheme_only_with_name", "http://example", KindURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Classify(tc.in)
			if err != nil {
				t.Fatalf("Classify(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestClassify_SymlinkGateRejectsStorageRoot pins the
// "file inside ~/.notebooklm/" rejection. The symlink gate
// lives in gate.go; this test constructs a sandboxed HOME
// so the test never touches the operator's real storage root.
//
// The test seeds a tmp directory, sets HOME to that tmp
// directory (so paths.Home() resolves there), drops a file
// inside it, and verifies Validate rejects the path.
//
// Symlinks are exercised when supported by the OS; on
// platforms where os.Symlink returns ErrPermission we
// skip the symlink-specific subtests.
func TestClassify_SymlinkGateRejectsStorageRoot(t *testing.T) {
	// Not parallel — t.Setenv mutates process-global state
	// and the testing package forbids combining Setenv with
	// Parallel.

	tmp := t.TempDir()
	// paths.Home() resolves NOTEBOOKLM_HOME first, then falls
	// back to $HOME/.notebooklm. We point NOTEBOOKLM_HOME at
	// tmp directly so the storage root equals tmp and any file
	// inside tmp is "inside the storage root".
	t.Setenv("NOTEBOOKLM_HOME", tmp)
	// Belt and braces: clear HOME so a future paths.Home
	// change cannot pick up $HOME/.notebooklm as the root.
	t.Setenv("HOME", tmp)
	// The storage root is now <tmp>; the gate rejects anything
	// inside it.

	// Drop a regular file inside the storage root.
	inside := filepath.Join(tmp, "cassettes", "foo.yaml")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(inside, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// Drop a regular file outside the storage root.
	outside := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(outside, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed outside: %v", err)
	}

	t.Run("file_inside_storage_root_rejected", func(t *testing.T) {
		_, err := Validate(inside, KindUnknown)
		if err == nil {
			t.Fatalf("Validate(%q) returned no error; want ErrSymlinkInsideProfile", inside)
		}
		if !errors.Is(err, ErrSymlinkInsideProfile) {
			t.Errorf("Validate(%q) error = %v, want wraps ErrSymlinkInsideProfile", inside, err)
		}
	})

	t.Run("file_outside_storage_root_accepted", func(t *testing.T) {
		kind, err := Validate(outside, KindUnknown)
		if err != nil {
			t.Fatalf("Validate(%q) returned error: %v", outside, err)
		}
		if kind != KindFile {
			t.Errorf("Validate(%q) kind = %q, want %q", outside, kind, KindFile)
		}
	})

	// Symlink-inside-storage-root: create a symlink in the
	// outside area that points inside the storage root. This
	// is the malicious-user scenario the gate defends against.
	if runtime.GOOS != "windows" {
		t.Run("symlink_to_storage_root_rejected", func(t *testing.T) {
			linkDir := t.TempDir()
			linkPath := filepath.Join(linkDir, "link.yaml")
			if err := os.Symlink(inside, linkPath); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			_, err := Validate(linkPath, KindUnknown)
			if err == nil {
				t.Fatalf("Validate(%q) returned no error; want ErrSymlinkInsideProfile", linkPath)
			}
			if !errors.Is(err, ErrSymlinkInsideProfile) {
				t.Errorf("Validate(%q) error = %v, want wraps ErrSymlinkInsideProfile", linkPath, err)
			}
		})
	}

	// Symlink-from-storage-root-to-outside: a symlink that lives
	// inside the storage root but points outside. The gate's job
	// per its docstring is to reject "paths that resolve inside
	// ~/.notebooklm/" — the resolved target here is OUTSIDE, so
	// the gate accepts it. The link itself lives in the storage
	// root but is never read; only the resolved target is.
	if runtime.GOOS != "windows" {
		t.Run("symlink_from_storage_root_to_outside_accepted", func(t *testing.T) {
			linkPath := filepath.Join(tmp, "from.yaml")
			if err := os.Symlink(outside, linkPath); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			kind, err := Validate(linkPath, KindUnknown)
			if err != nil {
				t.Fatalf("Validate(%q) returned error: %v", linkPath, err)
			}
			if kind != KindFile {
				t.Errorf("Validate(%q) kind = %q, want %q", linkPath, kind, KindFile)
			}
		})
	}
}

// TestClassify_InternalAddressGate pins the URL gate. The
// table covers the four address families the gate refuses
// (loopback v4, IMDS / link-local, RFC1918 10/8, RFC1918
// 192.168/16, IPv6 loopback, IPv4 wildcard 0.0.0.0) plus the
// two acceptance cases (public DNS name, public IP literal).
func TestClassify_InternalAddressGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		in        string
		wantErr   bool
		wantBlock bool
	}{
		// Loopback v4.
		{"loopback_v4", "http://127.0.0.1/x", true, true},
		{"loopback_v4_alt", "http://127.0.0.42/x", true, true},
		// IMDS / link-local.
		{"imds", "http://169.254.169.254/latest/meta-data/", true, true},
		{"link_local", "http://169.254.10.20/x", true, true},
		// RFC1918 ranges.
		{"rfc1918_10", "http://10.0.0.5/x", true, true},
		{"rfc1918_10_high", "http://10.255.255.255/x", true, true},
		{"rfc1918_172_16", "http://172.16.0.1/x", true, true},
		{"rfc1918_172_31", "http://172.31.255.254/x", true, true},
		{"rfc1918_192_168", "http://192.168.1.1/x", true, true},
		// IPv6 loopback + ULA.
		{"ipv6_loopback", "http://[::1]/x", true, true},
		{"ipv6_ula", "http://[fc00::1]/x", true, true},
		// IPv4 wildcard 0.0.0.0.
		{"ipv4_wildcard", "http://0.0.0.0/x", true, true},
		// Public IP literal — accepted.
		{"public_ip_literal", "http://8.8.8.8/x", false, false},
		// Public DNS name — accepted (DNS is the transport's job).
		{"public_dns_name", "http://example.com/x", false, false},
		// Public IP literal with a non-blocked range.
		{"public_ip_alt", "https://1.1.1.1/x", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kind, err := Validate(tc.in, KindUnknown)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Validate(%q) returned no error; want internal-address rejection", tc.in)
				}
				if tc.wantBlock && !errors.Is(err, ErrInternalAddress) {
					t.Errorf("Validate(%q) error = %v, want wraps ErrInternalAddress", tc.in, err)
				}
				if kind != KindUnknown {
					t.Errorf("Validate(%q) kind = %q, want %q", tc.in, kind, KindUnknown)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate(%q) returned error: %v", tc.in, err)
			}
			// Kind must round-trip through Classify's URL
			// branch — KindURL for the public-IP / DNS cases.
			if kind != KindURL {
				t.Errorf("Validate(%q) kind = %q, want %q", tc.in, kind, KindURL)
			}
		})
	}
}

// TestClassify_KindStringValues pins the wire values the
// Kind constants carry. The strings are the canonical JSON
// `kind:` field on the source row the SDK returns; renaming
// a constant is a wire-incompatible change. Pinned here so a
// future refactor does not silently break the contract.
func TestClassify_KindStringValues(t *testing.T) {
	t.Parallel()
	cases := map[Kind]string{
		KindURL:     "url",
		KindYouTube: "youtube",
		KindText:    "text",
		KindFile:    "file",
		KindDrive:   "drive",
		KindUnknown: "unknown",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Errorf("Kind(%q) stringer = %q, want %q", k, string(k), want)
		}
	}
}

// TestExpandTilde_Unit exercises the tilde-expansion helper
// directly so a regression in the helper is caught even when
// the higher-level Classify path is happy.
//
// The table covers: bare "~", "~/path", "~user/path" (left
// alone), absolute path (no-op), relative path (no-op), and
// the empty string.
func TestExpandTilde_Unit(t *testing.T) {
	// Not parallel — t.Setenv mutates process-global state
	// and the testing package forbids combining Setenv with
	// Parallel.
	tmp := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
	t.Setenv("HOME", tmp)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare_tilde", "~", tmp},
		{"tilde_slash_path", "~/foo.txt", filepath.Join(tmp, "foo.txt")},
		{"absolute_no_tilde", "/tmp/foo", "/tmp/foo"},
		{"relative_no_tilde", "./foo", "./foo"},
		{"empty_string", "", ""},
		// Other-user tilde (e.g. ~alice/foo) is NOT expanded;
		// the helper passes it through unchanged. The Go
		// runtime does not expand ~user; the classifier's
		// tilde rule requires "~/<segment>" so a stray
		// "~user" never reaches here as a path-shaped string.
		{"other_user_tilde_pass_through", "~alice/foo", "~alice/foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := expandTilde(tc.in)
			if err != nil {
				t.Fatalf("expandTilde(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("expandTilde(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestClassify_NoHOMEFallback exercises the
// UserHomeDir-error path indirectly. The classifier surfaces
// this as an ambiguous-source error rather than a panic; the
// test verifies the error chain wraps ErrAmbiguousSource so a
// caller can errors.Is on it.
//
// Setting HOME to the empty string causes os.UserHomeDir to
// fail on every platform (the runtime falls back to
// os.Getenv("USERPROFILE") on Windows but we set both).
func TestClassify_NoHOMEFallback(t *testing.T) {
	// Not parallel — the test mutates HOME.
	origHome := os.Getenv("HOME")
	origUserProfile := os.Getenv("USERPROFILE")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		_ = os.Setenv("USERPROFILE", origUserProfile)
	})
	_ = os.Setenv("HOME", "")
	_ = os.Setenv("USERPROFILE", "")

	// "~" requires home-dir resolution; the classifier must
	// return an error rather than panic.
	got, err := Classify("~")
	if err == nil {
		t.Fatalf("Classify(\"~\") returned no error; want ambiguous-source error")
	}
	if !errors.Is(err, ErrAmbiguousSource) {
		t.Errorf("Classify(\"~\") error = %v, want wraps ErrAmbiguousSource", err)
	}
	if got != KindUnknown {
		t.Errorf("Classify(\"~\") kind = %q, want %q", got, KindUnknown)
	}
}
