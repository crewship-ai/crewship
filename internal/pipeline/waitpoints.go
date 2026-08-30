package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/policy"
)

// SQLWaitpointStore is the production WaitpointStore backed by the
// pipeline_waitpoints table (v79 migration). Maintains an in-memory
// channel registry keyed by token so a CompleteApproval call wakes
// the goroutine waiting on WaitFor.
//
// Persistence model:
//   - CreateApproval inserts a row with status=pending, token, prompt,
//     timeout_at; returns the token to the caller (the wait step
//     embeds it in journal so the inbox UI can render the approval
//     card)
//   - WaitFor blocks on a channel keyed by token. Either:
//     (a) CompleteApproval fires (HTTP endpoint POSTed by the inbox
//     UI) → status = approved/denied → channel signal → return
//     (b) timeout_at passes (background sweeper marks status =
//     timed_out + signals channel)
//     (c) ctx cancellation (run cancelled) → channel listener exits
//   - Recovery scan on startup re-attaches goroutines to pending
//     waitpoints whose runs are still running — without it, a
//     restart leaves approvals orphaned forever
//
// Sweeper runs in a separate goroutine, fired every 30 s. Cheap
// query against idx_pipeline_waitpoints_workspace_pending; no need
// to schedule per-waitpoint timers.
type SQLWaitpointStore struct {
	db *sql.DB

	mu        sync.Mutex
	listeners map[string]chan waitDecision
	sweeperWg sync.WaitGroup
	stopCh    chan struct{}

	// resumer, when wired, is invoked with a waitpoint's pipeline_run_id
	// the moment a timeout sweep flips it terminal (#1425). Without it a
	// timed-out approval only signals the in-memory WaitFor listener — but
	// an async-parked run has NO live listener (it released its slot and
	// returned WAITING), so nothing drove it out of 'waiting' until the
	// next process restart. Production wires this to
	// Executor.ResumeAfterApproval so the parked run fails/continues per
	// its on_fail immediately.
	resumer func(runID string)

	// trust reads standing approval grants. It shares this store's DB
	// handle and has no dependencies of its own, so the feature needs no
	// bootstrap wiring — every construction of a waitpoint store gets it.
	trust *TrustGrantStore
}

// SetTimeoutResumer wires the callback the sweeper (and boot recovery)
// invokes with a waitpoint's pipeline_run_id when a timeout flips it
// terminal, so a parked async run resumes immediately instead of stranding
// in 'waiting' until restart. Safe to leave unset (no resume cascade).
func (s *SQLWaitpointStore) SetTimeoutResumer(fn func(runID string)) {
	s.mu.Lock()
	s.resumer = fn
	s.mu.Unlock()
}

// resumeRun invokes the wired resumer (if any) on a detached goroutine so a
// slow resume can't stall the sweep loop. Snapshotting under the lock keeps
// SetTimeoutResumer race-free.
func (s *SQLWaitpointStore) resumeRun(runID string) {
	if runID == "" {
		return
	}
	s.mu.Lock()
	fn := s.resumer
	s.mu.Unlock()
	if fn != nil {
		go fn(runID)
	}
}

// waitDecision is the value passed through the channel when an
// approval/denial fires. approved=true → return true; otherwise
// false. We don't surface the decision_payload through the
// goroutine path in MVP — Phase 2 may pipe it through.
type waitDecision struct {
	approved bool
}

// NewSQLWaitpointStore constructs a store and starts the timeout
// sweeper. Caller must call Close() to stop the sweeper goroutine
// at process shutdown.
func NewSQLWaitpointStore(db *sql.DB) *SQLWaitpointStore {
	s := &SQLWaitpointStore{
		db:        db,
		listeners: make(map[string]chan waitDecision),
		stopCh:    make(chan struct{}),
		trust:     NewTrustGrantStore(db),
	}
	s.sweeperWg.Add(1)
	go s.sweeper()
	return s
}

// RecoverPending eagerly sweeps any waitpoints whose timeout already
// elapsed (the regular sweeper would catch them within 30 s, but at
// boot we want fast cleanup) and reports how many pending entries
// remain. Pending waitpoints from before this process started are
// "stranded" — the goroutine that called WaitFor is gone with the
// previous lifetime. Approving them via the inbox still updates the
// DB (the row is real), but the parent run cannot resume because its
// in-memory state is lost. This method does NOT auto-mark them as
// abandoned so an operator can still see + decide on them; the count
// flows to the boot log so abnormal accumulation is visible.
//
// Returns (timedOutCount, pendingCount, err). The server's main.go
// calls this once before declaring readiness.
//
// Timestamp format: timeout_at is stored as RFC3339Nano (set by
// CreateApproval); we compare against the same format here. SQLite's
// default `datetime('now','subsec')` would NOT lex-sort against
// RFC3339Nano (no T separator, no Z suffix) — so passing the same
// Go-formatted now to BOTH the SET and WHERE clauses keeps the
// compare consistent with the stored values.
func (s *SQLWaitpointStore) RecoverPending(ctx context.Context) (timedOut int, pending int, err error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// UPDATE first, then SELECT the rows we actually transitioned by
	// matching on (status='timed_out' AND decided_at = now). This
	// closes the SELECT-then-UPDATE race: if CompleteApproval wins
	// between the original pre-SELECT and the UPDATE, the row's
	// status flips to approved/denied and the timed_out filter
	// won't pick it up — so we won't cascade a wrong timeout signal
	// into the inbox.
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_waitpoints
SET status = 'timed_out', decided_at = ?
WHERE status = 'pending' AND timeout_at <= ?`, now, now)
	if err != nil {
		return 0, 0, fmt.Errorf("waitpoints: recover sweep: %w", err)
	}
	n, _ := res.RowsAffected()
	timedOut = int(n)

	if timedOut > 0 {
		rows, qerr := s.db.QueryContext(ctx, `
SELECT token, pipeline_run_id FROM pipeline_waitpoints
WHERE status = 'timed_out' AND decided_at = ?`, now)
		if qerr != nil {
			return timedOut, 0, fmt.Errorf("waitpoints: recover transitioned scan: %w", qerr)
		}
		type expiredWP struct{ token, runID string }
		var expired []expiredWP
		for rows.Next() {
			var e expiredWP
			if scanErr := rows.Scan(&e.token, &e.runID); scanErr == nil {
				expired = append(expired, e)
			}
		}
		rows.Close()
		// Mirror each timeout into the inbox so the "blocking" row
		// clears at the same moment the source becomes terminal, and
		// resume the parked run if a resumer is wired (#1425). At boot the
		// resumer is usually unset — the boot run-recovery scan handles
		// these — so resumeRun is a no-op there.
		for _, wp := range expired {
			inbox.ResolveBySource(ctx, s.db, slog.Default(), "waitpoint", wp.token, "timed_out", "")
			s.resumeRun(wp.runID)
		}
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipeline_waitpoints WHERE status = 'pending'`,
	).Scan(&pending); err != nil {
		return timedOut, 0, fmt.Errorf("waitpoints: recover count: %w", err)
	}
	return timedOut, pending, nil
}

// Close stops the timeout sweeper. Safe to call multiple times.
func (s *SQLWaitpointStore) Close() {
	s.mu.Lock()
	select {
	case <-s.stopCh:
		s.mu.Unlock()
		return
	default:
		close(s.stopCh)
	}
	s.mu.Unlock()
	s.sweeperWg.Wait()
}

// CreateApproval mints a token, persists the waitpoint row, and
// returns the token. Default timeout is 24h if req.TimeoutSec is 0.
func (s *SQLWaitpointStore) CreateApproval(ctx context.Context, req WaitpointApprovalRequest) (string, error) {
	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 24 * 3600
	}
	token := generateWaitpointToken()
	timeoutAt := time.Now().Add(time.Duration(timeoutSec) * time.Second).UTC().Format(time.RFC3339Nano) // tsformat:allow: timeout_at is string-compared by the sweep query in sweepOnce; the whole column is RFC3339Nano and tsformat would order differently against existing rows

	// Standing trust: an operator who has waved this exact gate through
	// on this exact routine body can have said "stop asking me". Consult
	// that BEFORE writing a pending row, so a trusted gate never parks
	// the run and never mints an inbox card that nobody needs to answer.
	//
	// Every failure here falls through to the normal blocking path: a
	// gate whose trust cannot be established is a gate that asks. The
	// lookup is deliberately incapable of failing open.
	trustCtx := s.routineTrust(ctx, req)
	if use, ok := s.consumeTrust(ctx, req, trustCtx); ok {
		payload, _ := json.Marshal(map[string]any{
			"auto_approved":      true,
			"grant_id":           use.GrantID,
			"granted_by_user_id": use.GrantedByUserID,
			"grant_uses":         use.Uses,
			"definition_hash":    trustCtx.definitionHash,
		})
		decidedAt := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: decided_at parity with CompleteApproval, which writes the same format for a human decision — an auto-approval must be indistinguishable in shape from the manual one it stands in for
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO pipeline_waitpoints (
    token, workspace_id, pipeline_run_id, step_id, kind, prompt,
    invoking_crew_id, status, timeout_at, decided_at, decided_by_user_id, decision_payload
) VALUES (?, ?, ?, ?, 'approval', ?, ?, 'approved', ?, ?, ?, ?)`,
			token, req.WorkspaceID, req.PipelineRunID, req.StepID,
			nullableStr(req.Prompt), nullableStr(req.InvokingCrewID), timeoutAt,
			decidedAt, nullableStr(use.GrantedByUserID), string(payload),
		); err != nil {
			return "", fmt.Errorf("waitpoints: insert auto-approved: %w", err)
		}
		// The row is written approved, so WaitFor resolves from
		// checkDecided. The listener is still pre-created to keep the
		// one shape every token has in the map.
		s.mu.Lock()
		s.listeners[token] = make(chan waitDecision, 1)
		s.mu.Unlock()
		return token, nil
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO pipeline_waitpoints (
    token, workspace_id, pipeline_run_id, step_id, kind, prompt,
    invoking_crew_id, status, timeout_at
) VALUES (?, ?, ?, ?, 'approval', ?, ?, 'pending', ?)`,
		token, req.WorkspaceID, req.PipelineRunID, req.StepID,
		nullableStr(req.Prompt), nullableStr(req.InvokingCrewID), timeoutAt,
	)
	if err != nil {
		return "", fmt.Errorf("waitpoints: insert: %w", err)
	}
	// Mirror into the unified inbox so the bell + /inbox light up
	// the moment the wait step fires. Best-effort: a SQL error here
	// is logged but doesn't fail the waitpoint creation — the
	// pipeline_waitpoints row remains the source of truth and a
	// follow-up rebuild job can backfill missed projections.
	// Title is a one-line label, not a body excerpt. The prompt is often
	// a multi-line markdown change plan ("Approve this…\n\n## Change
	// Plan…"); CleanTitle takes the first line, drops markdown markers,
	// and truncates — so the list row reads cleanly instead of dragging
	// "\n\n##" into the title. Body is redacted in case the prompt quotes
	// a credential.
	// Redact the prompt before deriving the title — CleanTitle takes the
	// first line verbatim, so a credential quoted there would otherwise
	// land in the title even though the body below is redacted.
	//
	// An author-declared approval_title wins when there is one: the
	// prompt's first line is boilerplate and identical on every
	// invocation, so three pending approvals for three different
	// production changes were indistinguishable in the list. The title
	// is redacted and cleaned on the same path as the fallback, because
	// it is templated and `{{ inputs.token }}` renders just as happily
	// as `{{ inputs.action }}`.
	title := inbox.CleanTitle(inbox.RedactSecrets(req.Prompt), 80, "Waitpoint pending approval")
	if strings.TrimSpace(req.Title) != "" {
		title = inbox.CleanTitle(inbox.RedactSecrets(req.Title), 80, title)
	}
	// Give the row a sender so the UI can render an icon + "From" line
	// instead of a blank. The invoking crew is the most meaningful actor
	// behind a pipeline approval; fall back to a generic label.
	riskLevel := req.RiskLevel
	if riskLevel == "" {
		riskLevel = "normal"
	}
	senderName := "Approval required"
	if req.InvokingCrewID != "" {
		var name string
		if err := s.db.QueryRowContext(ctx,
			`SELECT name FROM crews WHERE id = ?`, req.InvokingCrewID).Scan(&name); err == nil && name != "" {
			senderName = name
		}
	}
	inbox.Insert(ctx, s.db, slog.Default(), inbox.Item{
		WorkspaceID: req.WorkspaceID,
		Kind:        inbox.KindWaitpoint,
		SourceID:    token,
		TargetRole:  "MANAGER",
		Title:       title,
		BodyMD:      inbox.RedactSecrets(req.Prompt),
		SenderType:  "pipeline",
		SenderName:  senderName,
		Priority:    "high",
		Blocking:    true,
		Payload: map[string]interface{}{
			"pipeline_run_id":  req.PipelineRunID,
			"step_id":          req.StepID,
			"invoking_crew_id": req.InvokingCrewID,
			"timeout_at":       timeoutAt,
			// Carries the "you have approved this N times — always
			// allow?" offer, with every coordinate the grant call needs
			// so the surface asking the question never has to re-derive
			// which routine body was on screen.
			"trust_offer": s.trustOffer(ctx, req, trustCtx),
			// Author-declared, defaulted here rather than in the UI so
			// every consumer (inbox, CLI, notification channels) reads
			// the same value instead of each inventing its own default.
			"risk_level": riskLevel,
		},
	})
	// Pre-create the listener channel so a fast CompleteApproval
	// (somehow racing the WaitFor) doesn't get lost. Buffered 1
	// so a complete-then-wait still delivers.
	s.mu.Lock()
	s.listeners[token] = make(chan waitDecision, 1)
	s.mu.Unlock()
	return token, nil
}

// TrustOfferThreshold is how many times an operator must have approved
// the same gate, on the same routine body, before they are offered a
// standing grant.
//
// Three, not one: the first approval is how you learn what the gate
// does, the second is how you learn it is repetitive, and the third is
// where "am I adding any judgement here?" is answerable honestly. Set it
// lower and the product is nagging people to disarm gates they have not
// understood yet.
const TrustOfferThreshold = 3

// routineTrustCtx is the routine identity behind a waitpoint: which
// definition body the run is executing, and how much autonomy its crew
// was given. Every field is best-effort; a zero value means "we could
// not establish trust", which the callers treat as "so ask a human".
type routineTrustCtx struct {
	pipelineID     string
	definitionHash string
	autonomy       string // crews.autonomy_level; "" when the run has no crew
}

// routineTrust resolves the run's routine identity. Errors are swallowed
// into a zero value on purpose: this lookup can only ever REMOVE a
// human from the loop, so every uncertain answer must be the one that
// keeps them in it.
func (s *SQLWaitpointStore) routineTrust(ctx context.Context, req WaitpointApprovalRequest) routineTrustCtx {
	var out routineTrustCtx
	var crewID string
	err := s.db.QueryRowContext(ctx, `
SELECT pipeline_id, COALESCE(definition_hash, ''), COALESCE(invoking_crew_id, '')
  FROM pipeline_runs WHERE id = ?`, req.PipelineRunID).Scan(&out.pipelineID, &out.definitionHash, &crewID)
	if err != nil {
		// A run row we cannot read (test rigs that never insert one,
		// a call_pipeline child, a deleted run) simply gets the
		// blocking path it would have got before this feature existed.
		return routineTrustCtx{}
	}
	if req.InvokingCrewID != "" {
		crewID = req.InvokingCrewID
	}
	if crewID == "" {
		// Fall back to the routine's AUTHOR crew. invoking_crew_id is
		// empty for the ordinary case — a user triggering a routine from
		// the CLI or the dashboard — so reading only that would apply the
		// strict posture to cross-crew invocations and quietly skip it
		// for the runs operators actually make.
		//
		// The author crew is the right one on the merits, not just as a
		// fallback: a routine executes in its author's context, reusing
		// that crew's persona and credentials. The posture that governs
		// the execution is that crew's.
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(author_crew_id, '') FROM pipelines WHERE id = ?`, out.pipelineID).Scan(&crewID); err != nil {
			// Cannot establish which crew owns this routine → assume the
			// strictest posture rather than auto-approving blind.
			out.autonomy = string(policy.AutonomyStrict)
			return out
		}
	}
	if crewID == "" {
		// A routine with no author crew at all: no crew-level posture
		// exists to respect, and the grant was still an explicit
		// per-gate decision by a named human.
		return out
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(autonomy_level, '') FROM crews WHERE id = ?`, crewID).Scan(&out.autonomy); err != nil {
		// Cannot read the dial → assume the strictest posture.
		out.autonomy = string(policy.AutonomyStrict)
	}
	return out
}

// consumeTrust fires a standing grant if one covers this gate.
func (s *SQLWaitpointStore) consumeTrust(ctx context.Context, req WaitpointApprovalRequest, tc routineTrustCtx) (TrustGrantUse, bool) {
	if tc.pipelineID == "" || tc.definitionHash == "" {
		return TrustGrantUse{}, false
	}
	// A strict crew has opted out of every shortcut around the operator.
	// v106 made the identical call for self-learning: a strict crew
	// cannot self-promote "regardless of this flag".
	if tc.autonomy == string(policy.AutonomyStrict) {
		return TrustGrantUse{}, false
	}
	use, ok, err := s.trust.Consume(ctx, req.WorkspaceID, tc.pipelineID, req.StepID, tc.definitionHash)
	if err != nil {
		slog.Default().Warn("waitpoints: trust grant lookup failed, falling back to blocking approval",
			"error", err, "run_id", req.PipelineRunID, "step_id", req.StepID)
		return TrustGrantUse{}, false
	}
	return use, ok
}

// trustOffer builds the "stop asking me?" offer attached to a blocking
// inbox card. Ineligible offers are still emitted (eligible=false) so the
// payload shape is stable for consumers.
func (s *SQLWaitpointStore) trustOffer(ctx context.Context, req WaitpointApprovalRequest, tc routineTrustCtx) map[string]any {
	offer := map[string]any{"eligible": false}
	if tc.pipelineID == "" || tc.definitionHash == "" || tc.autonomy == string(policy.AutonomyStrict) {
		return offer
	}
	prior, err := s.trust.PriorApprovals(ctx, req.WorkspaceID, tc.pipelineID, req.StepID, tc.definitionHash)
	if err != nil {
		slog.Default().Warn("waitpoints: prior-approval count failed, offering nothing",
			"error", err, "run_id", req.PipelineRunID, "step_id", req.StepID)
		return offer
	}
	offer["prior_approvals"] = prior
	if prior < TrustOfferThreshold {
		return offer
	}
	offer["eligible"] = true
	offer["threshold"] = TrustOfferThreshold
	offer["pipeline_id"] = tc.pipelineID
	offer["step_id"] = req.StepID
	offer["definition_hash"] = tc.definitionHash
	return offer
}

// WaitFor blocks until the waitpoint resolves or ctx is cancelled.
// Returns approved=true on approval, false on denial / timeout /
// cancellation.
//
// Order of operations is critical for avoiding the lost-wakeup race:
// the listener channel is registered BEFORE the decided-state DB
// check, so CompleteApproval racing with us either delivers to the
// channel or we observe its DB write — never both miss. Without the
// pre-registration, CompleteApproval firing between checkDecided and
// listener-insert would signal to a nonexistent channel and the
// goroutine would park forever.
func (s *SQLWaitpointStore) WaitFor(ctx context.Context, token string) (bool, error) {
	s.mu.Lock()
	ch, ok := s.listeners[token]
	if !ok {
		// Pre-register the listener while holding the mutex so any
		// CompleteApproval that arrives after this point has a
		// channel to deliver to. The DB re-check below covers the
		// case where CompleteApproval already fired before we
		// pre-registered.
		ch = make(chan waitDecision, 1)
		s.listeners[token] = ch
	}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.listeners, token)
		s.mu.Unlock()
	}()

	// Now that the listener is registered, re-check DB state. If
	// CompleteApproval ran before our listener was in place, the
	// signal hit `default` (no listener) — but the DB write is
	// committed, so checkDecided observes it. If CompleteApproval
	// runs AFTER listener registration, it delivers to the channel
	// directly. Either way, no lost wake-up.
	decided, approved, err := s.checkDecided(ctx, token)
	if err != nil {
		return false, err
	}
	if decided {
		return approved, nil
	}

	select {
	case d := <-ch:
		return d.approved, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// FindApprovalForStep implements WaitpointResumer: it returns the
// most recent approval-kind token for (pipelineRunID, stepID), or ""
// when none exists. Status is deliberately NOT filtered — a token
// decided (or timed out by the boot sweep) between the kill and the
// resume should be returned so WaitFor resolves it from DB state,
// rather than the resumed step minting a fresh approval and silently
// resurrecting an already-answered question.
func (s *SQLWaitpointStore) FindApprovalForStep(ctx context.Context, pipelineRunID, stepID string) (string, error) {
	var token string
	err := s.db.QueryRowContext(ctx, `
SELECT token FROM pipeline_waitpoints
WHERE pipeline_run_id = ? AND step_id = ? AND kind = 'approval'
ORDER BY created_at DESC LIMIT 1`, pipelineRunID, stepID).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("waitpoints: find for step: %w", err)
	}
	return token, nil
}

// RunIDForToken returns the pipeline_run_id a waitpoint token belongs to.
// The approve handler uses it to trigger an in-process resume of the parked
// run after recording the decision (async WAITING model).
func (s *SQLWaitpointStore) RunIDForToken(ctx context.Context, token string) (string, error) {
	var runID string
	err := s.db.QueryRowContext(ctx,
		`SELECT pipeline_run_id FROM pipeline_waitpoints WHERE token = ?`, token,
	).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("waitpoints: run id for token: %w", err)
	}
	return runID, nil
}

// WorkspaceIDForToken returns the workspace a waitpoint token belongs
// to. The public token-callback endpoint (no workspace JWT — the
// token itself is the bearer credential) uses this to look up the
// token's own workspace and feed it back into CompleteApproval, so
// that call site conforms to the same workspace-scoped UPDATE as the
// authed inbox path without changing who is allowed to complete a
// public token (still: anyone holding the token).
func (s *SQLWaitpointStore) WorkspaceIDForToken(ctx context.Context, token string) (string, error) {
	var workspaceID string
	err := s.db.QueryRowContext(ctx,
		`SELECT workspace_id FROM pipeline_waitpoints WHERE token = ?`, token,
	).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("waitpoints: workspace id for token: %w", err)
	}
	return workspaceID, nil
}

// WaitpointStatus implements WaitpointStatusReader: it returns the
// row's current status string so the wait step can distinguish a
// timeout/cancellation from a human denial after WaitFor reports
// approved=false. Every negative transition (CompleteApproval,
// sweeper, RecoverPending) commits the status to the DB before
// signalling the listener channel, so this read is never ahead of
// the decision the waiter observed.
func (s *SQLWaitpointStore) WaitpointStatus(ctx context.Context, token string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("waitpoint %q not found", token)
	}
	if err != nil {
		return "", err
	}
	return status, nil
}

// CompleteApproval marks the waitpoint approved/denied and signals
// any waiting goroutine. Called by the public HTTP endpoint when
// an operator clicks approve/deny in the inbox.
//
// workspaceID scopes the UPDATE so a caller can only complete a
// waitpoint that belongs to their own workspace — mirrors the
// rec.WorkspaceID != workspaceID guard SignalRun uses
// (pipeline_run_metadata.go). Without it, a MANAGER of workspace A who
// learns/guesses a pending token from workspace B could approve/deny
// B's gated run (issue #1415). A workspace mismatch affects zero rows,
// same as an unknown token or an already-decided one, so it surfaces
// as ErrAlreadyDecided — the caller can't distinguish "wrong
// workspace" from "already decided" from the error alone, which is
// the same non-disclosure property SignalRun's 404 gives.
//
// Returns ErrAlreadyDecided if the waitpoint already has a final
// status (or belongs to a different workspace) — protects against
// double-decide races and cross-tenant completion alike.
func (s *SQLWaitpointStore) CompleteApproval(ctx context.Context, workspaceID, token string, approved bool, deciderUserID, payload string) error {
	status := "approved"
	if !approved {
		status = "denied"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_waitpoints
SET status = ?, decided_at = ?, decided_by_user_id = ?, decision_payload = ?
WHERE token = ? AND workspace_id = ? AND status = 'pending'`,
		status, now, nullableStr(deciderUserID), nullableStr(payload), token, workspaceID,
	)
	if err != nil {
		return fmt.Errorf("waitpoints: update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAlreadyDecided
	}
	// Mirror the decision into the unified inbox so the row drops
	// from "needs action" into the resolved feed in real time. The
	// pipeline_waitpoints UPDATE has already committed by the time
	// we get here, so a failure in the projection write is safe to
	// log + swallow — a follow-up rebuild can patch up missed rows.
	inbox.ResolveBySource(ctx, s.db, slog.Default(), "waitpoint", token, status, deciderUserID)
	s.mu.Lock()
	if ch, ok := s.listeners[token]; ok {
		select {
		case ch <- waitDecision{approved: approved}:
		default:
			// Buffered channel full — already delivered. Safe
			// to drop; the decision is in the DB.
		}
	}
	s.mu.Unlock()
	return nil
}

// CancelWaitpointsForRun flips every still-pending waitpoint belonging to a
// run to 'cancelled' (#1426, 3.1/3.2). Used when a parked or blocking run is
// cancelled or dies: without it the inbox "needs approval" card stays
// actionable and an approve/deny would resolve a waitpoint whose run is gone.
// Cascades into the inbox projection and wakes any live WaitFor listener so a
// blocking run unblocks immediately. Returns the number of waitpoints
// cancelled. Best-effort inbox/listener cascade mirrors CompleteApproval.
func (s *SQLWaitpointStore) CancelWaitpointsForRun(ctx context.Context, pipelineRunID string) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: decided_at is stored/compared as RFC3339Nano throughout this store (see the timeout sweep match) — format parity is required, tsformat would mismatch
	rows, err := s.db.QueryContext(ctx,
		`SELECT token FROM pipeline_waitpoints WHERE pipeline_run_id = ? AND status = 'pending'`,
		pipelineRunID)
	if err != nil {
		return 0, fmt.Errorf("waitpoints: cancel-for-run scan: %w", err)
	}
	var tokens []string
	for rows.Next() {
		var tok string
		if scanErr := rows.Scan(&tok); scanErr == nil {
			tokens = append(tokens, tok)
		}
	}
	rows.Close()

	cancelled := 0
	for _, tok := range tokens {
		res, execErr := s.db.ExecContext(ctx, `
UPDATE pipeline_waitpoints
SET status = 'cancelled', decided_at = ?
WHERE token = ? AND status = 'pending'`, now, tok)
		if execErr != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		cancelled++
		inbox.ResolveBySource(ctx, s.db, slog.Default(), "waitpoint", tok, "cancelled", "")
		s.mu.Lock()
		if ch, ok := s.listeners[tok]; ok {
			select {
			case ch <- waitDecision{approved: false}:
			default:
			}
		}
		s.mu.Unlock()
	}
	return cancelled, nil
}

// CancelOrphanedWaitpoints settles every pending waitpoint whose run can no
// longer act on it — #2163.
//
// A waitpoint is reachable only while its run can still resume. Once the run
// is terminal (interrupted, failed, completed, cancelled) or its row is gone
// entirely, no decision on that gate can ever execute: the inbox would keep
// offering it, the approve endpoint would accept it, the row would flip to
// resolved, and nothing would run. The operator is told they approved a
// production change that did not happen.
//
// MarkInterrupted now cascades so this cannot recur, but every install that
// upgrades still carries the rows that already have the defect. This is the
// repair for those, run once per boot. Idempotent — a second pass finds
// nothing, because the first left no pending rows behind.
//
// Deliberately not scoped to a workspace: the invariant is global and a boot
// sweep has no workspace context. Returns how many were settled so the wireup
// can log an abnormal count.
func (s *SQLWaitpointStore) CancelOrphanedWaitpoints(ctx context.Context) (int, error) {
	// LEFT JOIN so a waitpoint whose run row was deleted outright is caught
	// too — that is the same unreachable gate with less evidence.
	rows, err := s.db.QueryContext(ctx, `
SELECT w.pipeline_run_id
  FROM pipeline_waitpoints w
  LEFT JOIN pipeline_runs r ON r.id = w.pipeline_run_id
 WHERE w.status = 'pending'
   AND (r.id IS NULL OR r.status IN ('interrupted','failed','completed','cancelled'))
 GROUP BY w.pipeline_run_id`)
	if err != nil {
		return 0, fmt.Errorf("waitpoints: orphan scan: %w", err)
	}
	var runIDs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			runIDs = append(runIDs, id)
		}
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return 0, fmt.Errorf("waitpoints: orphan scan: %w", rerr)
	}

	total := 0
	for _, runID := range runIDs {
		// Reuse the cancel path so the inbox cascade and any live listener
		// wake exactly as they do everywhere else.
		n, cerr := s.CancelWaitpointsForRun(ctx, runID)
		if cerr != nil {
			return total, fmt.Errorf("waitpoints: cancel orphans for run %s: %w", runID, cerr)
		}
		total += n
	}
	return total, nil
}

// WaitpointCanceller is the optional capability a WaitpointStore implements to
// cancel a run's pending waitpoints. Detected via type assertion so test
// stubs implementing only WaitpointStore keep working.
type WaitpointCanceller interface {
	CancelWaitpointsForRun(ctx context.Context, pipelineRunID string) (int, error)
}

// ErrAlreadyDecided is returned by CompleteApproval when the
// waitpoint isn't in pending state.
var ErrAlreadyDecided = errors.New("waitpoint: already decided or expired")

// checkDecided reads the current status from DB. Used by WaitFor
// when the in-memory listener is gone (post-restart recovery).
func (s *SQLWaitpointStore) checkDecided(ctx context.Context, token string) (decided bool, approved bool, err error) {
	var status string
	err = s.db.QueryRowContext(ctx,
		`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, fmt.Errorf("waitpoint %q not found", token)
	}
	if err != nil {
		return false, false, err
	}
	switch status {
	case "pending":
		return false, false, nil
	case "approved":
		return true, true, nil
	case "denied", "timed_out", "cancelled":
		return true, false, nil
	}
	return false, false, fmt.Errorf("waitpoint %q unknown status %q", token, status)
}

// sweeper marks expired pending waitpoints as timed_out and signals
// any in-memory listeners. Runs every 30s; cheap thanks to the
// partial index on (status='pending', timeout_at).
func (s *SQLWaitpointStore) sweeper() {
	defer s.sweeperWg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.sweepOnce()
		}
	}
}

func (s *SQLWaitpointStore) sweepOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// timeout_at is RFC3339Nano (set by CreateApproval); we compare
	// against the same format. SQLite's `datetime('now','subsec')`
	// emits "YYYY-MM-DD HH:MM:SS.sss" which does NOT lex-sort against
	// RFC3339Nano values, so a naive compare lets some timeouts slip
	// past the regular sweep until RecoverPending runs at next boot.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `
SELECT token, pipeline_run_id FROM pipeline_waitpoints
WHERE status = 'pending' AND timeout_at <= ?
LIMIT 200`, now)
	if err != nil {
		return
	}
	defer rows.Close()
	type expiredWP struct{ token, runID string }
	var expired []expiredWP
	for rows.Next() {
		var e expiredWP
		if err := rows.Scan(&e.token, &e.runID); err == nil {
			expired = append(expired, e)
		}
	}
	_ = rows.Err()
	for _, wp := range expired {
		tok := wp.token
		// Gate the cascade on whether THIS UPDATE actually flipped
		// the row. RowsAffected==0 means CompleteApproval (or
		// another sweep) already moved the waitpoint terminal —
		// re-firing the timeout signal here would deliver the
		// wrong outcome to a WaitFor goroutine and resolve the
		// inbox row with a stale "timed_out" action.
		res, execErr := s.db.ExecContext(ctx, `
UPDATE pipeline_waitpoints
SET status = 'timed_out', decided_at = ?
WHERE token = ? AND status = 'pending'`, now, tok)
		if execErr != nil {
			continue
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		// Cascade into the inbox projection so the user's "needs
		// approval" row clears at the same moment the source
		// becomes terminal. Idempotent at the SQL layer.
		inbox.ResolveBySource(ctx, s.db, slog.Default(), "waitpoint", tok, "timed_out", "")
		s.mu.Lock()
		if ch, ok := s.listeners[tok]; ok {
			select {
			case ch <- waitDecision{approved: false}:
			default:
			}
		}
		s.mu.Unlock()
		// Drive the parked run out of 'waiting' (#1425). A live in-memory
		// WaitFor listener (blocking-mode run) is woken by the signal above;
		// an async-parked run has none, so without this it strands in
		// 'waiting' until restart. resumeRun is a no-op when no resumer is
		// wired and detaches so a slow resume can't stall the sweep.
		s.resumeRun(wp.runID)
	}
}

// generateWaitpointToken returns a 32-hex-char random token. We
// don't bother with a prefix — these are URL fragments and tokens,
// not entity IDs. Random bytes from crypto/rand.
func generateWaitpointToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-derived bytes if rand fails (extremely
		// unlikely; better than panicking and breaking the run).
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (i * 8))
		}
	}
	return hex.EncodeToString(b)
}

// nullableStr converts an empty string to SQL NULL via NullString.
// Mirrors the helper in store.go but kept local to avoid pulling
// store.go's transitive deps into a Phase 2 file.
func nullableStr(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}
