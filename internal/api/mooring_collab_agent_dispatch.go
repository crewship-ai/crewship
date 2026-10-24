package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/work"
)

// This controller is deliberately not installed in the router or generic
// dispatcher. SQL work is durable; original human consent is not recoverable.
// A restarted controller cannot launch rows left by an earlier process.
type collaborationAgentTaskDispatcher struct {
	mu       sync.Mutex // COMMIT -> publication and dispatch lookup share this lock.
	mandates *collaborationAgentMandateStore
	entries  map[string]*collaborationAgentDispatchEntry
}

type collaborationAgentDispatchEntry struct {
	pending    *collaborationAgentPending
	task       collaborationAgentTaskBinding
	runID      string
	attempt    int
	generation int64
}

func newCollaborationAgentTaskDispatcher(s *collaborationAgentMandateStore) (*collaborationAgentTaskDispatcher, error) {
	if s == nil || !s.enabled || s.fence == nil {
		return nil, errCollaborationAgentAccess
	}
	return &collaborationAgentTaskDispatcher{mandates: s, entries: make(map[string]*collaborationAgentDispatchEntry)}, nil
}

// Produce holds the original ingress lease before acquiring SQL's writer, and
// publishes only after COMMIT. An authorizer that sees the committed row waits
// on mu until publication. No accepted response or dispatcher hint precedes it.
func (d *collaborationAgentTaskDispatcher) Produce(ctx context.Context, key mooring.SessionKey, intent, message string, original *chataudience.Receipt) (work.Receipt, error) {
	var result work.Receipt
	if d == nil || d.mandates == nil {
		return result, errCollaborationAgentAccess
	}
	s := d.mandates
	stamp, err := s.fence.capture(ctx)
	if err != nil {
		return result, err
	}
	release, err := s.fence.acquire(ctx, stamp)
	if err != nil {
		return result, err
	}
	defer release()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneLocked()
	if len(d.entries) >= s.capacity {
		return result, errCollaborationAgentAccess
	}
	tx, err := d.writer(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	pending, accepted, err := s.StartTask(ctx, tx, key, intent, message, original)
	if err != nil {
		return result, err
	}
	s.mu.Lock()
	entry, exists := s.pending[pending.id]
	s.mu.Unlock()
	if !exists {
		return result, errCollaborationAgentAccess
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	d.entries[accepted.WorkID] = &collaborationAgentDispatchEntry{pending: pending, task: entry.task}
	return accepted, nil
}

func (d *collaborationAgentTaskDispatcher) writer(ctx context.Context) (*sql.Tx, error) {
	tx, err := d.mandates.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (d *collaborationAgentTaskDispatcher) pruneLocked() {
	s := d.mandates
	s.mu.Lock()
	defer s.mu.Unlock()
	boot, tick, err := s.now()
	if err != nil {
		return
	}
	s.pruneLocked(boot, tick)
	for id, entry := range d.entries {
		if _, ok := s.pending[entry.pending.id]; !ok {
			delete(d.entries, id)
		}
	}
}

// Authorize never converts an uncertain loader/policy failure into terminal
// permission history. Only this process's original pending handle can launch;
// a persisted row, serialized origin or run bearer cannot recreate it.
func (d *collaborationAgentTaskDispatcher) Authorize(ctx context.Context, a dispatch.Assignment) (dispatch.Decision, error) {
	if d == nil || a.Item == nil {
		return dispatch.Decision{}, errCollaborationAgentAccess
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneLocked()
	entry := d.entries[a.Item.ID]
	if entry == nil {
		return dispatch.Decision{}, errCollaborationAgentAccess
	}
	release, err := d.mandates.fence.acquireOriginal(entry.task.ingress)
	if err != nil {
		return dispatch.Decision{}, err
	}
	defer release()
	tx, err := d.writer(ctx)
	if err != nil {
		return dispatch.Decision{}, err
	}
	defer tx.Rollback()
	if err = d.checkAssignment(ctx, tx, entry, a); err != nil {
		return dispatch.Decision{}, err
	}
	if err = tx.Commit(); err != nil {
		return dispatch.Decision{}, err
	}
	// Freeze the FIRST actual attempt, even if a later launch fails. A new
	// generation must obtain a new actual-human task, never freshen this consent.
	entry.runID, entry.attempt, entry.generation = a.RunID, a.Attempt, a.Generation
	return dispatch.Allow(), nil
}

// checkPending retains the original BOOTTIME horizon. SQL attempt leases and
// later attachment cannot restore expired or uncertain original human consent.
func (d *collaborationAgentTaskDispatcher) checkPending(entry *collaborationAgentDispatchEntry) error {
	if d == nil || d.mandates == nil || entry == nil || entry.pending == nil || entry.pending.store != d.mandates {
		return errCollaborationAgentAccess
	}
	s := d.mandates
	s.mu.Lock()
	defer s.mu.Unlock()
	boot, tick, err := s.now()
	pending, exists := s.pending[entry.pending.id]
	if err != nil || !exists || pending.boot != boot || tick >= pending.expiresTick || pending.task.work != entry.task.work {
		return errCollaborationAgentAccess
	}
	return nil
}

func (d *collaborationAgentTaskDispatcher) checkAssignment(ctx context.Context, tx *sql.Tx, entry *collaborationAgentDispatchEntry, a dispatch.Assignment) error {
	if err := d.checkPending(entry); err != nil {
		return err
	}
	if a.Item == nil || !mooring.ValidWireIdentifier(a.RunID) || a.Generation < 1 || a.Attempt < 1 || entry.runID != "" && (entry.runID != a.RunID || entry.attempt != a.Attempt || entry.generation != a.Generation) {
		return errCollaborationAgentAccess
	}
	w := entry.task.work
	itemBinding := collaborationAgentWorkBinding{workID: a.Item.ID, workspaceID: a.Item.WorkspaceID, agentID: a.Item.AgentID, crewID: a.Item.CrewID, humanUserID: a.Item.AuthorizedByUserID, chatID: a.Item.SessionID, source: string(a.Item.Source), sourceRef: a.Item.SourceRef, domainKind: a.Item.DomainKind, domainID: a.Item.DomainID, authorizedScope: a.Item.AuthorizedScope, inputSHA256: a.Item.InputSHA256, targetRevision: a.Item.TargetRevision}
	if itemBinding != w {
		return errCollaborationAgentAccess
	}
	// Compare the ledger, not only the dispatcher's in-memory Item. Deadline,
	// lease, generation, live producer identity and exact input hash all matter.
	var current collaborationAgentWorkBinding
	var input, state, class, lease string
	var ended, terminal, deadline sql.NullString
	var attempt, generation int64
	err := tx.QueryRowContext(ctx, `SELECT w.id,w.workspace_id,w.agent_id,w.crew_id,w.authorized_by_user_id,w.session_id,w.source,w.source_ref,w.domain_kind,w.domain_id,w.authorized_scope,w.input_sha256,w.target_revision,w.input_json,w.state,w.class,at.attempt,at.generation,at.lease_expires_at,at.ended_at,w.terminal_at,w.deadline_at
 FROM work_items w JOIN work_attempts at ON at.work_id=w.id AND at.generation=w.generation AND at.attempt=w.attempts
 WHERE w.id=? AND at.run_id=? AND length(CAST(w.input_json AS BLOB))<=?`, w.workID, a.RunID, mooring.MaxRPCFrame).Scan(&current.workID, &current.workspaceID, &current.agentID, &current.crewID, &current.humanUserID, &current.chatID, &current.source, &current.sourceRef, &current.domainKind, &current.domainID, &current.authorizedScope, &current.inputSHA256, &current.targetRevision, &input, &state, &class, &attempt, &generation, &lease, &ended, &terminal, &deadline)
	digest := sha256.Sum256([]byte(input))
	if err != nil || current != w || a.Item.InputJSON != input || hex.EncodeToString(digest[:]) != w.inputSHA256 || class != "chat" || state != "starting" || generation != a.Generation || attempt != int64(a.Attempt) || ended.Valid || terminal.Valid {
		return errCollaborationAgentAccess
	}
	for _, timestamp := range []string{lease, deadline.String} {
		limit, e := time.Parse(time.RFC3339Nano, timestamp)
		if e != nil || !time.Now().Before(limit) {
			return errCollaborationAgentAccess
		}
	}
	s := d.mandates
	receipt := entry.task.originalHuman
	currentReceipt, err := chataudience.RevalidateOriginalUserQuery(ctx, tx, &receipt)
	if err != nil || currentReceipt == nil || *currentReceipt != receipt {
		return errCollaborationAgentAccess
	}
	proof, err := s.pages.authorizeCollaborationPageSubject(ctx, tx, entry.task.key, entry.task.intent, entry.task.originalCredential)
	write, valid := collaborationAgentIntent(entry.task.intent)
	pageCtx := context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if err != nil || !valid || proof.record == nil || proof.record.PrivateAudience || proof.permissionVersion != entry.task.permissionVersion || proof.memberID != entry.task.memberID || proof.memberRevision != entry.task.memberRevision || !s.pages.artifactAgentPolicyAllows(pageCtx, w.workspaceID, w.crewID) || !collaborationAgentGrantAllows(pageCtx, s.pages, w.workspaceID, proof.record, w.agentID, write) {
		return errCollaborationAgentAccess
	}
	var live bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL JOIN conversation_messages m ON m.agent_id=a.id AND m.id=? AND m.session_id=? AND m.author_user_id=? AND m.role='user' WHERE a.id=? AND a.workspace_id=? AND a.crew_id=? AND a.deleted_at IS NULL AND a.expired_at IS NULL)`, entry.task.messageID, w.chatID, w.humanUserID, w.agentID, w.workspaceID, w.crewID).Scan(&live)
	if err != nil || !live {
		return errCollaborationAgentAccess
	}
	// Authority may expire while the same-writer SQL checks are running.
	return d.checkPending(entry)
}
