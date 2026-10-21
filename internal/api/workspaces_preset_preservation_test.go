package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestPresetApplyPreservesUnavailableOrthogonalGrant(t *testing.T) {
	for _, failure := range []string{"", "audit", "selection"} {
		t.Run(failure, func(t *testing.T) {
			f := newPresetApplyFixture(t)
			seedCrewRow(t, f.db, "preserved-crew", f.workspace, "Preserved", "preserved-crew")
			execOrFatal(t, f.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('preserved-agent',?,'preserved-crew','Preserved','preserved-agent')`, f.workspace)
			m, err := f.store.Membership(t.Context(), "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.store.Replace(t.Context(), f.owner, "apply-subject", f.workspace, "restricted", m, []access.Right{{Kind: "agent", ID: "preserved-agent", Operation: "discover"}})
			if err != nil {
				t.Fatal(err)
			}
			// Historical restore can contain canonical FK-valid grants whose resource
			// is unavailable. Normal soft deletion itself correctly removes grants.
			execOrFatal(t, f.db, `CREATE TABLE restored_grant AS SELECT * FROM access_grants WHERE agent_id='preserved-agent';
   UPDATE agents SET deleted_at='2026-10-17T00:00:00Z' WHERE id='preserved-agent';
   INSERT INTO access_grants SELECT * FROM restored_grant;
   DROP TABLE restored_grant;`)
			m, err = f.store.Membership(t.Context(), "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			f.request.Revision = m.Revision
			snapshot := func() string {
				t.Helper()
				var row string
				err := f.db.QueryRow(`SELECT json_object('id',id,'member',member_id,'kind',resource_kind,'agent',agent_id,'operation',operation,'issuer',created_by,'created',created_at) FROM access_grants WHERE member_id=? AND agent_id='preserved-agent'`, m.ID).Scan(&row)
				if err != nil {
					t.Fatal(err)
				}
				return row
			}
			before := snapshot()
			if err := f.store.Check(t.Context(), "apply-subject", f.workspace, access.Right{Kind: "agent", ID: "preserved-agent", Operation: "discover"}); err == nil {
				t.Fatal("unavailable historical resource granted authority")
			}
			p := f.preview(t)
			if len(p.OrthogonalRights) != 1 || p.OrthogonalRights[0].ID != "preserved-agent" {
				t.Fatalf("missing preserved unavailable-resource grant:%+v", p.OrthogonalRights)
			}
			// The ordinary complete-document contract is intentionally unchanged.
			if _, err = f.store.Replace(t.Context(), f.owner, "apply-subject", f.workspace, "restricted", m, p.AfterRights); err == nil {
				t.Fatal("manual PUT unexpectedly accepted unavailable resource")
			}
			if failure == "audit" {
				execOrFatal(t, f.db, `CREATE TRIGGER reject_preserved_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.access_changed' BEGIN SELECT RAISE(ABORT,'synthetic'); END`)
			}
			if failure == "selection" {
				execOrFatal(t, f.db, `CREATE TRIGGER reject_preserved_selection BEFORE INSERT ON access_preset_selections BEGIN SELECT RAISE(ABORT,'synthetic'); END`)
			}
			w := f.apply(t, applyBody(t, p))
			want := 200
			if failure != "" {
				want = 500
			}
			if w.Code != want {
				t.Fatalf("apply=%d want%d %s", w.Code, want, w.Body.String())
			}
			if after := snapshot(); after != before {
				t.Fatalf("unrelated canonical row changed:%s ->%s", before, after)
			}
			policy, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			if failure != "" {
				if policy.Revision != m.Revision || len(policy.Rights) != 1 || len(*policy.PresetSelections) != 0 {
					t.Fatalf("failed apply retained partial changes:%+v", policy)
				}
			} else {
				if policy.Revision <= m.Revision || len(policy.Rights) != 3 || len(*policy.PresetSelections) != 1 {
					t.Fatalf("apply did not persist managed selection:%+v", policy)
				}
			}
		})
	}
}
