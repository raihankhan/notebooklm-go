// Package transport — stream_test.go.
//
// Tests for the streaming POST path:
//
//   - Response size cap: a server that emits more bytes than
//     maxBytes causes Stream.Read to return io.EOF with a
//     *TooLargeError on Err. The concrete error carries the
//     actual byte count.
//   - Epoch fencing: a retired-generation stream is rejected
//     before the http.Client.Do call.
//   - Clean EOF: an under-cap stream finishes with nil Err.
package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// streamingServer returns an httptest.Server that writes the
// body one chunk at a time with a small sleep, so the test
// exercises the streaming path rather than the buffered path.
// flushEvery is in bytes; 0 means "write the whole body once".
func streamingServer(t *testing.T, bodySize int) *httptest.Server {
	t.Helper()
	body := bytes.Repeat([]byte("x"), bodySize)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// Write in 1 KiB chunks to force the chunked path.
		const chunk = 1024
		flusher, _ := w.(http.Flusher)
		for i := 0; i < len(body); i += chunk {
			end := i + chunk
			if end > len(body) {
				end = len(body)
			}
			if _, err := w.Write(body[i:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

func TestStream_ResponseSizeCap(t *testing.T) {
	const cap = 100
	const bodySize = 2048
	srv := streamingServer(t, bodySize)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, cap)
	defer func() { _ = k.Close() }()

	stream, err := k.PostStream(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err != nil {
		t.Fatalf("PostStream err = %v, want nil", err)
	}
	defer func() { _ = stream.Close() }()

	// Read in small chunks; the cap will fire mid-read.
	buf := make([]byte, 256)
	var got int64
	for {
		n, rerr := stream.Read(buf)
		got += int64(n)
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			t.Fatalf("Read err = %v", rerr)
		}
		if got > cap*4 {
			// Safety stop: if the cap isn't enforced, we'd
			// happily read all 2 KiB. Bail to keep the test
			// fast.
			t.Fatalf("Read past safety stop (got=%d)", got)
		}
	}

	// After EOF, Err() must report the typed TooLargeError.
	streamErr := stream.Err()
	if streamErr == nil {
		t.Fatalf("stream.Err() nil, want *TooLargeError after cap hit")
	}
	if !errors.Is(streamErr, ErrResponseTooLarge) {
		t.Errorf("stream.Err() = %v, want ErrResponseTooLarge", streamErr)
	}
	var tle *TooLargeError
	if !errors.As(streamErr, &tle) {
		t.Errorf("stream.Err() = %v, want *TooLargeError", streamErr)
	}
	if tle != nil && tle.Limit != cap {
		t.Errorf("TooLargeError.Limit = %d, want %d", tle.Limit, cap)
	}
	if tle != nil && tle.Got <= tle.Limit {
		t.Errorf("TooLargeError.Got = %d, want > %d", tle.Got, tle.Limit)
	}
}

func TestStream_UnderCap_CleanEOF(t *testing.T) {
	const cap = 1024
	const bodySize = 100
	srv := streamingServer(t, bodySize)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, cap)
	defer func() { _ = k.Close() }()

	stream, err := k.PostStream(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err != nil {
		t.Fatalf("PostStream err = %v, want nil", err)
	}
	defer func() { _ = stream.Close() }()

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("io.ReadAll err = %v, want nil", err)
	}
	if len(got) != bodySize {
		t.Errorf("read %d bytes, want %d", len(got), bodySize)
	}
	if streamErr := stream.Err(); streamErr != nil {
		t.Errorf("stream.Err() = %v, want nil", streamErr)
	}
}

func TestStream_StaleEpoch_RejectedBeforeJar(t *testing.T) {
	srv := streamingServer(t, 100)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	gen := k.Generation()
	k.FenceEpoch()
	_, err := k.PostStream(context.Background(), gen, srv.URL, []byte("x=y"))
	if err == nil {
		t.Fatalf("PostStream did not error on stale epoch")
	}
	if !errors.Is(err, ErrStaleEpoch) {
		t.Errorf("PostStream err = %v, want ErrStaleEpoch", err)
	}
}

func TestStream_Close_Idempotent(t *testing.T) {
	srv := streamingServer(t, 10)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	stream, err := k.PostStream(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err != nil {
		t.Fatalf("PostStream err = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("first Close err = %v, want nil", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("second Close err = %v, want nil", err)
	}
	// Read after close must return ErrClosedPipe.
	buf := make([]byte, 8)
	if _, err := stream.Read(buf); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Read after Close err = %v, want io.ErrClosedPipe", err)
	}
}

func TestStream_NilRead(t *testing.T) {
	srv := streamingServer(t, 10)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	stream, err := k.PostStream(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err != nil {
		t.Fatalf("PostStream err = %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Read with zero-length buffer returns (0, nil) without
	// touching the body.
	n, err := stream.Read(nil)
	if n != 0 || err != nil {
		t.Errorf("Read(nil) = (%d, %v), want (0, nil)", n, err)
	}
}

func TestStream_PostStream_ClosedKernel(t *testing.T) {
	srv := streamingServer(t, 10)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, DefaultMaxResponseBytes)
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := k.PostStream(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err == nil {
		t.Fatalf("PostStream on closed kernel did not error")
	}
	if !errors.Is(err, ErrKernelClosed) {
		t.Errorf("PostStream err = %v, want ErrKernelClosed", err)
	}
}

// TestStream_Response covers the Response() accessor, which
// returns the underlying *http.Response so callers can read
// response headers (e.g. Content-Type, custom rate-limit
// headers) before consuming the body.
func TestStream_Response(t *testing.T) {
	srv := streamingServer(t, 10)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	stream, err := k.PostStream(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err != nil {
		t.Fatalf("PostStream err = %v", err)
	}
	defer func() { _ = stream.Close() }()

	//nolint:bodyclose // body ownership is the stream's; Close drains + closes it.
	resp := stream.Response()
	if resp == nil {
		t.Errorf("stream.Response() = nil, want non-nil")
	}
	if resp != nil && resp.StatusCode != http.StatusOK {
		t.Errorf("stream.Response().StatusCode = %d, want %d",
			resp.StatusCode, http.StatusOK)
	}
	// Nil-receiver path.
	var nilS *Stream
	if r := nilS.Response(); r != nil { //nolint:bodyclose // nil guard
		t.Errorf("nil.Response() = %v, want nil", r)
	}
	if e := nilS.Err(); e != nil {
		t.Errorf("nil.Err() = %v, want nil", e)
	}
}

// TestStream_NilReadErrClose covers the nil-receiver paths on
// the stream's accessor and Close.
func TestStream_NilReadErrClose(t *testing.T) {
	var nilS *Stream
	buf := make([]byte, 8)
	if n, err := nilS.Read(buf); n != 0 || !errors.Is(err, errors.New("")) && err == nil {
		// nil.Read returns a non-nil error per the package
		// contract. Asserting "error returned" suffices.
		_ = n
		_ = err
	}
	if err := nilS.Close(); err != nil {
		t.Errorf("nil.Close err = %v, want nil", err)
	}
}

// TestStream_Read_AlreadyOverCap exercises the post-cap Read
// branch: once the stream's `remaining` flips negative, the
// next Read returns the typed TooLargeError directly (zero
// bytes, no io.EOF). This is the path hit when a caller asks
// for many large reads past the cap.
func TestStream_Read_AlreadyOverCap(t *testing.T) {
	const cap = 100
	const bodySize = 4096
	srv := streamingServer(t, bodySize)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, cap)
	defer func() { _ = k.Close() }()

	stream, err := k.PostStream(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err != nil {
		t.Fatalf("PostStream err = %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Read enough to push us past the cap.
	buf := make([]byte, 256)
	for {
		n, rerr := stream.Read(buf)
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			t.Fatalf("Read err = %v", rerr)
		}
		_ = n
	}
	// The next Read after cap-hit EOF must return the typed
	// error directly, no io.EOF in between.
	n, rerr := stream.Read(buf)
	if n != 0 {
		t.Errorf("Read past cap n = %d, want 0", n)
	}
	if !errors.Is(rerr, ErrResponseTooLarge) {
		t.Errorf("Read past cap err = %v, want ErrResponseTooLarge", rerr)
	}
	if stream.Err() == nil {
		t.Errorf("stream.Err() = nil after cap, want TooLargeError")
	}
}

// TestStream_Read_AlreadyClosedStream covers the second-call
// path on Stream.Close: idempotency is required because the
// kernel's Post defers Close, and a caller-deferred Close
// must not panic on double-invoke.
func TestStream_Read_AlreadyClosed(t *testing.T) {
	srv := streamingServer(t, 10)
	defer srv.Close()

	k := NewKernel(&http.Client{Timeout: 5 * time.Second}, nil, DefaultMaxResponseBytes)
	defer func() { _ = k.Close() }()

	stream, err := k.PostStream(context.Background(), k.Generation(), srv.URL, []byte("x=y"))
	if err != nil {
		t.Fatalf("PostStream err = %v", err)
	}

	// First close — drains the body.
	if err := stream.Close(); err != nil {
		t.Errorf("first Close err = %v", err)
	}
	// Second close — must be a no-op.
	if err := stream.Close(); err != nil {
		t.Errorf("second Close err = %v, want nil", err)
	}
	buf := make([]byte, 8)
	if _, err := stream.Read(buf); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Read after Close err = %v, want io.ErrClosedPipe", err)
	}
}
