package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mentions"
	"github.com/crewship-ai/crewship/internal/missionactivity"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type issueMutationDelivery struct {
	Journal    *journal.Entry  `json:"journal,omitempty"`
	Mention    *mentionContext `json:"mention,omitempty"`
	Target     resolvedMention `json:"target,omitempty"`
	EventID    string          `json:"event_id,omitempty"`
	DeliveryID string          `json:"delivery_id,omitempty"`
	Overflow   int             `json:"overflow,omitempty"`
}

func enqueueIssueMutation(ctx context.Context, tx *sql.Tx, workspace, mission, comment, kind string, payload issueMutationDelivery) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := tsformat.Format(time.Now())
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_mutation_outbox(id,workspace_id,mission_id,comment_id,kind,payload,next_attempt_at,created_at) VALUES(?,?,?,NULLIF(?,''),?,?,?,?)`, generateCUID(), workspace, mission, comment, kind, string(raw), now, now)
	return err
}

// Audit state is owned by the mutation transaction. Only journal projection
// and model dispatch happen later; both have stable identities for retries.
func persistIssueActivityTx(ctx context.Context, tx *sql.Tx, ev issueEvent) (string, error) {
	id := generateCUID()
	payload, err := json.Marshal(issueEventPayload(ev))
	if err != nil {
		return "", err
	}
	written, err := missionactivity.EmitTx(ctx, tx, missionactivity.Entry{ID: id, MissionID: ev.MissionID, ActorType: ev.ActorType, ActorID: ev.ActorID, Action: string(ev.Action), Details: ev.Details, PayloadJSON: string(payload)})
	if err != nil {
		return "", err
	}
	trace := ev.RunID
	if trace == "" {
		trace = "issue-activity:" + id
	}
	// This human event trace names audit delivery, not an agent run/admission.
	entry := journal.Entry{ID: "issue-journal:" + id, WorkspaceID: written.WorkspaceID, CrewID: written.CrewID, MissionID: ev.MissionID, TS: time.Now().UTC().Truncate(time.Millisecond), Type: journalTypeForIssueAction(ev.Action), Severity: journal.SeverityInfo, ActorType: journal.ActorType(ev.ActorType), ActorID: ev.ActorID, Summary: string(ev.Action) + ": " + truncate(ev.Details, 120), TraceID: trace, SpanID: id, Payload: issueEventPayload(ev), Refs: map[string]any{"mission_id": ev.MissionID, "activity_id": id, "seq": written.Seq}}
	if err = enqueueIssueMutation(ctx, tx, written.WorkspaceID, ev.MissionID, "", "journal", issueMutationDelivery{Journal: &entry}); err != nil {
		return "", err
	}
	return id, nil
}

func persistCommentMentionsTx(ctx context.Context, tx *sql.Tx, mc mentionContext) error {
	ids := mentions.ExtractAgentIDs(mc.CommentBody)
	if len(ids) > mentionMaxPerComment {
		if err := enqueueIssueMutation(ctx, tx, mc.WorkspaceID, mc.MissionID, mc.CommentID, "mention", issueMutationDelivery{Mention: &mc, Overflow: len(ids) - mentionMaxPerComment}); err != nil {
			return err
		}
		ids = ids[:mentionMaxPerComment]
	}
	position := 0
	for _, id := range ids {
		target := resolvedMention{Position: position}
		err := tx.QueryRowContext(ctx, `SELECT id,slug,name,COALESCE(crew_id,'') FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, id, mc.WorkspaceID).Scan(&target.AgentID, &target.AgentSlug, &target.AgentName, &target.CrewID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		position++
		eventID, err := persistIssueActivityTx(ctx, tx, issueEvent{MissionID: mc.MissionID, ActorType: mc.AuthorType, ActorID: mc.AuthorID, Action: actionMentioned, Details: id})
		if err != nil {
			return err
		}
		deliveryID := generateCUID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO mission_comment_mentions(id,workspace_id,mission_id,comment_id,event_id,agent_id,position,state,priority,dispatch_state,created_at) VALUES(?,?,?,?,?,?,?,'claimed','normal','skipped',?)`, deliveryID, mc.WorkspaceID, mc.MissionID, mc.CommentID, eventID, id, target.Position, tsformat.Format(time.Now())); err != nil {
			return err
		}
		if err = enqueueIssueMutation(ctx, tx, mc.WorkspaceID, mc.MissionID, mc.CommentID, "mention", issueMutationDelivery{Mention: &mc, Target: target, EventID: eventID, DeliveryID: deliveryID}); err != nil {
			return err
		}
	}
	return nil
}

func stableMentionAssignmentID(deliveryID string) string {
	sum := sha256.Sum256([]byte("crewship/issue-mention-assignment/v1:" + deliveryID))
	return "c" + hex.EncodeToString(sum[:16])
}

func durableMentionAssignmentID(deliveryID string) string {
	if deliveryID == "" {
		return ""
	}
	return stableMentionAssignmentID(deliveryID)
}

type claimedIssueMutation struct {
	id, token             string
	workspace             string
	attempts, maxAttempts int
	decodeErr             error
	payload               issueMutationDelivery
}

func (h *IssueHandler) claimIssueMutation(ctx context.Context, workspace string) (claimedIssueMutation, error) {
	var job claimedIssueMutation
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return job, err
	}
	defer tx.Rollback()
	now := tsformat.Format(time.Now())
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT id,workspace_id,payload,attempts,max_attempts FROM issue_mutation_outbox WHERE (?='' OR workspace_id=?) AND ((state='pending' AND next_attempt_at<=?) OR (state='processing' AND lease_expires_at<=?)) ORDER BY next_attempt_at,created_at,id LIMIT 1`, workspace, workspace, now, now).Scan(&job.id, &job.workspace, &raw, &job.attempts, &job.maxAttempts)
	if err != nil {
		return job, err
	}

	job.token = generateCUID()
	if _, err = tx.ExecContext(ctx, `UPDATE issue_mutation_outbox SET state='processing',lease_token=?,lease_expires_at=?,attempts=attempts+1 WHERE id=?`, job.token, tsformat.Format(time.Now().Add(2*time.Minute)), job.id); err != nil {
		return job, err
	}
	if err = tx.Commit(); err != nil {
		return job, err
	}
	job.attempts++
	job.decodeErr = json.Unmarshal([]byte(raw), &job.payload)
	if job.decodeErr == nil && (job.payload.Journal == nil) == (job.payload.Mention == nil) {
		job.decodeErr = fmt.Errorf("invalid issue mutation payload shape")
	}
	return job, nil
}

// Permanent authority denial retains a failed receipt and notifies its human
// author atomically; it must never retry as an autonomous operation.
type issueMutationAuthorityRevoked struct{}

func (*issueMutationAuthorityRevoked) Error() string {
	return "original requester authority is no longer valid"
}

type issueJournalReplay interface {
	EmitReplaySync(context.Context, journal.Entry) (string, bool, error)
}

func (h *IssueHandler) deliverIssueMutation(ctx context.Context, p issueMutationDelivery) error {
	if p.Journal != nil {
		if writer, ok := h.journal.(issueJournalReplay); ok {
			_, _, err := writer.EmitReplaySync(ctx, *p.Journal)
			return err
		}
		// Nonpersistent test emitters keep their existing observable event surface.
		// Runtime *journal.Writer always uses the synchronous replay path above.
		_, noop := h.journal.(noopEmitter)
		if h.journal == nil || noop {
			return fmt.Errorf("issue journal unavailable")
		}
		if _, err := h.journal.Emit(ctx, *p.Journal); err != nil {
			return err
		}
		return h.journal.Flush(ctx)
	}
	if p.Mention == nil {
		return fmt.Errorf("invalid issue mutation delivery")
	}
	mc := *p.Mention
	if p.Overflow > 0 {
		return h.mentionRecorder().notifyMentionOverflow(ctx, mc, p.Overflow)
	}
	var state, assignment, dispatch, claimed sql.NullString
	err := h.db.QueryRowContext(ctx, `SELECT state,assignment_id,dispatch_state,claimed_by_run_id FROM mission_comment_mentions WHERE id=? AND workspace_id=? AND mission_id=?`, p.DeliveryID, mc.WorkspaceID, mc.MissionID).Scan(&state, &assignment, &dispatch, &claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if assignment.Valid || claimed.Valid || dispatch.String == "queued" || state.String == "consumed" || state.String == "superseded" {
		return nil
	}
	// A stable assignment PK is the admission receipt even if the process died
	// after committing work but before attaching the delivery/acknowledging it.
	expected := stableMentionAssignmentID(p.DeliveryID)
	var existing bool
	if err = h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM assignments WHERE id=? AND workspace_id=? AND mission_id=? AND assigned_to_id=?)`, expected, mc.WorkspaceID, mc.MissionID, p.Target.AgentID).Scan(&existing); err != nil {
		return err
	}
	recorder := h.mentionRecorder()
	deliveriesEnabled, flagErr := featureflags.IsEnabled(ctx, h.db, mc.WorkspaceID, issueDeliveriesFlagKey)
	if flagErr != nil {
		return flagErr
	}
	dispatchState, assignmentID, detail := mentionDispatchDispatched, expected, ""
	if !existing {
		// A retained outbox attempt may retry a failed delivery, but never turn
		// a revoked/missing original human grant into autonomous admission.
		var origin string
		if err = h.db.QueryRowContext(ctx, `SELECT COALESCE(c.chat_origin_id,'') FROM mission_comments c JOIN missions m ON m.id=c.mission_id WHERE c.id=? AND c.mission_id=? AND m.workspace_id=?`, mc.CommentID, mc.MissionID, mc.WorkspaceID).Scan(&origin); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		var authorityErr error
		if origin == "" {
			authorityErr = h.validateAutonomousMentionSource(ctx, mc)
		} else {
			_, authorityErr = runauthority.ValidateOrigin(ctx, h.db, origin, p.Target.AgentID, mc.WorkspaceID)
		}
		if authorityErr != nil {
			if !errors.Is(authorityErr, runauthority.ErrRequesterStale) && !errors.Is(authorityErr, runauthority.ErrIdentityMismatch) && !errors.Is(authorityErr, runauthority.ErrUnavailable) {
				return authorityErr
			}
			detail = "original requester authority is no longer valid"
			if err = recorder.persist(ctx, mc, p.Target, p.EventID, mentionDispatchRefused, "", detail); err != nil {
				return err
			}
			if _, err = h.db.ExecContext(ctx, `UPDATE mission_comment_mentions SET state='failed' WHERE id=?`, p.DeliveryID); err != nil {
				return err
			}
			return &issueMutationAuthorityRevoked{}
		}
		if deliveriesEnabled {
			var seq int
			if err = h.db.QueryRowContext(ctx, `SELECT seq FROM mission_activity WHERE id=?`, p.EventID).Scan(&seq); err != nil {
				return err
			}
			broadcastWorkspaceEvent(h.hub, mc.WorkspaceID, "issue.delivery.acked", map[string]any{"mission_id": mc.MissionID, "identifier": mc.Identifier, "agent_id": p.Target.AgentID, "delivery_id": p.DeliveryID, "event_id": p.EventID, "seq": seq})
		}
		// Outbox leasing owns dispatch concurrency. Delivery state remains pending
		// until the outcome is attached, so a crash can safely retry the same ID.
		mc.DeliveryID = p.DeliveryID
		dispatchState, assignmentID, detail = recorder.dispatchOne(ctx, mc, p.Target)
		if dispatchState == mentionDispatchFailed {
			if err = recorder.persist(ctx, mc, p.Target, p.EventID, dispatchState, "", detail); err != nil {
				return err
			}
			return fmt.Errorf("dispatch mention: %s", detail)
		}
	}
	if dispatchState == mentionDispatchQueued {
		if _, err = h.db.ExecContext(ctx, `UPDATE mission_comment_mentions SET dispatch_state='queued',state='pending',priority='correction',dispatch_detail=? WHERE id=?`, detail, p.DeliveryID); err != nil {
			return err
		}
	} else {
		if err = recorder.persist(ctx, mc, p.Target, p.EventID, dispatchState, assignmentID, detail); err != nil {
			return err
		}
		if assignmentID != "" {
			_, err = h.db.ExecContext(ctx, `UPDATE mission_comment_mentions SET state=CASE WHEN EXISTS(SELECT 1 FROM assignments WHERE id=? AND status IN ('COMPLETED','FAILED','CANCELLED')) THEN 'consumed' ELSE 'claimed' END,claimed_by_run_id=? WHERE id=?`, assignmentID, assignmentID, p.DeliveryID)
			if err == nil && !deliveriesEnabled {
				_, err = h.db.ExecContext(ctx, `UPDATE mission_comment_mentions SET state='consumed',claimed_by_run_id=NULL WHERE id=?`, p.DeliveryID)
			}
		} else {
			_, err = h.db.ExecContext(ctx, `UPDATE mission_comment_mentions SET state='failed' WHERE id=?`, p.DeliveryID)
		}
	}
	if err != nil {
		return err
	}
	recorder.notifyMentionUndelivered(ctx, mc, p.Target, dispatchState, detail)
	return nil
}

// A committed machine delegation may outlive its source run. Its original
// host admission is positive evidence; absence of a human grant alone is not.
func (h *IssueHandler) validateAutonomousMentionSource(ctx context.Context, mc mentionContext) error {
	if mc.AutonomousPipeline {
		return validateAutonomousPipelineMentionSource(ctx, h.db, mc)
	}
	if mc.AuthorType != "agent" || mc.SourceRunID == "" {
		return runauthority.ErrUnavailable
	}
	var count int
	var agent, raw string
	err := h.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(agent_id),''),COALESCE(MAX(payload),'') FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type='run.started'`, mc.WorkspaceID, mc.SourceRunID).Scan(&count, &agent, &raw)
	if err != nil {
		return err
	}
	if count != 1 || agent != mc.AuthorID {
		return runauthority.ErrIdentityMismatch
	}
	var p struct {
		HostAdmitted bool            `json:"host_admitted"`
		Missing      bool            `json:"requester_authority_missing"`
		Trigger      string          `json:"trigger_type"`
		Human        json.RawMessage `json:"human_authority"`
		Origin       string          `json:"chat_origin_id"`
	}
	if json.Unmarshal([]byte(raw), &p) != nil || !p.HostAdmitted || p.Missing || len(p.Human) > 0 || p.Origin != "" {
		return runauthority.ErrUnavailable
	}
	switch p.Trigger {
	case "SCHEDULED", "ASSIGNMENT", "WEBHOOK", "ROUTINE", "CRON", "PEER_QUERY":
		return nil
	}
	return runauthority.ErrUnavailable
}

// DrainIssueMutationOutbox is bounded and safe to run concurrently or after
// restart. Leases recover interrupted attempts; stable effects fence lost ACKs.
func (h *IssueHandler) DrainIssueMutationOutbox(ctx context.Context, limit int) error {
	return h.drainIssueMutationOutbox(ctx, limit, "")
}

// Request paths never deliver another workspace's model work.
func (h *IssueHandler) drainIssueMutationOutbox(ctx context.Context, limit int, workspace string) error {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	var firstError error
	for i := 0; i < limit; i++ {
		job, err := h.claimIssueMutation(ctx, workspace)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		deliveryErr := job.decodeErr
		if deliveryErr == nil {
			deliveryErr = h.deliverIssueMutation(ctx, job.payload)
		}
		var permanent *issueMutationAuthorityRevoked
		terminal := deliveryErr != nil && (errors.As(deliveryErr, &permanent) || job.decodeErr != nil || job.attempts >= job.maxAttempts)
		if deliveryErr == nil {
			_, err = h.db.ExecContext(ctx, `UPDATE issue_mutation_outbox SET state='done',payload='{}',failure_code='',failed_at=NULL,completed_at=?,lease_token=NULL,lease_expires_at=NULL,last_error='' WHERE id=? AND lease_token=?`, tsformat.Format(time.Now()), job.id, job.token)
		} else if terminal {
			err = h.failIssueMutation(ctx, job, deliveryErr)

		} else {
			delay := 5 * time.Second
			for n := 1; n < job.attempts && delay < 10*time.Minute; n++ {
				delay *= 2
			}
			if delay > 10*time.Minute {
				delay = 10 * time.Minute
			}
			_, err = h.db.ExecContext(ctx, `UPDATE issue_mutation_outbox SET state='pending',next_attempt_at=?,lease_token=NULL,lease_expires_at=NULL,last_error=? WHERE id=? AND lease_token=?`, tsformat.Format(time.Now().Add(delay)), truncate(deliveryErr.Error(), 500), job.id, job.token)
		}
		if err != nil {
			return err
		}
		if deliveryErr != nil && firstError == nil {
			firstError = deliveryErr
		}
	}
	// Completed receipts retain only minimal metadata, and expire after seven
	// days. Failed receipts remain available for operator diagnosis.
	_, err := h.db.ExecContext(ctx, `DELETE FROM issue_mutation_outbox WHERE state='done' AND completed_at<? AND (?='' OR workspace_id=?)`, tsformat.Format(time.Now().Add(-7*24*time.Hour)), workspace, workspace)
	if firstError != nil {
		return firstError
	}
	return err
}

// Terminal status and its human notice share one commit. A lost ACK cannot
// tell the author to retry while the model job still owns automatic retries.
func (h *IssueHandler) failIssueMutation(ctx context.Context, job claimedIssueMutation, failure error) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_mutation_outbox WHERE id=? AND state='processing' AND lease_token=?)`, job.id, job.token).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return nil
	}
	var permanent *issueMutationAuthorityRevoked
	state, reason := mentionDispatchFailed, "delivery attempts exhausted"
	if errors.As(failure, &permanent) {
		state, reason = mentionDispatchRefused, permanent.Error()
	}
	if job.payload.Mention != nil && job.payload.DeliveryID != "" {
		if err = h.mentionRecorder().notifyMentionUndelivered(ctx, *job.payload.Mention, job.payload.Target, state, reason, tx); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE issue_mutation_outbox SET state='failed',failed_at=?,failure_code=?,lease_token=NULL,lease_expires_at=NULL,last_error=? WHERE id=? AND lease_token=?`, tsformat.Format(time.Now()), issueMutationFailureCode(job.decodeErr, failure), truncate(failure.Error(), 500), job.id, job.token); err != nil {
		return err
	}
	return tx.Commit()
}

func issueMutationFailureCode(decodeErr error, failure error) string {
	var permanent *issueMutationAuthorityRevoked
	if errors.As(failure, &permanent) {
		return "authority_revoked"
	}
	if decodeErr != nil {
		return "invalid_payload"
	}
	return "delivery_exhausted"
}

func (h *IssueHandler) drainCommittedIssueMutation(workspace string) {
	if workspace == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.drainIssueMutationOutbox(ctx, 32, workspace); err != nil && h.logger != nil {
		h.logger.Warn("issue mutation delivery deferred", "error", err)
	}
}

type issueMutationWorker struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (r *Router) StartIssueMutationOutbox(ctx context.Context) {
	if r.issueMutationHandler == nil || r.issueMutationWorker != nil {
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	worker := &issueMutationWorker{cancel: cancel}
	r.issueMutationWorker = worker
	worker.wg.Add(1)
	go func() {
		defer worker.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if workerCtx.Err() != nil {
				return
			}
			attempt, cancel := context.WithTimeout(workerCtx, 30*time.Second)
			err := r.issueMutationHandler.DrainIssueMutationOutbox(attempt, 50)
			cancel()
			if err != nil && workerCtx.Err() == nil {
				r.logger.Warn("issue mutation outbox retry", "error", err)
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (w *issueMutationWorker) stop() {
	if w != nil {
		w.cancel()
		w.wg.Wait()
	}
}

func replaceIssueLabelsTx(ctx context.Context, tx *sql.Tx, missionID string, labels []string) error {
	rows, err := tx.QueryContext(ctx, `SELECT label_id FROM mission_labels WHERE mission_id=?`, missionID)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, id := range labels {
		wanted[id] = true
	}
	for id := range existing {
		if !wanted[id] {
			if _, err = tx.ExecContext(ctx, `DELETE FROM mission_labels WHERE mission_id=? AND label_id=?`, missionID, id); err != nil {
				return err
			}
		}
	}
	for id := range wanted {
		if !existing[id] {
			if _, err = tx.ExecContext(ctx, `INSERT INTO mission_labels(mission_id,label_id) VALUES(?,?)`, missionID, id); err != nil {
				return err
			}
		}
	}
	return nil
}
