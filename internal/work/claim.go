package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Limits is the admission contract every producer shares (I7). §6's reference
// profile is 8 active executions per server, at most 6 of them background, and
// 2 places reserved for chat that background may not borrow in 1.0.
//
// Per agent: the verified Claude profile is chat<=1, background<=1, total<=2.
// Every other adapter is total<=1 until it passes the same tests — that is the
// release profile, not a default someone may raise in config and still call
// supported.
type Limits struct {
	ServerTotal      int
	ServerBackground int
	ServerChatReserv int

	AgentTotal      int
	AgentChat       int
	AgentBackground int
}

// DefaultLimits is §6's reference profile. A deployment configured below the
// 1+1 shape must not advertise the parallel profile in the UI.
func DefaultLimits() Limits {
	return Limits{
		ServerTotal:      8,
		ServerBackground: 6,
		ServerChatReserv: 2,
		AgentTotal:       2,
		AgentChat:        1,
		AgentBackground:  1,
	}
}

// AgingThreshold is §6's: work that has been eligible for longer than this
// sorts ahead of younger work, so a steady arrival of new items cannot lap an
// old one indefinitely.
const AgingThreshold = 60 * time.Second

// SerialAgentLimits is what an adapter gets before it has passed T06/T07: one
// run at a time, of either class.
func SerialAgentLimits() Limits {
	l := DefaultLimits()
	l.AgentTotal, l.AgentChat, l.AgentBackground = 1, 1, 1
	return l
}

// ClaimOptions selects what a dispatcher is willing to pick up.
type ClaimOptions struct {
	// LeaseOwner identifies the claiming process. Recovery uses it to tell "my
	// own abandoned attempt" from "someone else's live one".
	LeaseOwner string
	// Limits admits the claim. Per-agent limits may be narrowed by the caller
	// for an adapter that is not yet verified for the parallel profile.
	Limits Limits
	// Class, when set, restricts the claim to one class. A dispatcher that runs
	// one pump per class uses this; the default considers both.
	Class Class
	// AgentID, when set, restricts the claim to one agent — the shape the
	// existing per-agent pump uses when a run finishes and frees its slot.
	AgentID string
	// Kinds, when set, restricts the claim to the work types this dispatcher
	// can actually execute.
	//
	// A dispatcher can only run what its Runtime knows how to run. Without this
	// the first dispatcher to exist would claim every producer's work and then
	// fail it, which is worse than not running it: the work is consumed, its
	// attempt is burned, its generation and attempt count move, and the
	// executor that could have handled it never sees it again.
	//
	// It is a (source, domain kind) PAIR and not two independent lists,
	// because the source alone does not identify a work type and assuming it
	// does is a live bug rather than a hypothetical: `webhook` carries both
	// `agent_run` and `pipeline_run`, so a dispatcher that filtered on the
	// source would look specific and silently swallow the other one. Two
	// independent lists would be no better — they match the cross product, so
	// {webhook, chat} x {agent_run, pipeline_run} admits pairs nobody declared.
	Kinds []Kind
	// WorkID, when set, restricts the claim to one work item. This is what a
	// wake-up hint turns into: acceptance commits and says "there is something
	// for you", and the dispatcher tries to claim THAT item rather than
	// re-scanning the whole queue. It is a hint, not a reservation — the item
	// still has to pass every limit, and losing it to another dispatcher is a
	// normal ErrNoWork.
	WorkID string
}

// Claimed is a successful claim: the work, the attempt that now owns it, and
// the generation that fences every later transition.
type Claimed struct {
	Item       *Item
	RunID      string
	Attempt    int
	Generation int64
	LeaseUntil time.Time
}

// ErrNoWork means nothing eligible passed the limits. It is not an error
// condition; a dispatcher polls and gets it most of the time.
var ErrNoWork = errors.New("work: nothing claimable")

// errAttemptsExhausted is internal: it tells the claim scan that this candidate
// was just failed in the current transaction and the scan should move on.
var errAttemptsExhausted = errors.New("work: attempts exhausted")

// Claim atomically reserves capacity and starts one attempt.
//
// Capacity is counted and the row is taken in ONE immediate transaction, so two
// dispatchers cannot both see a free slot (I3). Nothing outside the database
// happens inside it: no Docker call, no HTTP request, no CLI start. §4 is
// explicit about that, and it is also the only way the transaction stays short
// enough not to hold the single SQLite writer.
//
// Candidate selection and its ordering live in scanCandidatesTx; read that for
// how §6's aging and round-robin are applied and what they cost.
func (s *Store) Claim(ctx context.Context, opts ClaimOptions) (*Claimed, error) {
	if opts.LeaseOwner == "" {
		return nil, errors.New("work: claim requires a lease owner")
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("work: begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()

	// A deadline that passed while the work waited expires it here, before it
	// can consume a slot. Work without a deadline never lands in this branch.
	if err := s.expireOverdueTx(ctx, tx, now); err != nil {
		return nil, err
	}

	live, err := s.countLiveTx(ctx, tx)
	if err != nil {
		return nil, err
	}

	// Server-level gates do not vary per row, so they are settled before the
	// scan rather than rejecting candidates one at a time inside it.
	classes, ok := live.admissibleClasses(opts.Limits, opts.Class)
	if !ok {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("work: commit claim scan: %w", err)
		}
		return nil, ErrNoWork
	}

	candidates, err := s.scanCandidatesTx(ctx, tx, opts, classes, now)
	if err != nil {
		return nil, err
	}

	for _, it := range candidates {
		ok, err := s.admitsTx(ctx, tx, it, opts.Limits, live)
		if err != nil {
			return nil, err
		}
		if !ok {
			// The scan already filtered on every per-row rule, so this branch
			// means the SQL and the Go rules disagree. Keep going — but see the
			// divergence check after the loop, which refuses to report it as
			// "nothing to do".
			continue
		}
		claimed, err := s.startAttemptTx(ctx, tx, it, opts.LeaseOwner, now)
		if errors.Is(err, errAttemptsExhausted) {
			// startAttemptTx already wrote the failure into this transaction.
			// Keep scanning rather than returning: this candidate is dead, but
			// the work behind it is not, and abandoning the scan here would let
			// one exhausted item stall the whole queue.
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("work: commit claim: %w", err)
		}
		return claimed, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("work: commit claim scan: %w", err)
	}
	return nil, ErrNoWork
}

// liveCounts is one snapshot of what currently holds execution slots, read
// inside the claiming transaction so it cannot go stale between check and take.
type liveCounts struct {
	total      int
	chat       int
	background int
}

func (s *Store) countLiveTx(ctx context.Context, tx *sql.Tx) (liveCounts, error) {
	var c liveCounts
	err := tx.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN class = 'chat' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN class = 'background' THEN 1 ELSE 0 END), 0)
		FROM work_items WHERE state IN (`+sqlHoldsExecutionSlot+`)`,
	).Scan(&c.total, &c.chat, &c.background)
	if err != nil {
		return c, fmt.Errorf("work: count live: %w", err)
	}
	return c, nil
}

// admissibleClasses returns the classes that could start right now given the
// server-wide caps, narrowed by an explicit request. ok is false when nothing
// can start at all.
func (c liveCounts) admissibleClasses(lim Limits, only Class) ([]Class, bool) {
	if c.total >= lim.ServerTotal {
		return nil, false
	}
	var out []Class
	if only == "" || only == ClassChat {
		// Chat draws on its reservation first, so the server total is its only
		// server-level gate.
		out = append(out, ClassChat)
	}
	if only == "" || only == ClassBackground {
		if c.background < lim.ServerBackground &&
			// Background may not eat into the chat reservation. Without this a
			// busy queue starves a person out of their own conversation.
			c.total+1 <= lim.ServerTotal-lim.ServerChatReserv+c.chat {
			out = append(out, ClassBackground)
		}
	}
	return out, len(out) > 0
}

// candidateBatch bounds one scan. It is small on purpose: scanCandidatesTx
// filters on every per-row rule in SQL, so a row that comes back is claimable
// unless the Go re-check disagrees, and taking the first is correct.
const candidateBatch = 50

// scanCandidatesTx returns the fairest claimable work, cheapest first.
//
// Every per-row blocking rule is applied in SQL — session occupancy and both
// per-agent caps — and NOT in Go after a LIMIT. That ordering matters: an
// earlier version selected the oldest 200 rows and then filtered them in Go, so
// two hundred items belonging to one busy agent hid the claimable work of every
// other agent behind them, and re-polling walked the same dead prefix forever.
//
// Ordering implements §6:
//   - priority first
//   - then aging: anything eligible for longer than AgingThreshold sorts ahead
//     of younger work, so a long-waiting item cannot be lapped indefinitely
//   - then round-robin across workspaces and then agents, by how much capacity
//     each is already using — a workspace with nothing running goes before one
//     that already has a run in flight
//   - then FIFO by eligible_at and acceptance order, with the id as a stable
//     tiebreak so the scan is deterministic
func (s *Store) scanCandidatesTx(ctx context.Context, tx *sql.Tx, opts ClaimOptions, classes []Class, now time.Time) ([]*Item, error) {
	args := []any{tsformat.Format(now)}
	q := `SELECT ` + itemColumns + ` FROM work_items w
	      WHERE w.state IN ('queued','retry_wait') AND w.eligible_at <= ?`

	placeholders := make([]string, 0, len(classes))
	for _, c := range classes {
		placeholders = append(placeholders, "?")
		args = append(args, string(c))
	}
	q += ` AND w.class IN (` + strings.Join(placeholders, ",") + `)`

	if opts.AgentID != "" {
		q += ` AND w.agent_id = ?`
		args = append(args, opts.AgentID)
	}
	if opts.WorkID != "" {
		q += ` AND w.id = ?`
		args = append(args, opts.WorkID)
	}
	// The executable-kind filter, applied HERE — inside the transaction that
	// selects candidates, before anything is taken. Checking after the claim
	// would not prevent the harm it is for: the claim has already bumped the
	// generation, burned an attempt and moved the item out of `queued` by then,
	// so "take it and then decline it" is indistinguishable from destroying it.
	if len(opts.Kinds) > 0 {
		terms := make([]string, 0, len(opts.Kinds))
		for _, k := range opts.Kinds {
			terms = append(terms, "(w.source = ? AND w.domain_kind = ?)")
			args = append(args, string(k.Source), k.DomainKind)
		}
		q += ` AND (` + strings.Join(terms, " OR ") + `)`
	}

	// One active turn per session (I3). needs_reconciliation counts here: the
	// previous turn's runtime may still be alive.
	q += ` AND (w.session_id = '' OR NOT EXISTS (
	          SELECT 1 FROM work_items s
	          WHERE s.session_id = w.session_id AND s.state IN (` + sqlOccupiesSession + `)))`

	// Per-agent total and per-class caps.
	q += ` AND (w.agent_id = '' OR (
	          SELECT COUNT(*) FROM work_items a
	          WHERE a.agent_id = w.agent_id AND a.state IN (` + sqlHoldsExecutionSlot + `)) < ?)`
	args = append(args, opts.Limits.AgentTotal)

	q += ` AND (w.agent_id = '' OR (
	          SELECT COUNT(*) FROM work_items a
	          WHERE a.agent_id = w.agent_id AND a.class = w.class
	            AND a.state IN (` + sqlHoldsExecutionSlot + `))
	          < CASE w.class WHEN 'chat' THEN ? ELSE ? END)`
	args = append(args, opts.Limits.AgentChat, opts.Limits.AgentBackground)

	q += ` ORDER BY
	        w.priority DESC,
	        CASE WHEN w.eligible_at <= ? THEN 0 ELSE 1 END ASC,
	        (SELECT COUNT(*) FROM work_items ws
	           WHERE ws.workspace_id = w.workspace_id AND ws.state IN (` + sqlHoldsExecutionSlot + `)) ASC,
	        (SELECT COUNT(*) FROM work_items ag
	           WHERE ag.agent_id = w.agent_id AND ag.state IN (` + sqlHoldsExecutionSlot + `)) ASC,
	        w.eligible_at ASC, w.created_at ASC, w.id ASC
	      LIMIT ?`
	args = append(args, tsformat.Format(now.Add(-AgingThreshold)), candidateBatch)

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("work: scan candidates: %w", err)
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// admitsTx re-applies every limit to one candidate.
//
// scanCandidatesTx already filtered on the per-row rules in SQL, so this is a
// second, authoritative pass rather than the only one. Keeping both is
// deliberate: the SQL is where a rule has to live to be usable as a filter, and
// this is where it can be read. TestClaimSQLAndGoAgree pins that they do not
// drift apart.
func (s *Store) admitsTx(ctx context.Context, tx *sql.Tx, it *Item, lim Limits, live liveCounts) (bool, error) {
	if live.total >= lim.ServerTotal {
		return false, nil
	}
	switch it.Class {
	case ClassBackground:
		if live.background >= lim.ServerBackground {
			return false, nil
		}
		if live.total+1 > lim.ServerTotal-lim.ServerChatReserv+live.chat {
			return false, nil
		}
	case ClassChat:
		// Chat draws on its reservation first, so it is admitted whenever the
		// server total allows.
	}

	// I3: one active turn per session. needs_reconciliation is in this set
	// because the previous turn's runtime may still be alive under a locator
	// nobody has checked.
	if it.SessionID != "" {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM work_items WHERE session_id = ? AND state IN (`+sqlOccupiesSession+`)`,
			it.SessionID).Scan(&n); err != nil {
			return false, fmt.Errorf("work: count session turns: %w", err)
		}
		if n > 0 {
			return false, nil
		}
	}

	if it.AgentID != "" {
		var agentTotal, agentChat, agentBackground int
		if err := tx.QueryRowContext(ctx, `
			SELECT
				COUNT(*),
				COALESCE(SUM(CASE WHEN class = 'chat' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN class = 'background' THEN 1 ELSE 0 END), 0)
			FROM work_items WHERE agent_id = ? AND state IN (`+sqlHoldsExecutionSlot+`)`,
			it.AgentID).Scan(&agentTotal, &agentChat, &agentBackground); err != nil {
			return false, fmt.Errorf("work: count agent runs: %w", err)
		}
		if agentTotal >= lim.AgentTotal {
			return false, nil
		}
		if it.Class == ClassChat && agentChat >= lim.AgentChat {
			return false, nil
		}
		if it.Class == ClassBackground && agentBackground >= lim.AgentBackground {
			return false, nil
		}
	}
	return true, nil
}

// startAttemptTx bumps the generation, writes the attempt and moves the item to
// starting. The generation bump is what makes every later transition fenceable.
func (s *Store) startAttemptTx(ctx context.Context, tx *sql.Tx, it *Item, owner string, now time.Time) (*Claimed, error) {
	// Two different numbers, which used to be one.
	//
	// `spent` is the BUDGET: how many attempts have counted against MaxAttempts.
	// `seq` is the attempt's IDENTITY within this work item, and it only ever
	// goes up, because work_attempts is UNIQUE(work_id, attempt) and an attempt
	// row is never rewritten.
	//
	// They diverge the moment anything gives an attempt back — a deferral does,
	// for work held on a human. Keeping them as one field meant the next claim
	// reused a sequence number that already had a row, and the insert failed
	// with a constraint error the dispatcher could only log and retry, forever.
	spent := it.Attempts + 1
	if spent > MaxAttempts {
		// Reachable: recovery returns an abandoned attempt to the queue without
		// consulting the attempt count, so an item that lost its lease on its
		// last attempt arrives here eligible and out of budget. Fail it in this
		// transaction and tell the caller to keep scanning — an earlier version
		// returned ErrNoWork straight out of Claim, which rolled the failure
		// back AND stopped the dispatcher, so one exhausted item stalled
		// everything behind it forever.
		if err := s.setStateTx(ctx, tx, it, StateFailed, "", it.Generation, "max attempts exhausted", now); err != nil {
			return nil, err
		}
		return nil, errAttemptsExhausted
	}

	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(attempt), 0) + 1 FROM work_attempts WHERE work_id = ?`, it.ID).
		Scan(&seq); err != nil {
		return nil, fmt.Errorf("work: next attempt number: %w", err)
	}

	generation := it.Generation + 1
	runID := s.idFn()
	leaseUntil := now.Add(LeaseDuration)

	res, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET state = 'starting', state_reason = '', generation = ?, attempts = ?, updated_at = ?
		WHERE id = ? AND generation = ? AND state IN ('queued','retry_wait')`,
		generation, spent, tsformat.Format(now), it.ID, it.Generation)
	if err != nil {
		return nil, fmt.Errorf("work: claim update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		// Someone else claimed it between the scan and here. Inside one
		// immediate transaction this should be unreachable; treat it as a lost
		// race rather than an assertion, because a future reader may relax the
		// transaction and this is the line that tells them they did.
		return nil, ErrNoWork
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_attempts (
			run_id, work_id, attempt, generation, lease_owner, lease_expires_at,
			heartbeat_at, started_at, start_reason
		) VALUES (?,?,?,?,?,?,?,?,?)`,
		runID, it.ID, seq, generation, owner,
		tsformat.Format(leaseUntil), tsformat.Format(now), tsformat.Format(now), "claimed",
	); err != nil {
		return nil, fmt.Errorf("work: insert attempt: %w", err)
	}

	if err := appendEventTx(ctx, tx, it.ID, string(it.State), StateStarting, runID, generation, "claimed", now); err != nil {
		return nil, err
	}

	claimedItem := *it
	claimedItem.State = StateStarting
	claimedItem.Generation = generation
	claimedItem.Attempts = spent
	return &Claimed{
		Item:       &claimedItem,
		RunID:      runID,
		Attempt:    seq,
		Generation: generation,
		LeaseUntil: leaseUntil,
	}, nil
}

func (s *Store) expireOverdueTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+itemColumns+`
		FROM work_items
		WHERE deadline_at IS NOT NULL AND deadline_at <= ? AND state IN ('queued','retry_wait')`,
		tsformat.Format(now))
	if err != nil {
		return fmt.Errorf("work: scan overdue: %w", err)
	}
	var overdue []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return err
		}
		overdue = append(overdue, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, it := range overdue {
		if err := s.setStateTx(ctx, tx, it, StateExpired, "", it.Generation, "deadline passed before start", now); err != nil {
			return err
		}
	}
	return nil
}

// setStateTx writes a state change and its event together. It does not check
// the transition table — callers that accept an outside request go through
// Transition, which does.
func (s *Store) setStateTx(ctx context.Context, tx *sql.Tx, it *Item, to State, runID string, generation int64, reason string, now time.Time) error {
	var terminal any
	if to.Terminal() {
		terminal = tsformat.Format(now)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE work_items SET state = ?, state_reason = ?, updated_at = ?, terminal_at = COALESCE(?, terminal_at)
		WHERE id = ?`,
		string(to), reason, tsformat.Format(now), terminal, it.ID); err != nil {
		return fmt.Errorf("work: set state: %w", err)
	}
	return appendEventTx(ctx, tx, it.ID, string(it.State), to, runID, generation, reason, now)
}
