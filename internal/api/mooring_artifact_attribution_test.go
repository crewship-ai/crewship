package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Original credentials, task attachments and SQL are exercised here. Runtime
// identity and the saved row are synthetic; actual Rust/MCP acceptance is not
// claimed by this attribution-boundary fixture.
func attributionAgentFixture(t *testing.T) (*collaborationAgentTestFixture, *collaborationSubmissionRegistry, collaborationHumanStoreBinding, mooring.BatchChange, mooring.BatchChangeResult) {
	t.Helper()
	x := newCollaborationAgentTestFixture(t)
	// The ordinary API fixture defaults to NORMAL; actual session commits and
	// owner acquisition require FULL. Keep that real durability guard intact.
	mustExec(t, x.f.rig.db, `PRAGMA synchronous=FULL`)
	handle := x.attach(t, x.prepare(t))
	stamp, err := collaborationHumanLifecycleStamp(x.fence)
	if err != nil {
		t.Fatal(err)
	}
	h := &collaborationHumanRegistry{lifecycle: stamp, bindingID: "attribution-binding", sessionID: "attribution-session", runtime: &mooring.Runtime{}, pages: x.f.pages, fence: x.fence, clock: x.clock, scope: x.key}
	s, err := newCollaborationSubmissionRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := h.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	var change mooring.BatchChange
	r := x.request()
	err = s.run(r.Context(), binding, func(ctx context.Context) error {
		participant, err := s.admitAgent(ctx, r, x.store, handle)
		if err != nil {
			return err
		}
		change = submissionTestChange(t, x.key, "attributed-change", x.workID, participant.id)
		return s.sealAgent(ctx, r, participant, []mooring.BatchChange{change})
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := mooring.BatchChangeResult{ID: change.ID, Status: "saved", FirstRevision: "1", LastRevision: "1", Operations: []mooring.BatchOperationResult{{ID: change.Operations[0].ID, Status: "saved", Revision: "1", Issuer: "crewship", Durability: "node"}}}
	return x, s, binding, change, saved
}

func insertSyntheticAttributionSaved(ctx context.Context, tx *sql.Tx, key mooring.SessionKey, c mooring.BatchChange, saved mooring.BatchChangeResult) error {
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	var task any
	if c.TaskID != "" {
		task = c.TaskID
		if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_collab_tasks(page_id,workspace_id,task_id,participant_ref,allow_partial,status) VALUES(?,?,?,?,0,'running')`, key.ArtifactID, key.WorkspaceID, c.TaskID, c.Operations[0].Participant); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_changes(page_id,workspace_id,change_id,membership_hash,task_id,result_json) VALUES(?,?,?,?,?,?)`, key.ArtifactID, key.WorkspaceID, c.ID, c.MembershipHash, task, string(raw))
	return err
}

func TestMooringArtifactAttributionUsesOriginalTaskAndIsImmutable(t *testing.T) {
	x, s, binding, change, saved := attributionAgentFixture(t)
	err := s.run(t.Context(), binding, func(ctx context.Context) error {
		return s.withWriter(ctx, func(tx *sql.Tx) error {
			if err := insertSyntheticAttributionSaved(ctx, tx, x.key, change, saved); err != nil {
				return err
			}
			return s.recordSavedAttribution(ctx, tx, x.key, s.humans.hostSessionID(), change, saved)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var kind, human, agent, crew, chat, message, work, run, session, first, last string
	var attempt, generation int64
	err = x.f.rig.db.QueryRow(`SELECT actor_kind,human_user_id,agent_id,crew_id,chat_id,message_id,work_id,run_id,host_session_id,first_revision,last_revision,attempt_number,attempt_generation FROM mooring_collab_artifact_edits WHERE page_id=?`, x.key.ArtifactID).Scan(&kind, &human, &agent, &crew, &chat, &message, &work, &run, &session, &first, &last, &attempt, &generation)
	x.store.mu.Lock()
	var original collaborationAgentMandateBinding
	for _, entry := range x.store.attached {
		original = entry.binding
	}
	x.store.mu.Unlock()
	a := original.attempt
	if err != nil || kind != "agent" || human != a.humanUserID || agent != a.agentID || crew != a.crewID || chat != a.chatID || message != original.task.messageID || work != a.workID || run != a.runID || session != "attribution-session" || first != "1" || last != "1" || attempt != a.attempt || generation != a.generation {
		t.Fatal("provenance did not retain the exact original task", err, kind, human, agent, crew, chat, message, work, run, session, attempt, generation)
	}
	if _, err := x.f.rig.db.Exec(`UPDATE mooring_collab_artifact_edits SET chat_id='substituted-chat'`); err == nil {
		t.Fatal("historical attribution was rewritten")
	}
	var originalCreator sql.NullString
	if err := x.f.rig.db.QueryRow(`SELECT created_by_agent_id FROM pages WHERE id=?`, x.key.ArtifactID).Scan(&originalCreator); err != nil || originalCreator.Valid {
		t.Fatal("editing attributed original creation to this agent", err, originalCreator)
	}
	// Historical origin outlives the runtime principal, without becoming a way
	// to restore it or authorize another write.
	mustExec(t, x.f.rig.db, `DELETE FROM artifact_run_principals WHERE run_id=?`, a.runID)
	var retained int
	if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_artifact_edits WHERE run_id=?`, a.runID).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("principal retirement erased historical attribution", retained, err)
	}
}

func TestMooringArtifactAttributionSeparatesHumanAndAgentOriginals(t *testing.T) {
	x, s, _, agentChange, agentSaved := attributionAgentFixture(t)
	h := s.humans
	h.owners = mooring.NewOwnerStore(x.f.rig.db, true, x.clock, time.Minute)
	h.limit = 4
	h.admissions = make(map[string]collaborationHumanAdmission)
	h.tickets = make(map[string]collaborationHumanJoinTicket)
	owner, err := h.owners.Acquire(x.human, x.key, "attribution-owner")
	if err != nil {
		t.Fatal(err)
	}
	_, admission, err := h.issue(x.human, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := h.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	humanChange := submissionTestChange(t, x.key, "human-attributed-change", "", admission.participant)
	r := httptest.NewRequest("POST", "/", nil).WithContext(x.human)
	err = h.runHumanOperation(r, admission.participant, func(ctx context.Context) error {
		return s.run(ctx, binding, func(ctx context.Context) error {
			return s.sealHuman(ctx, []mooring.BatchChange{humanChange})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	humanSaved := mooring.BatchChangeResult{ID: humanChange.ID, Status: "saved", FirstRevision: "2", LastRevision: "2", Operations: []mooring.BatchOperationResult{{ID: humanChange.Operations[0].ID, Status: "saved", Revision: "2", Issuer: "crewship", Durability: "node"}}}
	err = s.run(t.Context(), binding, func(ctx context.Context) error {
		return s.withWriter(ctx, func(tx *sql.Tx) error {
			for _, pair := range []struct {
				change mooring.BatchChange
				saved  mooring.BatchChangeResult
			}{{agentChange, agentSaved}, {humanChange, humanSaved}} {
				if err := insertSyntheticAttributionSaved(ctx, tx, x.key, pair.change, pair.saved); err != nil {
					return err
				}
				if err := s.recordSavedAttribution(ctx, tx, x.key, h.hostSessionID(), pair.change, pair.saved); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_artifact_edits WHERE change_id=? AND actor_kind='human' AND human_user_id=? AND agent_id IS NULL AND crew_id IS NULL AND chat_id IS NULL AND message_id IS NULL AND work_id IS NULL AND run_id IS NULL AND attempt_number IS NULL AND attempt_generation IS NULL`, humanChange.ID, admission.source.UserID).Scan(&count)
	if err != nil || count != 1 {
		t.Fatal("human edit inherited agent/chat/task attribution", count, err)
	}
	if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_artifact_edits WHERE actor_kind='agent' AND change_id=?`, agentChange.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("mixed batch lost separate agent attribution", count, err)
	}
	// Two real sealed originals may not be recombined into a synthetic atomic
	// membership and attributed as if one author had submitted the whole thing.
	hybrid := agentChange
	hybrid.ID, hybrid.MembershipHash = "hybrid", "sha256:synthetic-hybrid"
	hybrid.Operations = []mooring.BatchOperation{agentChange.Operations[0], humanChange.Operations[0]}
	hybridSaved := agentSaved
	hybridSaved.ID, hybridSaved.LastRevision = hybrid.ID, "4"
	hybridSaved.FirstRevision = "3"
	hybridSaved.Operations = append(append([]mooring.BatchOperationResult{}, agentSaved.Operations...), humanSaved.Operations...)
	err = s.run(t.Context(), binding, func(ctx context.Context) error {
		return s.withWriter(ctx, func(tx *sql.Tx) error {
			raw, err := json.Marshal(hybridSaved)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_changes(page_id,workspace_id,change_id,membership_hash,task_id,result_json) VALUES(?,?,?,?,?,?)`, x.key.ArtifactID, x.key.WorkspaceID, hybrid.ID, hybrid.MembershipHash, hybrid.TaskID, string(raw)); err != nil {
				return err
			}
			return s.recordSavedAttribution(ctx, tx, x.key, h.hostSessionID(), hybrid, hybridSaved)
		})
	})
	if !errors.Is(err, errCollaborationSubmission) {
		t.Fatal("hybrid original membership was not refused", err)
	}
	if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_artifact_edits`).Scan(&count); err != nil || count != 2 {
		t.Fatal("refused hybrid changed historical attribution", count, err)
	}
}

func TestMooringArtifactAttributionRejectsMissingOrChangedOriginal(t *testing.T) {
	for _, mode := range []string{"no_saved_row", "changed_operation", "missing_seal", "revoked_grant", "reader_grant", "missing_attachment", "wrong_session", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			x, s, binding, change, saved := attributionAgentFixture(t)
			session := s.humans.hostSessionID()
			switch mode {
			case "missing_seal":
				s.mu.Lock()
				clear(s.entries)
				s.mu.Unlock()
			case "revoked_grant":
				mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id=?`, x.key.ArtifactID)
			case "reader_grant":
				mustExec(t, x.f.rig.db, `UPDATE page_grants SET level='read' WHERE page_id=?`, x.key.ArtifactID)
			case "missing_attachment":
				x.store.mu.Lock()
				clear(x.store.attached)
				x.store.mu.Unlock()
			case "wrong_session":
				session = "another-session"
			case "rejected":
				saved.Status = "rejected"
			}
			err := s.run(t.Context(), binding, func(ctx context.Context) error {
				return s.withWriter(ctx, func(tx *sql.Tx) error {
					if mode != "no_saved_row" {
						if err := insertSyntheticAttributionSaved(ctx, tx, x.key, change, saved); err != nil {
							return err
						}
					}
					if mode == "changed_operation" {
						change.Operations[0].Payload = []byte(`{"substituted":true}`)
					}
					return s.recordSavedAttribution(ctx, tx, x.key, session, change, saved)
				})
			})
			if !errors.Is(err, errCollaborationSubmission) && !errors.Is(err, errCollaborationAgentAccess) {
				t.Fatal("expected original-proof denial, not another setup/storage failure", mode, err)
			}
			var n int
			if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_artifact_edits`).Scan(&n); err != nil || n != 0 {
				t.Fatal("denied attribution retained a record", n, err)
			}
		})
	}
}
