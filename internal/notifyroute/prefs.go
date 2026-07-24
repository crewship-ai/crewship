// Package notifyroute is the orchestration layer for the native outbound
// notification system (issue #1412): it sits between internal/inbox's
// write-through chokepoint (Insert/UpsertMessage) and internal/notify's
// per-channel delivery primitives, and owns everything neither of those
// leaf packages should: category resolution, the two-layer preference
// matrix, presence-aware suppression, anti-storm rate limiting, and the
// persistent delivery log.
//
//	inbox.Insert/UpsertMessage
//	    -> notifyroute.Router.NotifyInboxItem   (this package)
//	        -> category + audience resolution
//	        -> presence gate (chatnotify-style)
//	        -> PrefStore (user's category x channel matrix)
//	        -> RateLimiter (token bucket, approvals/escalations exempt)
//	        -> DeliveryStore (outbox log: pending -> sent|failed|dropped_*)
//	        -> notify.Dispatcher.DeliverCategoryMessage (per channel type)
package notifyroute

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/notify"
)

// PrefCell is one (category, channel) cell in a user's preference matrix.
type PrefCell struct {
	Category  string `json:"category"`
	ChannelID string `json:"channel_id"`
	State     string `json:"state"` // off | immediate | digest (digest is v2-reserved; MVP never writes it)
}

// PrefStore is the persistence layer for user_notification_prefs (v153).
type PrefStore struct {
	db *sql.DB
}

func NewPrefStore(db *sql.DB) *PrefStore { return &PrefStore{db: db} }

// validState is the set of legal cell states this MVP writes. 'digest' is
// legal in the DB CHECK (schema is v2-ready) but the API/CLI reject it for
// now — there is no digest scheduler to honor it.
func validState(s string) bool {
	switch s {
	case "off", "immediate":
		return true
	default:
		return false
	}
}

// Get returns userID's full matrix in workspaceID: every cell they've
// explicitly set, PLUS the mute-all ('*') row per channel when present.
// Absence of a cell means 'off' — the API/CLI/UI render that as the
// default rather than a special "unset" state, matching the store's
// opt-in-by-default contract.
func (s *PrefStore) Get(ctx context.Context, workspaceID, userID string) ([]PrefCell, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT category, channel_id, state
		FROM user_notification_prefs
		WHERE workspace_id = ? AND user_id = ?
		ORDER BY channel_id, category`, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("notifyroute: query prefs: %w", err)
	}
	defer rows.Close()
	var out []PrefCell
	for rows.Next() {
		var c PrefCell
		if err := rows.Scan(&c.Category, &c.ChannelID, &c.State); err != nil {
			return nil, fmt.Errorf("notifyroute: scan pref: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Set upserts a batch of cells for userID, replacing whatever state (if
// any) each (category, channel) pair had. Validates every cell before
// writing any — a single bad row rejects the whole batch rather than
// partially applying the user's intended matrix.
func (s *PrefStore) Set(ctx context.Context, workspaceID, userID string, cells []PrefCell) error {
	for _, c := range cells {
		if !notify.ValidPrefCategory(c.Category) {
			return fmt.Errorf("notifyroute: unknown category %q", c.Category)
		}
		if c.ChannelID == "" {
			return fmt.Errorf("notifyroute: channel_id required")
		}
		if !validState(c.State) {
			return fmt.Errorf("notifyroute: unknown state %q (want off or immediate)", c.State)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notifyroute: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO user_notification_prefs (id, workspace_id, user_id, category, channel_id, state)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, category, channel_id) DO UPDATE SET
		    state = excluded.state,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
	if err != nil {
		return fmt.Errorf("notifyroute: prepare: %w", err)
	}
	defer stmt.Close()

	for _, c := range cells {
		id := generateID("pref")
		if _, err := stmt.ExecContext(ctx, id, workspaceID, userID, c.Category, c.ChannelID, c.State); err != nil {
			return fmt.Errorf("notifyroute: upsert pref (category=%s channel=%s): %w", c.Category, c.ChannelID, err)
		}
	}
	return tx.Commit()
}

// cellIndex is an in-memory lookup keyed "category\x00channelID" built
// from Get's output, so the router can answer "what does this user want
// for (category, channel)?" without a query per candidate channel.
type cellIndex map[string]string // key -> state

func indexCells(cells []PrefCell) cellIndex {
	idx := make(cellIndex, len(cells))
	for _, c := range cells {
		idx[c.Category+"\x00"+c.ChannelID] = c.State
	}
	return idx
}

// state returns the cell's state, defaulting to "off" when unset.
func (idx cellIndex) state(category, channelID string) string {
	if s, ok := idx[category+"\x00"+channelID]; ok {
		return s
	}
	return "off"
}

// muted reports whether the user has muted this channel entirely via the
// '*' sentinel cell. Its state enum is the same off/immediate pair as any
// other cell, but the MEANING flips: state='immediate' on the mute-all row
// means "this rule (muting) is active," matching every other cell's
// "'immediate' == this rule's effect is ON" convention, rather than
// overloading 'off' (already "no delivery" for real categories) to also
// mean "unmuted."
func (idx cellIndex) muted(channelID string) bool {
	return idx.state(notify.CategoryMuteAll, channelID) == "immediate"
}
