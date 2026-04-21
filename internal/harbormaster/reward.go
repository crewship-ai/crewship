package harbormaster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// RewardHistorySize is the size of the rolling window considered when
// deciding whether to auto-tune a gate mode. 20 balances recency
// (operator intent from the last handful of decisions matters most)
// against statistical stability (a single outlier in a window of 5
// would flip a mode back and forth). Tunable if we see instability
// in the field.
const RewardHistorySize = 20

// Auto-tuning thresholds. ApproveRate > 0.9 over the rolling window
// downgrades sync → async (humans are rubber-stamping, stop blocking
// the agent). DenyRate > 0.7 upgrades async → sync (humans are
// rejecting, start blocking the agent instead of logging it and
// running anyway). Timeouts count as inaction, not signal.
const (
	AutoDowngradeApproveRate = 0.9
	AutoUpgradeDenyRate      = 0.7
)

// Outcome is the terminal status of an approval row, written to
// gate_reward_history when Decide() lands or SweepTimeouts flips.
type Outcome string

const (
	OutcomeApproved  Outcome = "approved"
	OutcomeDenied    Outcome = "denied"
	OutcomeTimeout   Outcome = "timeout"
	OutcomeCancelled Outcome = "cancelled"
)

// RecordOutcome inserts a row into gate_reward_history. Called by
// Decide() after the approval row is updated and by the timeout
// sweeper when it flips a row to StatusTimeout. args is hashed, not
// stored verbatim — the original row already carries the full payload
// and we don't want a secondary copy of sensitive parameters.
func RecordOutcome(ctx context.Context, db *sql.DB, workspaceID, tool string,
	args map[string]any, outcome Outcome, decidedBy, requestID string) error {
	if workspaceID == "" || tool == "" {
		return fmt.Errorf("harbormaster: RecordOutcome requires workspace_id + tool")
	}
	id := "grh_" + randomToken(8)
	_, err := db.ExecContext(ctx, `INSERT INTO gate_reward_history
		(id, workspace_id, tool_name, args_hash, outcome, decided_by, decided_at, request_id)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'), ?)`,
		id, workspaceID, tool, HashArgs(args), string(outcome),
		nullableStr(decidedBy), nullableStr(requestID))
	if err != nil {
		return fmt.Errorf("harbormaster: record outcome: %w", err)
	}
	return nil
}

// HashArgs is the stable hashing function used to group "same-shape"
// arg sets. Keys are sorted before marshal so two semantically-equal
// maps (different insertion order) hash the same. Values are
// preserved structurally — only complex nested trees pay the full
// marshal cost. Collisions are semantically harmless: the worst case
// is two different calls being treated as the same cohort, which
// biases auto-tuning slightly but never leaks tenant data.
func HashArgs(args map[string]any) string {
	if len(args) == 0 {
		return "empty"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([][2]any, 0, len(keys))
	for _, k := range keys {
		ordered = append(ordered, [2]any{k, args[k]})
	}
	b, err := json.Marshal(ordered)
	if err != nil {
		return "unhashable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// OutcomeCounts bundles the tallies RewardHistory returns. Total is
// the sum of all fields — callers use it to decide whether there's
// enough data to tune at all.
type OutcomeCounts struct {
	Approved  int
	Denied    int
	Timeout   int
	Cancelled int
	Total     int
}

// ApproveRate is approved/total, ignoring timeouts and cancellations
// so inaction doesn't dilute the actual human intent.
func (c OutcomeCounts) ApproveRate() float64 {
	decided := c.Approved + c.Denied
	if decided == 0 {
		return 0
	}
	return float64(c.Approved) / float64(decided)
}

// DenyRate mirrors ApproveRate. Timeouts and cancellations excluded.
func (c OutcomeCounts) DenyRate() float64 {
	decided := c.Approved + c.Denied
	if decided == 0 {
		return 0
	}
	return float64(c.Denied) / float64(decided)
}

// RewardHistory returns the tally for the last limit outcomes of
// (tool, argsHash) in the workspace. The rolling-window size is the
// caller's choice but RewardHistorySize is the sensible default.
func RewardHistory(ctx context.Context, db *sql.DB, workspaceID, tool, argsHash string, limit int) (OutcomeCounts, error) {
	if limit <= 0 {
		limit = RewardHistorySize
	}
	rows, err := db.QueryContext(ctx, `
		SELECT outcome FROM gate_reward_history
		 WHERE workspace_id = ? AND tool_name = ? AND args_hash = ?
		 ORDER BY decided_at DESC
		 LIMIT ?`, workspaceID, tool, argsHash, limit)
	if err != nil {
		return OutcomeCounts{}, fmt.Errorf("harbormaster: reward history: %w", err)
	}
	defer rows.Close()
	var c OutcomeCounts
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return OutcomeCounts{}, err
		}
		switch Outcome(o) {
		case OutcomeApproved:
			c.Approved++
		case OutcomeDenied:
			c.Denied++
		case OutcomeTimeout:
			c.Timeout++
		case OutcomeCancelled:
			c.Cancelled++
		}
		c.Total++
	}
	if err := rows.Err(); err != nil {
		return OutcomeCounts{}, err
	}
	return c, nil
}

// ResetAutoTuning deletes the reward history for a tool in a workspace
// so the next call falls back to the operator-requested mode. Exposed
// via `crewship approvals reset-auto-tuning <tool>` for the case where
// operators want to reset a mis-trained gate.
func ResetAutoTuning(ctx context.Context, db *sql.DB, workspaceID, tool string) (int64, error) {
	if workspaceID == "" || tool == "" {
		return 0, fmt.Errorf("harbormaster: reset requires workspace_id + tool")
	}
	res, err := db.ExecContext(ctx,
		`DELETE FROM gate_reward_history WHERE workspace_id = ? AND tool_name = ?`,
		workspaceID, tool)
	if err != nil {
		return 0, fmt.Errorf("harbormaster: reset: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// AdjustMode considers the rolling reward history for (tool, args) and
// returns the adjusted mode plus a reason string. If no tuning applies
// the requested mode is returned unchanged with an empty reason. When
// a tuning decision fires, caller is expected to emit
// keeper.rule_auto_tuned to the journal so operators see the mode
// change in the audit trail.
func AdjustMode(ctx context.Context, db *sql.DB, workspaceID, tool string,
	args map[string]any, requested Mode) (adjusted Mode, reason string, err error) {
	// Only sync ↔ async tuning. ModeNone is an operator opt-out and
	// shouldn't be auto-overridden; returning it unchanged means a
	// trusted code path stays trusted.
	if requested == ModeNone {
		return ModeNone, "", nil
	}
	counts, err := RewardHistory(ctx, db, workspaceID, tool, HashArgs(args), RewardHistorySize)
	if err != nil {
		return requested, "", err
	}
	// Need a quorum before tuning — acting on 3 decisions flips modes
	// too aggressively. Half-window is the minimum evidence bar.
	if counts.Total < RewardHistorySize/2 {
		return requested, "", nil
	}
	if requested == ModeSync && counts.ApproveRate() > AutoDowngradeApproveRate {
		return ModeAsync, fmt.Sprintf("auto-downgrade sync→async: %.0f%% approved over last %d decisions",
			counts.ApproveRate()*100, counts.Total), nil
	}
	if requested == ModeAsync && counts.DenyRate() > AutoUpgradeDenyRate {
		return ModeSync, fmt.Sprintf("auto-upgrade async→sync: %.0f%% denied over last %d decisions",
			counts.DenyRate()*100, counts.Total), nil
	}
	return requested, "", nil
}

// EmitAutoTuned writes the keeper.rule_auto_tuned journal entry that
// records a mode adjustment. Kept separate from AdjustMode so tests
// can verify the decision logic without a journal emitter.
func EmitAutoTuned(ctx context.Context, j journal.Emitter, workspaceID, crewID, agentID, missionID, tool string,
	from, to Mode, reason string) {
	if j == nil {
		return
	}
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		AgentID:     agentID,
		MissionID:   missionID,
		Type:        journal.EntryType("keeper.rule_auto_tuned"),
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorSystem,
		ActorID:     "harbormaster",
		Summary:     fmt.Sprintf("gate auto-tuned for %s: %s → %s (%s)", tool, from, to, reason),
		Payload: map[string]any{
			"tool":      tool,
			"mode_from": from.String(),
			"mode_to":   to.String(),
			"reason":    reason,
		},
	})
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Convenience for callers that already know the timestamp they want to
// record — exposed for tests and the compactor loop that backfills
// outcomes from old rows. Production Decide/SweepTimeouts paths use
// RecordOutcome which stamps with now().
func RecordOutcomeAt(ctx context.Context, db *sql.DB, workspaceID, tool string,
	args map[string]any, outcome Outcome, decidedBy, requestID string, at time.Time) error {
	if workspaceID == "" || tool == "" {
		return fmt.Errorf("harbormaster: workspace_id and tool required")
	}
	id := "grh_" + randomToken(8)
	_, err := db.ExecContext(ctx, `INSERT INTO gate_reward_history
		(id, workspace_id, tool_name, args_hash, outcome, decided_by, decided_at, request_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceID, tool, HashArgs(args), string(outcome),
		nullableStr(decidedBy), at.UTC().Format(time.RFC3339Nano), nullableStr(requestID))
	if err != nil {
		return fmt.Errorf("harbormaster: record outcome at: %w", err)
	}
	return nil
}
