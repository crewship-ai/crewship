package api

import (
	"context"
	"sync"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func TestCollaborationAgentOriginalActionConsumesExistingDelegationWithoutSharing(t *testing.T) {
	for _, level := range []string{"none", "read", "write"} {
		t.Run(level, func(t *testing.T) {
			x := newCollaborationAgentTestFixture(t)
			// The actor is an editor, not the owner or a sharing administrator.
			mustExec(t, x.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, x.f.rig.adminID, x.key.ArtifactID)
			mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id=?`, x.key.ArtifactID)
			mustExec(t, x.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, x.key.ArtifactID, x.f.a.HumanUserID, x.f.rig.adminID)
			if level != "none" {
				mustExec(t, x.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,?,?)`, x.key.ArtifactID, x.f.a.AgentID, level, x.f.rig.adminID)
			}
			pageCtx := context.WithValue(x.human, pageAuthReaderKey{}, x.f.rig.db)
			rec, err := x.f.pages.loadPageForRead(pageCtx, x.key.WorkspaceID, "@"+x.key.ArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			if x.f.pages.mayAdministerGrants(pageCtx, x.key.WorkspaceID, x.f.a.HumanUserID, "MEMBER", rec) {
				t.Fatal("fixture editor unexpectedly may share")
			}
			var before, after int
			if err = x.f.rig.db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id=?`, x.key.ArtifactID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			d, err := newCollaborationAgentTaskDispatcher(x.store)
			if err != nil {
				t.Fatal(err)
			}
			_, err = d.Produce(x.human, x.key, "task", x.f.a.MessageID, x.f.receipt)
			if (err == nil) != (level == "write") {
				t.Fatalf("task delegation level %s accepted=%v", level, err == nil)
			}
			if err = x.f.rig.db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id=?`, x.key.ArtifactID).Scan(&after); err != nil || after != before {
				t.Fatal("task action created or removed a grant", err)
			}
			if level != "none" {
				var retained string
				if err = x.f.rig.db.QueryRow(`SELECT level FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id=?`, x.key.ArtifactID, x.f.a.AgentID).Scan(&retained); err != nil || retained != level {
					t.Fatal("task action broadened agent delegation", err)
				}
			}
		})
	}
}

func TestCollaborationAgentOriginalMessageActionCannotCreateDuplicateWork(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	d, err := newCollaborationAgentTaskDispatcher(x.store)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.Produce(x.human, x.key, "task", x.f.a.MessageID, x.f.receipt)
			results <- err == nil
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for ok := range results {
		if ok {
			accepted++
		}
	}
	var rows int
	if err = x.f.rig.db.QueryRow(`SELECT count(*) FROM work_items WHERE domain_kind='mooring_wordcraft_task'`).Scan(&rows); err != nil || rows != 1 || accepted != 1 {
		t.Fatalf("original action duplicated: accepted=%d rows=%d error=%v", accepted, rows, err)
	}
	// SQL is denial evidence only. A new controller may not issue another task
	// from the same original message after losing the in-memory consent handle.
	restarted, err := newCollaborationAgentTaskDispatcher(x.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Produce(x.human, x.key, "task", x.f.a.MessageID, x.f.receipt); err == nil {
		t.Fatal("SQL row reconstructed a fresh launch")
	}
}

func TestCollaborationAgentUnsentOriginalRetainsExactRetryAndBudget(t *testing.T) {
	a := &collaborationLiveAgent{ref: "original-agent"}
	source := &collaborationOriginalSource{agent: a}
	group := &collaborationOriginalGroup{source: source, requestID: "original-public", outerID: "original-host-outer", captured: true, packet: []byte(`{"original":"unchanged"}`)}
	l := &collaborationLiveOriginals{groups: map[string]*collaborationOriginalGroup{a.ref + "\x00original-public": group}, bytes: 123}
	outer, first, duplicate, err := l.sendPublicGroup(group)
	if err != nil || duplicate {
		t.Fatal("first original send unavailable", err)
	}
	// Coupled transport tests establish that canceled queue acquisition emits
	// no bytes and preserves the actual child. This is the caller's definite
	// unsent branch, not a retry after an uncertain physical write.
	if err = l.retryUnsentAgentGroup(group, a); err != nil {
		t.Fatal(err)
	}
	retryOuter, retry, repeated, err := l.sendPublicGroup(group)
	if err != nil || repeated || retryOuter != outer || string(retry) != string(first) || l.bytes != 123 || l.groups[a.ref+"\x00original-public"] != group {
		t.Fatal("definite unsent lost original identity, bytes or charge", err)
	}
	retry[0] = 'x'
	if string(group.packet) != string(first) {
		t.Fatal("retry mutated retained original")
	}
	if err = l.retryUnsentAgentGroup(group, &collaborationLiveAgent{ref: a.ref}); err == nil {
		t.Fatal("foreign agent rewound original send")
	}
	group.receipt = &protocol.CommitResult{}
	if err = l.retryUnsentAgentGroup(group, a); err == nil {
		t.Fatal("terminal original rewound into fresh dispatch")
	}
	if l.bytes != 123 {
		t.Fatal("retry released original budget")
	}
}
