package access

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Attempt contains durable server authority, not prompt metadata. Handle is
// returned only on admission; only its digest is persisted. Retries must Admit
// again. Scope separates two clients of the same agent and changes on revocation.
type Attempt struct {
	WorkflowContinuation                         bool
	ChatGeneration                               string
	AdmissionOperation                           string
	ContextAudience                              string
	ChatRevision                                 int64
	ID, Workspace, Principal, Agent, Chat, Scope string
	Member                                       string
	Revision, Generation                         int64
	Parent                                       string
	Rights                                       []Right
	Expires                                      time.Time
}

func digest(value string) string { b := sha256.Sum256([]byte(value)); return hex.EncodeToString(b[:]) }

// chatRead deliberately uses current DB state within the admission transaction.
// The creator/group rule matches chataudience, with no operator bypass for
// restricted work. A grant to an agent does not grant another human's chat.
func chatRead(ctx context.Context, q queryer, user, workspace, chat string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM chats c
        WHERE c.id=? AND c.workspace_id=? AND c.visibility IN ('private','group')
        AND (c.created_by=? OR (c.visibility='group' AND EXISTS (
            SELECT 1 FROM chat_participants p WHERE p.chat_id=c.id AND p.user_id=?)))`, chat, workspace, user, user).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	return err
}

func subset(child, parent []Right) bool {
	for _, r := range child {
		if !slices.Contains(parent, r) {
			return false
		}
	}
	return true
}

// Admit must be called before prompt construction or memory access. parent is
// an opaque server handle obtained from the dispatch context, never task input.
func (s Store) Admit(ctx context.Context, user, workspace, agent, chat, parent string, rights []Right) (string, Attempt, error) {
	return s.admit(ctx, user, workspace, agent, chat, parent, rights, "run")
}

// AdmitChat admits only conversational execution. The operation is selected by
// this trusted method, never by a field in client task JSON.
func (s Store) AdmitChat(ctx context.Context, user, workspace, agent, chat, parent string, rights []Right) (string, Attempt, error) {
	return s.admit(ctx, user, workspace, agent, chat, parent, rights, "chat")
}

func (s Store) admit(ctx context.Context, user, workspace, agent, chat, parent string, rights []Right, operation string) (string, Attempt, error) {
	return s.admitContinuation(ctx, user, workspace, agent, chat, parent, rights, operation, false)
}

// AdmitWorkflowContinuation is a host-only return to a workflow's original
// agent. Its provider slot and active orchestration parent must already be
// frozen by the workflow service. It does not grant a new delegated target.
func (s Store) AdmitWorkflowContinuation(ctx context.Context, user, workspace, agent, chat, parent string, rights []Right) (string, Attempt, error) {
	if parent == "" {
		return "", Attempt{}, ErrDenied
	}
	return s.admitContinuation(ctx, user, workspace, agent, chat, parent, rights, "run", true)
}

func (s Store) admitContinuation(ctx context.Context, user, workspace, agent, chat, parent string, rights []Right, operation string, continuation bool) (string, Attempt, error) {
	var a Attempt
	if s.DB == nil {
		return "", a, ErrDenied
	}
	if len(rights) > 256 {
		return "", a, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", a, err
	}
	defer tx.Rollback()
	m, err := check(ctx, tx, user, workspace, Right{"agent", agent, operation})
	if err != nil {
		return "", a, err
	}
	if m.Mode != "restricted" {
		return "", a, ErrDenied
	}
	if err = chatRead(ctx, tx, user, workspace, chat); err != nil {
		return "", a, err
	}
	rights = append([]Right{{"agent", agent, operation}}, rights...)
	unique := make([]Right, 0, len(rights))
	for _, r := range rights {
		if _, err = check(ctx, tx, user, workspace, r); err != nil {
			return "", a, err
		}
		if !slices.Contains(unique, r) {
			unique = append(unique, r)
		}
	}
	parentID := ""
	if parent != "" {
		p, e := resolve(ctx, tx, digest(parent), true, map[string]bool{"admission": true})
		if e != nil {
			return "", a, e
		}
		if p.Principal != user || p.Workspace != workspace || p.Chat != chat || !subset(unique, p.Rights) || (!slices.Contains(p.Rights, Right{"agent", agent, "delegate"}) && !continuation) {
			return "", a, ErrDenied
		}
		if continuation && (operation != "run" || workflowContinuationSlot(ctx, tx, p, agent) != nil) {
			return "", a, ErrDenied
		}
		parentID = p.ID
	} else {
		var chatAgent string
		if err = tx.QueryRowContext(ctx, `SELECT agent_id FROM chats WHERE id=?`, chat).Scan(&chatAgent); err != nil {
			return "", a, err
		}
		if chatAgent != agent {
			return "", a, ErrDenied
		}
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0)+1 FROM access_attempts WHERE chat_id=?`, chat).Scan(&generation); err != nil {
		return "", a, err
	}
	handle := randomID()
	a = Attempt{WorkflowContinuation: continuation, AdmissionOperation: operation, ID: randomID(), Workspace: workspace, Principal: user, Agent: agent, Chat: chat, Member: m.ID, Revision: m.Revision, Generation: generation, Parent: parentID, Rights: unique}
	if err = tx.QueryRowContext(ctx, `SELECT authority_generation,authority_revision FROM chats WHERE id=?`, chat).Scan(&a.ChatGeneration, &a.ChatRevision); err != nil {
		return "", Attempt{}, err
	}
	if a.ChatGeneration == "" || a.ChatRevision < 1 {
		return "", Attempt{}, ErrDenied
	}
	a.ContextAudience, err = contextAudience(ctx, tx, a)
	if err != nil {
		return "", Attempt{}, err
	}
	data, err := json.Marshal(unique)
	if err != nil {
		return "", Attempt{}, err
	}
	var parentValue any
	if parentID != "" {
		parentValue = parentID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO access_attempts(id,handle_hash,member_id,member_revision,workspace_id,principal_id,agent_id,chat_id,parent_id,generation,rights,created_at,chat_generation,chat_revision,admission_operation,context_audience,workflow_continuation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, digest(handle), m.ID, m.Revision, workspace, user, agent, chat, parentValue, generation, string(data), tsformat.Format(time.Now()), a.ChatGeneration, a.ChatRevision, operation, a.ContextAudience, continuation)
	if err != nil {
		return "", Attempt{}, err
	}
	if err = tx.Commit(); err != nil {
		return "", Attempt{}, err
	}
	a.Scope = scope(a)
	return handle, a, nil
}

func scope(a Attempt) string {
	if a.ContextAudience != "" {
		b, _ := json.Marshal([]any{"group", a.Workspace, a.Agent, a.Chat, a.ChatGeneration, a.ChatRevision, a.ContextAudience})
		return digest(string(b))
	}
	b, _ := json.Marshal([]any{a.Workspace, a.Principal, a.Chat, a.Member, a.Revision, a.ChatGeneration, a.ChatRevision})
	return digest(string(b))
}

func resolve(ctx context.Context, q queryer, key string, byHandle bool, seen map[string]bool) (Attempt, error) {
	return resolveState(ctx, q, key, byHandle, seen, false)
}

// resolveState admits completed origins only for historical provenance reads.
func resolveState(ctx context.Context, q queryer, key string, byHandle bool, seen map[string]bool, history bool) (Attempt, error) {
	var a Attempt
	if key == "" || seen[key] || len(seen) >= 8 {
		return a, ErrDenied
	}
	seen[key] = true
	column := "id"
	if byHandle {
		column = "handle_hash"
	}
	var raw string
	completion := " AND a.completed_at IS NULL"
	if history {
		completion = ""
	}
	err := q.QueryRowContext(ctx, `SELECT a.id,a.member_id,a.member_revision,a.workspace_id,a.principal_id,a.agent_id,a.chat_id,COALESCE(a.parent_id,''),a.generation,a.rights,a.chat_generation,a.chat_revision,a.admission_operation,a.context_audience,a.workflow_continuation
 FROM access_attempts a JOIN chats c ON c.id=a.chat_id AND c.authority_generation=a.chat_generation AND c.authority_revision=a.chat_revision
 WHERE a.`+column+`=? AND a.revoked_at IS NULL`+completion, key).
		Scan(&a.ID, &a.Member, &a.Revision, &a.Workspace, &a.Principal, &a.Agent, &a.Chat, &a.Parent, &a.Generation, &raw, &a.ChatGeneration, &a.ChatRevision, &a.AdmissionOperation, &a.ContextAudience, &a.WorkflowContinuation)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrDenied
	}
	if err != nil {
		return a, err
	}
	// Workflow execution roots carry the shared dispatch fence. Every child
	// resolves this ancestor; neither credentials nor continuation can outlive it.
	var bound, valid bool
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM restricted_workflow_attempt_roots WHERE access_attempt_id=?),
 EXISTS(SELECT 1 FROM restricted_workflow_attempt_roots r JOIN work_attempts wa ON wa.run_id=r.run_id AND wa.generation=r.generation
 JOIN work_items w ON w.id=wa.work_id AND w.id=r.workflow_id AND w.generation=r.generation
 WHERE r.access_attempt_id=? AND ((w.state IN ('starting','running') AND wa.ended_at IS NULL AND wa.lease_expires_at>?)
 OR (? AND w.state='succeeded')))`, a.ID, a.ID, tsformat.Format(time.Now()), history).Scan(&bound, &valid); err != nil {
		return Attempt{}, err
	}
	if bound && !valid {
		return Attempt{}, ErrDenied
	}
	m, err := member(ctx, q, a.Principal, a.Workspace)
	if err != nil {
		return Attempt{}, err
	}
	if m.ID != a.Member || m.Revision != a.Revision || m.Mode != "restricted" {
		return Attempt{}, ErrDenied
	}
	if err = chatRead(ctx, q, a.Principal, a.Workspace, a.Chat); err != nil {
		return Attempt{}, err
	}
	if err = json.Unmarshal([]byte(raw), &a.Rights); err != nil {
		return Attempt{}, ErrDenied
	}
	if len(a.Rights) == 0 || len(a.Rights) > 257 || (a.AdmissionOperation != "chat" && a.AdmissionOperation != "run") || !slices.Contains(a.Rights, Right{"agent", a.Agent, a.AdmissionOperation}) {
		return Attempt{}, ErrDenied
	}
	for _, r := range a.Rights {
		if _, err = check(ctx, q, a.Principal, a.Workspace, r); err != nil {
			return Attempt{}, err
		}
	}
	if a.Parent != "" {
		p, e := resolveState(ctx, q, a.Parent, false, seen, history)
		if e != nil {
			return Attempt{}, e
		}
		if p.Principal != a.Principal || p.Workspace != a.Workspace || p.Chat != a.Chat || !subset(a.Rights, p.Rights) || (!slices.Contains(p.Rights, Right{"agent", a.Agent, "delegate"}) && !a.WorkflowContinuation) {
			return Attempt{}, ErrDenied
		}
		if a.WorkflowContinuation && (a.AdmissionOperation != "run" || workflowContinuationSlot(ctx, q, p, a.Agent) != nil) {
			return Attempt{}, ErrDenied
		}
	} else if a.WorkflowContinuation {
		return Attempt{}, ErrDenied
	}

	audience, err := contextAudience(ctx, q, a)
	if err != nil || audience != a.ContextAudience {
		return Attempt{}, ErrDenied
	}
	a.Scope = scope(a)
	if err = checkProjectInputs(ctx, q, a); err != nil {
		return Attempt{}, err
	}
	return a, nil
}

// Resolve rechecks membership, grants, chat audience and every ancestor in one
// database snapshot. A slow check cannot issue a fresh lease from stale state.
func (s Store) Resolve(ctx context.Context, handle string) (Attempt, error) {
	if s.DB == nil || handle == "" {
		return Attempt{}, ErrDenied
	}
	deadline := time.Now().Add(15 * time.Second)
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Attempt{}, err
	}
	defer tx.Rollback()
	a, err := resolve(ctx, tx, digest(handle), true, map[string]bool{})
	if err != nil {
		return Attempt{}, err
	}
	if err = tx.Commit(); err != nil {
		return Attempt{}, err
	}
	if !time.Now().Before(deadline) {
		return Attempt{}, ErrDenied
	}
	a.Expires = deadline
	return a, nil
}

func (s Store) RevokeAttempt(ctx context.Context, handle string) error {
	if s.DB == nil || handle == "" {
		return ErrDenied
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE access_attempts SET revoked_at=? WHERE handle_hash=? AND revoked_at IS NULL`, tsformat.Format(time.Now()), digest(handle))
	return err
}

// CompleteAttempt closes execution authority permanently while preserving
// classified history under its still-current membership/resource provenance.
func (s Store) CompleteAttempt(ctx context.Context, handle string) error {
	if s.DB == nil || handle == "" {
		return ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := resolve(ctx, tx, digest(handle), true, map[string]bool{})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE access_attempts SET completed_at=? WHERE id=? AND completed_at IS NULL AND revoked_at IS NULL`, tsformat.Format(time.Now()), a.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
