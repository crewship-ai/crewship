package api

// The workspace-scoped read surface over the durable work ledger
// (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
//
// internal/work owns dispatch — claim, retry, recovery. This file owns only
// the question "what happened to my work", and the two ways an operator is
// allowed to act on the answer: cancel and replay.
//
// Three things this surface deliberately does NOT return, because §9 forbids
// them and a read API is exactly where they would leak:
//
//   - work_items.input_json. It is the accepted input, which for a chat or
//     assignment source IS the conversation text. The immutable fingerprint
//     (input_sha256) and the revision it was accepted against are returned
//     instead, which is what an operator needs to tell two work items apart
//     without reading either one's contents.
//   - work_events.detail_json. Free-form, written by whatever produced the
//     transition; nothing constrains it to be safe to publish.
//   - webhook_deliveries.raw_body — see webhook_deliveries.go.
//
// And the authorization note from §9, which is the whole reason a receipt is
// modelled as an identifier: a webhook token is permission to DELIVER, never
// permission to read the work the delivery produced. Every route here is
// behind RequireAuth + RequireWorkspace and re-checks the caller's role, so a
// leaked work id buys nothing.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/work"
)

// WorkItemsHandler reads the work ledger and serves the two operator actions
// on it. Same shape as the other definition handlers (ProviderPoolHandler):
// a database and a logger, no cached state, so nothing here can go stale
// against a ledger three other processes are writing.
type WorkItemsHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewWorkItemsHandler(db *sql.DB, logger *slog.Logger) *WorkItemsHandler {
	return &WorkItemsHandler{db: db, logger: logger}
}

// workItemView is the wire shape of one work item. Every field is emitted on
// every response (no omitempty anywhere), so the generated `required` list
// derived from these tags is the truth — see
// TestOpenAPIRequired_MatchesTheStructsOwnJSONTags.
type workItemView struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`

	Source     string `json:"source"`
	SourceRef  string `json:"source_ref"`
	DomainKind string `json:"domain_kind"`
	DomainID   string `json:"domain_id"`

	AgentID   string `json:"agent_id"`
	CrewID    string `json:"crew_id"`
	SessionID string `json:"session_id"`
	Class     string `json:"class"`

	// Who admitted it. I8 re-checks this at dispatch, so an operator reading a
	// stuck item needs to see whose authorization it is still carrying.
	AuthorizedByUserID string `json:"authorized_by_user_id"`

	// The input's fingerprint, never the input.
	InputSHA256    string `json:"input_sha256"`
	TargetRevision string `json:"target_revision"`

	State       string `json:"state"`
	StateReason string `json:"state_reason"`
	Generation  int64  `json:"generation"`
	// AttemptCount, not "attempts": the detail's `attempts` is the LIST, and
	// one key that is a number on the summary and an array on the detail is how
	// a client ends up branching on typeof.
	AttemptCount int `json:"attempt_count"`
	Priority     int `json:"priority"`

	EligibleAt string  `json:"eligible_at"`
	DeadlineAt *string `json:"deadline_at"`

	ReplayOf     *string `json:"replay_of"`
	ReplayReason string  `json:"replay_reason"`

	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	TerminalAt *string `json:"terminal_at"`
}

// workAttemptView is one attempt. run_id is the same namespace as
// agent_runs.id, which is what lets the UI join an attempt to its journal.
type workAttemptView struct {
	RunID      string `json:"run_id"`
	Attempt    int    `json:"attempt"`
	Generation int64  `json:"generation"`

	LeaseOwner     string `json:"lease_owner"`
	LeaseExpiresAt string `json:"lease_expires_at"`
	HeartbeatAt    string `json:"heartbeat_at"`
	// RuntimeLocator is where recovery looks before it decides a process is
	// gone. It is an operational address (a container / process locator), not
	// a credential, and an item parked in needs_reconciliation cannot be
	// diagnosed without it.
	RuntimeLocator string `json:"runtime_locator"`

	StartedAt   string  `json:"started_at"`
	StartReason string  `json:"start_reason"`
	EndedAt     *string `json:"ended_at"`
	EndReason   string  `json:"end_reason"`
	// ExitEvidence is an exit code, a signal or a provider status — §3 forbids
	// credentials in it, and nothing here relaxes that.
	ExitEvidence string  `json:"exit_evidence"`
	CostUSD      float64 `json:"cost_usd"`
}

// workEventView is one row of the append-only history. detail_json is
// deliberately absent (see the file comment).
type workEventView struct {
	Seq        int64  `json:"seq"`
	At         string `json:"at"`
	FromState  string `json:"from_state"`
	ToState    string `json:"to_state"`
	RunID      string `json:"run_id"`
	Generation int64  `json:"generation"`
	Reason     string `json:"reason"`
}

type workItemDetailView struct {
	workItemView
	Attempts []workAttemptView `json:"attempts"`
	Events   []workEventView   `json:"events"`
}

type workItemPage struct {
	Items      []workItemView `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

// workCancelResponse answers a cancel REQUEST, which §4 is explicit is not the
// same thing as a cancelled state. Outcome says what actually happened;
// `state` is always the item's REAL state after the call, including when a
// completion won the transactional race.
type workCancelResponse struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Outcome is one of:
	//   cancelled       the item was queued or waiting to retry and is now
	//                   confirmed stopped, atomically.
	//   requested       the item holds a runtime; the request is recorded and
	//                   the signal is the dispatcher's to deliver. `cancelled`
	//                   follows only on confirmation.
	//   already_terminal  it finished before this call. `state` is the real
	//                   finished state — this endpoint never reports a cancel
	//                   it did not perform.
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

const (
	workCancelOutcomeCancelled = "cancelled"
	workCancelOutcomeRequested = "requested"
	workCancelOutcomeTerminal  = "already_terminal"
)

// cancelRequestedReason prefixes the history row a signalled cancel writes.
// The prefix is matched to keep a repeated request idempotent, so polling the
// endpoint does not grow the history without bound.
const cancelRequestedReason = "cancel requested"

// workItemColumns is the projection every read in this file shares, so the
// list and the detail cannot drift into describing the same row differently.
const workItemColumns = `id, workspace_id, source, source_ref, domain_kind, domain_id,
	agent_id, crew_id, session_id, class, authorized_by_user_id,
	input_sha256, target_revision, state, state_reason, generation, attempts, priority,
	eligible_at, deadline_at, replay_of, replay_reason, created_at, updated_at, terminal_at`

func scanWorkItemView(row interface{ Scan(...any) error }) (workItemView, error) {
	var v workItemView
	var deadline, replayOf, terminal sql.NullString
	if err := row.Scan(
		&v.ID, &v.WorkspaceID, &v.Source, &v.SourceRef, &v.DomainKind, &v.DomainID,
		&v.AgentID, &v.CrewID, &v.SessionID, &v.Class, &v.AuthorizedByUserID,
		&v.InputSHA256, &v.TargetRevision, &v.State, &v.StateReason, &v.Generation, &v.AttemptCount, &v.Priority,
		&v.EligibleAt, &deadline, &replayOf, &v.ReplayReason, &v.CreatedAt, &v.UpdatedAt, &terminal,
	); err != nil {
		return workItemView{}, err
	}
	v.DeadlineAt = workNullString(deadline)
	v.ReplayOf = workNullString(replayOf)
	v.TerminalAt = workNullString(terminal)
	return v, nil
}

func workNullString(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

// workReadContext is the gate every route in this file passes through:
// authenticated, a member of the workspace, and holding at least read on it.
// §9's "API checks read/manage permission and workspace ownership" is this
// function plus the route's own registration.
func workReadContext(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !requireRole(w, r, "read") {
		return "", false
	}
	ws := WorkspaceIDFromContext(r.Context())
	if ws == "" {
		replyError(w, http.StatusBadRequest, "Workspace context required")
		return "", false
	}
	return ws, true
}

// Valid filter vocabularies. An unrecognised value is a 400 rather than an
// empty page: a typo in `?state=runnign` that answers 200 with zero rows reads
// as "nothing is running", which is the wrong answer to a question about a
// queue.
var (
	workStates = map[string]bool{
		"queued": true, "starting": true, "running": true, "waiting": true,
		"retry_wait": true, "succeeded": true, "failed": true, "expired": true,
		"cancelled": true, "needs_reconciliation": true,
	}
	workClasses = map[string]bool{"chat": true, "background": true}
	workSources = map[string]bool{
		"webhook": true, "chat": true, "assignment": true,
		"schedule": true, "pipeline_step": true, "manual": true,
	}
)

// List is a bounded, keyset-paginated read of the ledger.
func (h *WorkItemsHandler) List(w http.ResponseWriter, r *http.Request) {
	ws, ok := workReadContext(w, r)
	if !ok {
		return
	}

	query := `SELECT ` + workItemColumns + ` FROM work_items WHERE workspace_id=? AND id>?`
	args := []any{ws, r.URL.Query().Get("after")}

	if state := r.URL.Query().Get("state"); state != "" {
		if !workStates[state] {
			replyError(w, http.StatusBadRequest, "Unknown state filter: "+state)
			return
		}
		query += ` AND state=?`
		args = append(args, state)
	}
	if class := r.URL.Query().Get("class"); class != "" {
		if !workClasses[class] {
			replyError(w, http.StatusBadRequest, "Unknown class filter: "+class)
			return
		}
		query += ` AND class=?`
		args = append(args, class)
	}
	if source := r.URL.Query().Get("source"); source != "" {
		if !workSources[source] {
			replyError(w, http.StatusBadRequest, "Unknown source filter: "+source)
			return
		}
		query += ` AND source=?`
		args = append(args, source)
	}
	if agentID := r.URL.Query().Get("agent_id"); agentID != "" {
		query += ` AND agent_id=?`
		args = append(args, agentID)
	}
	query += ` ORDER BY id LIMIT 101`

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		replyInternalError(w, h.logger, "list work items", err)
		return
	}
	defer rows.Close()
	items := make([]workItemView, 0)
	for rows.Next() {
		item, err := scanWorkItemView(rows)
		if err != nil {
			replyInternalError(w, h.logger, "scan work item", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "iterate work items", err)
		return
	}
	var next *string
	if len(items) > 100 {
		items = items[:100]
		next = &items[99].ID
	}
	writeJSON(w, http.StatusOK, workItemPage{Items: items, NextCursor: next})
}

// Get returns one work item with its attempts and its whole history.
//
// Attempts and events are read after the item, not in one transaction with it:
// a live item's history genuinely grows while this runs, and a snapshot that
// refused to show an event because it arrived mid-read would be less true, not
// more. The item row is the authoritative state; the events are what led to it.
func (h *WorkItemsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ws, ok := workReadContext(w, r)
	if !ok {
		return
	}
	item, found, err := h.readItem(r, ws, r.PathValue("workItemId"))
	if err != nil {
		replyInternalError(w, h.logger, "read work item", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Work item not found")
		return
	}

	attempts, err := h.readAttempts(r, item.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read work attempts", err)
		return
	}
	events, err := h.readEvents(r, item.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read work history", err)
		return
	}
	writeJSON(w, http.StatusOK, workItemDetailView{workItemView: item, Attempts: attempts, Events: events})
}

// readItem fetches one item and fences it to the workspace. A work item in
// another workspace is absent, not forbidden — answering 403 would confirm the
// id exists to a caller who has no business knowing that.
func (h *WorkItemsHandler) readItem(r *http.Request, workspaceID, workItemID string) (workItemView, bool, error) {
	if strings.TrimSpace(workItemID) == "" {
		return workItemView{}, false, nil
	}
	row := h.db.QueryRowContext(r.Context(),
		`SELECT `+workItemColumns+` FROM work_items WHERE workspace_id=? AND id=?`, workspaceID, workItemID)
	item, err := scanWorkItemView(row)
	if errors.Is(err, sql.ErrNoRows) {
		return workItemView{}, false, nil
	}
	if err != nil {
		return workItemView{}, false, err
	}
	return item, true, nil
}

func (h *WorkItemsHandler) readAttempts(r *http.Request, workItemID string) ([]workAttemptView, error) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT run_id, attempt, generation, lease_owner, lease_expires_at, heartbeat_at,
		       runtime_locator, started_at, start_reason, ended_at, end_reason, exit_evidence, cost_usd
		FROM work_attempts WHERE work_id=? ORDER BY attempt`, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]workAttemptView, 0)
	for rows.Next() {
		var a workAttemptView
		var ended sql.NullString
		if err := rows.Scan(&a.RunID, &a.Attempt, &a.Generation, &a.LeaseOwner, &a.LeaseExpiresAt,
			&a.HeartbeatAt, &a.RuntimeLocator, &a.StartedAt, &a.StartReason, &ended,
			&a.EndReason, &a.ExitEvidence, &a.CostUSD); err != nil {
			return nil, err
		}
		a.EndedAt = workNullString(ended)
		out = append(out, a)
	}
	return out, rows.Err()
}

// readEvents goes through work.Store.History rather than its own SELECT: the
// ledger package owns what an event is and in what order they are true, and a
// second copy of that query here is a second thing to keep in step.
func (h *WorkItemsHandler) readEvents(r *http.Request, workItemID string) ([]workEventView, error) {
	events, err := work.NewStore(h.db).History(r.Context(), workItemID)
	if err != nil {
		return nil, err
	}
	out := make([]workEventView, 0, len(events))
	for _, e := range events {
		out = append(out, workEventView{
			Seq: e.Seq, At: tsformat.Format(e.At), FromState: string(e.FromState),
			ToState: string(e.ToState), RunID: e.RunID, Generation: e.Generation, Reason: e.Reason,
		})
	}
	return out, nil
}

// Cancel records an idempotent cancel REQUEST (§4).
//
// The three answers are genuinely different and the endpoint must not blur
// them:
//
//   - Queued or waiting to retry: nothing is executing, so the stop is taken
//     atomically in the ledger and the item is confirmed `cancelled`.
//   - Holding a runtime (starting / running / waiting / needs_reconciliation):
//     the request is recorded durably and the item stays in its live state.
//     `cancelled` means CONFIRMED stopped, and this call has confirmed
//     nothing — claiming otherwise is how a UI ends up showing a cancelled
//     item whose container is still burning tokens.
//   - Already terminal: a completion won the transactional race. The response
//     carries the REAL finished state. Cancelling does not undo external
//     effects that already happened, and this is where that shows up.
func (h *WorkItemsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	ws, ok := workReadContext(w, r)
	if !ok {
		return
	}
	user := UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		replyError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	item, found, err := h.readItem(r, ws, r.PathValue("workItemId"))
	if err != nil {
		replyInternalError(w, h.logger, "read work item for cancel", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Work item not found")
		return
	}

	state := work.State(item.State)
	if state.Terminal() {
		outcome, detail := workCancelOutcomeTerminal, "This work finished as "+item.State+" before the cancel was applied; nothing was stopped."
		if state == work.StateCancelled {
			// A repeat of a cancel that already completed. Idempotent, and it
			// reads as success rather than as a race it lost.
			outcome, detail = workCancelOutcomeCancelled, "Already cancelled."
		}
		writeJSON(w, http.StatusOK, workCancelResponse{ID: item.ID, State: item.State, Outcome: outcome, Detail: detail})
		return
	}

	// Nothing is executing: take the stop atomically and confirm it.
	if state == work.StateQueued || state == work.StateRetryWait {
		err := work.NewStore(h.db).Transition(r.Context(), work.TransitionRequest{
			WorkID: item.ID,
			To:     work.StateCancelled,
			Reason: cancelRequestedReason + " by " + user.ID,
		})
		switch {
		case err == nil:
			auditFromRequest(r, h.db, "work.cancel", "WORK_ITEM", item.ID, map[string]interface{}{"outcome": workCancelOutcomeCancelled, "from_state": item.State})
			writeJSON(w, http.StatusOK, workCancelResponse{ID: item.ID, State: string(work.StateCancelled),
				Outcome: workCancelOutcomeCancelled, Detail: "Cancelled before it started; no runtime was involved."})
			return
		case errors.Is(err, work.ErrTerminal), errors.Is(err, work.ErrIllegalTransition):
			// It moved under us between the read and the transition — a claim
			// or a completion won. Re-read and answer with what is true now
			// rather than with what we intended.
			h.replyCancelRace(w, r, ws, item.ID)
			return
		default:
			replyInternalError(w, h.logger, "cancel work item", err)
			return
		}
	}

	// A live runtime. Record the request; the dispatcher owns delivering the
	// signal and the 10s grace escalation, and only a confirmed stop writes
	// `cancelled`.
	recorded, err := h.recordCancelRequest(r, item, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "record cancel request", err)
		return
	}
	if recorded {
		auditFromRequest(r, h.db, "work.cancel", "WORK_ITEM", item.ID, map[string]interface{}{"outcome": workCancelOutcomeRequested, "from_state": item.State})
	}
	writeJSON(w, http.StatusOK, workCancelResponse{ID: item.ID, State: item.State, Outcome: workCancelOutcomeRequested,
		Detail: "Cancel requested. This work holds a runtime, so it is signalled rather than stopped here; it becomes cancelled only once the stop is confirmed, and an unstoppable process goes to needs_reconciliation instead."})
}

// replyCancelRace answers with the item's state as of now, after a transition
// lost a race. It never invents an outcome: whatever the ledger says is what
// the caller is told.
func (h *WorkItemsHandler) replyCancelRace(w http.ResponseWriter, r *http.Request, workspaceID, workItemID string) {
	item, found, err := h.readItem(r, workspaceID, workItemID)
	if err != nil {
		replyInternalError(w, h.logger, "re-read work item after cancel race", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Work item not found")
		return
	}
	outcome, detail := workCancelOutcomeRequested, "The item changed state while the cancel was being applied; its current state is authoritative."
	if work.State(item.State).Terminal() {
		outcome = workCancelOutcomeTerminal
		detail = "This work finished as " + item.State + " before the cancel was applied; nothing was stopped."
		if work.State(item.State) == work.StateCancelled {
			outcome, detail = workCancelOutcomeCancelled, "Already cancelled."
		}
	}
	writeJSON(w, http.StatusOK, workCancelResponse{ID: item.ID, State: item.State, Outcome: outcome, Detail: detail})
}

// recordCancelRequest appends the request to the item's history without
// touching its authoritative state, and reports whether it wrote anything.
//
// It is an event with from_state == to_state ON PURPOSE. §3 requires an
// authoritative state change to carry an event; it does not require every
// event to be a state change, and a cancel request that left no durable trace
// would be a promise made only in an HTTP response — gone the moment the
// process that answered it restarts. Keying idempotency on the item's current
// generation is what makes polling this endpoint safe: one request per
// attempt, not one per poll.
func (h *WorkItemsHandler) recordCancelRequest(r *http.Request, item workItemView, userID string) (bool, error) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var existing int
	if err := tx.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM work_events WHERE work_id=? AND generation=? AND reason LIKE ?`,
		item.ID, item.Generation, cancelRequestedReason+"%").Scan(&existing); err != nil {
		return false, err
	}
	if existing > 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO work_events (work_id, seq, at, from_state, to_state, run_id, generation, reason)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM work_events WHERE work_id = ?), ?, ?, ?, '', ?, ?)`,
		item.ID, item.ID, tsformat.Format(time.Now().UTC()), item.State, item.State, item.Generation,
		cancelRequestedReason+" by "+userID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Replay mints a NEW work item from a finished one (§4).
//
// It is not a retry. A retry keeps the work id and mints a run id, and the
// queue does that on its own for a safely repeatable failure. A replay is a
// new authorization: it carries the CURRENT caller's identity, not the
// original one, and it says so in the ledger via replay_of and a reason.
func (h *WorkItemsHandler) Replay(w http.ResponseWriter, r *http.Request) {
	ws, ok := workReadContext(w, r)
	if !ok {
		return
	}
	user := UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		replyError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	var body struct {
		Reason string `json:"reason"`
		// TargetRevision re-targets the replay. §4: running against a
		// different revision must be EXPLICIT, so an absent field inherits
		// the original and never guesses "latest".
		TargetRevision string `json:"target_revision"`
	}
	// The body is optional — a bare replay is legal — but an unknown field is
	// refused rather than acknowledged and ignored, because the one field that
	// changes what runs (target_revision) must never be silently dropped
	// because of a typo.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	item, found, err := h.readItem(r, ws, r.PathValue("workItemId"))
	if err != nil {
		replyInternalError(w, h.logger, "read work item for replay", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Work item not found")
		return
	}
	if !work.State(item.State).Terminal() {
		replyError(w, http.StatusConflict, "This work is still "+item.State+
			"; replay creates a second run of the same input and is only available once the original has finished. Cancel it first if it is stuck.")
		return
	}
	if reason, available, err := h.replayAvailability(r, ws, item); err != nil {
		replyInternalError(w, h.logger, "check replay availability", err)
		return
	} else if !available {
		// The button the UI would otherwise offer cannot work. Say why, in the
		// response, rather than failing on a NULL somewhere downstream.
		replyError(w, http.StatusConflict, reason)
		return
	}

	// Read the immutable input inside the same request that copies it. It is
	// never returned to the caller — the replay carries it forward, the
	// response does not.
	var inputJSON string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT input_json FROM work_items WHERE workspace_id=? AND id=?`, ws, item.ID).Scan(&inputJSON); err != nil {
		replyInternalError(w, h.logger, "read work input for replay", err)
		return
	}

	targetRevision := item.TargetRevision
	if strings.TrimSpace(body.TargetRevision) != "" {
		targetRevision = strings.TrimSpace(body.TargetRevision)
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin replay", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	receipt, err := work.NewStore(h.db).AcceptTx(r.Context(), tx, work.AcceptRequest{
		WorkspaceID: ws,
		Source:      work.Source(item.Source),
		DomainKind:  item.DomainKind,
		DomainID:    item.DomainID,
		AgentID:     item.AgentID,
		CrewID:      item.CrewID,
		// Deliberately NOT the original session: a replay is a new turn, and
		// dropping it into the original conversation would make the mailbox's
		// one-active-turn rule adjudicate a turn nobody in that conversation
		// asked for.
		SessionID: "",
		Class:     work.Class(item.Class),
		// The CURRENT caller's authorization, per §4. Re-using the original's
		// would let a replay outlive the permission that admitted it.
		AuthorizedByUserID: user.ID,
		AuthorizedScope:    RoleFromContext(r.Context()),
		InputJSON:          inputJSON,
		InputSHA256:        item.InputSHA256,
		TargetRevision:     targetRevision,
		Priority:           item.Priority,
		ReplayOf:           item.ID,
		ReplayReason:       strings.TrimSpace(body.Reason),
	})
	if err != nil {
		replyInternalError(w, h.logger, "accept replay", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit replay", err)
		return
	}

	auditFromRequest(r, h.db, "work.replay", "WORK_ITEM", receipt.WorkID, map[string]interface{}{"replay_of": item.ID})

	created, found, err := h.readItem(r, ws, receipt.WorkID)
	if err != nil || !found {
		// Committed but unreadable is a real failure to report, not a silent
		// 200 — the work exists and the caller must be able to find it.
		replyInternalError(w, h.logger, "read replayed work item",
			fmt.Errorf("replay %s committed but could not be read back: %v", receipt.WorkID, err))
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// replayAvailability answers whether the original input can still be replayed,
// and if not, says why in a sentence meant for the person reading it.
//
// Webhook-sourced work is the case that expires: §6 keeps the raw body at
// least until the work is terminal and then to its own retention, after which
// a replay cannot reproduce what the sender sent. The delivery row survives
// the payload, so "we received it, we can no longer replay it" is a
// distinguishable answer from "we never saw it".
func (h *WorkItemsHandler) replayAvailability(r *http.Request, workspaceID string, item workItemView) (string, bool, error) {
	if item.Source != string(work.SourceWebhook) || strings.TrimSpace(item.SourceRef) == "" {
		return "", true, nil
	}
	var rawPresent int
	var expires sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT CASE WHEN raw_body IS NULL THEN 0 ELSE 1 END, raw_body_expires_at
		 FROM webhook_deliveries WHERE workspace_id=? AND id=?`, workspaceID, item.SourceRef).Scan(&rawPresent, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "The delivery that produced this work is no longer in the ledger, so its payload cannot be replayed. Ask the sender to deliver it again.", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if rawPresent == 0 {
		msg := "The raw payload for this delivery has passed its retention window and was dropped, so a replay cannot reproduce the original input."
		if expires.Valid && expires.String != "" {
			msg += " It expired at " + expires.String + "."
		}
		msg += " Ask the sender to deliver it again."
		return msg, false, nil
	}
	return "", true, nil
}
