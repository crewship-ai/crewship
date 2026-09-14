package pipeline

// #2500, opponent round 2 — a coalesce is judged as the pair it produces.
//
// A debounce coalesce keeps the first trigger's pin and adopts the last
// trigger's inputs. Each rule is right on its own; together they can store a
// row whose inputs the pinned recipe rejects. The store cannot judge the
// inputs (it does not know the DSL), so it hands the caller the pin the row
// WILL carry and lets the caller refuse — and a refusal must leave the row
// exactly as it was. Two further invariants ride along: the UPDATE only
// lands on the row whose pin was judged, so two concurrent coalesces cannot
// judge one pin and store another; and a row the dispatcher has already
// claimed is not "coalesced into" silently — the trigger gets a fresh row.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func pendingRow(t *testing.T, db *sql.DB, id string) (inputs, user string, pin *int, status string) {
	t.Helper()
	if err := db.QueryRow(`SELECT inputs_json, COALESCE(invoking_user_id,''), pinned_version, status FROM pending_runs WHERE id=?`, id).
		Scan(&inputs, &user, &pin, &status); err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return inputs, user, pin, status
}

func TestPendingRuns_CoalesceAdmitSeesTheKeptPin(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	one, two := 1, 2
	base := PendingRun{WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", DebounceKey: "k", FireAt: time.Now().Add(30 * time.Second)}

	first := base
	first.ID, first.PinnedVersion, first.InputsJSON, first.InvokingUserID = "p1", &one, `{"region":"eu"}`, "alice"
	if _, err := s.EnqueueChecked(ctx, first, nil); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Refused: the admit hook is asked about pin 1 (the row's), not pin 2
	// (this trigger's), and its refusal leaves the row untouched.
	second := base
	second.ID, second.PinnedVersion, second.InputsJSON, second.InvokingUserID = "p2", &two, `{"zone":"us"}`, "bob"
	refuse := errors.New("zone is not an input of v1")
	var judged *int
	_, err := s.EnqueueChecked(ctx, second, func(_ context.Context, pin *int) error {
		judged = pin
		return refuse
	})
	if !errors.Is(err, refuse) {
		t.Fatalf("refused coalesce returned %v, want the admit error", err)
	}
	if judged == nil || *judged != 1 {
		t.Fatalf("admit was asked about pin %v, want 1 — the row's pin is the effective one", judged)
	}
	inputs, user, pin, status := pendingRow(t, db, "p1")
	if inputs != `{"region":"eu"}` || user != "alice" || pin == nil || *pin != 1 || status != "pending" {
		t.Fatalf("refused coalesce changed the row: inputs=%s user=%s pin=%v status=%s", inputs, user, pin, status)
	}

	// Accepted: a compatible payload still replaces inputs and attribution,
	// keeps the pin, and the result reports what the row now carries.
	third := base
	third.ID, third.PinnedVersion, third.InputsJSON, third.InvokingUserID = "p3", &two, `{"region":"us"}`, "carol"
	res, err := s.EnqueueChecked(ctx, third, func(_ context.Context, pin *int) error { return nil })
	if err != nil {
		t.Fatalf("compatible coalesce: %v", err)
	}
	if res.ID != "p1" || !res.Coalesced || res.PinnedVersion == nil || *res.PinnedVersion != 1 {
		t.Fatalf("result = %+v, want id p1, coalesced, pinned 1", res)
	}
	inputs, user, pin, _ = pendingRow(t, db, "p1")
	if inputs != `{"region":"us"}` || user != "carol" || pin == nil || *pin != 1 {
		t.Fatalf("accepted coalesce: inputs=%s user=%s pin=%v, want the new payload under carol on pin 1", inputs, user, pin)
	}
}

// TestPendingRuns_CoalesceFillsAnEmptyPinAndReportsIt — the legacy row case:
// an unpinned row adopts the first pin that meets it, the admit hook is
// asked about THAT pin, and the result says so.
func TestPendingRuns_CoalesceFillsAnEmptyPinAndReportsIt(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	two := 2
	base := PendingRun{WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", DebounceKey: "k", FireAt: time.Now().Add(30 * time.Second)}
	first := base
	first.ID = "p1"
	if _, err := s.EnqueueChecked(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	second := base
	second.ID, second.PinnedVersion = "p2", &two
	var judged *int
	res, err := s.EnqueueChecked(ctx, second, func(_ context.Context, pin *int) error { judged = pin; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if judged == nil || *judged != 2 || res.PinnedVersion == nil || *res.PinnedVersion != 2 {
		t.Fatalf("judged=%v result=%+v, want both to name pin 2", judged, res)
	}
	if _, _, pin, _ := pendingRow(t, db, "p1"); pin == nil || *pin != 2 {
		t.Fatalf("row pin = %v, want 2", pin)
	}
}

// TestPendingRuns_CoalesceMissesAClaimedRow — the dispatcher claimed the row
// between the lookup and the merge. The trigger must not be lost into a row
// that already fired; it gets its own.
func TestPendingRuns_CoalesceMissesAClaimedRow(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	base := PendingRun{WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", DebounceKey: "k", FireAt: time.Now().Add(30 * time.Second)}
	first := base
	first.ID = "p1"
	if _, err := s.EnqueueChecked(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	second := base
	second.ID = "p2"
	res, err := s.EnqueueChecked(ctx, second, func(ctx context.Context, _ *int) error {
		// The claim lands while the admission check is running.
		if ok, err := s.MarkFired(ctx, "p1", "run_x"); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue after claim: %v", err)
	}
	if res.Coalesced || res.ID != "p2" {
		t.Fatalf("result = %+v, want a fresh row p2, not a coalesce into the fired one", res)
	}
	if _, _, _, status := pendingRow(t, db, "p1"); status != "fired" {
		t.Fatalf("p1 status = %s, want fired (the merge must not touch a claimed row)", status)
	}
	if _, _, _, status := pendingRow(t, db, "p2"); status != "pending" {
		t.Fatalf("p2 status = %s, want pending", status)
	}
}

// TestPendingRuns_CoalesceMissesAPinThatMoved — two triggers meet an unpinned
// row; the one whose pin lost must be judged again against the winner's.
func TestPendingRuns_CoalesceMissesAPinThatMoved(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	one, two := 1, 2
	base := PendingRun{WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", DebounceKey: "k", FireAt: time.Now().Add(30 * time.Second)}
	first := base
	first.ID = "p1"
	if _, err := s.EnqueueChecked(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	second := base
	second.ID, second.PinnedVersion = "p2", &two
	var judged []int
	res, err := s.EnqueueChecked(ctx, second, func(ctx context.Context, pin *int) error {
		judged = append(judged, *pin)
		if len(judged) == 1 {
			// A rival fills the pin while we are judging ours.
			rival := base
			rival.ID, rival.PinnedVersion = "p9", &one
			if _, err := s.EnqueueChecked(ctx, rival, nil); err != nil {
				t.Fatalf("rival: %v", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(judged) != 2 || judged[0] != 2 || judged[1] != 1 {
		t.Fatalf("judged pins %v, want [2 1]: first our own, then the rival's after it won", judged)
	}
	if res.PinnedVersion == nil || *res.PinnedVersion != 1 {
		t.Fatalf("result pin = %v, want 1", res.PinnedVersion)
	}
}
