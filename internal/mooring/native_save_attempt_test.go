package mooring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// pauseValidation makes a save of exactly `held` wait between validation
// (outside the publication transaction) and Apply until release is closed.
func pauseValidation(s *NativeStore, held []byte) (entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	validate := s.config.Validate
	s.config.Validate = func(ctx context.Context, b []byte) error {
		if string(b) == string(held) {
			close(entered)
			<-release
		}
		return validate(ctx, b)
	}
	return
}

// A save validated outside the publication transaction can still be pending
// when its client gives up and reconciles. A reconcile that answers "nothing
// landed" must make that true: the paused original may never commit after it
// (#4899, review of #4900).
func TestNativeReconcileRetiresAPendingSaveAttempt(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	lost := []byte(`{"v":"lost"}`)
	entered, release := pauseValidation(s, lost)
	done := make(chan error, 1)
	go func() {
		_, err := s.SaveAttempt(context.Background(), l, 1, "r-lost", nativeReceiptHash(lost), lost, "attempt-lost-1")
		done <- err
	}()
	<-entered
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r-lost", nativeReceiptHash(lost), "attempt-lost-1"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatalf("reconcile of a pending attempt: %v", err)
	}
	close(release)
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("paused save never finished")
	}
	if !errors.Is(err, ErrNativeAttemptRetired) {
		t.Fatalf("the retired attempt was not refused at publication: %v", err)
	}
	if state, err := s.Read(t.Context(), scope); err != nil || state.LatestVersion != 1 {
		t.Fatalf("head moved after a retired attempt: %+v %v", state.LatestVersion, err)
	}
	// The editor resumes with a fresh attempt of its current bytes, at the same base.
	fresh := []byte(`{"v":"fresh"}`)
	r, err := s.SaveAttempt(t.Context(), l, 1, "r-fresh", nativeReceiptHash(fresh), fresh, "attempt-fresh-1")
	if err != nil || r.Version != "2" {
		t.Fatalf("fresh attempt after retirement: %+v %v", r, err)
	}
	// Retirement is idempotent and stays "absent" even once a later version exists.
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r-lost", nativeReceiptHash(lost), "attempt-lost-1"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatalf("second reconcile: %v", err)
	}
}

// Identical bytes and revision in a fresh attempt are not refused by the
// retired attempt's row: retirement is by attempt id, not by content.
func TestNativeRetiredAttemptDoesNotRefuseAnIdenticalFreshAttempt(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), "attempt-a-0001"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatal(err)
	}
	if _, err = s.SaveAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), raw, "attempt-a-0001"); !errors.Is(err, ErrNativeAttemptRetired) {
		t.Fatalf("retired attempt saved: %v", err)
	}
	if r, err := s.SaveAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), raw, "attempt-b-0001"); err != nil || r.Version != "2" {
		t.Fatalf("identical fresh attempt: %+v %v", r, err)
	}
	// The landed attempt is still proven by its own reconcile.
	if r, err := s.ReconcileAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), "attempt-b-0001"); err != nil || r.Version != "2" {
		t.Fatalf("reconcile of the landed attempt: %+v %v", r, err)
	}
}

// A head that moved past base by another commit also proves the attempt
// absent: publication requires base == head, so it can never commit.
func TestNativeReconcileAttemptAfterAnotherCommitIsAbsent(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	other := []byte(`{"v":"other"}`)
	if _, err = s.SaveAttempt(t.Context(), l, 1, "r-other", nativeReceiptHash(other), other, "attempt-other"); err != nil {
		t.Fatal(err)
	}
	mine := []byte(`{"v":"mine"}`)
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r-mine", nativeReceiptHash(mine), "attempt-mine-1"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatalf("reconcile after another commit: %v", err)
	}
	if _, err = s.SaveAttempt(t.Context(), l, 1, "r-mine2", nativeReceiptHash(mine), mine, "attempt-mine-2"); !errors.Is(err, artifacts.ErrVersionConflict) {
		t.Fatalf("a fresh attempt at the old base over another version: %v", err)
	}
}

// Without an attempt id nothing can be retired: the answer stays unconfirmed
// and a pending original may still commit (the pre-#4899 contract, kept for
// the CLI and older editors).
func TestNativeReconcileWithoutAttemptIDStaysUnconfirmed(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), ""); !errors.Is(err, ErrNativeUnconfirmed) {
		t.Fatalf("reconcile without attempt id: %v", err)
	}
	var rows int
	if err = db.QueryRow(`SELECT count(*) FROM craft_native_retired_attempts`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("retired rows without attempt id: %d %v", rows, err)
	}
	for _, bad := range []string{"short", "has space in it", "x/../../y-attempt", string(make([]byte, 65))} {
		if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("attempt id %q accepted: %v", bad, err)
		}
		if _, err = s.SaveAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), raw, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("attempt id %q accepted by save: %v", bad, err)
		}
	}
}
