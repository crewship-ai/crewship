package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

type cancelIssueResponseHeader struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w cancelIssueResponseHeader) Header() http.Header {
	w.cancel()
	return w.ResponseRecorder.Header()
}

type unavailableIssueJournal struct{}

func (unavailableIssueJournal) Emit(context.Context, journal.Entry) (string, error) {
	return "", errors.New("synthetic journal outage")
}
func (unavailableIssueJournal) Flush(context.Context) error {
	return errors.New("synthetic journal outage")
}

type loseMentionAck struct {
	delegate mentionDispatcher
	calls    atomic.Int64
}

type failBeforeMentionAdmission struct {
	delegate mentionDispatcher
	calls    atomic.Int64
}

func (d *failBeforeMentionAdmission) DispatchMention(ctx context.Context, r mentionDispatchRequest) (string, error) {
	if d.calls.Add(1) == 1 {
		return "", errors.New("synthetic transient failure before admission")
	}
	return d.delegate.DispatchMention(ctx, r)
}

func (d *loseMentionAck) DispatchMention(ctx context.Context, r mentionDispatchRequest) (string, error) {
	d.calls.Add(1)
	id, err := d.delegate.DispatchMention(ctx, r)
	if err != nil {
		return id, err
	}
	return "", errors.New("synthetic process loss after admission before delivery ACK")
}

func outboxCommentRequest(f *mentionFixture, key, body string) *http.Request {
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{"body": body}))
	req.SetPathValue("crewId", f.crewID)
	req.SetPathValue("identifier", f.ident)
	req.Header.Set("Idempotency-Key", key)
	return withWorkspaceUser(req, f.userID, f.wsID, "OWNER")
}

func TestHumanCommentDisconnectAndReplayKeepOneDurableMention(t *testing.T) {
	f := setupMentionFixture(t)
	req := outboxCommentRequest(f, "disconnect-once", mentionToken("lead", f.target)+" please review")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	first := httptest.NewRecorder()
	f.issues.CreateComment(cancelIssueResponseHeader{first, cancel}, req.WithContext(ctx))
	f.assign.WaitDispatches()
	if first.Code != 201 {
		t.Fatalf("comment: %d %s", first.Code, first.Body.String())
	}
	replay := httptest.NewRecorder()
	f.issues.CreateComment(replay, outboxCommentRequest(f, "disconnect-once", mentionToken("lead", f.target)+" please review"))
	f.assign.WaitDispatches()
	if replay.Code != 201 || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	if f.mentionRows(t) != 1 || f.mentionActivity(t) != 1 || f.assignments(t) != 1 {
		t.Fatalf("lost/duplicated mention: rows=%d events=%d work=%d", f.mentionRows(t), f.mentionActivity(t), f.assignments(t))
	}
}

func TestIssueOutboxRestartRecoversLostMentionAdmissionACK(t *testing.T) {
	f := setupMentionFixture(t)
	dispatch := &loseMentionAck{delegate: f.assign}
	f.issues.SetMentionDispatcher(dispatch)
	rr := httptest.NewRecorder()
	f.issues.CreateComment(rr, outboxCommentRequest(f, "lost-ack", mentionToken("lead", f.target)+" review this"))
	f.assign.WaitDispatches()
	if rr.Code != 201 || dispatch.calls.Load() != 1 || f.assignments(t) != 1 {
		t.Fatalf("first admission %d calls=%d work=%d %s", rr.Code, dispatch.calls.Load(), f.assignments(t), rr.Body.String())
	}
	execOrFatal(t, f.db, `UPDATE issue_mutation_outbox SET state='processing',lease_token='dead-process',lease_expires_at='2000-01-01T00:00:00.000Z' WHERE kind='mention'`)
	recovered := NewIssueHandler(f.db, nil, nil, newTestLogger())
	recovered.SetMentionDispatcher(dispatch)
	_ = recovered.DrainIssueMutationOutbox(t.Context(), 50) // No journal configured; mention work must still recover.
	if dispatch.calls.Load() != 1 || f.assignments(t) != 1 {
		t.Fatalf("restart duplicated admission: calls=%d work=%d", dispatch.calls.Load(), f.assignments(t))
	}
	var state, dispatchState string
	if err := f.db.QueryRow(`SELECT state,dispatch_state FROM mission_comment_mentions`).Scan(&state, &dispatchState); err != nil || state == "pending" || dispatchState != "dispatched" {
		t.Fatalf("delivery not attached after restart: %s/%s %v", state, dispatchState, err)
	}
}

func TestIssueOutboxRetriesFailedDeliveryBeforeAdmission(t *testing.T) {
	f := setupMentionFixture(t)
	dispatcher := &failBeforeMentionAdmission{delegate: f.assign}
	f.issues.SetMentionDispatcher(dispatcher)
	rr := httptest.NewRecorder()
	f.issues.CreateComment(rr, outboxCommentRequest(f, "retry-before-admission", mentionToken("lead", f.target)))
	if rr.Code != 201 || dispatcher.calls.Load() != 1 || f.assignments(t) != 0 {
		t.Fatalf("first failed attempt %d calls=%d work=%d %s", rr.Code, dispatcher.calls.Load(), f.assignments(t), rr.Body.String())
	}
	// A failed delivery row is diagnostic state, not an ACK of pending outbox
	// intent. Recovery must re-drive it using the original frozen authority.
	execOrFatal(t, f.db, `UPDATE mission_comment_mentions SET state='failed'`)
	execOrFatal(t, f.db, `UPDATE issue_mutation_outbox SET next_attempt_at='2000-01-01T00:00:00.000Z' WHERE kind='mention'`)
	_ = f.issues.DrainIssueMutationOutbox(t.Context(), 50)
	f.assign.WaitDispatches()
	if dispatcher.calls.Load() != 2 || f.assignments(t) != 1 {
		t.Fatalf("failed intent lost/duplicated: calls=%d work=%d", dispatcher.calls.Load(), f.assignments(t))
	}
	_ = f.issues.DrainIssueMutationOutbox(t.Context(), 50)
	if dispatcher.calls.Load() != 2 || f.assignments(t) != 1 {
		t.Fatal("completed retry dispatched twice")
	}
}

func TestIssueOutboxRetryDoesNotAdmitRevokedHumanOrigin(t *testing.T) {
	f := setupMentionFixture(t)
	dispatcher := &failBeforeMentionAdmission{delegate: f.assign}
	f.issues.SetMentionDispatcher(dispatcher)
	rr := httptest.NewRecorder()
	f.issues.CreateComment(rr, outboxCommentRequest(f, "revoked-before-retry", mentionToken("lead", f.target)))
	if rr.Code != 201 || f.assignments(t) != 0 {
		t.Fatalf("first attempt %d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, f.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.wsID, f.userID)
	execOrFatal(t, f.db, `UPDATE issue_mutation_outbox SET next_attempt_at='2000-01-01T00:00:00.000Z' WHERE kind='mention'`)
	_ = f.issues.DrainIssueMutationOutbox(t.Context(), 50)
	if dispatcher.calls.Load() != 1 || f.assignments(t) != 0 {
		t.Fatalf("revoked intent admitted: calls=%d work=%d", dispatcher.calls.Load(), f.assignments(t))
	}
	var state string
	if err := f.db.QueryRow(`SELECT dispatch_state FROM mission_comment_mentions`).Scan(&state); err != nil || state != mentionDispatchRefused {
		t.Fatalf("revoked delivery not refused: %s %v", state, err)
	}
	var pending int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM issue_mutation_outbox WHERE kind='mention' AND state IN('pending','processing')`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("permanent denial remains a retry loop: %d %v", pending, err)
	}
	var terminal, code string
	if err := f.db.QueryRow(`SELECT state,failure_code FROM issue_mutation_outbox WHERE kind='mention'`).Scan(&terminal, &code); err != nil || terminal != "failed" || code != "authority_revoked" {
		t.Fatalf("revoked grant not terminal: %s/%s %v", terminal, code, err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM inbox_items WHERE source_id LIKE 'mention_%'`); n != 1 {
		t.Fatalf("permanent denial notice count=%d", n)
	}
	_ = f.issues.DrainIssueMutationOutbox(t.Context(), 50)
	if dispatcher.calls.Load() != 1 {
		t.Fatal("revoked terminal intent retried dispatch")
	}

}

func TestIssueOutboxWorkerRecoversJournalAndDeduplicatesLostACK(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	h.SetJournal(unavailableIssueJournal{})
	req := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"priority":"high"}`))
	req.SetPathValue("crewId", crew)
	req.SetPathValue("identifier", "ENG-1")
	req.Header.Set("Idempotency-Key", "durable-audit")
	req = withWorkspaceUser(req, user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("patch %d %s", rr.Code, rr.Body.String())
	}
	var activity int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id=? AND action='priority_changed'`, id).Scan(&activity); err != nil || activity != 1 {
		t.Fatalf("activity not in source transaction: %d %v", activity, err)
	}
	execOrFatal(t, h.db, `UPDATE issue_mutation_outbox SET next_attempt_at='2000-01-01T00:00:00.000Z'`)
	var originalPayload string
	if err := h.db.QueryRow(`SELECT payload FROM issue_mutation_outbox LIMIT 1`).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	recovered := NewIssueHandler(h.db, nil, nil, newTestLogger())
	recovered.SetJournal(writer)
	router := &Router{issueMutationHandler: recovered, logger: newTestLogger()}
	router.StartIssueMutationOutbox(t.Context())
	defer router.Shutdown()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var count int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM issue_mutation_outbox WHERE state<>'done'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restart sweeper did not deliver journal")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Simulate an ACK lost after a real hash-chained journal commit, and recover
	// with a new Writer instance. Stable replay identity must not append twice.
	router.Shutdown()
	writer.Close()
	writer = journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	recovered.SetJournal(writer)
	execOrFatal(t, h.db, `UPDATE issue_mutation_outbox SET payload=?,state='processing',lease_token='lost-ack',lease_expires_at='2000-01-01T00:00:00.000Z'`, originalPayload)
	if err := recovered.DrainIssueMutationOutbox(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE mission_id=? AND entry_type=?`, id, journalTypeForIssueAction(actionPriorityChanged)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("journal duplicated/missing: %d %v", count, err)
	}
	chain, err := journal.VerifyChain(t.Context(), h.db, ws)
	if err != nil || !chain.OK {
		t.Fatalf("hash chain invalid: %+v %v", chain, err)
	}
}

func TestHumanIssueOutboxFailureRollsBackCommentAndActivity(t *testing.T) {
	f := setupMentionFixture(t)
	execOrFatal(t, f.db, `CREATE TRIGGER reject_issue_outbox BEFORE INSERT ON issue_mutation_outbox BEGIN SELECT RAISE(ABORT,'synthetic outbox failure');END`)
	rr := httptest.NewRecorder()
	f.issues.CreateComment(rr, outboxCommentRequest(f, "rollback-outbox", mentionToken("lead", f.target)))
	if rr.Code != 500 {
		t.Fatalf("comment %d %s", rr.Code, rr.Body.String())
	}
	for _, table := range []string{"mission_comments", "mission_activity", "agent_operations", "issue_mutation_outbox", "mission_comment_mentions", "assignments"} {
		if n := f.countRows(t, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("partial transaction in %s: %d", table, n)
		}
	}
}

func TestHumanIssueUnchangedLabelsDoNotAdvanceRevision(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	execOrFatal(t, h.db, `INSERT INTO labels(id,workspace_id,name) VALUES('unchanged-a',?,'A'),('unchanged-b',?,'B')`, ws, ws)
	execOrFatal(t, h.db, `INSERT INTO mission_labels(mission_id,label_id) VALUES(?,'unchanged-a'),(?,'unchanged-b')`, id, id)
	var before int64
	if err := h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"labels":["unchanged-b","unchanged-a","unchanged-a"]}`))
	req.SetPathValue("crewId", crew)
	req.SetPathValue("identifier", "ENG-1")
	req = withWorkspaceUser(req, user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("no-op label patch %d %s", rr.Code, rr.Body.String())
	}
	var after int64
	if err := h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, id).Scan(&after); err != nil || after != before {
		t.Fatalf("no-op label patch changed revision %d -> %d: %v", before, after, err)
	}
}

func TestIssueOutboxAuditSurvivesHardDeleteDuringJournalOutage(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	h.SetJournal(unavailableIssueJournal{})
	patch := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"priority":"high"}`))
	patch.SetPathValue("crewId", crew)
	patch.SetPathValue("identifier", "ENG-1")
	patch = withWorkspaceUser(patch, user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, patch)
	if rr.Code != 200 {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}
	remove := httptest.NewRequest("DELETE", "/", nil)
	remove.SetPathValue("crewId", crew)
	remove.SetPathValue("identifier", "ENG-1")
	remove = withWorkspaceUser(remove, user, ws, "OWNER")
	rr = httptest.NewRecorder()
	h.Delete(rr, remove)
	if rr.Code != 204 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	var intents int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM issue_mutation_outbox WHERE mission_id=? AND kind='journal' AND state<>'done'`, id).Scan(&intents); err != nil || intents != 1 {
		t.Fatalf("hard delete erased audit intent: %d %v", intents, err)
	}
	execOrFatal(t, h.db, `UPDATE issue_mutation_outbox SET next_attempt_at='2000-01-01T00:00:00.000Z'`)
	var originalPayload string
	if err := h.db.QueryRow(`SELECT payload FROM issue_mutation_outbox LIMIT 1`).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	h.SetJournal(writer)
	if err := h.DrainIssueMutationOutbox(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE issue_mutation_outbox SET payload=?,state='processing',lease_token='lost-ack',lease_expires_at='2000-01-01T00:00:00.000Z'`, originalPayload)
	if err := h.DrainIssueMutationOutbox(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	var count int
	var missionID string
	var refs string
	if err := h.db.QueryRow(`SELECT COUNT(*),mission_id,refs FROM journal_entries WHERE workspace_id=? AND entry_type=?`, ws, journalTypeForIssueAction(actionPriorityChanged)).Scan(&count, &missionID, &refs); err != nil || count != 1 || missionID != id || !strings.Contains(refs, id) {
		t.Fatalf("deleted issue audit lost/duplicated: count=%d mission=%v refs=%s err=%v", count, missionID, refs, err)
	}
	chain, err := journal.VerifyChain(t.Context(), h.db, ws)
	if err != nil || !chain.OK {
		t.Fatalf("deleted issue audit chain invalid: %+v %v", chain, err)
	}
}

func TestIssueOutboxCommentDeletionCancelsPendingModelIntent(t *testing.T) {
	f := setupMentionFixture(t)
	dispatcher := &failBeforeMentionAdmission{delegate: f.assign}
	f.issues.SetMentionDispatcher(dispatcher)
	rr := httptest.NewRecorder()
	f.issues.CreateComment(rr, outboxCommentRequest(f, "delete-before-retry", mentionToken("lead", f.target)))
	if rr.Code != 201 || f.assignments(t) != 0 {
		t.Fatalf("first attempt: %d %s", rr.Code, rr.Body.String())
	}
	var comment string
	if err := f.db.QueryRow(`SELECT id FROM mission_comments WHERE mission_id=?`, f.missionID).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	// This is the same durable deletion boundary the HTTP comment delete uses.
	execOrFatal(t, f.db, `DELETE FROM mission_comments WHERE id=?`, comment)
	execOrFatal(t, f.db, `UPDATE issue_mutation_outbox SET next_attempt_at='2000-01-01T00:00:00.000Z'`)
	_ = f.issues.DrainIssueMutationOutbox(t.Context(), 50)
	if dispatcher.calls.Load() != 1 || f.assignments(t) != 0 {
		t.Fatalf("deleted comment launched work: calls=%d work=%d", dispatcher.calls.Load(), f.assignments(t))
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM issue_mutation_outbox WHERE kind='mention'`); n != 0 {
		t.Fatalf("deleted comment left model intent: %d", n)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM issue_mutation_outbox WHERE kind='journal'`); n != 2 {
		t.Fatalf("comment delete removed historical mention audit: %d", n)
	}
}

func TestIssueOutboxQueuedAndClaimedLostACKDoNotDispatchAgain(t *testing.T) {
	for _, queued := range []bool{true, false} {
		t.Run(map[bool]string{true: "queued", false: "claimed"}[queued], func(t *testing.T) {
			f := setupMentionFixture(t)
			dispatcher := &failBeforeMentionAdmission{delegate: f.assign}
			f.issues.SetMentionDispatcher(dispatcher)
			rr := httptest.NewRecorder()
			f.issues.CreateComment(rr, outboxCommentRequest(f, "queued-lost-ack", mentionToken("lead", f.target)))
			if rr.Code != 201 || dispatcher.calls.Load() != 1 {
				t.Fatalf("initial dispatch: %d calls=%d", rr.Code, dispatcher.calls.Load())
			}
			// A session-followup queue has durably accepted this intent, but the
			// outbox ACK was lost. A later claim must not dispatch it a second time.
			if queued {
				execOrFatal(t, f.db, `UPDATE mission_comment_mentions SET state='pending',dispatch_state='queued',assignment_id=NULL`)
			} else {
				execOrFatal(t, f.db, `INSERT OR IGNORE INTO chats(id,workspace_id,agent_id) VALUES(?,?,?)`, f.missionID, f.wsID, f.target)
				execOrFatal(t, f.db, `INSERT INTO assignments(id,workspace_id,chat_id,mission_id,assigned_by_id,assigned_to_id,task,status) VALUES('followup-run',?,?,?,?,?,'queued followup','PENDING')`, f.wsID, f.missionID, f.missionID, f.target, f.target)
				execOrFatal(t, f.db, `UPDATE mission_comment_mentions SET state='claimed',dispatch_state='dispatched',claimed_by_run_id='followup-run',assignment_id=NULL`)
			}
			existingWork := f.assignments(t)
			execOrFatal(t, f.db, `UPDATE issue_mutation_outbox SET state='processing',lease_token='dead',lease_expires_at='2000-01-01T00:00:00.000Z' WHERE kind='mention'`)
			_ = f.issues.DrainIssueMutationOutbox(t.Context(), 50)
			if dispatcher.calls.Load() != 1 || f.assignments(t) != existingWork {
				t.Fatal("queued/claimed intent was dispatched twice")
			}
			var state, payload string
			if err := f.db.QueryRow(`SELECT state,payload FROM issue_mutation_outbox WHERE kind='mention'`).Scan(&state, &payload); err != nil || state != "done" || payload != "{}" {
				t.Fatalf("ACK not compacted: %s %s %v", state, payload, err)
			}
		})
	}
}

func TestIssueOutboxPoisonIsolationBackoffAndWorkspaceFence(t *testing.T) {
	h, _, ws, crew, lead, _ := newTestIssueHandler(t)
	mission := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('outbox-foreign','Foreign','outbox-foreign')`)
	raw, _ := json.Marshal(issueMutationDelivery{Journal: &journal.Entry{ID: "retry-audit", WorkspaceID: ws, MissionID: mission}})
	execOrFatal(t, h.db, `INSERT INTO issue_mutation_outbox(id,workspace_id,mission_id,kind,payload,next_attempt_at,created_at) VALUES('poison',?,?,'journal','[]','2000-01-01T00:00:00.000Z','2000-01-01T00:00:00.000Z'),('retry',?,?,'journal',?,'2000-01-01T00:00:00.000Z','2001-01-01T00:00:00.000Z'),('foreign','outbox-foreign','historical','journal','[]','2000-01-01T00:00:00.000Z','2000-01-01T00:00:00.000Z')`, ws, mission, ws, mission, string(raw))
	_ = h.drainIssueMutationOutbox(t.Context(), 50, ws)
	var state, code string
	var attempts int
	if err := h.db.QueryRow(`SELECT state,failure_code,attempts FROM issue_mutation_outbox WHERE id='poison'`).Scan(&state, &code, &attempts); err != nil || state != "failed" || code != "invalid_payload" || attempts != 1 {
		t.Fatalf("poison not isolated: %s %s %d %v", state, code, attempts, err)
	}
	var firstNext string
	if err := h.db.QueryRow(`SELECT state,attempts,next_attempt_at FROM issue_mutation_outbox WHERE id='retry'`).Scan(&state, &attempts, &firstNext); err != nil || state != "pending" || attempts != 1 {
		t.Fatalf("healthy tail starved: %s %d %v", state, attempts, err)
	}
	if err := h.db.QueryRow(`SELECT attempts FROM issue_mutation_outbox WHERE id='foreign'`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("foreign workspace touched: %d %v", attempts, err)
	}
	execOrFatal(t, h.db, `UPDATE issue_mutation_outbox SET next_attempt_at='2000-01-01T00:00:00.000Z' WHERE id='retry'`)
	_ = h.drainIssueMutationOutbox(t.Context(), 50, ws)
	var secondNext string
	if err := h.db.QueryRow(`SELECT next_attempt_at FROM issue_mutation_outbox WHERE id='retry'`).Scan(&secondNext); err != nil {
		t.Fatal(err)
	}
	if secondNext <= firstNext {
		t.Fatalf("backoff did not grow: %s -> %s", firstNext, secondNext)
	}
	execOrFatal(t, h.db, `UPDATE issue_mutation_outbox SET max_attempts=3,next_attempt_at='2000-01-01T00:00:00.000Z' WHERE id='retry'`)
	_ = h.drainIssueMutationOutbox(t.Context(), 50, ws)
	if err := h.db.QueryRow(`SELECT state,failure_code,attempts FROM issue_mutation_outbox WHERE id='retry'`).Scan(&state, &code, &attempts); err != nil || state != "failed" || code != "delivery_exhausted" || attempts != 3 {
		t.Fatalf("no terminal limit: %s %s %d %v", state, code, attempts, err)
	}
	execOrFatal(t, h.db, `INSERT INTO issue_mutation_outbox(id,workspace_id,mission_id,kind,payload,state,next_attempt_at,created_at,completed_at) VALUES('expired',?,?,'journal','{}','done','2000-01-01T00:00:00.000Z','2000-01-01T00:00:00.000Z','2000-01-01T00:00:00.000Z')`, ws, mission)
	_ = h.drainIssueMutationOutbox(t.Context(), 50, ws)
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM issue_mutation_outbox WHERE id='expired'`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal("completed metadata retention failed", err)
	}
}

type alwaysFailIssueMention struct{ calls atomic.Int64 }

func (d *alwaysFailIssueMention) DispatchMention(context.Context, mentionDispatchRequest) (string, error) {
	d.calls.Add(1)
	return "", errors.New("temporary unavailable")
}

func TestIssueOutboxFailureNoticeOnlyAfterExhaustion(t *testing.T) {
	f := setupMentionFixture(t)
	d := &alwaysFailIssueMention{}
	f.issues.SetMentionDispatcher(d)
	rr := httptest.NewRecorder()
	f.issues.CreateComment(rr, outboxCommentRequest(f, "exhausted", mentionToken("lead", f.target)))
	if rr.Code != 201 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM inbox_items WHERE source_id LIKE 'mention_%'`); n != 0 {
		t.Fatal("transient attempt told author nothing is queued")
	}
	execOrFatal(t, f.db, `UPDATE issue_mutation_outbox SET max_attempts=2,next_attempt_at='2000-01-01T00:00:00.000Z' WHERE kind='mention'`)
	_ = f.issues.DrainIssueMutationOutbox(t.Context(), 50)
	var state string
	if err := f.db.QueryRow(`SELECT state FROM issue_mutation_outbox WHERE kind='mention'`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("terminal state %s %v", state, err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM inbox_items WHERE source_id LIKE 'mention_%'`); n != 1 {
		t.Fatalf("terminal notice count=%d", n)
	}
	_ = f.issues.DrainIssueMutationOutbox(t.Context(), 50)
	if d.calls.Load() != 2 {
		t.Fatal("terminal failure kept dispatching")
	}
}

func TestIssueOutboxAutonomousSourceRequiresPositiveHostAdmission(t *testing.T) {
	h, _, ws, _, lead, _ := newTestIssueHandler(t)
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	for _, tc := range []struct {
		name, raw string
		allowed   bool
	}{
		{"machine", `{"host_admitted":true,"trigger_type":"ASSIGNMENT"}`, true},
		{"missing", `{"host_admitted":true,"trigger_type":"ASSIGNMENT","requester_authority_missing":true}`, false},
		{"null-human", `{"host_admitted":true,"trigger_type":"ASSIGNMENT","human_authority":null}`, false},
		{"unadmitted", `{"trigger_type":"ASSIGNMENT"}`, false},
	} {
		var payload map[string]any
		if err := json.Unmarshal([]byte(tc.raw), &payload); err != nil {
			t.Fatal(err)
		}
		entry := journal.Entry{ID: "source-" + tc.name, WorkspaceID: ws, AgentID: lead, Type: journal.EntryRunStarted, TS: time.Now().UTC(), Severity: journal.SeverityInfo, ActorType: journal.ActorOrchestrator, Summary: "source", TraceID: tc.name, SpanID: "source-span-" + tc.name, Payload: payload}
		if _, _, err := writer.EmitReplaySync(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
		err := h.validateAutonomousMentionSource(t.Context(), mentionContext{AuthorType: "agent", AuthorID: lead, WorkspaceID: ws, SourceRunID: tc.name})
		if (err == nil) != tc.allowed {
			t.Fatalf("%s allowed=%v err=%v", tc.name, tc.allowed, err)
		}
	}
	if err := h.validateAutonomousMentionSource(t.Context(), mentionContext{AuthorType: "user", AuthorID: lead, WorkspaceID: ws, SourceRunID: "machine"}); err == nil {
		t.Fatal("human source became autonomous")
	}
}

func TestIssueOutboxStableAdmissionReplayDoesNotBecomeNewDispatch(t *testing.T) {
	f := setupMentionFixture(t)
	rr := httptest.NewRecorder()
	f.issues.CreateComment(rr, outboxCommentRequest(f, "stable-replay", mentionToken("lead", f.target)))
	f.assign.WaitDispatches()
	if rr.Code != 201 || f.assignments(t) != 1 {
		t.Fatal("initial admission failed", rr.Code, rr.Body.String())
	}
	var id string
	if err := f.db.QueryRow(`SELECT id FROM assignments WHERE mission_id=?`, f.missionID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	_, err := insertCappedAssignment(t.Context(), f.db, delegationScope{Depth: 1}, delegationLimits{MaxDepth: 2, MaxFanout: 8}, dispatchCaller{FanoutSubjectID: f.target}, cappedAssignment{ID: id, WorkspaceID: f.wsID, MissionID: f.missionID, TargetID: f.target})
	var admitted *assignmentAlreadyAdmittedError
	if !errors.As(err, &admitted) || admitted.ID != id {
		t.Fatalf("stable replay was treated as a fresh admission: %v", err)
	}
}
