package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// recordSavedAttribution receives a fresh, already inserted atomic outcome in
// the commit writer. It reads ONLY the original sealed host author, never model
// output, a client-supplied identity or a restored SQL principal. The normal
// final current-author and owner checks still run after this metadata insert.
func (s *collaborationSubmissionRegistry) recordSavedAttribution(ctx context.Context, tx *sql.Tx, key mooring.SessionKey, session string, change mooring.BatchChange, saved mooring.BatchChangeResult) error {
	if _, err := s.active(ctx); err != nil {
		return err
	}
	if tx == nil || key != s.humans.scope || session == "" || session != s.humans.hostSessionID() || saved.Status != "saved" || saved.ID != change.ID || len(change.Operations) == 0 || len(saved.Operations) != len(change.Operations) {
		return errCollaborationSubmission
	}
	for i, op := range saved.Operations {
		if op.ID != change.Operations[i].ID || op.Status != "saved" {
			return errCollaborationSubmission
		}
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	var task any
	if change.TaskID != "" {
		task = change.TaskID
	}
	var present bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_collab_changes WHERE page_id=? AND workspace_id=? AND change_id=? AND membership_hash=? AND task_id IS ? AND result_json=?)`, key.ArtifactID, key.WorkspaceID, change.ID, change.MembershipHash, task, string(raw)).Scan(&present); err != nil {
		return err
	}
	if !present {
		return errCollaborationSubmission
	}
	s.mu.Lock()
	var author collaborationSubmissionAuthor
	valid := true
	for i, op := range change.Operations {
		entry, ok := s.entries[op.ID]
		if !ok || entry.change != change.ID || entry.membership != change.MembershipHash || entry.task != change.TaskID || !reflect.DeepEqual(entry.operation, op) {
			valid = false
			break
		}
		if i == 0 {
			author = entry.author
		} else if !reflect.DeepEqual(author, entry.author) {
			valid = false
			break
		}
	}
	s.mu.Unlock()
	if !valid || (author.human == nil) == (author.agent == nil) {
		return errCollaborationSubmission
	}
	kind, human := "human", ""
	var agent, crew, chat, message, work, run, attempt, generation any
	if author.human != nil {
		if change.TaskID != "" || !author.human.source.Valid() || !author.human.authorVector.matches(key, author.human.source.UserID, "", "") {
			return errCollaborationSubmission
		}
		human = author.human.source.UserID
	} else {
		captured := *author.agent
		if captured.store == nil || change.TaskID != captured.workID {
			return errCollaborationSubmission
		}
		decision, err := captured.store.DecideCaptured(ctx, tx, captured, key, "task")
		if err != nil {
			return err
		}
		if decision.kind != collaborationAgentAllowed {
			return errCollaborationSubmission
		}
		captured.store.mu.Lock()
		entry, found := captured.store.attached[captured.mandateRef]
		captured.store.mu.Unlock()
		if !found || entry.binding.credentialSHA256 != captured.credentialBinding || entry.binding.task.key != key || entry.binding.attempt.runID != captured.runID || entry.binding.attempt.workID != captured.workID {
			return errCollaborationSubmission
		}
		a, original := entry.binding.attempt, entry.binding.task
		if original.originalHuman.ChatID != a.chatID || original.originalHuman.UserID != a.humanUserID || original.originalCredential.UserID != a.humanUserID {
			return errCollaborationSubmission
		}
		kind, human = "agent", a.humanUserID
		agent, crew, chat, message, work, run = a.agentID, a.crewID, a.chatID, original.messageID, a.workID, a.runID
		attempt, generation = a.attempt, a.generation
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_artifact_edits(page_id,workspace_id,application_id,change_id,membership_hash,first_revision,last_revision,host_session_id,actor_kind,human_user_id,agent_id,crew_id,chat_id,message_id,work_id,run_id,attempt_number,attempt_generation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, key.ArtifactID, key.WorkspaceID, key.ApplicationID, change.ID, change.MembershipHash, saved.FirstRevision, saved.LastRevision, session, kind, human, agent, crew, chat, message, work, run, attempt, generation)
	return err
}
