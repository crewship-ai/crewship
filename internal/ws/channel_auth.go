package ws

import (
	"context"
	"database/sql"
	"strings"
)

// DBChannelAuthorizer checks channel access against workspace membership in the DB.
type DBChannelAuthorizer struct {
	db *sql.DB
}

// NewDBChannelAuthorizer creates a channel authorizer backed by workspace
// membership queries against the given database.
//
// Panics if db is nil: a nil DB handle is a startup-time misconfiguration, and
// failing fast here gives the operator a loud signal instead of a delayed nil
// dereference the first time a client tries to subscribe. CanSubscribe also
// keeps a defensive nil check so a synthetically constructed zero value still
// fails closed rather than panicking on the auth path.
func NewDBChannelAuthorizer(db *sql.DB) *DBChannelAuthorizer {
	if db == nil {
		panic("ws: NewDBChannelAuthorizer called with nil *sql.DB")
	}
	return &DBChannelAuthorizer{db: db}
}

// CanSubscribe returns true if the user is allowed to subscribe to the given channel.
// Channel format: "type:id" (e.g., "workspace:abc123", "session:xyz456").
// Fails closed (returns false) if the authorizer, its DB handle, or the
// userID are missing — we never want an unauthenticated or misconfigured
// path to accidentally grant access, and we want panics in the membership
// helpers to be impossible.
func (a *DBChannelAuthorizer) CanSubscribe(ctx context.Context, userID, channel string) bool {
	if a == nil || a.db == nil || userID == "" {
		return false
	}
	// Parse "type:id" without allocating a []string — previously strings.SplitN
	// cost one slice header per subscription call.
	idx := strings.IndexByte(channel, ':')
	if idx <= 0 || idx == len(channel)-1 {
		return false
	}
	chType, chID := channel[:idx], channel[idx+1:]

	switch chType {
	case "workspace":
		return a.isMemberOfWorkspace(ctx, userID, chID)
	case "crew":
		return a.isMemberOfCrewWorkspace(ctx, userID, chID)
	case "agent":
		return a.isMemberOfAgentWorkspace(ctx, userID, chID)
	case "session":
		return a.isSessionOwner(ctx, userID, chID)
	case "keeper":
		// keeper:{workspaceId} — check workspace membership
		return a.isMemberOfWorkspace(ctx, userID, chID)
	case "files":
		// files:{crewId} — check crew's workspace membership
		return a.isMemberOfCrewWorkspace(ctx, userID, chID)
	case "mission":
		// mission:{missionId} — check mission's workspace membership
		return a.isMemberOfMissionWorkspace(ctx, userID, chID)
	case "user":
		// user:{userId} — a per-user channel (notification.created is
		// broadcast here). Only the user themselves may subscribe; there is
		// no workspace membership to consult because the channel is scoped
		// to the identity, not a tenant. Without this case the authorizer
		// fell through to default:false, so no client could ever subscribe
		// and real-time notifications silently never arrived (issue #614).
		return userID == chID
	case "providers":
		// global channel — any authenticated user
		return userID != ""
	default:
		return false
	}
}

func (a *DBChannelAuthorizer) isMemberOfWorkspace(ctx context.Context, userID, workspaceID string) bool {
	var exists int
	err := a.db.QueryRowContext(ctx,
		"SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		workspaceID, userID).Scan(&exists)
	return err == nil
}

func (a *DBChannelAuthorizer) isMemberOfCrewWorkspace(ctx context.Context, userID, crewID string) bool {
	var wsID string
	err := a.db.QueryRowContext(ctx,
		"SELECT workspace_id FROM crews WHERE id = ? AND deleted_at IS NULL", crewID).Scan(&wsID)
	if err != nil {
		return false
	}
	return a.isMemberOfWorkspace(ctx, userID, wsID)
}

func (a *DBChannelAuthorizer) isMemberOfAgentWorkspace(ctx context.Context, userID, agentID string) bool {
	var wsID string
	err := a.db.QueryRowContext(ctx,
		"SELECT workspace_id FROM agents WHERE id = ? AND deleted_at IS NULL", agentID).Scan(&wsID)
	if err != nil {
		return false
	}
	return a.isMemberOfWorkspace(ctx, userID, wsID)
}

func (a *DBChannelAuthorizer) isMemberOfMissionWorkspace(ctx context.Context, userID, missionID string) bool {
	var wsID string
	err := a.db.QueryRowContext(ctx,
		`SELECT c.workspace_id FROM missions m
		 JOIN crews c ON c.id = m.crew_id
		 WHERE m.id = ?`, missionID).Scan(&wsID)
	if err != nil {
		return false
	}
	return a.isMemberOfWorkspace(ctx, userID, wsID)
}

func (a *DBChannelAuthorizer) isSessionOwner(ctx context.Context, userID, chatID string) bool {
	// Check if the chat belongs to a workspace the user is a member of
	var wsID string
	err := a.db.QueryRowContext(ctx,
		"SELECT workspace_id FROM chats WHERE id = ?", chatID).Scan(&wsID)
	if err != nil {
		return false
	}
	return a.isMemberOfWorkspace(ctx, userID, wsID)
}
