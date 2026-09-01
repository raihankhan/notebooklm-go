// Package transport — stream.go.
//
// Streaming POST is the path the chat RPC uses to receive
// server-sent events one chunk at a time. The kernel applies the
// same size cap as the buffered path so a buggy server cannot
// stream gigabytes past the cap. The cap is enforced per read:
// the function returns ErrResponseTooLarge (wrapped as
// *TooLargeError) when the cumulative byte count crosses
// maxBytes, with the actual byte count carried on the error.
//
// Streaming POST respects epoch fencing identically to Post:
// AssertEpoch runs before the http.Request is built, so a
// retired-generation stream never reads the cookie jar.
//
// Boundary: per docs/AGENTS.md rule 5, this package is
// mode=internal; this file imports stdlib only.
//
// docs/04-transport.md is the design spec for this file.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// PostStream issues a POST and returns a *Stream whose Read
// method yields the response body byte-by-byte up to the
// configured size cap. The stream is closed by the caller via
// (*Stream).Close; the kernel tracks the in-flight call in its
// WaitGroup so Close on the kernel drains any unread stream.
//
// issued is the epoch under which the request was issued. A
// retired generation returns ErrStaleEpoch before any HTTP work
// happens (and the cookie jar is never read).
//
// On wire error (dial failure, mid-stream connection drop), Read
// returns the error wrapped with `transport: stream: ...` so
// callers can errors.Is against *url.Error or net.OpError if
// they care about the underlying transport.
//
// On cap hit, Read returns io.EOF after placing a *TooLargeError
// in the stream's Err field, and the next Read returns the same
// error. This mirrors io.Reader's "EOF then error" convention
// poorly — callers should check Err() before treating an EOF as
// a clean termination. We do not panic on cap hit; we return
// the typed error so a chain middleware can map it to the SDK's
// transport-error category.
func (k *Kernel) PostStream(ctx context.Context, issued uint64, url string, body []byte) (*Stream, error) {
	if k == nil {
		return nil, &ClosedError{}
	}
	if err := k.AssertEpoch(issued); err != nil {
		// Same regression guard as Post: AssertEpoch runs
		// before http.NewRequestWithContext so the cookie jar
		// is never read for a retired-generation call.
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("transport: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Track the in-flight call under closeMu so Close cannot
	// race ahead of this Add.
	k.closeMu.Lock()
	if k.closed {
		k.closeMu.Unlock()
		return nil, &ClosedError{CloserEpoch: k.gen}
	}
	k.wg.Add(1)
	k.closeMu.Unlock()

	//nolint:bodyclose // body ownership transfers to the returned
	// Stream, whose Close drains and closes the body. The
	// transport kernel never reads resp.Body directly.
	resp, err := k.client.Do(req)
	if err != nil {
		k.wg.Done()
		return nil, fmt.Errorf("transport: send: %w", err)
	}
	s := &Stream{
		body:      resp.Body,
		resp:      resp,
		remaining: k.maxBytes,
		kernel:    k,
	}
	return s, nil
}

// Stream is an io.ReadCloser over a chunked HTTP response body,
// capped at the kernel's maxBytes. Read returns the next chunk
// of bytes; if the cumulative byte count would exceed the cap,
// Read returns the bytes that fit AND sets Err to a
// *TooLargeError so the caller's next Read returns the typed
// error.
//
// Stream is safe for sequential use from a single goroutine. It
// is not safe for concurrent Read calls — io.Reader is not.
type Stream struct {
	body      io.ReadCloser
	resp      *http.Response
	remaining int64 // remaining bytes before cap; negative = over cap
	kernel    *Kernel

	// read tracks cumulative bytes read so the cap check is O(1).
	read int64

	// closed is true after Close.
	closed bool

	// err holds the terminal error. Set on cap hit or wire
	// error; subsequent Reads return this error.
	err error
}

// Read implements io.Reader. Returns the next chunk of the
// response body up to `len(p)` bytes. Once the cumulative read
// crosses the kernel's maxBytes, the stream sets Err to a
// *TooLargeError and returns io.EOF for that call; subsequent
// Reads return the typed error.
//
// On a mid-stream wire failure, Read returns the underlying
// error wrapped with `transport: stream: ...` and sets Err to
// the wrapped value. The body is closed automatically on error.
func (s *Stream) Read(p []byte) (int, error) {
	if s == nil {
		return 0, errors.New("transport: nil stream")
	}
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if s.err != nil {
		return 0, s.err
	}
	if len(p) == 0 {
		return 0, nil
	}

	// Cap the read window so we never read past the cap in one
	// call. A reader asking for 1 MiB when 100 bytes remain
	// must not pull the extra past the cap.
	allowed := s.remaining
	if allowed < 0 {
		// Cap already exceeded; surface the typed error.
		s.err = &TooLargeError{
			Limit: s.kernel.maxBytes,
			Got:   s.read,
		}
		return 0, s.err
	}
	if int64(len(p)) > allowed {
		p = p[:allowed+1] // allow reading 1 byte past the cap to detect overflow
	}

	n, err := s.body.Read(p)
	s.read += int64(n)
	s.remaining -= int64(n)
	if s.remaining < 0 {
		// Over the cap. Drain the rest of the body so the
		// connection can be reused, then set the typed error.
		_, _ = io.Copy(io.Discard, s.body)
		s.err = &TooLargeError{
			Limit: s.kernel.maxBytes,
			Got:   s.read,
		}
		// Return the bytes that fit plus io.EOF so the caller's
		// read loop terminates cleanly. The Err field carries
		// the typed error.
		return n, io.EOF
	}
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.err = fmt.Errorf("transport: stream: %w", err)
			err = s.err
		}
	}
	return n, err
}

// Close releases the response body and decrements the kernel's
// WaitGroup. Safe to call multiple times.
func (s *Stream) Close() error {
	if s == nil {
		return nil
	}
	if s.closed {
		return nil
	}
	s.closed = true
	if s.kernel != nil {
		defer s.kernel.wg.Done()
	}
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

// Err returns the terminal error if the stream terminated
// abnormally (cap hit, wire failure). Returns nil on clean EOF.
func (s *Stream) Err() error {
	if s == nil {
		return nil
	}
	return s.err
}

// Response returns the underlying http.Response so callers can
// read response headers (for tracing) before the body is
// consumed. Returns nil if the stream is closed.
func (s *Stream) Response() *http.Response {
	if s == nil {
		return nil
	}
	return s.resp
}

// Ensure http.Response is referenced; the type is exported via
// Response() so callers can read headers.
var _ = (*http.Response)(nil)
