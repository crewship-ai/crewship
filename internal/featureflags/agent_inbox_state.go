package featureflags

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
)

// AgentInboxRequestHeader distinguishes an explicit inbox write from a
// canonical conversation or issue write using the same public endpoint.
const AgentInboxRequestHeader = "X-Crewship-Human-Inbox"

// AgentInboxState keeps the optional inbox admission path out of ordinary
// workspace queries. The router loads it at startup and refreshes it after
// supported flag mutations. Source reads still check the database in their
// authorization transaction.
type AgentInboxState struct {
	mu       sync.Mutex
	snapshot atomic.Pointer[agentInboxSnapshot]
}

type agentInboxSnapshot struct {
	defaultEnabled bool
	overrides      map[string]bool
}

func (s *AgentInboxState) Enabled(workspace string) bool {
	if s == nil {
		return false
	}
	cur := s.snapshot.Load()
	if cur == nil {
		return false
	}
	if enabled, ok := cur.overrides[workspace]; ok {
		return enabled
	}
	return cur.defaultEnabled
}

func (s *AgentInboxState) Refresh(ctx context.Context, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := db.QueryContext(ctx, `SELECT f.enabled, o.workspace_id, o.enabled
		FROM feature_flags f LEFT JOIN feature_flag_overrides o ON o.flag_id=f.id
		WHERE f.key=?`, AgentHumanInbox)
	if err != nil {
		s.snapshot.Store(nil)
		return err
	}
	defer rows.Close()
	next := &agentInboxSnapshot{overrides: make(map[string]bool)}
	for rows.Next() {
		var w sql.NullString
		var defaultEnabled bool
		var override sql.NullBool
		if err := rows.Scan(&defaultEnabled, &w, &override); err != nil {
			s.snapshot.Store(nil)
			return err
		}
		next.defaultEnabled = defaultEnabled
		if w.Valid && override.Valid {
			next.overrides[w.String] = override.Bool
		}
	}
	if err := rows.Err(); err != nil {
		s.snapshot.Store(nil)
		return err
	}
	s.snapshot.Store(next)
	return nil
}
