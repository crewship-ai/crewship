package access

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Outcome contains no prompt, log, output, provider detail or executable handle.
// It is an authenticated actor's own audit of accepted work, not model context.
type Outcome struct {
	State      string `json:"state"`
	CreatedAt  string `json:"created_at"`
	RecordedAt string `json:"recorded_at"`
}

// RecordOutcome is called by trusted host lifecycle code with the admission
// handle. It deliberately permits recording failure after execution revocation.
// The database derives the attempt identity; public IDs cannot authorize writes.
func (s Store) RecordOutcome(ctx context.Context, handle, state string) error {
	if s.DB == nil || handle == "" {
		return ErrDenied
	}
	switch state {
	case "completed", "failed", "canceled", "denied":
	default:
		return ErrDenied
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO access_attempt_outcomes(attempt_id,state,recorded_at)
 SELECT id,?,? FROM access_attempts WHERE handle_hash=?
 ON CONFLICT(attempt_id) DO NOTHING`, state, tsformat.Format(time.Now()), digest(handle))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	// Idempotence permits repeating the same terminal state, not replacing it.
	var previous string
	if err = s.DB.QueryRowContext(ctx, `SELECT o.state FROM access_attempt_outcomes o JOIN access_attempts a ON a.id=o.attempt_id WHERE a.handle_hash=?`, digest(handle)).Scan(&previous); err != nil || previous != state {
		return ErrDenied
	}
	return nil
}

// OutcomesForChat preserves content-free failure audit independently of an old
// execution grant. Every read still checks current membership, agent chat access
// and chat audience. Another participant's outcomes are never projected.
func (s Store) OutcomesForChat(ctx context.Context, user, workspace, chat string) ([]Outcome, error) {
	if s.DB == nil {
		return nil, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = chatRead(ctx, tx, user, workspace, chat); err != nil {
		return nil, err
	}
	var agent, generation string
	if err = tx.QueryRowContext(ctx, `SELECT agent_id,authority_generation FROM chats WHERE id=? AND workspace_id=?`, chat, workspace).Scan(&agent, &generation); err != nil {
		return nil, ErrDenied
	}
	member, err := check(ctx, tx, user, workspace, Right{"agent", agent, "chat"})
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.state,a.created_at,o.recorded_at
 FROM access_attempt_outcomes o JOIN access_attempts a ON a.id=o.attempt_id
 WHERE a.principal_id=? AND a.workspace_id=? AND a.chat_id=? AND a.chat_generation=? AND a.agent_id=?
 ORDER BY a.generation DESC LIMIT 100`, user, workspace, chat, generation, agent)
	if err != nil {
		return nil, err
	}
	out := []Outcome{}
	for rows.Next() {
		var item Outcome
		if err = rows.Scan(&item.State, &item.CreatedAt, &item.RecordedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	// Fence a concurrent audience or grant mutation before delivering metadata.
	if err = chatRead(ctx, s.DB, user, workspace, chat); err != nil {
		return nil, err
	}
	if err = s.Check(ctx, user, workspace, Right{"agent", agent, "chat"}); err != nil {
		return nil, err
	}
	fresh, err := s.Membership(ctx, user, workspace)
	if err != nil || fresh.ID != member.ID || fresh.Revision != member.Revision {
		return nil, ErrDenied
	}
	var currentGeneration string
	if err = s.DB.QueryRowContext(ctx, `SELECT authority_generation FROM chats WHERE id=? AND workspace_id=? AND agent_id=?`, chat, workspace, agent).Scan(&currentGeneration); err != nil || currentGeneration != generation {
		return nil, ErrDenied
	}
	return out, nil
}
