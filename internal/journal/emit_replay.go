package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrReplayConflict = errors.New("journal replay identity conflicts with stored content")

// EmitReplaySync durably records a replay event once. Callers supply stable
// identity, timestamp and trace/span, and scrub payloads before calling it.
// A repeated identity is accepted only when every immutable field agrees.
// The bool reports a new insertion; false does not re-run commit observers.
// Replay receipts outlive journal retention: pruning a visible row cannot
// turn a lost source ACK into another append or commit-observer invocation.
// Receipts have no expiry and must not be deleted with journal rows.
// This is not an outbox for external effects: a crash after commit can still
// interrupt best-effort observer delivery. It also does not authorize a scope.
func (w *Writer) EmitReplaySync(ctx context.Context, e Entry) (string, bool, error) {
	if e.ID == "" || e.TS.IsZero() || e.TraceID == "" || e.SpanID == "" {
		return "", false, errors.New("journal replay requires stable id, timestamp, trace and span")
	}
	e, err := prepareEntry(ctx, e)
	if err != nil {
		return "", false, err
	}
	exists, err := w.matchReplayEntry(ctx, e)
	if err != nil {
		return "", false, err
	}
	if exists {
		return e.ID, false, nil
	}
	e.replay = true
	if err = w.persistOne(ctx, e); err != nil {
		// Another caller can win between lookup and insertion. The primary key
		// makes the collision atomic; only matching committed content counts.
		if exists, matchErr := w.matchReplayEntry(ctx, e); matchErr == nil && exists {
			return e.ID, false, nil
		} else if matchErr != nil {
			return "", false, errors.Join(err, matchErr)
		}
		return "", false, err
	}
	w.afterCommit([]Entry{e})
	return e.ID, true, nil
}

// replayFingerprint commits to exactly the immutable storage representation.
// JSON strings preserve integer precision and make field boundaries explicit.
func replayFingerprint(e Entry, payload, refs, expires string) (string, error) {
	data, err := json.Marshal([]string{e.ID, e.WorkspaceID, e.CrewID, e.AgentID, e.MissionID,
		e.TS.UTC().Format("2006-01-02T15:04:05.000Z"), string(e.Type), string(e.Severity),
		priorityOrNormal(e.Priority), string(e.ActorType), e.ActorID, e.Summary, payload, refs,
		e.TraceID, e.SpanID, expires})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (w *Writer) matchReplayEntry(ctx context.Context, e Entry) (bool, error) {
	payload, err := e.payloadJSON()
	if err != nil {
		return false, err
	}
	refs, err := e.refsJSON()
	if err != nil {
		return false, err
	}
	expires := ""
	if e.ExpiresAt != nil {
		expires = e.ExpiresAt.UTC().Format(time.RFC3339Nano) // tsformat:allow: fingerprint uses the existing journal expiry serialization
	}
	wanted, err := replayFingerprint(e, payload, refs, expires)
	if err != nil {
		return false, err
	}
	var actual string
	err = w.db.QueryRowContext(ctx, `SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, e.ID).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("journal replay receipt lookup: %w", err)
	}
	if actual != wanted {
		return false, ErrReplayConflict
	}
	return true, nil
}

// ReplayTerminal returns an already committed terminal receipt after visible
// history has expired. Scope is authenticated before its type is exposed; a
// receipt for a chunk or another run can never acknowledge a terminal result.
// When expectedExit is supplied, the receipt must also agree with that exact
// exit fact, including the distinction between an absent code and zero.
func (w *Writer) ReplayTerminal(ctx context.Context, id, workspace, agent, run string, expectedExit ...*int) (EntryType, bool, error) {
	if id == "" || workspace == "" || agent == "" || run == "" {
		return "", false, errors.New("journal terminal receipt requires exact identity")
	}
	if len(expectedExit) > 1 {
		return "", false, errors.New("ambiguous terminal exit expectation")
	}
	var storedWorkspace, storedAgent, trace, kind string
	var code sql.NullInt64
	err := w.db.QueryRowContext(ctx, `SELECT workspace_id,agent_id,trace_id,entry_type,terminal_exit_code FROM journal_replay_receipts WHERE id=?`, id).Scan(&storedWorkspace, &storedAgent, &trace, &kind, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if storedWorkspace != workspace || storedAgent != agent || trace != run {
		return "", false, ErrReplayConflict
	}
	if len(expectedExit) == 1 {
		expected := expectedExit[0]
		if (expected == nil && code.Valid) || (expected != nil && (!code.Valid || code.Int64 != int64(*expected))) {
			return "", false, ErrReplayConflict
		}
	}
	switch EntryType(kind) {
	case EntryRunCompleted, EntryRunFailed, EntryRunCancelled, EntryRunTimeout:
		return EntryType(kind), true, nil
	default:
		return "", false, ErrReplayConflict
	}
}

// Only terminal exit facts are copied into the receipt, not output payloads.
func terminalReplayExitCode(kind EntryType, payload string) (sql.NullInt64, error) {
	switch kind {
	case EntryRunCompleted, EntryRunFailed, EntryRunCancelled, EntryRunTimeout:
		var result struct {
			ExitCode *int `json:"exit_code"`
		}
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			return sql.NullInt64{}, err
		}
		if result.ExitCode != nil {
			return sql.NullInt64{Int64: int64(*result.ExitCode), Valid: true}, nil
		}
	}
	return sql.NullInt64{}, nil
}
