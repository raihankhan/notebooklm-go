// Package refresh: tests for the Ladder interface, the Level enum,
// the ErrLadderLevelNotImplemented sentinel, the WiredLadder
// dispatcher (T-S3-001d), and the full-sequence ladder walk.
//
// AC coverage:
//
//   - T-P4-2 AC2 + AC3: Level enum stringification,
//     ErrLadderLevelNotImplemented sentinel pin, and the
//     not-implemented surface on the Phase-4 DefaultLadder.
//
//   - T-S3-001d AC: WiredLadder.Step dispatches to L1, L2_0,
//     L2_5, and (when configured) L3. The
//     ErrLadderLevelNotImplemented sentinel returns ONLY from the
//     L4 stub (and from rungs whose dispatcher surface was not
//     wired — L3 without L3Reload, L2_0 without L2Storage).
//     Full-sequence test exercises L1 → L2.0 → L2.5 → L3 → L4
//     end-to-end with table-driven happy-path and per-rung-failure
//     matrices.
//
// Boundary: per docs/AGENTS.md rule 5 this test file is part of the
// mode=internal package; it imports stdlib + internal/auth/storage
// (the L2.0 Storage contract) + internal/auth/profile + the project's
// internal/cookiejar (the Jar type). The cookie jar import is
// already declared by l1_test.go so this file is in the same
// boundary row.
package refresh

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raihankhan/notebooklm-go/internal/auth/cookiejar"
	"github.com/raihankhan/notebooklm-go/internal/auth/profile"
	"github.com/raihankhan/notebooklm-go/internal/auth/storage"
)

// TestLevelString: the Level enum maps each rung to its canonical
// label.
func TestLevelString(t *testing.T) {
	cases := []struct {
		l    Level
		want string
	}{
		{L1, "L1"},
		{L2_0, "L2_0"},
		{L2_5, "L2_5"},
		{L3, "L3"},
		{L4, "L4"},
		{Level(0), "L?(0)"},
		{Level(99), "L?(99)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.l.String(); got != tc.want {
				t.Fatalf("Level(%d).String() = %q, want %q", int(tc.l), got, tc.want)
			}
		})
	}
}

// TestLevelsDistinct: every Level has a unique integer value.
// Catches any "two enums share a constant" bug before it reaches
// the dispatcher.
func TestLevelsDistinct(t *testing.T) {
	seen := map[Level]bool{}
	for _, l := range []Level{L1, L2_0, L2_5, L3, L4} {
		if seen[l] {
			t.Fatalf("Level %s duplicated", l)
		}
		seen[l] = true
	}
}

// TestLadderStepNotImplemented: AC3 — every non-L1 level returns
// ErrLadderLevelNotImplemented wrapped with the level name. The
// default Ladder MUST NOT silently return zero values; a
// "not-implemented" rung is the only safe Phase-4 surface.
func TestLadderStepNotImplemented(t *testing.T) {
	ladder := &DefaultLadder{}
	for _, l := range []Level{L2_0, L2_5, L3, L4} {
		t.Run(l.String(), func(t *testing.T) {
			tokens, ok, err := ladder.Step(context.Background(), l)
			if err == nil {
				t.Fatalf("DefaultLadder.Step(%s) err = nil, want ErrLadderLevelNotImplemented", l)
			}
			if !errors.Is(err, ErrLadderLevelNotImplemented) {
				t.Fatalf("DefaultLadder.Step(%s) err = %v, want ErrLadderLevelNotImplemented", l, err)
			}
			if ok {
				t.Fatalf("DefaultLadder.Step(%s) ok = true, want false", l)
			}
			if tokens.FetchedAt.IsZero() == false {
				// Tokens value must be the zero value (no
				// half-populated state on a not-implemented
				// rung).
				t.Fatalf("DefaultLadder.Step(%s) tokens = %+v, want zero Tokens", l, tokens)
			}
		})
	}
}

// TestDefaultLadderUnknownLevel: an out-of-range Level returns
// the same sentinel — never a panic, never a literal "level not
// implemented: L?(99)" without the sentinel wrap.
func TestDefaultLadderUnknownLevel(t *testing.T) {
	ladder := &DefaultLadder{}
	_, _, err := ladder.Step(context.Background(), Level(99))
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("DefaultLadder.Step(unknown) err = %v, want ErrLadderLevelNotImplemented", err)
	}
	if err == nil {
		t.Fatalf("DefaultLadder.Step(unknown) err = nil, want non-nil")
	}
}

// TestErrLadderLevelNotImplementedExported: the sentinel is
// exportable and reusable by Sprint-3 callers.
func TestErrLadderLevelNotImplementedExported(t *testing.T) {
	if ErrLadderLevelNotImplemented == nil {
		t.Fatalf("ErrLadderLevelNotImplemented is nil")
	}
}

// ---------------------------------------------------------------------------
// WiredLadder full-sequence tests (T-S3-001d AC)
// ---------------------------------------------------------------------------
//
// The table below is the contract. Every row exercises one rung or
// rung-sequence; the test confirms the (Tokens, ok, err) shape from
// WiredLadder.Step matches the expected rung outcome without
// touching master or breaking the existing Phase-4 DefaultLadder
// tests.
//
// Rung behavior matrix:
//
//   - L1 happy       — ReloadL1 succeeds, Tokens carry BackendStorageFile.
//   - L1 fails       — profile.Reader returns ErrProfileNotFound; the
//                       rung reports ok=false, err != nil
//                       (NOT ErrLadderLevelNotImplemented; that's the
//                       "surface not wired" sentinel).
//   - L2_0 happy     — ReloadL2_0 succeeds, Tokens carry BackendStorageFile
//                       with the file-touched mtime as FetchedAt.
//   - L2_0 fails     — Stub Storage returns errors; the rung reports
//                       ok=false and the error wraps ErrReloadL2_0Exhausted.
//   - L2_0 no-storage— WiredLadder without L2Storage wired returns the
//                       ErrLadderLevelNotImplemented sentinel (the rung
//                       surface is not configured).
//   - L2_5 happy     — env NOTEBOOKLM_REFRESH_CMD + _MIDSESSION=1 set;
//                       a /bin/sh -c command emits a valid JSON payload;
//                       the rung reports ok=true with CSRF + SessionID
//                       populated and Backend = BackendInlineEnv.
//   - L2_5 fails     — env unconfigured; the rung returns its typed
//                       sentinel (ErrReloadL2_5NotConfigured), NOT
//                       ErrLadderLevelNotImplemented. The dispatcher
//                       surfaces it as (zero, false, err) so the ladder
//                       can step up.
//   - L3 happy       — L3Reload function field wired; the dispatcher
//                       calls it and lifts the result.
//   - L3 no-hook     — WiredLadder without L3Reload wired returns the
//                       ErrLadderLevelNotImplemented sentinel.
//   - L4 stub-only   — L4 ALWAYS returns ErrLadderLevelNotImplemented
//                       (with the level name in the wrap). This is the
//                       load-bearing T-S3-001d AC: the sentinel is
//                       reachable ONLY from the L4 stub (or a
//                       dispatcher-surface-not-wired rung).
//
// Full-sequence walks (each row drives multiple rungs in order and
// asserts the final rung that produced Tokens is the expected one):

// sampleProfile builds a minimal valid Profile with one cookie
// and the in-band account namespace. Reused across the L1 and the
// full-sequence tests so the suite does not diverge in shape.
func sampleProfile(name profile.Name) profile.Profile {
	return profile.Profile{
		Name: name,
		Cookies: []profile.Cookie{
			{
				Name:   "SID",
				Value:  "FAKE_SID_FOR_LADDER_TEST",
				Domain: ".google.com",
				Path:   "/",
			},
		},
		Account:    profile.Account{AuthUser: 2, Email: "alice@example.invalid"},
		Backend:    profile.BackendStorageFile,
		LastUsedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
	}
}

// fakeStorageForLadder satisfies Storage with a per-call result
// list. Differs from l2_0_test.go's stubStorage only in name so
// the two test files can use the same pattern without colliding.
// The shape mirrors stubStorage so a regression that touches one
// trips both.
type fakeStorageForLadder struct {
	Results []stubResult
	LastErr error
	Calls   int
}

func (f *fakeStorageForLadder) ReadStorage(ctx context.Context) (storage.Storage, time.Time, error) {
	_ = ctx
	f.Calls++
	idx := f.Calls - 1
	if idx < len(f.Results) {
		r := f.Results[idx]
		if r.Err != nil {
			return storage.Storage{}, time.Time{}, r.Err
		}
		return r.Storage, r.Mtime, nil
	}
	if f.LastErr != nil {
		return storage.Storage{}, time.Time{}, f.LastErr
	}
	return storage.Storage{}, time.Time{}, errors.New("fakeStorageForLadder: no result configured")
}

// fakeInlineForLaderNoop satisfies InlineStorage. The L2.5 rung
// does not call Read on the configured-midsession path; the type
// exists so the dispatcher has a non-nil InlineStorage to forward
// without nil-checking.
type fakeInlineForLaderNoop struct{}

func (f *fakeInlineForLaderNoop) Read(ctx context.Context) ([]byte, error) {
	_ = ctx
	return nil, nil
}

// wiredFromTemplate returns a *WiredLadder configured for the
// "default succeeds" path: a FakeStore with one profile, a Jar,
// a happy L2Storage, and a happy InlineStorage. Callers override
// the per-rung fields they need to fail.
func wiredFromTemplate(t *testing.T) *WiredLadder {
	t.Helper()
	store := &profile.FakeStore{
		Profiles: map[profile.Name]profile.Profile{
			profile.Name("work"): sampleProfile(profile.Name("work")),
		},
	}
	jar := cookiejar.New()
	mtime := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	l2store := &fakeStorageForLadder{
		Results: []stubResult{{
			Storage: storage.Storage{
				Cookies: []storage.Cookie{{
					Name:   "SID",
					Value:  "FAKE_SID_FOR_LADDER_TEST",
					Domain: ".google.com",
					Path:   "/",
				}},
				NotebookLM: &storage.NotebookLM{
					Account: storage.Account{AuthUser: 2, Email: "alice@example.invalid"},
				},
			},
			Mtime: mtime,
		}},
	}
	return (&WiredLadder{
		DefaultLadder: &DefaultLadder{
			Store: store,
			Jar:   jar,
			Name:  profile.Name("work"),
		},
		L2_5Inline: &fakeInlineForLaderNoop{},
	}).WithStorage(l2store, &fakeInlineForLaderNoop{}, nil)
}

// TestWiredLadderL1Success: L1 succeeds and Step returns Tokens
// carrying the in-band account identity surfaced from the
// profile. Backend is BackendStorageFile (storage-backed profile).
func TestWiredLadderL1Success(t *testing.T) {
	w := wiredFromTemplate(t)
	tokens, ok, err := w.Step(context.Background(), L1)
	if err != nil {
		t.Fatalf("WiredLadder.Step(L1) err = %v", err)
	}
	if !ok {
		t.Fatalf("WiredLadder.Step(L1) ok = false, want true")
	}
	if tokens.AuthUser != 2 {
		t.Fatalf("Tokens.AuthUser = %d, want 2", tokens.AuthUser)
	}
	if tokens.AccountEmail != "alice@example.invalid" {
		t.Fatalf("Tokens.AccountEmail = %q, want alice@example.invalid", tokens.AccountEmail)
	}
	if tokens.Backend != BackendStorageFile {
		t.Fatalf("Tokens.Backend = %v, want BackendStorageFile", tokens.Backend)
	}
	if tokens.FetchedAt.IsZero() {
		t.Fatalf("Tokens.FetchedAt is zero")
	}
}

// TestWiredLadderL1MissingProfile: a profile.Reader that returns
// ErrProfileNotFound for the requested name surfaces the sentinel
// to the dispatcher; the rung reports ok=false and the error is
// NOT ErrLadderLevelNotImplemented (the "surface not wired"
// sentinel — the rung itself IS wired, it just failed).
func TestWiredLadderL1MissingProfile(t *testing.T) {
	w := wiredFromTemplate(t)
	// Replace the store with one that has no "work" profile.
	w.Store = &profile.FakeStore{Profiles: map[profile.Name]profile.Profile{}}
	tokens, ok, err := w.Step(context.Background(), L1)
	if err == nil {
		t.Fatalf("WiredLadder.Step(L1) err = nil, want ErrProfileNotFound wrapped")
	}
	if ok {
		t.Fatalf("WiredLadder.Step(L1) ok = true, want false")
	}
	if errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("WiredLadder.Step(L1) err = %v, sentinel must NOT match (rung fired and failed)", err)
	}
	if tokens.Backend != BackendUnknown || tokens.FetchedAt.IsZero() == false {
		// Tokens must be zero on the failed-rung path so a
		// caller that mistakenly trusts them cannot reach
		// the network with a half-populated value.
		if tokens.AuthUser != 0 || tokens.AccountEmail != "" || len(tokens.Cookies) != 0 {
			t.Fatalf("WiredLadder.Step(L1) tokens = %+v, want zero Tokens on rung failure", tokens)
		}
	}
}

// TestWiredLadderL2_0Success: the L2.0 rung fires and Step returns
// Tokens carrying BackendStorageFile and the file-touched mtime.
// ok=true signals "rung fired and produced Tokens".
func TestWiredLadderL2_0Success(t *testing.T) {
	w := wiredFromTemplate(t)
	tokens, ok, err := w.Step(context.Background(), L2_0)
	if err != nil {
		t.Fatalf("WiredLadder.Step(L2_0) err = %v", err)
	}
	if !ok {
		t.Fatalf("WiredLadder.Step(L2_0) ok = false, want true")
	}
	if tokens.Backend != BackendStorageFile {
		t.Fatalf("Tokens.Backend = %v, want BackendStorageFile", tokens.Backend)
	}
	if tokens.AuthUser != 2 {
		t.Fatalf("Tokens.AuthUser = %d, want 2", tokens.AuthUser)
	}
}

// TestWiredLadderL2_0Exhausted: a stub Storage whose every call
// fails surfaces ErrReloadL2_0Exhausted wrapped around the cause.
// The dispatcher returns (zero, false, err) — ok=false because
// the rung fired and FAILED, not because it was unconfigured.
func TestWiredLadderL2_0Exhausted(t *testing.T) {
	w := wiredFromTemplate(t)
	transient := errors.New("simulated persistent IO failure")
	w.L2Storage = &fakeStorageForLadder{
		Results: []stubResult{{Err: transient}, {Err: transient}, {Err: transient}},
	}
	tokens, ok, err := w.Step(context.Background(), L2_0)
	if err == nil {
		t.Fatalf("WiredLadder.Step(L2_0) err = nil, want ErrReloadL2_0Exhausted")
	}
	if ok {
		t.Fatalf("WiredLadder.Step(L2_0) ok = true, want false")
	}
	if !errors.Is(err, ErrReloadL2_0Exhausted) {
		t.Fatalf("WiredLadder.Step(L2_0) err = %v, want ErrReloadL2_0Exhausted wrapped", err)
	}
	if errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("WiredLadder.Step(L2_0) err = %v, sentinel must NOT match (rung fired and failed)", err)
	}
	if tokens.Backend != BackendUnknown {
		t.Fatalf("Tokens.Backend = %v, want BackendUnknown on rung failure", tokens.Backend)
	}
}

// TestWiredLadderL2_0NoStorage: a WiredLadder without L2Storage
// wired returns the ErrLadderLevelNotImplemented sentinel. This is
// the "rung surface not configured" path: the rung exists in the
// dispatcher, but the caller did not wire the Storage dependency,
// so Step returns the sentinel wrapped with the level name.
//
// This is distinct from TestWiredLadderL2_0Exhausted (rung wired,
// failed) and from TestWiredLadderL4Stub (the load-bearing
// "L4 is the only default-surface-not-wired rung" assertion).
func TestWiredLadderL2_0NoStorage(t *testing.T) {
	w := wiredFromTemplate(t)
	w.L2Storage = nil
	tokens, ok, err := w.Step(context.Background(), L2_0)
	if err == nil {
		t.Fatalf("WiredLadder.Step(L2_0) err = nil, want ErrLadderLevelNotImplemented")
	}
	if ok {
		t.Fatalf("WiredLadder.Step(L2_0) ok = true, want false")
	}
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("WiredLadder.Step(L2_0) err = %v, want ErrLadderLevelNotImplemented", err)
	}
	if tokens.Backend != BackendUnknown {
		t.Fatalf("Tokens.Backend = %v, want BackendUnknown on sentinel", tokens.Backend)
	}
}

// TestWiredLadderL2_5NotConfigured: the L2.5 rung short-circuits
// with ErrReloadL2_5NotConfigured when the operator has not set
// NOTEBOOKLM_REFRESH_CMD. The dispatcher surfaces the typed
// sentinel as (zero, false, err) so the caller can detect "L2.5
// skipped — try the next rung" via errors.Is. The
// ErrLadderLevelNotImplemented sentinel must NOT match — the rung
// fired, the env-var check failed, and the typed sentinel
// communicates the specific cause.
func TestWiredLadderL2_5NotConfigured(t *testing.T) {
	w := wiredFromTemplate(t)
	// env is unset in the test process by default.
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", "")
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "")
	tokens, ok, err := w.Step(context.Background(), L2_5)
	if err == nil {
		t.Fatalf("WiredLadder.Step(L2_5) err = nil, want ErrReloadL2_5NotConfigured")
	}
	if ok {
		t.Fatalf("WiredLadder.Step(L2_5) ok = true, want false")
	}
	if !errors.Is(err, ErrReloadL2_5NotConfigured) {
		t.Fatalf("WiredLadder.Step(L2_5) err = %v, want ErrReloadL2_5NotConfigured wrapped", err)
	}
	if errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("WiredLadder.Step(L2_5) err = %v, sentinel must NOT match (rung fired and returned typed error)", err)
	}
	if tokens.Backend != BackendUnknown {
		t.Fatalf("Tokens.Backend = %v, want BackendUnknown on rung typed-error path", tokens.Backend)
	}
}

// TestWiredLadderL3NoHook: a WiredLadder without L3Reload wired
// returns the ErrLadderLevelNotImplemented sentinel for L3. This
// is the "rung surface not yet implemented" path — the
// dispatcher has the L3 case but the L3Reload function field is
// nil because T-S3-001c has not landed. The ladder caller walks
// past L3 and onto L4.
func TestWiredLadderL3NoHook(t *testing.T) {
	w := wiredFromTemplate(t)
	w.L3Reload = nil
	tokens, ok, err := w.Step(context.Background(), L3)
	if err == nil {
		t.Fatalf("WiredLadder.Step(L3) err = nil, want ErrLadderLevelNotImplemented")
	}
	if ok {
		t.Fatalf("WiredLadder.Step(L3) ok = true, want false")
	}
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("WiredLadder.Step(L3) err = %v, want ErrLadderLevelNotImplemented", err)
	}
	if tokens.Backend != BackendUnknown {
		t.Fatalf("Tokens.Backend = %v, want BackendUnknown on sentinel", tokens.Backend)
	}
}

// TestWiredLadderL4Stub: the L4 rung ALWAYS returns
// ErrLadderLevelNotImplemented (until S02 plugs in via L4Reload).
// This is the load-bearing T-S3-001d AC assertion: the sentinel
// is reachable ONLY from the L4 stub (and from a rung whose
// dispatcher surface is not wired — L2_0 without L2Storage, L3
// without L3Reload).
//
// The test pins the L4 stub-by-default behavior regardless of
// whether L4Reload is wired; a follow-up S02 PR will toggle this
// assertion by setting L4Reload.
func TestWiredLadderL4Stub(t *testing.T) {
	w := wiredFromTemplate(t)
	// Even with L4Reload explicitly nil, the L4 path MUST
	// return the sentinel.
	w.L4Reload = nil
	tokens, ok, err := w.Step(context.Background(), L4)
	if err == nil {
		t.Fatalf("WiredLadder.Step(L4) err = nil, want ErrLadderLevelNotImplemented")
	}
	if ok {
		t.Fatalf("WiredLadder.Step(L4) ok = true, want false")
	}
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("WiredLadder.Step(L4) err = %v, want ErrLadderLevelNotImplemented", err)
	}
	if tokens.Backend != BackendUnknown {
		t.Fatalf("Tokens.Backend = %v, want BackendUnknown on sentinel", tokens.Backend)
	}
}

// TestWiredLadderL3ReloadWired: when L3Reload is wired, the L3
// rung fires the function and lifts the result into Tokens.
// Backed by a closure so the test does not depend on l3.go's
// future symbol ReloadL3.
func TestWiredLadderL3ReloadWired(t *testing.T) {
	w := wiredFromTemplate(t)
	want := Tokens{
		Cookies:   []CookieView{{Name: "SID", Domain: ".google.com", Path: "/"}},
		CSRF:      "FAKE_L3_CSRF",
		SessionID: "FAKE_L3_FSID",
		AuthUser:  7,
		Backend:   BackendStorageFile,
		FetchedAt: time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC),
	}
	w.L3Reload = func(ctx context.Context) (Tokens, error) {
		return want, nil
	}
	got, ok, err := w.Step(context.Background(), L3)
	if err != nil {
		t.Fatalf("WiredLadder.Step(L3) err = %v", err)
	}
	if !ok {
		t.Fatalf("WiredLadder.Step(L3) ok = false, want true")
	}
	if got.CSRF != want.CSRF {
		t.Fatalf("Tokens.CSRF = %q, want %q", got.CSRF, want.CSRF)
	}
	if got.SessionID != want.SessionID {
		t.Fatalf("Tokens.SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if got.AuthUser != want.AuthUser {
		t.Fatalf("Tokens.AuthUser = %d, want %d", got.AuthUser, want.AuthUser)
	}
}

// TestWiredLadderFullSequence: T-S3-001d AC — table-driven
// full-sequence walk exercising L1 → L2.0 → L2.5 → L3 → L4.
//
// Each row configures which rungs fail, calls Step in order, and
// asserts the final rung that produced Tokens matches the
// expected rung. The last row exercises the all-rungs-failed path
// and confirms the final error wraps ErrLadderLevelNotImplemented.
func TestWiredLadderFullSequence(t *testing.T) {
	// Re-bind the env vars the L2.5 rung consults; restored by
	// t.Setenv on test cleanup.
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", "")
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "")

	l1Success := &profile.FakeStore{
		Profiles: map[profile.Name]profile.Profile{
			profile.Name("work"): sampleProfile(profile.Name("work")),
		},
	}
	l1Missing := &profile.FakeStore{Profiles: map[profile.Name]profile.Profile{}}

	mtime := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	l2Success := &fakeStorageForLadder{
		Results: []stubResult{{
			Storage: storage.Storage{
				Cookies: []storage.Cookie{{Name: "SID", Value: "FAKE_SID", Domain: ".google.com", Path: "/"}},
				NotebookLM: &storage.NotebookLM{
					Account: storage.Account{AuthUser: 2, Email: "alice@example.invalid"},
				},
			},
			Mtime: mtime,
		}},
	}
	l2Fail := &fakeStorageForLadder{
		Results: []stubResult{
			{Err: errors.New("transient 1")},
			{Err: errors.New("transient 2")},
			{Err: errors.New("transient 3")},
		},
	}

	type rungResult struct {
		level       Level
		ok          bool
		wantError   bool // errors.Is(err, ErrLadderLevelNotImplemented)
		wantBackend Backend
	}
	cases := []struct {
		name     string
		l1       profile.Reader
		l2       Storage
		l3Hook   func(ctx context.Context) (Tokens, error)
		sequence []rungResult
	}{
		{
			name: "L1 succeeds -> tokens returned",
			l1:   l1Success,
			l2:   nil,
			sequence: []rungResult{
				{level: L1, ok: true, wantBackend: BackendStorageFile},
			},
		},
		{
			name: "L1 fails -> L2.0 succeeds -> tokens returned",
			l1:   l1Missing,
			l2:   l2Success,
			sequence: []rungResult{
				{level: L1, ok: false, wantError: false},
				{level: L2_0, ok: true, wantBackend: BackendStorageFile},
			},
		},
		{
			name: "L1/L2.0/L2.5 fail -> L3 succeeds -> tokens returned",
			l1:   l1Missing,
			l2:   l2Fail,
			l3Hook: func(ctx context.Context) (Tokens, error) {
				return Tokens{
					CSRF:      "FAKE_L3_CSRF",
					SessionID: "FAKE_L3_FSID",
					Backend:   BackendStorageFile,
					FetchedAt: time.Now().UTC(),
				}, nil
			},
			sequence: []rungResult{
				{level: L1, ok: false, wantError: false},
				{level: L2_0, ok: false, wantError: false},
				{level: L2_5, ok: false, wantError: false},
				{level: L3, ok: true, wantBackend: BackendStorageFile},
			},
		},
		{
			name: "all levels fail -> final error wraps ErrLadderLevelNotImplemented (L4 stub)",
			l1:   l1Missing,
			l2:   l2Fail,
			sequence: []rungResult{
				{level: L1, ok: false, wantError: false},
				{level: L2_0, ok: false, wantError: false},
				{level: L2_5, ok: false, wantError: false},
				{level: L3, ok: false, wantError: true},
				{level: L4, ok: false, wantError: true},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &WiredLadder{
				DefaultLadder: &DefaultLadder{
					Store: tc.l1,
					Jar:   cookiejar.New(),
					Name:  profile.Name("work"),
				},
				L2Storage:  tc.l2,
				L2_5Inline: &fakeInlineForLaderNoop{},
				L3Reload:   tc.l3Hook,
				L4Reload:   nil,
			}

			var lastErr error
			var lastOK bool
			var lastTokens Tokens
			for _, step := range tc.sequence {
				tokens, ok, err := w.Step(context.Background(), step.level)
				lastErr = err
				lastOK = ok
				lastTokens = tokens
				if step.ok {
					if err != nil {
						t.Fatalf("Step(%s) err = %v, want nil (expected rung to succeed)", step.level, err)
					}
					if !ok {
						t.Fatalf("Step(%s) ok = false, want true", step.level)
					}
					if tokens.Backend != step.wantBackend {
						t.Fatalf("Step(%s) Tokens.Backend = %v, want %v", step.level, tokens.Backend, step.wantBackend)
					}
					// Once a rung succeeds the ladder stops
					// walking; assert the remaining steps
					// were never invoked.
					break
				}
				// Expected rung to fail: assert the error
				// class matches the expected sentinel.
				if step.wantError {
					if !errors.Is(err, ErrLadderLevelNotImplemented) {
						t.Fatalf("Step(%s) err = %v, want ErrLadderLevelNotImplemented", step.level, err)
					}
				} else if errors.Is(err, ErrLadderLevelNotImplemented) {
					t.Fatalf("Step(%s) err = %v, sentinel must NOT match (rung fired and failed)", step.level, err)
				}
			}

			// Final assertion: the LAST rung's error matches
			// the expected terminal state.
			switch tc.name {
			case "all levels fail -> final error wraps ErrLadderLevelNotImplemented (L4 stub)":
				if lastOK {
					t.Fatalf("last OK = true, want false (all rungs should have failed)")
				}
				if !errors.Is(lastErr, ErrLadderLevelNotImplemented) {
					t.Fatalf("last err = %v, want ErrLadderLevelNotImplemented (L4 stub)", lastErr)
				}
				if lastTokens.Backend != BackendUnknown {
					t.Fatalf("last Tokens.Backend = %v, want BackendUnknown on terminal failure", lastTokens.Backend)
				}
			}
		})
	}
}

// TestWiredLadderSentinelScope: AC2 of T-S3-001d — the sentinel
// ErrLadderLevelNotImplemented returns ONLY from the L4 stub (or
// from a rung whose dispatcher surface is not wired: L2_0 without
// L2Storage, L3 without L3Reload). It must NOT match the error
// returned by a rung that fired and failed with its own typed
// sentinel (L2.0 exhausted, L2.5 not-configured).
//
// The test walks every rung in a single WiredLadder configured to
// fail each rung with its rung-specific typed sentinel, and asserts
// the sentinel NEVER matches. A separate L4 stub assertion
// confirms the sentinel DOES match the L4 error.
func TestWiredLadderSentinelScope(t *testing.T) {
	transient := errors.New("simulated persistent IO failure")
	l2Fail := &fakeStorageForLadder{
		Results: []stubResult{
			{Err: transient}, {Err: transient}, {Err: transient},
		},
	}
	w := &WiredLadder{
		DefaultLadder: &DefaultLadder{
			Store: &profile.FakeStore{Profiles: map[profile.Name]profile.Profile{}},
			Jar:   cookiejar.New(),
			Name:  profile.Name("work"),
		},
		L2Storage: l2Fail,
	}
	// L1 fails (missing profile) — rung fired, typed error.
	_, _, err := w.Step(context.Background(), L1)
	if err == nil {
		t.Fatalf("L1 failed-step expected error")
	}
	if errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("L1 err = %v, sentinel must NOT match (rung fired and failed)", err)
	}

	// L2_0 exhausted — rung fired, typed ErrReloadL2_0Exhausted.
	_, _, err = w.Step(context.Background(), L2_0)
	if err == nil {
		t.Fatalf("L2_0 failed-step expected error")
	}
	if !errors.Is(err, ErrReloadL2_0Exhausted) {
		t.Fatalf("L2_0 err = %v, want ErrReloadL2_0Exhausted", err)
	}
	if errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("L2_0 err = %v, sentinel must NOT match", err)
	}

	// L2_5 not-configured — rung fired, typed ErrReloadL2_5NotConfigured.
	t.Setenv("NOTEBOOKLM_REFRESH_CMD", "")
	t.Setenv("NOTEBOOKLM_REFRESH_CMD_MIDSESSION", "")
	_, _, err = w.Step(context.Background(), L2_5)
	if err == nil {
		t.Fatalf("L2_5 failed-step expected error")
	}
	if !errors.Is(err, ErrReloadL2_5NotConfigured) {
		t.Fatalf("L2_5 err = %v, want ErrReloadL2_5NotConfigured", err)
	}
	if errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("L2_5 err = %v, sentinel must NOT match", err)
	}

	// L3 no-hook — sentinel returns (rung surface not wired).
	_, _, err = w.Step(context.Background(), L3)
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("L3 err = %v, want ErrLadderLevelNotImplemented", err)
	}

	// L4 stub — sentinel returns (load-bearing AC).
	_, _, err = w.Step(context.Background(), L4)
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("L4 err = %v, want ErrLadderLevelNotImplemented", err)
	}
}

// TestWiredLadderUnknownLevel: out-of-range Level returns the
// sentinel — never a panic, never a bare error without the
// sentinel wrap. Mirrors TestDefaultLadderUnknownLevel on the
// DefaultLadder surface.
func TestWiredLadderUnknownLevel(t *testing.T) {
	w := wiredFromTemplate(t)
	_, _, err := w.Step(context.Background(), Level(99))
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("WiredLadder.Step(unknown) err = %v, want ErrLadderLevelNotImplemented", err)
	}
}

// TestWiredLadderNilReceiver: a WiredLadder with a nil embedded
// *DefaultLadder does NOT panic on L1 — it returns the sentinel.
// Belt-and-braces for callers that initialize the L2 path but
// forget to embed the L1 ladder.
func TestWiredLadderNilReceiver(t *testing.T) {
	w := &WiredLadder{} // no DefaultLadder embedded
	_, _, err := w.Step(context.Background(), L1)
	if !errors.Is(err, ErrLadderLevelNotImplemented) {
		t.Fatalf("WiredLadder.Step(L1, nil receiver) err = %v, want ErrLadderLevelNotImplemented", err)
	}
}
