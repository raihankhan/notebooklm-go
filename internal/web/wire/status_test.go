package wire

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestSanitizeStatusMessage_TableDriven covers the documented whitespace
// collapse, the 300-char cap with ellipsis, the empty-input short-circuit,
// and the nil passthrough. Each row mirrors one rule in the doc-comment
// on SanitizeStatusMessage.
func TestSanitizeStatusMessage_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		// AC4: whitespace collapse.
		{"collapse_double_space", "  hello  world  ", "hello world"},
		{"collapse_tab_newline", "\tfoo\n\nbar\t", "foo bar"},
		{"collapse_only_whitespace", "    \t\n  ", ""},
		{"empty_string", "", ""},

		// Plain pass-through cases.
		{"no_whitespace", "plain", "plain"},
		{"already_collapsed", "a b c", "a b c"},

		// Cap at 300 chars with ellipsis.
		{"exact_300_chars", strings.Repeat("a", MaxStatusMessageChars), strings.Repeat("a", MaxStatusMessageChars)},
		{"over_300_chars", strings.Repeat("a", MaxStatusMessageChars+10), strings.Repeat("a", MaxStatusMessageChars) + "…"},
		// Whitespace at the cut boundary: 150 'a's, then " ", then
		// another 150 'a's, then 5 trailing spaces. After Fields the
		// body is "a"*150 + " " + "a"*150 (301 chars); the [:300] slice
		// ends mid-run-of-'a', so rstrip is a no-op and we get
		// "a"*150 + " " + "a"*149 + "…" (the interior space survives).
		{"cap_with_internal_whitespace",
			strings.Repeat("a", 150) + " " + strings.Repeat("a", 150) + "     ",
			strings.Repeat("a", 150) + " " + strings.Repeat("a", 149) + "…"},
		// rstrip path: 299 'a's, then 5 spaces, then more 'a's. After
		// Fields: "a"*299 + " " + "a"*N. We pick N so the 300th char
		// (index 299) is the space — slice [:300] = "a"*299 + " ",
		// rstrip drops the space, + "…" = 299 'a's + "…".
		{"cap_rstrip_trims_boundary_space",
			strings.Repeat("a", 299) + "     " + strings.Repeat("a", 20),
			strings.Repeat("a", MaxStatusMessageChars-1) + "…"},

		// Nil passes through as empty.
		{"nil", nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeStatusMessage(tc.in)
			if got != tc.want {
				t.Fatalf("SanitizeStatusMessage(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeStatusMessage_NonStringDegradesAndWarns is the AC5 test:
// a non-string input must return a degraded message (sentinel that
// contains "invalid") AND emit exactly one WARN log line. It must not
// panic on anything we can throw at it.
func TestSanitizeStatusMessage_NonStringDegradesAndWarns(t *testing.T) {
	inputs := []any{
		42,
		true,
		[]string{"a", "b"},
		map[string]int{"k": 1},
		struct{ X int }{X: 1},
	}

	for _, in := range inputs {
		t.Run(typeName(in), func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			got := SanitizeStatusMessage(in)
			if !strings.Contains(got, "invalid") {
				t.Fatalf("SanitizeStatusMessage(%#v) = %q, want a degraded message containing %q", in, got, "invalid")
			}
			out := buf.String()
			if !strings.Contains(out, "level=WARN") {
				t.Fatalf("expected a WARN log line for non-string input, got:\n%s", out)
			}
			if !strings.Contains(out, "SanitizeStatusMessage") {
				t.Fatalf("expected log line to name the function, got:\n%s", out)
			}
		})
	}
}

// TestSanitizeStatusMessage_NoPanic fires a battery of weird inputs at
// the sanitizer to lock down the panic-safety guarantee. The function is
// on the error-reporting hot path; a panic here replaces a server
// rejection with a decoder crash.
func TestSanitizeStatusMessage_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SanitizeStatusMessage panicked: %v", r)
		}
	}()

	inputs := []any{
		nil, "", " ", "\x00", "embedded\x00null",
		make(chan int), // an unsupported type — must degrade, not panic.
	}
	for _, in := range inputs {
		_ = SanitizeStatusMessage(in)
	}
}

// TestContainsUserDisplayableError_TableDriven is the depth-cap and
// marker-scan suite. Positive rows assert the marker is found at a
// realistic depth; negative rows assert we never report a marker that
// is not actually present.
func TestContainsUserDisplayableError_TableDriven(t *testing.T) {
	marker := "type.googleapis.com/google.rpc.UserDisplayableError"

	cases := []struct {
		name string
		in   any
		want bool
	}{
		// Positives: marker is in the payload at a realistic depth.
		{"marker_in_string", marker, true},
		{"marker_in_list", []any{[]any{[]any{marker}}}, true},
		{"marker_in_map_value", map[string]any{"err": marker}, true},
		{"marker_in_nested_list_map", []any{map[string]any{"k": []any{marker}}}, true},
		{"marker_in_slice_of_strings", []any{"a", "b", marker, "d"}, true},

		// Negatives: no marker present at all.
		{"no_marker_in_string", "plain text", false},
		{"empty_string", "", false},
		{"empty_list", []any{}, false},
		{"empty_map", map[string]any{}, false},
		{"nil", nil, false},
		{"int_value", 42, false},
		{"marker_partial_string", "UserDisplayab", false},

		// Depth cap: the marker is present, but deeper than the scan
		// allows, so the result is false (caller falls through to its
		// regular classification).
		{"deeper_than_cap", deeplyNestedMarker(UserDisplayableErrorDepth+5, marker), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ContainsUserDisplayableError(tc.in)
			if got != tc.want {
				t.Fatalf("ContainsUserDisplayableError(%v) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestContainsUserDisplayableError_DepthCapExplicit walks the boundary:
// at exactly UserDisplayableErrorDepth the marker must be found; one
// level deeper the same payload must NOT report it.
func TestContainsUserDisplayableError_DepthCapExplicit(t *testing.T) {
	marker := "UserDisplayableError"

	atCap := deeplyNestedMarker(UserDisplayableErrorDepth, marker)
	if !ContainsUserDisplayableError(atCap) {
		t.Fatalf("marker at depth %d should be found", UserDisplayableErrorDepth)
	}

	beyondCap := deeplyNestedMarker(UserDisplayableErrorDepth+1, marker)
	if ContainsUserDisplayableError(beyondCap) {
		t.Fatalf("marker at depth %d should NOT be found (cap is %d)", UserDisplayableErrorDepth+1, UserDisplayableErrorDepth)
	}
}

// TestContainsUserDisplayableError_NoPanicOnHugeString asserts a
// 64 KB string holding the marker is found without blowing the stack or
// taking forever. Strings are walked linearly.
func TestContainsUserDisplayableError_NoPanicOnHugeString(t *testing.T) {
	huge := strings.Repeat("x", 64*1024) + "UserDisplayableError" + strings.Repeat("y", 64*1024)
	if !ContainsUserDisplayableError(huge) {
		t.Fatal("marker not found in huge string")
	}
}

// TestGrpcStatusLabels covers every code in the table returns a label,
// and every label maps back to the same code.
func TestGrpcStatusLabels(t *testing.T) {
	all := AllGrpcStatusCodes()
	if len(all) == 0 {
		t.Fatal("AllGrpcStatusCodes returned no entries")
	}
	for _, e := range all {
		got := GrpcStatusLabel(e.Code)
		if got != e.Label {
			t.Errorf("GrpcStatusLabel(%d) = %q, want %q", e.Code, got, e.Label)
		}
	}
	// Spot-check a few canonical labels.
	cases := []struct {
		code GrpcStatusCode
		want string
	}{
		{GrpcStatusOK, "OK"},
		{GrpcStatusNotFound, "NOT_FOUND"},
		{GrpcStatusPermissionDenied, "PERMISSION_DENIED"},
		{GrpcStatusUnauthenticated, "UNAUTHENTICATED"},
	}
	for _, c := range cases {
		if got := GrpcStatusLabel(c.code); got != c.want {
			t.Errorf("GrpcStatusLabel(%d) = %q, want %q", c.code, got, c.want)
		}
	}
}

// TestGrpcStatusLabel_UnknownCode returns the empty string for a code
// outside the canonical 0..16 range, so callers can distinguish a
// real gRPC status from a slipped HTTP code.
func TestGrpcStatusLabel_UnknownCode(t *testing.T) {
	if got := GrpcStatusLabel(404); got != "" {
		t.Errorf("GrpcStatusLabel(404) = %q, want empty", got)
	}
	if got := GrpcStatusLabel(-1); got != "" {
		t.Errorf("GrpcStatusLabel(-1) = %q, want empty", got)
	}
}

// TestAccountRoutingHint_StableSubstrings asserts the two substrings
// the auth-error detector matches on are ABSENT from the hint. The
// Python implementation guards this explicitly; the Go port must too,
// or a NOT_FOUND can spuriously trigger a token refresh.
func TestAccountRoutingHint_StableSubstrings(t *testing.T) {
	hint := AccountRoutingHint
	for _, forbidden := range []string{
		// Substrings used by is_auth_error in
		// notebooklm-py/src/notebooklm/_runtime/helpers.py.
		// We assert on a representative sample; the precise list is
		// part of the contract on doc 03 "Classify a null result".
		"sign-in",
		"signin",
		"login",
		"Session",
		"Cookie",
		"reauth",
	} {
		if strings.Contains(strings.ToLower(hint), strings.ToLower(forbidden)) {
			t.Errorf("AccountRoutingHint contains forbidden substring %q; this could misclassify NOT_FOUND as an auth error", forbidden)
		}
	}
	// And the contract: the two substrings we DO want to be present
	// (so the hint actually says something useful) are in the text.
	for _, required := range []string{"account", "authuser"} {
		if !strings.Contains(hint, required) {
			t.Errorf("AccountRoutingHint missing required substring %q", required)
		}
	}
}

// deeplyNestedMarker builds a []any tree of exactly depth levels with
// the marker string at the leaf. depth == 1 means the leaf is the
// top-level element (no wrappers); depth == 2 means one []any wrapper,
// etc.
func deeplyNestedMarker(depth int, marker string) any {
	var v any = marker
	for i := 0; i < depth-1; i++ {
		v = []any{v}
	}
	return v
}

// typeName returns a stable, human-readable name for any value so
// sub-test names stay readable.
func typeName(v any) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case string:
		return "string"
	case int:
		return "int"
	case bool:
		return "bool"
	case []string:
		return "[]string"
	case map[string]int:
		return "map[string]int"
	default:
		return "other"
	}
}
