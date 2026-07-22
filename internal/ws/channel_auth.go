package ws

import (
	"context"
	"database/sql"
	"errors"
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

// CanSubscribe reports whether the user is allowed to subscribe to the given
// channel. Channel format: "type:id" (e.g., "workspace:abc123",
// "session:xyz456").
//
// The two return values distinguish a DEFINITIVE deny (false, nil — the
// membership row provably does not exist) from a FAILED CHECK (false,
// non-nil err — DB timeout, closed handle, momentary unavailability).
// Callers on the grant path (subscribe/resume/send) must treat both as
// deny — fail closed. The hub's periodic re-authorization sweep must only
// act on a definitive deny: revoking every live subscription because one
// tick's queries hit a transient DB hiccup would mass-disconnect clients
// that are still perfectly authorized (and the frontend does not
// re-subscribe without a reconnect).
//
// Structural denies (missing authorizer/DB/userID, malformed channel,
// unknown channel type) are definitive: they cannot heal on retry.
func (a *DBChannelAuthorizer) CanSubscribe(ctx context.Context, userID, channel string) (bool, error) {
	if a == nil || a.db == nil || userID == "" {
		return false, nil
	}
	// Parse "type:id" without allocating a []string — previously strings.SplitN
	// cost one slice header per subscription call.
	idx := strings.IndexByte(channel, ':')
	if idx <= 0 || idx == len(channel)-1 {
		return false, nil
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
		return userID == chID, nil
	case "providers":
		// global channel — any authenticated user
		return userID != "", nil
	default:
		return false, nil
	}
}

// existsRow adapts a single-row EXISTS-style Scan result to the
// (allowed, error) contract: a row means allow, sql.ErrNoRows means a
// definitive deny, anything else is a failed check the caller must not
// interpret as "access removed".
func existsRow(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func (a *DBChannelAuthorizer) isMemberOfWorkspace(ctx context.Context, userID, workspaceID string) (bool, error) {
	var exists int
	err := a.db.QueryRowContext(ctx,
		"SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		workspaceID, userID).Scan(&exists)
	return existsRow(err)
}

func (a *DBChannelAuthorizer) isMemberOfCrewWorkspace(ctx context.Context, userID, crewID string) (bool, error) {
	var wsID string
	err := a.db.QueryRowContext(ctx,
		"SELECT workspace_id FROM crews WHERE id = ? AND deleted_at IS NULL", crewID).Scan(&wsID)
	if err != nil {
		// No such (live) crew is a definitive deny; anything else is a
		// failed check.
		return existsRow(err)
	}
	return a.isMemberOfWorkspace(ctx, userID, wsID)
}

func (a *DBChannelAuthorizer) isMemberOfAgentWorkspace(ctx context.Context, userID, agentID string) (bool, error) {
	var wsID string
	err := a.db.QueryRowContext(ctx,
		"SELECT workspace_id FROM agents WHERE id = ? AND deleted_at IS NULL", agentID).Scan(&wsID)
	if err != nil {
		return existsRow(err)
	}
	return a.isMemberOfWorkspace(ctx, userID, wsID)
}

func (a *DBChannelAuthorizer) isMemberOfMissionWorkspace(ctx context.Context, userID, missionID string) (bool, error) {
	var wsID string
	err := a.db.QueryRowContext(ctx,
		`SELECT c.workspace_id FROM missions m
		 JOIN crews c ON c.id = m.crew_id
		 WHERE m.id = ?`, missionID).Scan(&wsID)
	if err != nil {
		return existsRow(err)
	}
	return a.isMemberOfWorkspace(ctx, userID, wsID)
}

func (a *DBChannelAuthorizer) isSessionOwner(ctx context.Context, userID, chatID string) (bool, error) {
	// Check if the chat belongs to a workspace the user is a member of
	var wsID string
	err := a.db.QueryRowContext(ctx,
		"SELECT workspace_id FROM chats WHERE id = ?", chatID).Scan(&wsID)
	if err != nil {
		return existsRow(err)
	}
	return a.isMemberOfWorkspace(ctx, userID, wsID)
}
