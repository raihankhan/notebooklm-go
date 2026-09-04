// Package refresh: the L2.0 ladder rung — file-backed reload.
//
// L2.0 is the "sibling-process disk sample" rung documented in
// docs/05-auth.md §"The refresh ladder". A sibling process (another
// CLI invocation, the MCP server) may have refreshed the same
// storage_state.json while we were waiting on a different rung;
// before L2.0 escalates to L2.5 / L3 / L4 it re-reads disk under a
// bounded-attempt counter so a fresher cookie set has a chance to
// win.
//
// The contract:
//
//	tokens, err := refresh.ReloadL2_0(ctx, storage, logger)
//
// storage is any internal/auth/storage-backed read surface
// (production wires the on-disk storage_path; tests inject a
// stub). logger is the *slog.Logger used for per-attempt diagnostics
// — when nil, ReloadL2_0 uses slog.Default(). A nil logger is never
// an error so callers that do not yet carry a Logger can opt out
// without branching.
//
// On success ReloadL2_0 returns Tokens whose Backend reports
// BackendStorageFile, whose FetchedAt holds the file-touched
// timestamp surfaced from os.Stat (see "File-touched timestamp"
// below), and whose Cookies reflect the latest storage_state.json
// on disk.
//
// Bounded attempts: 3 with a file-backed profile. Backoff between
// attempts is short (50 ms baseline, doubled per retry) so the rung
// never blows past its caller-provided context. The attempt count
// is inclusive: attempt 0 is the first attempt, attempt 2 is the
// last. All three attempts are exhausted before the typed sentinel
// ErrReloadL2_0Exhausted is returned.
//
// File-touched timestamp: the on-disk mtime of storage_state.json
// is captured during the successful attempt and surfaced two ways:
//
//  1. As the Tokens.FetchedAt field, per the parent mega-ticket
//     contract — FetchedAt on an L2.0 reload means "when was the
//     storage last touched", which is the meaningful caller-facing
//     signal for a sibling-process sample.
//  2. Optionally, via ReloadL2_0WithMtime when the caller wants
//     both the mtime and the wall-clock attempt time as separate
//     fields.
//
// Boundary: per docs/AGENTS.md rule 5 this file is part of the
// mode=internal package; it imports stdlib +
// internal/auth/cookiejar + internal/auth/profile +
// internal/auth/storage only.
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/auth/storage"
)

// ErrReloadL2_0Exhausted is the typed sentinel ReloadL2_0 returns
// after 3 bounded attempts failed. Callers detect it with errors.Is
// and decide whether to escalate to L2.5 / L3 / L4 or surface the
// final message documented in docs/05-auth.md.
var ErrReloadL2_0Exhausted = errors.New("refresh: L2.0 file-backed reload exhausted")

// ErrReloadL2_0FileMissing is the typed sentinel ReloadL2_0 returns
// when the resolved storage_state.json path does not exist on disk.
// Distinguished from ErrReloadL2_0Exhausted so a caller that simply
// has no profile directory can short-circuit without retrying.
var ErrReloadL2_0FileMissing = errors.New("refresh: L2.0 storage_state.json missing")

// l2_0MaxAttempts is the bounded-attempt counter for the L2.0
// rung. Pinned at 3 per docs/05-auth.md and the parent mega-ticket
// acceptance criteria. A future ticket that wants to tune the count
// can extend this with a setter rather than widening the API.
const l2_0MaxAttempts = 3

// l2_0BaseBackoff is the backoff applied between L2.0 attempts.
// Doubled per retry (50 ms, 100 ms) so the rung never blows past a
// caller-provided context — the cumulative wait is bounded at
// ~150 ms, well under any sensible RPC deadline.
const l2_0BaseBackoff = 50 * time.Millisecond

// Storage is the read surface the L2.0 rung consumes. The
// production caller wires a disk-backed implementation that
// resolves a profile name to the canonical storage_state.json path
// and reads it through internal/auth/storage; tests inject a stub
// that simulates IO failures and per-call read errors.
//
// The interface is intentionally narrower than the storage.Read API
// so a single retry can change the underlying file (a sibling
// process rewriting it) without widening the contract. Implementors
// must surface os.ErrNotExist unmodified so the missing-file branch
// can detect "no profile" without scraping message text.
type Storage interface {
	// ReadStorage loads storage_state.json at the resolved path
	// and returns the typed Storage value plus the file's
	// last-modified time on disk.
	//
	// Errors:
	//   - os.ErrNotExist — the file is absent (errors.Is).
	//   - any wrapped IO / parse error on an existing file.
	ReadStorage(ctx context.Context) (storage.Storage, time.Time, error)
}

// DiskStorage is the production Storage implementation. It wraps
// the canonical storage_state.json path under a profile directory
// and reads through internal/auth/storage so the lossless
// Python-CLI normalization runs unchanged.
//
// DiskStorage is the only production Storage; future rungs (L2.5,
// L3) compose on top of it without modifying the interface.
type DiskStorage struct {
	// Path is the absolute storage_state.json path the
	// storage reads. Required.
	Path string
}

// ReadStorage loads storage_state.json at Path and returns the
// typed Storage plus the file's last-modified time.
//
// os.ErrNotExist propagates unchanged so ReloadL2_0 can route the
// missing-file branch through errors.Is without scraping message
// text.
func (d *DiskStorage) ReadStorage(ctx context.Context) (storage.Storage, time.Time, error) {
	if d == nil {
		return storage.Storage{}, time.Time{}, fmt.Errorf("refresh: L2.0 DiskStorage is nil")
	}
	if d.Path == "" {
		return storage.Storage{}, time.Time{}, storage.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return storage.Storage{}, time.Time{}, err
	}
	s, err := storage.Read(d.Path)
	if err != nil {
		return storage.Storage{}, time.Time{}, err
	}
	mtime, mtimeErr := fileMtime(d.Path)
	if mtimeErr != nil {
		// A successful read but failed stat is rare but
		// possible (the file was unlinked between read and
		// stat). Surface the zero time so callers still get
		// a usable Timestamp value rather than a fake.
		mtime = time.Time{}
	}
	return s, mtime, nil
}

// fileMtime returns the last-modified time of path. A missing file
// surfaces os.ErrNotExist unwrapped so ReloadL2_0's missing-file
// branch can detect it via errors.Is.
func fileMtime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime().UTC(), nil
}

// ReloadL2_0Result is the rich return shape of ReloadL2_0WithMtime.
// It splits the file-touched timestamp from the wall-clock
// attempt time so callers that want both — for example, to surface
// "the sibling process touched disk 3 s ago, our reload ran 50 ms
// ago" — can do so without scraping internal state.
//
// The simpler ReloadL2_0 signature collapses FileTouched into
// Tokens.FetchedAt (see "File-touched timestamp" in the package
// docstring), trading off the dual-clock resolution for a smaller
// call surface.
type ReloadL2_0Result struct {
	// Tokens is the typed Tokens value the ladder consumes.
	Tokens Tokens
	// FileTouched is the on-disk mtime of storage_state.json at
	// the moment the successful attempt read it. Used by callers
	// that want to warn about staleness separately from the
	// attempt latency.
	FileTouched time.Time
	// ReloadL2_0AttemptedAt is the wall-clock time of the
	// successful attempt. Useful when the caller wants to
	// distinguish "old data but a fresh read" from "old data
	// AND a fresh read" — the former is the sibling-process
	// signal, the latter is the local disk-cache signal.
	ReloadL2_0AttemptedAt time.Time
	// Attempts is the bounded counter value of the successful
	// attempt (1-indexed; range 1..l2_0MaxAttempts).
	Attempts int
}

// ReloadL2_0 reads storage_state.json under a 3-attempt bounded
// loop, surfaces the file-touched timestamp, and returns Tokens on
// success.
//
// On the third consecutive failure ReloadL2_0 returns
// ErrReloadL2_0Exhausted wrapped with the last attempt's error so
// callers can errors.Is the sentinel while still seeing the
// underlying cause. A missing file short-circuits immediately with
// ErrReloadL2_0FileMissing wrapped around os.ErrNotExist — the
// ladder does not retry "no profile" three times.
//
// On success the returned Tokens.FetchedAt is the file-touched
// timestamp surfaced from os.Stat. The Tokens otherwise follows
// the existing ladder contract: a freshly-allocated slice of
// CookieView, AuthUser and AccountEmail lifted from the in-band
// notebooklm.account namespace (or zero when absent), and
// Backend = BackendStorageFile.
func ReloadL2_0(ctx context.Context, st Storage, logger *slog.Logger) (Tokens, error) {
	res, err := ReloadL2_0WithMtime(ctx, st, logger)
	if err != nil {
		return Tokens{}, err
	}
	return res.Tokens, nil
}

// ReloadL2_0WithMtime is the rich form of ReloadL2_0 — it returns
// the Tokens alongside the file-touched timestamp and the
// successful-attempt metadata. Use this when the caller wants to
// surface a "stale-by-N-seconds" diagnostic on the reload path.
//
// The two-function split exists so the parent mega-ticket's
// signature `ReloadL2_0(ctx, storage, logger) (Tokens, error)` stays
// the canonical call shape; the WithMtime variant is opt-in.
func ReloadL2_0WithMtime(ctx context.Context, st Storage, logger *slog.Logger) (ReloadL2_0Result, error) {
	if st == nil {
		return ReloadL2_0Result{}, fmt.Errorf("refresh: L2.0 storage is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := ctx.Err(); err != nil {
		return ReloadL2_0Result{}, err
	}

	var lastErr error
	for attempt := 0; attempt < l2_0MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return ReloadL2_0Result{}, ctx.Err()
		default:
		}

		s, mtime, err := st.ReadStorage(ctx)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, storage.ErrEmptyPath) {
				// The "no profile directory" case is a
				// configuration problem, not a transient
				// IO failure — do not retry three times.
				// The ladder should escalate to L4 (or
				// surface "run 'notebooklm login'").
				return ReloadL2_0Result{}, fmt.Errorf("%w: %w", ErrReloadL2_0FileMissing, err)
			}
			lastErr = err
			logger.Warn("L2.0 attempt failed",
				slog.Int("attempt", attempt+1),
				slog.Int("max", l2_0MaxAttempts),
				slog.String("error", err.Error()),
			)
			// Backoff between attempts; the final attempt
			// does not sleep so we still fail fast.
			if attempt < l2_0MaxAttempts-1 {
				backoff := l2_0BaseBackoff << attempt
				select {
				case <-ctx.Done():
					return ReloadL2_0Result{}, ctx.Err()
				case <-time.After(backoff):
				}
			}
			continue
		}

		// Success — project into Tokens and surface the
		// file-touched timestamp so callers can warn about
		// staleness.
		logger.Debug("L2.0 reload succeeded",
			slog.Int("attempt", attempt+1),
			slog.Int("cookies", len(s.Cookies)),
			slog.Time("file_touched", mtime),
		)
		views := make([]CookieView, 0, len(s.Cookies))
		for _, c := range s.Cookies {
			views = append(views, CookieView{Name: c.Name, Domain: c.Domain, Path: c.Path})
		}
		authUser := 0
		email := ""
		if s.NotebookLM != nil {
			authUser = s.NotebookLM.Account.AuthUser
			email = s.NotebookLM.Account.Email
		}
		now := time.Now().UTC()
		tokens := Tokens{
			Cookies:      views,
			AuthUser:     authUser,
			AccountEmail: email,
			Backend:      BackendStorageFile,
			FetchedAt:    mtime,
		}
		return ReloadL2_0Result{
			Tokens:                tokens,
			FileTouched:           mtime,
			ReloadL2_0AttemptedAt: now,
			Attempts:              attempt + 1,
		}, nil
	}

	return ReloadL2_0Result{}, fmt.Errorf("%w: %w", ErrReloadL2_0Exhausted, lastErr)
}
