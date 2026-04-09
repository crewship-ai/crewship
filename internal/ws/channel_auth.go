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

func NewDBChannelAuthorizer(db *sql.DB) *DBChannelAuthorizer {
	return &DBChannelAuthorizer{db: db}
}

// CanSubscribe returns true if the user is allowed to subscribe to the given channel.
// Channel format: "type:id" (e.g., "workspace:abc123", "session:xyz456").
func (a *DBChannelAuthorizer) CanSubscribe(ctx context.Context, userID, channel string) bool {
	parts := strings.SplitN(channel, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return false
	}
	chType, chID := parts[0], parts[1]

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
