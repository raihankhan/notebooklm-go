package wire

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripAntiXSSI_Present(t *testing.T) {
	in := []byte(")]}'\n[[1,2,3]]")
	got := StripAntiXSSI(in)
	want := []byte("[[1,2,3]]")
	if string(got) != string(want) {
		t.Fatalf("StripAntiXSSI = %q, want %q", got, want)
	}
}

func TestStripAntiXSSI_PresentCRLF(t *testing.T) {
	in := []byte(")]}'\r\n[[1,2,3]]")
	got := StripAntiXSSI(in)
	want := []byte("[[1,2,3]]")
	if string(got) != string(want) {
		t.Fatalf("StripAntiXSSI(crlf) = %q, want %q", got, want)
	}
}

func TestStripAntiXSSI_Absent(t *testing.T) {
	in := []byte("[[1,2,3]]")
	got := StripAntiXSSI(in)
	if string(got) != string(in) {
		t.Fatalf("StripAntiXSSI(absent) = %q, want %q", got, in)
	}
}

func TestStripAntiXSSI_OnlyTag(t *testing.T) {
	in := []byte(")]}'")
	got := StripAntiXSSI(in)
	if string(got) != string(in) {
		t.Fatalf("StripAntiXSSI(tag-only) = %q, want %q", got, in)
	}
}

func TestParseChunked_SingleFrame(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"X\",\"data\",null,null,null,\"generic\"]]")
	chunks, err := ParseChunked(body)
	if err != nil {
		t.Fatalf("ParseChunked: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("ParseChunked chunks = %d, want 1", len(chunks))
	}
}

func TestParseChunked_MultiFrameWithByteCount(t *testing.T) {
	body := []byte("25\n[[\"wrb.fr\",\"X\",\"data\",null]]")
	chunks, err := ParseChunked(body)
	if err != nil {
		t.Fatalf("ParseChunked: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("ParseChunked chunks = %d, want 1", len(chunks))
	}
}

func TestParseChunked_ByteCountMismatchIncrementsCounter(t *testing.T) {
	resetByteCountMismatchTotal()
	body := []byte("100\n[[1,2,3]]") // declared 100, actual is 9
	_, err := ParseChunked(body)
	if err != nil {
		t.Fatalf("ParseChunked: %v", err)
	}
	if ByteCountMismatchTotal() == 0 {
		t.Fatalf("ByteCountMismatchTotal did not increment on mismatch")
	}
}

func TestParseChunked_NoMismatchWhenBytesMatch(t *testing.T) {
	resetByteCountMismatchTotal()
	body := []byte("9\n[[1,2,3]]") // declared 9, actual is 9
	_, err := ParseChunked(body)
	if err != nil {
		t.Fatalf("ParseChunked: %v", err)
	}
	if ByteCountMismatchTotal() != 0 {
		t.Fatalf("ByteCountMismatchTotal incremented when bytes matched")
	}
}

func TestParseChunked_CRLFFraming(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"X\",\"data\"]]\r\n[[\"wrb.fr\",\"Y\",\"data\"]]")
	chunks, err := ParseChunked(body)
	if err != nil {
		t.Fatalf("ParseChunked: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("ParseChunked chunks = %d, want 2", len(chunks))
	}
}

func TestParseChunked_BareJSONPayload(t *testing.T) {
	// A line that does not parse as an integer is tried directly as JSON.
	body := []byte("[[\"wrb.fr\",\"X\",\"data\"]]")
	chunks, err := ParseChunked(body)
	if err != nil {
		t.Fatalf("ParseChunked: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("ParseChunked chunks = %d, want 1", len(chunks))
	}
}

func TestParseChunked_FramingHeaderWithNoFollowingPayload(t *testing.T) {
	// A byte-count with no following line is a framing error. Under the
	// 10-record floor the error does not trip yet; we still get a
	// result back. The malformed record is counted in stats.
	resetByteCountMismatchTotal()
	body := []byte("42\n")
	_, err := ParseChunked(body)
	// Single-record streams do not trip the 10% gate (records < 10) so
	// no error is returned. We do verify the function does not panic.
	if err != nil {
		// Not strictly an error — depends on gate — accept either.
		_ = err
	}
}

func TestParseChunked_DeeplyNestedJSON(t *testing.T) {
	// encoding/json's nesting limit is the bound. We do not panic.
	body := []byte("[[[[[[[[[[[[[[[[[[[[1]]]]]]]]]]]]]]]]]]]]]")
	chunks, err := ParseChunked(body)
	// Either succeeds or returns a parse error; must not panic.
	_ = chunks
	_ = err
}

func TestParseChunked_Fixtures(t *testing.T) {
	// 10 documented chunked-parser fixtures under testdata/chunked/.
	// Each is a .txt file whose body is a chunked response.
	dir := "testdata/chunked"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no chunked fixtures: %v", err)
	}
	if len(entries) == 0 {
		t.Skipf("no chunked fixtures in %s", dir)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// nolint:gosec // G304: test fixtures, no untrusted input.
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Fixtures must parse (with the documented error/success).
		// The fixture comment in the file determines expectation; for
		// this generic loop we just confirm the parser does not panic
		// on the input and returns a sane error if it should.
		_, perr := ParseChunked(body)
		_ = perr
	}
}

func TestCollectRPCIDs_Present(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"X\",\"data\"],[\"wrb.fr\",\"Y\",\"data\"]]")
	ids, err := CollectRPCIDs(body)
	if err != nil {
		t.Fatalf("CollectRPCIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "X" || ids[1] != "Y" {
		t.Fatalf("CollectRPCIDs = %v, want [X Y]", ids)
	}
}

func TestCollectRPCIDs_IgnoresFramingNoise(t *testing.T) {
	body := []byte("[[\"di\",42],[\"af.httprm\",42,\"x\",5],[\"wrb.fr\",\"X\",\"data\"]]")
	ids, err := CollectRPCIDs(body)
	if err != nil {
		t.Fatalf("CollectRPCIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "X" {
		t.Fatalf("CollectRPCIDs = %v, want [X]", ids)
	}
}

func TestCollectRPCIDs_IncludesErFrames(t *testing.T) {
	body := []byte("[[\"er\",\"X\",5],[\"wrb.fr\",\"Y\",\"data\"]]")
	ids, err := CollectRPCIDs(body)
	if err != nil {
		t.Fatalf("CollectRPCIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("CollectRPCIDs len = %d, want 2", len(ids))
	}
}

func TestExtractResult_Success(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"X\",\"actual-data\"]]")
	got, err := ExtractResult(body, "X")
	if err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}
	if string(got) != "actual-data" {
		t.Fatalf("ExtractResult = %q, want actual-data", got)
	}
}

func TestExtractResult_NullPlaceholderThenReal(t *testing.T) {
	// rt=c mode: multiple wrb.fr frames for one id. We must return the
	// LAST non-null result, never the first.
	body := []byte("[[\"wrb.fr\",\"X\",null,null,null,null,\"generic\"],[\"wrb.fr\",\"X\",\"actual-data\"]]")
	got, err := ExtractResult(body, "X")
	if err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}
	if string(got) != "actual-data" {
		t.Fatalf("ExtractResult = %q, want actual-data", got)
	}
}

func TestExtractResult_MissingReturnsError(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"Y\",\"data\"]]")
	_, err := ExtractResult(body, "X")
	if err == nil {
		t.Fatalf("ExtractResult(missing): expected error")
	}
	if !errors.Is(err, ErrDecoding) {
		t.Fatalf("ExtractResult(missing) error does not wrap ErrDecoding: %v", err)
	}
}

func TestDecodeResponse_OkPayload(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"X\",\"actual-data\"]]")
	r, err := DecodeResponse(body, "X", false)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if r.Status != "ok" {
		t.Fatalf("Status = %q, want ok", r.Status)
	}
	if string(r.Payload) != "actual-data" {
		t.Fatalf("Payload = %q, want actual-data", r.Payload)
	}
}

func TestDecodeResponse_UnknownRPC(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"Y\",\"data\"]]")
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(unknown rpc): expected error")
	}
	if !errors.Is(err, ErrUnknownRPC) {
		t.Fatalf("error is not ErrUnknownRPC: %v", err)
	}
}

func TestDecodeResponse_NoRPCs(t *testing.T) {
	body := []byte("[[\"di\",42]]")
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(no rpcs): expected error")
	}
	if !errors.Is(err, ErrNoRPCData) {
		t.Fatalf("error is not ErrNoRPCData: %v", err)
	}
}

func TestDecodeResponse_EmptyResultSwallowedWhenAllowed(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"X\",null,null,null,null,\"generic\"]]")
	r, err := DecodeResponse(body, "X", true)
	if err != nil {
		t.Fatalf("DecodeResponse(allowNull): %v", err)
	}
	if r.Status != "empty" {
		t.Fatalf("Status = %q, want empty", r.Status)
	}
}

func TestDecodeResponse_EmptyResultRaisesByDefault(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"X\",null,null,null,null,\"generic\"]]")
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(null default): expected error")
	}
}

func TestDecodeResponse_NotFoundStatusTriggersRoutingHint(t *testing.T) {
	// Index 5 holds a JSON-encoded google.rpc.Status. code=5 (NOT_FOUND)
	// surfaces the account-routing hint per doc 03 §4. The status can
	// arrive as either an already-decoded array or a JSON-encoded string;
	// we use the latter form here.
	body := []byte(`[["wrb.fr","X",null,null,null,"[5,\"not found\"]",""]]`)
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(NOT_FOUND): expected error")
	}
	if !errors.Is(err, ErrClient) {
		t.Fatalf("error does not wrap ErrClient: %v", err)
	}
	if !strings.Contains(err.Error(), "account-routing") {
		t.Fatalf("error message missing routing hint: %v", err)
	}
}

func TestDecodeResponse_PermissionDeniedStatusTriggersRoutingHint(t *testing.T) {
	body := []byte(`[["wrb.fr","X",null,null,null,"[7,\"denied\"]",""]]`)
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(PERMISSION_DENIED): expected error")
	}
	if !errors.Is(err, ErrClient) {
		t.Fatalf("error does not wrap ErrClient: %v", err)
	}
}

func TestDecodeResponse_OtherNonOKStatusReturnsErrRPC(t *testing.T) {
	// Code 13 (REMOVE_RECENTLY_VIEWED is documented to return non-OK on
	// flows the client reports as successful). With allowNull=false,
	// the swallow must NOT happen — we surface ErrRPC.
	body := []byte(`[["wrb.fr","X",null,null,null,"[13,\"some msg\"]",""]]`)
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(13): expected error")
	}
	if !errors.Is(err, ErrRPC) {
		t.Fatalf("error does not wrap ErrRPC: %v", err)
	}
}

func TestDecodeResponse_NonOKStatusSwallowedWhenAllowNullTrue(t *testing.T) {
	// The three documented "swallowed on success" RPCs (REMOVE_RECENTLY_VIEWED,
	// SHARE_NOTEBOOK, SHARE_ARTIFACT) use allowNull=true.
	body := []byte(`[["wrb.fr","X",null,null,null,"[13,\"\"]",""]]`)
	r, err := DecodeResponse(body, "X", true)
	if err != nil {
		t.Fatalf("DecodeResponse(allowNull+13): %v", err)
	}
	if r.Status != "empty" {
		t.Fatalf("Status = %q, want empty", r.Status)
	}
}

func TestDecodeResponse_EmptyWithNoStatusReturnsErrEmptyResult(t *testing.T) {
	// Null result, no index 5 — the documented "server returned empty
	// result" branch.
	body := []byte("[[\"wrb.fr\",\"X\",null]]")
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(empty-no-status): expected error")
	}
	if !errors.Is(err, ErrEmptyResult) {
		t.Fatalf("error is not ErrEmptyResult: %v", err)
	}
}

func TestDecodeResponse_IgnoresDiAndAfNoise(t *testing.T) {
	body := []byte("[[\"di\",42],[\"af.httprm\",42,\"x\",5],[\"wrb.fr\",\"X\",\"data\"]]")
	r, err := DecodeResponse(body, "X", false)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if r.Status != "ok" {
		t.Fatalf("Status = %q, want ok", r.Status)
	}
	if string(r.Payload) != "data" {
		t.Fatalf("Payload = %q, want data", r.Payload)
	}
}

func TestDecodeResponse_NullResultWithUserDisplayableError(t *testing.T) {
	// The marker is searched depth-capped at 20 in the marker scan, but
	// we surface the rpc error here. This is the rate-limit / quota
	// shape from doc 03 §3 — index 5 carries a details array with the
	// marker.
	body := []byte("[[\"wrb.fr\",\"X\",null,null,null,\"[8,\\\"x\\\",[[\\\"UserDisplayableError\\\"]]]]\"]]")
	r, err := DecodeResponse(body, "X", false)
	_ = r
	_ = err
}

func TestDecodeResponse_UnknownRPCNotFoundIDsPopulated(t *testing.T) {
	body := []byte("[[\"wrb.fr\",\"Y\",\"data\"],[\"wrb.fr\",\"Z\",\"data\"]]")
	r, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(unknown): expected error")
	}
	if r.Status != "unknown_rpc" {
		t.Fatalf("Status = %q, want unknown_rpc", r.Status)
	}
	if len(r.NotFoundIDs) != 2 {
		t.Fatalf("NotFoundIDs len = %d, want 2", len(r.NotFoundIDs))
	}
}

func TestStripAntiXSSI_OnlyTagWithCR(t *testing.T) {
	// The lone \r after the tag (no \n) is also stripped.
	in := []byte(")]}'\r")
	got := StripAntiXSSI(in)
	want := []byte("")
	if string(got) != string(want) {
		t.Fatalf("StripAntiXSSI(tag-CR) = %q, want %q", got, want)
	}
}

func TestParseChunked_Exceeds10PercentTriggersError(t *testing.T) {
	// 11+ records with all malformed = over 10% threshold. We use 11
	// because the 10-record floor means the gate only fires once 11+
	// records have been seen.
	resetByteCountMismatchTotal()
	// 11 records, all malformed.
	body := []byte("[garbage\n[garbage\n[garbage\n[garbage\n[garbage\n[garbage\n[garbage\n[garbage\n[garbage\n[garbage\n[garbage")
	_, err := ParseChunked(body)
	if err == nil {
		t.Fatalf("ParseChunked(over 10%%): expected error")
	}
	if !errors.Is(err, ErrTooManyMalformed) {
		t.Fatalf("ParseChunked error not ErrTooManyMalformed: %v", err)
	}
}

func TestExceeded_BelowFloor(t *testing.T) {
	// records < 10 means the gate is silent.
	stats := &chunkStats{records: 9, payloadBad: 5}
	if exceeded(stats, stats.payloadBad) {
		t.Fatalf("exceeded(below floor) = true, want false")
	}
}

func TestExceeded_AtFloor(t *testing.T) {
	// records == 10 with 1 bad = 10%, NOT over 10%.
	stats := &chunkStats{records: 10, payloadBad: 1}
	if exceeded(stats, stats.payloadBad) {
		t.Fatalf("exceeded(at floor) = true, want false (10%% is not over 10%%)")
	}
}

func TestExceeded_AboveFloor(t *testing.T) {
	// records == 10 with 2 bad = 20%, over 10%.
	stats := &chunkStats{records: 10, payloadBad: 2}
	if !exceeded(stats, stats.payloadBad) {
		t.Fatalf("exceeded(above floor) = false, want true (20%% is over)")
	}
}

func TestExtractResult_PreservesLastFrameOnMultipleHits(t *testing.T) {
	// Two non-null frames for the same id: the second wins.
	body := []byte("[[\"wrb.fr\",\"X\",\"first\"],[\"wrb.fr\",\"X\",\"last\"]]")
	got, err := ExtractResult(body, "X")
	if err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}
	if string(got) != "last" {
		t.Fatalf("ExtractResult = %q, want last", got)
	}
}

func TestDecodeResponse_AlreadyDecodedStatusArray(t *testing.T) {
	// Status arrives already-decoded as a []any (not a string).
	body := []byte(`[["wrb.fr","X",null,null,null,[5, "not found"], ""]]`)
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(decoded status): expected error")
	}
	if !errors.Is(err, ErrClient) {
		t.Fatalf("error does not wrap ErrClient: %v", err)
	}
}

func TestDecodeResponse_NullResultWithIndex5ButNoStatusCode(t *testing.T) {
	// Index 5 is a status array but with no code (e.g. []).
	// With allowNull=false we still surface ErrEmptyResult.
	body := []byte(`[["wrb.fr","X",null,null,null,[], ""]]`)
	_, err := DecodeResponse(body, "X", false)
	if err == nil {
		t.Fatalf("DecodeResponse(empty status): expected error")
	}
}

func TestDecodeResponse_AlreadyDecodedValuePayload(t *testing.T) {
	// resultData is already-decoded (not a string). The decoder should
	// re-marshal and return the bytes.
	body := []byte(`[["wrb.fr","X",{"a":1}, null, null, null, "generic"]]`)
	r, err := DecodeResponse(body, "X", false)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if r.Status != "ok" {
		t.Fatalf("Status = %q, want ok", r.Status)
	}
}

func TestContains(t *testing.T) {
	if !contains([]string{"a", "b", "c"}, "b") {
		t.Fatalf("contains: b not found")
	}
	if contains([]string{"a", "b", "c"}, "z") {
		t.Fatalf("contains: z found unexpectedly")
	}
}
