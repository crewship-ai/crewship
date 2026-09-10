package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// Claim atomically reserves capacity and starts one attempt.
//
// Capacity is counted and the row is taken in ONE immediate transaction, so two
// dispatchers cannot both see a free slot (I3). Nothing outside the database
// happens inside it: no Docker call, no HTTP request, no CLI start. §4 is
// explicit about that, and it is also the only way the transaction stays short
// enough not to hold the single SQLite writer.
//
// Known gap, stated rather than papered over: ordering here is priority, then
// eligible_at, then created_at — which gives oldest-first within a class and so
// subsumes §6's 60s aging rule. §6's round-robin ACROSS workspaces and agents is
// NOT implemented; a workspace with many eligible items will currently be served
// ahead of a workspace with one older item of equal priority.
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
	nowStr := tsformat.Format(now)

	// A deadline that passed while the work waited expires it here, before it
	// can consume a slot. Work without a deadline never lands in this branch.
	if err := s.expireOverdueTx(ctx, tx, now); err != nil {
		return nil, err
	}

	// Server-wide live counts, by class.
	var liveTotal, liveChat, liveBackground int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN class = 'chat' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN class = 'background' THEN 1 ELSE 0 END), 0)
		FROM work_items WHERE state IN ('starting','running')`,
	).Scan(&liveTotal, &liveChat, &liveBackground); err != nil {
		return nil, fmt.Errorf("work: count live: %w", err)
	}

	args := []any{nowStr}
	q := `SELECT ` + itemColumns + ` FROM work_items
	      WHERE state IN ('queued','retry_wait') AND eligible_at <= ?`
	if opts.Class != "" {
		q += ` AND class = ?`
		args = append(args, string(opts.Class))
	}
	if opts.AgentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, opts.AgentID)
	}
	// The scan is bounded: with per-agent and per-session limits, a long run of
	// blocked candidates from one busy agent must not hide an eligible item
	// behind it, but neither should one claim walk the whole queue.
	q += ` ORDER BY priority DESC, eligible_at ASC, created_at ASC, id ASC LIMIT 200`

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("work: scan candidates: %w", err)
	}
	var candidates []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, it := range candidates {
		ok, err := s.admitsTx(ctx, tx, it, opts.Limits, liveTotal, liveChat, liveBackground)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		claimed, err := s.startAttemptTx(ctx, tx, it, opts.LeaseOwner, now)
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

// admitsTx applies every limit to one candidate. All counts are read inside the
// claiming transaction, so the answer cannot go stale between check and take.
func (s *Store) admitsTx(ctx context.Context, tx *sql.Tx, it *Item, lim Limits, liveTotal, liveChat, liveBackground int) (bool, error) {
	if liveTotal >= lim.ServerTotal {
		return false, nil
	}
	switch it.Class {
	case ClassBackground:
		if liveBackground >= lim.ServerBackground {
			return false, nil
		}
		// Background may not eat into the chat reservation. Without this a busy
		// queue starves a person out of their own conversation, which §6 forbids
		// and which is the whole point of reserving.
		if liveTotal+1 > lim.ServerTotal-lim.ServerChatReserv+liveChat {
			return false, nil
		}
	case ClassChat:
		// Chat draws on its reservation first, so it is admitted whenever the
		// server total allows.
	}

	// I3: one active turn per session. A second message for the same session
	// waits in the mailbox rather than starting a competing turn.
	if it.SessionID != "" {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM work_items WHERE session_id = ? AND state IN ('starting','running','waiting')`,
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
			FROM work_items WHERE agent_id = ? AND state IN ('starting','running')`,
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
	attempt := it.Attempts + 1
	if attempt > MaxAttempts {
		// Reaching here means retry accounting let an over-limit item stay
		// eligible. Fail it rather than start a sixth attempt.
		if err := s.setStateTx(ctx, tx, it, StateFailed, "", it.Generation, "max attempts exhausted", now); err != nil {
			return nil, err
		}
		return nil, ErrNoWork
	}

	generation := it.Generation + 1
	runID := s.idFn()
	leaseUntil := now.Add(LeaseDuration)

	res, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET state = 'starting', state_reason = '', generation = ?, attempts = ?, updated_at = ?
		WHERE id = ? AND generation = ? AND state IN ('queued','retry_wait')`,
		generation, attempt, tsformat.Format(now), it.ID, it.Generation)
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
		runID, it.ID, attempt, generation, owner,
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
	claimedItem.Attempts = attempt
	return &Claimed{
		Item:       &claimedItem,
		RunID:      runID,
		Attempt:    attempt,
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
