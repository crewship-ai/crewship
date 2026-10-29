package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestCollaborationAgentDispatchPublishesCommittedOriginalOnly(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	d, err := newCollaborationAgentTaskDispatcher(x.store)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := d.Produce(x.human, x.key, "task", x.f.a.MessageID, x.f.receipt)
	if err != nil {
		t.Fatal(err)
	}
	item, err := work.NewStore(x.f.rig.db).Get(t.Context(), accepted.WorkID)
	if err != nil || item.State != work.StateQueued {
		t.Fatalf("uncommitted acceptance: %+v %v", item, err)
	}
	claimed, err := work.NewStore(x.f.rig.db).Claim(t.Context(), work.ClaimOptions{LeaseOwner: "host-fixture", WorkID: item.ID, Limits: work.DefaultLimits(), Kinds: []work.Kind{{Source: work.SourceChat, DomainKind: "mooring_wordcraft_task"}}})
	if err != nil {
		t.Fatal(err)
	}
	a := dispatch.Assignment{Item: claimed.Item, RunID: claimed.RunID, Attempt: claimed.Attempt, Generation: claimed.Generation}
	if _, err = d.Authorize(t.Context(), a); err != nil {
		t.Fatal("actual committed assignment", err)
	}
	for _, field := range []string{"agent", "workspace", "scope"} {
		copyItem := *a.Item
		forged := a
		forged.Item = &copyItem
		switch field {
		case "agent":
			copyItem.AgentID = "other-agent"
		case "workspace":
			copyItem.WorkspaceID = "other-workspace"
		case "scope":
			copyItem.AuthorizedScope = "pages:read"
		}
		if _, err = d.Authorize(t.Context(), forged); err == nil {
			t.Fatalf("accepted drifted assignment %s", field)
		}
	}
	if _, err = d.Authorize(t.Context(), a); err != nil {
		t.Fatal("exact retry", err)
	}
	restarted, err := newCollaborationAgentTaskDispatcher(x.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Authorize(t.Context(), a); err == nil {
		t.Fatal("fresh controller recovered consent from durable work")
	}
	a.RunID = "replacement-run"
	if _, err = d.Authorize(t.Context(), a); err == nil {
		t.Fatal("replacement attempt refreshed original pending consent")
	}
}

func TestCollaborationAgentDispatchNoPublicationOnSQLFailure(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	d, err := newCollaborationAgentTaskDispatcher(x.store)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, x.f.rig.db, `CREATE TRIGGER fail_mooring_accept BEFORE INSERT ON work_items BEGIN SELECT RAISE(ABORT,'synthetic accept failure'); END`)
	if _, err = d.Produce(x.human, x.key, "task", x.f.a.MessageID, x.f.receipt); err == nil {
		t.Fatal("accepted failed writer")
	}
	if len(d.entries) != 0 {
		t.Fatal("published before COMMIT")
	}
	var rows int
	if err = x.f.rig.db.QueryRow(`SELECT count(*) FROM work_items WHERE domain_kind='mooring_wordcraft_task'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("work retained: %d %v", rows, err)
	}
}

func TestCollaborationAgentDispatchOriginalSourceAndIngressNeverRefresh(t *testing.T) {
	for _, change := range []string{"user-counter", "restore", "input-drift"} {
		t.Run(change, func(t *testing.T) {
			x := newCollaborationAgentTestFixture(t)
			d, err := newCollaborationAgentTaskDispatcher(x.store)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := d.Produce(x.human, x.key, "task", x.f.a.MessageID, x.f.receipt)
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := work.NewStore(x.f.rig.db).Claim(t.Context(), work.ClaimOptions{LeaseOwner: "host-fixture", WorkID: accepted.WorkID, Limits: work.DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "user-counter":
				mustExec(t, x.f.rig.db, `UPDATE users SET authority_revision=authority_revision+1 WHERE id=?`, x.f.a.HumanUserID)
			case "restore":
				x.fence.SuspendBeforeRestore()
				if err = x.fence.ResumeAfterRestore(); err != nil {
					t.Fatal(err)
				}
			case "input-drift":
				mustExec(t, x.f.rig.db, `UPDATE work_items SET input_json='{}' WHERE id=?`, accepted.WorkID)
			}
			a := dispatch.Assignment{Item: claimed.Item, RunID: claimed.RunID, Attempt: claimed.Attempt, Generation: claimed.Generation}
			if _, err = d.Authorize(t.Context(), a); err == nil {
				t.Fatal("changed original admitted")
			}
		})
	}
}
