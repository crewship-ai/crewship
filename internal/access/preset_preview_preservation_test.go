package access

import (
	"reflect"
	"testing"
)

func TestPresetPreviewPreservesHistoricalUnavailableOrthogonalGrant(t *testing.T) {
	s := fixture(t)
	retained := Right{Kind: "agent", ID: "a", Operation: "discover"}
	policy(t, s, "h1", retained)
	// Ordinary deletion correctly removes old shares. A historical restore can
	// still contain FK-valid rows whose referenced resource is unavailable.
	for _, q := range []string{`CREATE TABLE historical_preset_grant AS SELECT * FROM access_grants WHERE agent_id='a'`, `UPDATE agents SET deleted_at='2026-10-17T00:00:00Z' WHERE id='a'`, `INSERT INTO access_grants SELECT * FROM historical_preset_grant`, `DROP TABLE historical_preset_grant`} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(t.Context(), "h1", "w", retained); err == nil {
		t.Fatal("unavailable historical resource conferred authority")
	}
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	plan, err := s.PlanPresetIn(t.Context(), tx, "owner", "h1", "w", PresetRequest{MembershipID: before.ID, Revision: before.Revision, Resource: PresetResource{Kind: "project", ID: "p1"}, PresetID: "project-levels", Version: 1, Level: "view"})
	if err != nil {
		t.Fatalf("orthogonal unavailable resource blocked selected live resource:%v", err)
	}
	if len(plan.OrthogonalRights) != 1 || plan.OrthogonalRights[0].Right != retained || !reflect.DeepEqual(plan.OrthogonalRights, before.GrantDetails) {
		t.Fatalf("retained provenance lost:%+v", plan.OrthogonalRights)
	}
	found := false
	for _, r := range plan.AfterRights {
		if r == retained {
			found = true
		}
	}
	if !found || len(plan.AfterRights) != 3 {
		t.Fatalf("canonical orthogonal right lost:%+v", plan.AfterRights)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only preview changed stored policy:%+v ->%+v", before, after)
	}
	// This internal planner does not weaken ordinary complete-document writes.
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", before.Membership, plan.AfterRights); err == nil {
		t.Fatal("manual replacement silently accepted unavailable resource")
	}
}
