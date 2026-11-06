package mooring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

func nativeRetiredRows(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM craft_native_retired_attempts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Review round 2: distinct attempt ids cannot grow the table without bound.
// A page holds at most nativeRetiredAttemptsPerPage rows; beyond that nothing
// is recorded and the answer stays unconfirmed (409), never absent (410).
func TestNativeRetirementIsCappedPerPage(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":"never"}`)
	for i := 0; i < 3*nativeRetiredAttemptsPerPage; i++ {
		_, err = s.ReconcileAttempt(t.Context(), l, 1, "r", nativeReceiptHash(raw), fmt.Sprintf("attempt-spam-%04d", i))
		want := ErrNativeAttemptAbsent
		if i >= nativeRetiredAttemptsPerPage {
			want = ErrNativeRetirementLimit
		}
		if !errors.Is(err, want) {
			t.Fatalf("retirement %d: %v, want %v", i, err, want)
		}
	}
	if n := nativeRetiredRows(t, db); n != nativeRetiredAttemptsPerPage {
		t.Fatalf("rows %d, want %d", n, nativeRetiredAttemptsPerPage)
	}
	// An already retired attempt answers absent again without a new row.
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r", nativeReceiptHash(raw), "attempt-spam-0000"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatal(err)
	}
	// While the lease (and its admission) is alive, a retired attempt stays refused...
	if _, err = s.SaveAttempt(t.Context(), l, 1, "r", nativeReceiptHash(raw), raw, "attempt-spam-0003"); !errors.Is(err, ErrNativeAttemptRetired) {
		t.Fatalf("retired attempt saved: %v", err)
	}
	// ...and an attempt refused at the cap was not retired: it is unconfirmed, may still commit.
	if r, err := s.SaveAttempt(t.Context(), l, 1, "r", nativeReceiptHash(raw), raw, fmt.Sprintf("attempt-spam-%04d", nativeRetiredAttemptsPerPage)); err != nil || r.Version != "2" {
		t.Fatalf("unrecorded attempt: %+v %v", r, err)
	}
}

// Only the current live holder retires. Without a live lease nothing is
// recorded; rows of a lease that lapsed (admission gone, release, takeover)
// are pruned by the next retirement anywhere.
func TestNativeRetirementNeedsTheLiveLeaseAndPrunesLapsedLeases(t *testing.T) {
	s, leases, db, clock, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":"x"}`)
	for i := 0; i < 3; i++ {
		if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r", nativeReceiptHash(raw), fmt.Sprintf("attempt-old-%04d", i)); !errors.Is(err, ErrNativeAttemptAbsent) {
			t.Fatal(err)
		}
	}
	// The admission stops renewing: the lease lapses.
	clock.advance(5 * time.Second)
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r", nativeReceiptHash(raw), "attempt-old-late"); !errors.Is(err, ErrNativeUnconfirmed) {
		t.Fatalf("retirement without a live lease: %v", err)
	}
	if n := nativeRetiredRows(t, db); n != 3 {
		t.Fatalf("rows %d after refused retirement", n)
	}
	// The lapsed generation can never save again, so its rows are dead. The
	// fixture's page authority names one principal, so it takes the page again
	// (a new generation, as after any release, handoff or takeover).
	next, err := leases.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReconcileAttempt(t.Context(), next, 1, "r", nativeReceiptHash(raw), "attempt-new-0001"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatal(err)
	}
	if next.Generation == l.Generation {
		t.Fatal("re-acquire kept the generation")
	}
	var sessions []string
	rows, err := db.Query(`SELECT attempt_id||'@'||generation FROM craft_native_retired_attempts`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, id)
	}
	rows.Close()
	if len(sessions) != 1 || sessions[0] != fmt.Sprintf("attempt-new-0001@%d", next.Generation) {
		t.Fatalf("rows after prune: %v", sessions)
	}
	if _, err = s.SaveAttempt(t.Context(), l, 1, "r", nativeReceiptHash(raw), raw, "attempt-old-0001"); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("lapsed generation saved: %v", err)
	}
}

// A retired row is durable: a new store on the same database (a restarted
// process whose lease is still live) still refuses the attempt.
func TestNativeRetiredAttemptSurvivesAStoreRestart(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), "attempt-restart-1"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatal(err)
	}
	restarted := NewNativeStore(db, s.config)
	if _, err = restarted.SaveAttempt(t.Context(), l, 1, "r2", nativeReceiptHash(raw), raw, "attempt-restart-1"); !errors.Is(err, ErrNativeAttemptRetired) {
		t.Fatalf("retired attempt saved after restart: %v", err)
	}
	if n := nativeRetiredRows(t, db); n != 1 {
		t.Fatalf("rows %d", n)
	}
}

// A long-lived editor may recover from more than eight network failures over
// its lifetime. Once a fresh save advances the head, older attempts cannot
// publish through their base fence and must not consume the retirement limit.
func TestNativeRetirementRecoversRepeatedOutagesWithTheSameLease(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var firstReceipt NativeSaveReceipt
	for i := 0; i < 3*nativeRetiredAttemptsPerPage; i++ {
		base := int64(i + 1)
		raw := []byte(fmt.Sprintf(`{"v":%d}`, base+1))
		attempt := fmt.Sprintf("outage-attempt-%04d", i)
		if _, err := s.ReconcileAttempt(t.Context(), l, base, "r", nativeReceiptHash(raw), attempt); !errors.Is(err, ErrNativeAttemptAbsent) {
			t.Fatalf("outage %d at base %d: %v", i+1, base, err)
		}
		if _, err := s.SaveAttempt(t.Context(), l, base, "r", nativeReceiptHash(raw), raw, attempt); !errors.Is(err, ErrNativeAttemptRetired) {
			t.Fatalf("retired current-base attempt %d published: %v", i, err)
		}
		receipt, err := s.SaveAttempt(t.Context(), l, base, "r", nativeReceiptHash(raw), raw, fmt.Sprintf("fresh-attempt-%04d", i))
		if err != nil {
			t.Fatalf("fresh capture %d: %v", i, err)
		}
		if i == 0 {
			firstReceipt = receipt
		}
		if n := nativeRetiredRows(t, db); n > nativeRetiredAttemptsPerPage {
			t.Fatalf("unbounded retired rows: %d", n)
		}
	}
	// Pruning an old row never makes its delayed original publishable.
	old := []byte(`{"v":2}`)
	if _, err := s.SaveAttempt(t.Context(), l, 1, "r", nativeReceiptHash(old), old, "outage-attempt-0000"); !errors.Is(err, artifacts.ErrVersionConflict) {
		t.Fatalf("pruned stale-base attempt was not fenced: %v", err)
	}
	// Pruning changes only runtime fences, never the exact historical receipt.
	if receipt, err := s.ReconcileAttempt(t.Context(), l, 1, "r", nativeReceiptHash(old), "fresh-attempt-0000"); err != nil || receipt != firstReceipt {
		t.Fatalf("historical committed receipt changed after pruning: %+v %v", receipt, err)
	}
}

// Review of #5075: a retired attempt whose row was pruned must not get the
// receipt of another attempt that shares its revision and bytes. Pruning lifts
// the runtime fence, so the identity has to be durable in the committed version.
func TestNativeDelayedRetiredAttemptGetsNoReceiptOfASameRevisionAttempt(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	hash := nativeReceiptHash(raw)
	if _, err = s.ReconcileAttempt(t.Context(), l, 1, "r", hash, "attempt-a-0001"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatal(err)
	}
	committed, err := s.SaveAttempt(t.Context(), l, 1, "r", hash, raw, "attempt-b-0001")
	if err != nil || committed.Version != "2" {
		t.Fatalf("fresh attempt: %+v %v", committed, err)
	}
	// Retiring anything at the new head prunes A's row, whose base is now below it.
	next := []byte(`{"v":3}`)
	if _, err = s.ReconcileAttempt(t.Context(), l, 2, "r2", nativeReceiptHash(next), "attempt-c-0001"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatal(err)
	}
	var kept int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM craft_native_retired_attempts WHERE attempt_id='attempt-a-0001'`).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("A's row was not pruned: %d %v", kept, err)
	}
	if got, err := s.SaveAttempt(t.Context(), l, 1, "r", hash, raw, "attempt-a-0001"); !errors.Is(err, artifacts.ErrVersionConflict) || got.Version != "" {
		t.Fatalf("delayed retired attempt answered: %+v %v", got, err)
	}
	// Its reconcile reports the same thing: it did not commit.
	if got, err := s.ReconcileAttempt(t.Context(), l, 1, "r", hash, "attempt-a-0001"); !errors.Is(err, ErrNativeAttemptAbsent) {
		t.Fatalf("reconcile of the retired attempt: %+v %v", got, err)
	}
	// B keeps both proofs.
	if got, err := s.SaveAttempt(t.Context(), l, 1, "r", hash, raw, "attempt-b-0001"); err != nil || got != committed {
		t.Fatalf("repeat of B: %+v %v", got, err)
	}
	if got, err := s.ReconcileAttempt(t.Context(), l, 1, "r", hash, "attempt-b-0001"); err != nil || got != committed {
		t.Fatalf("reconcile of B: %+v %v", got, err)
	}
}
