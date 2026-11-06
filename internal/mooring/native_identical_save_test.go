package mooring

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

type nativeSaveMeasure struct{ versions, blobs, used int64 }

func measureNativeSaves(t *testing.T, s *NativeStore, scope EditLeaseScope) nativeSaveMeasure {
	t.Helper()
	var m nativeSaveMeasure
	for _, q := range []struct {
		query string
		into  *int64
	}{
		{`SELECT count(*) FROM artifact_versions WHERE workspace_id=? AND page_id=?`, &m.versions},
		{`SELECT count(*) FROM artifact_version_blobs WHERE workspace_id=? AND page_id=?`, &m.blobs},
	} {
		if err := s.db.QueryRowContext(t.Context(), q.query, scope.WorkspaceID, scope.ArtifactID).Scan(q.into); err != nil {
			t.Fatal(err)
		}
	}
	var total sql.NullInt64
	if err := s.db.QueryRowContext(t.Context(), `SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id=?`, scope.WorkspaceID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	m.used = total.Int64
	return m
}

// nativeHeadAtTwo commits `second` as version 2 under revision r1.
func nativeHeadAtTwo(t *testing.T, s *NativeStore, scope EditLeaseScope, acting EditLeasePrincipal, second []byte) (EditLease, NativeSaveReceipt) {
	t.Helper()
	l, _, err := s.Initialize(t.Context(), scope, acting, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.SaveAttempt(t.Context(), l, 1, "r1", nativeReceiptHash(second), second, "attempt-first")
	if err != nil || first.Version != "2" {
		t.Fatalf("seed save: %+v %v", first, err)
	}
	return l, first
}

// Content equality (#5075): a fresh attempt with the bytes already at the head,
// from any client, changes nothing and costs nothing.
func TestNativeIdenticalSaveCreatesNoVersionAndNoCharge(t *testing.T) {
	second := []byte(`{"v":2,"body":"the same bytes twice"}`)
	hash := nativeReceiptHash(second)
	for _, tc := range []struct {
		name    string
		attempt string
		repeats int
	}{
		{"one fresh attempt", "attempt-fresh-1", 1},
		{"no attempt id, as the agent tool saves", "", 1},
		{"a client without the fix pressing Ctrl+S repeatedly", "attempt-fresh-2", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, scope, acting, _ := nativeProposalFixture(t)
			l, _ := nativeHeadAtTwo(t, s, scope, *acting, second)
			before := measureNativeSaves(t, s, scope)
			for i := range tc.repeats {
				got, err := s.SaveAttempt(t.Context(), l, 2, "r"+string(rune('a'+i)), hash, second, tc.attempt)
				if err != nil {
					t.Fatal(err)
				}
				if !got.Unchanged || got.Version != "2" || got.BaseVersion != "2" || got.ContentHash != hash || got.CandidateHash != hash || got.Phase != "committed" {
					t.Fatalf("identical save answered %+v", got)
				}
			}
			if after := measureNativeSaves(t, s, scope); after != before {
				t.Fatalf("a no-op changed storage: %+v -> %+v", before, after)
			}
		})
	}
}

// Attempt idempotence (#5075): a repeat of the exact committed request is
// answered with its own receipt, whatever the head has become since.
func TestNativeSaveRepeatOfACommittedAttemptReturnsItsReceipt(t *testing.T) {
	second := []byte(`{"v":2}`)
	hash := nativeReceiptHash(second)
	t.Run("head is still the attempt", func(t *testing.T) {
		s, _, _, scope, acting, _ := nativeProposalFixture(t)
		l, first := nativeHeadAtTwo(t, s, scope, *acting, second)
		before := measureNativeSaves(t, s, scope)
		got, err := s.SaveAttempt(t.Context(), l, 1, "r1", hash, second, "attempt-first")
		if err != nil || got != first {
			t.Fatalf("repeat: %+v %v, want %+v", got, err, first)
		}
		if after := measureNativeSaves(t, s, scope); after != before {
			t.Fatalf("a repeat changed storage: %+v -> %+v", before, after)
		}
	})
	t.Run("head moved past the attempt", func(t *testing.T) {
		s, _, _, scope, acting, _ := nativeProposalFixture(t)
		l, first := nativeHeadAtTwo(t, s, scope, *acting, second)
		third := []byte(`{"v":3}`)
		if _, err := s.SaveSnapshot(t.Context(), l, 2, "r2", nativeReceiptHash(third), third); err != nil {
			t.Fatal(err)
		}
		before := measureNativeSaves(t, s, scope)
		got, err := s.SaveAttempt(t.Context(), l, 1, "r1", hash, second, "attempt-first")
		if err != nil || got != first {
			t.Fatalf("repeat: %+v %v, want %+v", got, err, first)
		}
		if after := measureNativeSaves(t, s, scope); after != before {
			t.Fatalf("a repeat changed storage: %+v -> %+v", before, after)
		}
	})
}

// Everything else keeps the contract: only the exact attempt and only the head
// at the same base are exempt from a save or its conflict.
func TestNativeSaveStillDecidesWhatIsNotARepeat(t *testing.T) {
	second := []byte(`{"v":2}`)
	third := []byte(`{"v":3}`)
	for _, tc := range []struct {
		name       string
		base       int64
		revision   string
		bytes      []byte
		wantErr    error
		wantVer    string
		newVersion bool
	}{
		{name: "different bytes at the head save", base: 2, revision: "r2", bytes: third, wantVer: "3", newVersion: true},
		{name: "same bytes as an older version save", base: 3, revision: "r2", bytes: []byte(`{"v":1}`), wantVer: "4", newVersion: true},
		{name: "another attempt with the head's bytes at an older base conflicts", base: 1, revision: "other", bytes: second, wantErr: artifacts.ErrVersionConflict},
		{name: "another session's revision does not prove the attempt", base: 1, revision: "r1 ", bytes: second, wantErr: artifacts.ErrVersionConflict},
		{name: "the attempt's revision with other bytes conflicts", base: 1, revision: "r1", bytes: third, wantErr: artifacts.ErrVersionConflict},
		{name: "another attempt with the same revision and bytes conflicts", base: 1, revision: "r1", bytes: second, wantErr: artifacts.ErrVersionConflict},
		{name: "a future base conflicts", base: 9, revision: "r2", bytes: second, wantErr: artifacts.ErrVersionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, scope, acting, _ := nativeProposalFixture(t)
			l, _ := nativeHeadAtTwo(t, s, scope, *acting, second)
			if tc.base == 3 {
				if _, err := s.SaveSnapshot(t.Context(), l, 2, "r-third", nativeReceiptHash(third), third); err != nil {
					t.Fatal(err)
				}
			}
			before := measureNativeSaves(t, s, scope)
			got, err := s.SaveAttempt(t.Context(), l, tc.base, tc.revision, nativeReceiptHash(tc.bytes), tc.bytes, "attempt-other")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error %v, want %v", err, tc.wantErr)
			}
			if got.Version != tc.wantVer || got.Unchanged {
				t.Fatalf("receipt %+v, want version %q", got, tc.wantVer)
			}
			want := before.versions
			if tc.newVersion {
				want++
			}
			if after := measureNativeSaves(t, s, scope); after.versions != want {
				t.Fatalf("versions %d, want %d", after.versions, want)
			}
		})
	}
}

// A version committed without an attempt id (an older binary, the agent tool)
// proves nothing about a named attempt's repeat, but its reconcile still finds
// it: a reply lost across an upgrade must not turn into a false "did not commit".
func TestNativeVersionWithoutAttemptIDStaysProvable(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, *acting, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	second := []byte(`{"v":2}`)
	hash := nativeReceiptHash(second)
	first, err := s.SaveSnapshot(t.Context(), l, 1, "r1", hash, second)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := s.snapshotMessage(l, 1, "r1", hash)
	if err != nil {
		t.Fatal(err)
	}
	if state.Manifest.Message != legacy || strings.Contains(legacy, "AttemptID") {
		t.Fatalf("the encoding of a version without an attempt changed: %s", state.Manifest.Message)
	}
	if got, err := s.SaveAttempt(t.Context(), l, 1, "r1", hash, second, "attempt-named"); !errors.Is(err, artifacts.ErrVersionConflict) || got.Version != "" {
		t.Fatalf("a named repeat claimed an anonymous version: %+v %v", got, err)
	}
	if got, err := s.SaveSnapshot(t.Context(), l, 1, "r1", hash, second); err != nil || got != first {
		t.Fatalf("anonymous repeat: %+v %v", got, err)
	}
	if got, err := s.ReconcileAttempt(t.Context(), l, 1, "r1", hash, "attempt-named"); err != nil || got != first {
		t.Fatalf("reconcile across the upgrade: %+v %v", got, err)
	}
}

// #5071 and revocation: a named attempt stays provable by its own reconcile once
// the head moved on, and after its lease lapsed, which is what revokeWrite's
// final lookup relies on. So does a caller that names no attempt.
func TestNativeNamedAttemptStaysProvableByReconcile(t *testing.T) {
	s, _, clock, scope, acting, _ := nativeProposalFixture(t)
	second := []byte(`{"v":2}`)
	hash := nativeReceiptHash(second)
	l, first := nativeHeadAtTwo(t, s, scope, *acting, second)
	third := []byte(`{"v":3}`)
	if _, err := s.SaveSnapshot(t.Context(), l, 2, "r2", nativeReceiptHash(third), third); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if got, err := s.ReconcileAttempt(t.Context(), l, 1, "r1", hash, "attempt-first"); err != nil || got != first {
		t.Fatalf("own attempt after the lease lapsed: %+v %v", got, err)
	}
	if got, err := s.ReconcileSnapshot(t.Context(), l, 1, "r1", hash); err != nil || got != first {
		t.Fatalf("no attempt named: %+v %v", got, err)
	}
	if got, err := s.ReconcileAttempt(t.Context(), l, 1, "r1", hash, "attempt-other"); !errors.Is(err, ErrNativeUnconfirmed) {
		t.Fatalf("another attempt under a lapsed lease: %+v %v", got, err)
	}
}

// The no-op is an answer about the document, so it stays behind every fence a
// save has: a fence must not start reporting a saved state.
func TestNativeIdenticalSaveKeepsItsFences(t *testing.T) {
	second := []byte(`{"v":2}`)
	hash := nativeReceiptHash(second)
	t.Run("a retired attempt is refused even with the head's bytes", func(t *testing.T) {
		s, _, _, scope, acting, _ := nativeProposalFixture(t)
		l, _ := nativeHeadAtTwo(t, s, scope, *acting, second)
		if _, err := s.ReconcileAttempt(t.Context(), l, 2, "r2", hash, "attempt-retired"); !errors.Is(err, ErrNativeAttemptAbsent) {
			t.Fatal(err)
		}
		if _, err := s.SaveAttempt(t.Context(), l, 2, "r2", hash, second, "attempt-retired"); !errors.Is(err, ErrNativeAttemptRetired) {
			t.Fatalf("retired attempt: %v", err)
		}
	})
	t.Run("a lapsed lease cannot confirm", func(t *testing.T) {
		s, _, clock, scope, acting, _ := nativeProposalFixture(t)
		l, _ := nativeHeadAtTwo(t, s, scope, *acting, second)
		clock.advance(time.Minute)
		if got, err := s.SaveAttempt(t.Context(), l, 2, "r2", hash, second, "attempt-lapsed"); err == nil || got.Unchanged {
			t.Fatalf("lapsed lease: %+v %v", got, err)
		}
	})
	t.Run("lost write authority cannot confirm", func(t *testing.T) {
		s, _, _, scope, acting, denied := nativeProposalFixture(t)
		l, _ := nativeHeadAtTwo(t, s, scope, *acting, second)
		*denied = ErrEditLeaseDenied
		if got, err := s.SaveAttempt(t.Context(), l, 2, "r2", hash, second, "attempt-denied"); err == nil || got.Unchanged {
			t.Fatalf("no write authority: %+v %v", got, err)
		}
	})
	t.Run("a repeat without write authority learns nothing", func(t *testing.T) {
		s, _, _, scope, acting, denied := nativeProposalFixture(t)
		l, _ := nativeHeadAtTwo(t, s, scope, *acting, second)
		*denied = ErrEditLeaseDenied
		if got, err := s.SaveAttempt(t.Context(), l, 1, "r1", hash, second, "attempt-denied"); err == nil || got.Version != "" {
			t.Fatalf("no write authority: %+v %v", got, err)
		}
	})
	t.Run("a wrong content hash is refused", func(t *testing.T) {
		s, _, _, scope, acting, _ := nativeProposalFixture(t)
		l, _ := nativeHeadAtTwo(t, s, scope, *acting, second)
		if _, err := s.SaveAttempt(t.Context(), l, 2, "r2", nativeReceiptHash([]byte(`{}`)), second, "attempt-hash"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("hash mismatch: %v", err)
		}
	})
}

// Simultaneous saves of the same bytes at one base publish exactly one version.
func TestNativeConcurrentIdenticalSavesPublishOnce(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, *acting, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	second := []byte(`{"v":2}`)
	hash := nativeReceiptHash(second)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.SaveAttempt(t.Context(), l, 1, "r"+string(rune('a'+i)), hash, second, "")
		}()
	}
	wg.Wait()
	if m := measureNativeSaves(t, s, scope); m.versions != 2 {
		t.Fatalf("versions %d, want 2", m.versions)
	}
}
