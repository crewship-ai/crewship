package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// Agent-initiated notifications: which channels an agent may post to.
//
// The capability question here is not "can this agent reach the network" —
// it always could — but "which of this workspace's channels may THIS agent
// post to". The answer defaults to none, and only a human can change it.
//
// Default-deny matters more here than it looks. A workspace channel is
// something an admin stood up for a team, on a surface (Slack, Discord) where
// a reader cannot tell an agent's message from a colleague's at a glance. An
// agent that could post to any channel by virtue of existing turns one
// confused or prompt-injected agent into a workspace-wide megaphone.

// AgentPairing is one grant: this agent may post to this channel.
type AgentPairing struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ChannelID   string `json:"channel_id"`
	AgentID     string `json:"agent_id"`
	GrantedBy   string `json:"granted_by,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// PairingStore persists notification_channel_agents (v170).
type PairingStore struct {
	db *sql.DB
}

// NewPairingStore builds a store over the given DB handle.
func NewPairingStore(db *sql.DB) *PairingStore { return &PairingStore{db: db} }

// Allow grants agentID permission to post to channelID. Idempotent: granting
// twice is a no-op rather than an error, so a re-run of a provisioning script
// doesn't fail.
//
// The caller is responsible for having verified that channelID belongs to
// workspaceID and that the granting user is allowed to manage it — this store
// records the decision, it does not make it.
func (s *PairingStore) Allow(ctx context.Context, workspaceID, channelID, agentID, grantedBy string) error {
	if workspaceID == "" || channelID == "" || agentID == "" {
		return fmt.Errorf("notify: workspace, channel and agent are all required to pair")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO notification_channel_agents
		    (id, workspace_id, channel_id, agent_id, granted_by)
		VALUES (?, ?, ?, ?, NULLIF(?, ''))`,
		newPairingID(), workspaceID, channelID, agentID, grantedBy)
	if err != nil {
		return fmt.Errorf("notify: pair agent to channel: %w", err)
	}
	return nil
}

// Deny revokes the grant. Returns whether a row was removed, so a caller can
// tell "revoked" from "there was nothing to revoke".
func (s *PairingStore) Deny(ctx context.Context, workspaceID, channelID, agentID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM notification_channel_agents
		WHERE workspace_id = ? AND channel_id = ? AND agent_id = ?`,
		workspaceID, channelID, agentID)
	if err != nil {
		return false, fmt.Errorf("notify: unpair agent from channel: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// IsPaired reports whether agentID may post to channelID.
//
// This is the authorization check on the agent send path. It is deliberately
// a single indexed lookup with no fallback, no "unless the channel is
// personal", and no role escalation: any additional way to be authorised is
// another way to be authorised by accident.
func (s *PairingStore) IsPaired(ctx context.Context, workspaceID, channelID, agentID string) (bool, error) {
	if workspaceID == "" || channelID == "" || agentID == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM notification_channel_agents
		WHERE workspace_id = ? AND channel_id = ? AND agent_id = ?`,
		workspaceID, channelID, agentID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("notify: check agent pairing: %w", err)
	}
	return true, nil
}

// ListForAgent returns the channels agentID may post to. Used by the
// notify_send tool's discovery path so an agent can be told what it has
// rather than guessing a channel id and getting a permission error.
func (s *PairingStore) ListForAgent(ctx context.Context, workspaceID, agentID string) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.type, c.provider, c.config_json, c.enabled
		FROM notification_channel_agents p
		JOIN notification_channels c ON c.id = p.channel_id
		WHERE p.workspace_id = ? AND p.agent_id = ? AND c.deleted_at IS NULL
		ORDER BY c.created_at DESC`, workspaceID, agentID)
	if err != nil {
		return nil, fmt.Errorf("notify: list agent channels: %w", err)
	}
	defer rows.Close()

	var out []Channel
	for rows.Next() {
		var (
			c        Channel
			typ      string
			provider string
			cfg      string
			enabled  int
		)
		if err := rows.Scan(&c.ID, &typ, &provider, &cfg, &enabled); err != nil {
			return nil, fmt.Errorf("notify: scan agent channel: %w", err)
		}
		c.WorkspaceID = workspaceID
		c.Type = ChannelType(typ)
		c.Provider = provider
		c.Enabled = enabled != 0
		// config_json is intentionally NOT unmarshalled into URL/To here: an
		// agent has no business learning a channel's destination address, only
		// that a channel exists and what kind it is.
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListForChannel returns the agent ids paired to a channel — the "who can
// post here?" view an admin needs when reviewing a channel.
func (s *PairingStore) ListForChannel(ctx context.Context, workspaceID, channelID string) ([]AgentPairing, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, channel_id, agent_id, COALESCE(granted_by, ''), created_at
		FROM notification_channel_agents
		WHERE workspace_id = ? AND channel_id = ?
		ORDER BY created_at`, workspaceID, channelID)
	if err != nil {
		return nil, fmt.Errorf("notify: list channel agents: %w", err)
	}
	defer rows.Close()

	var out []AgentPairing
	for rows.Next() {
		var p AgentPairing
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.ChannelID, &p.AgentID, &p.GrantedBy, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("notify: scan channel agent: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func newPairingID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// The UNIQUE(channel_id, agent_id) constraint plus INSERT OR IGNORE
		// makes a collision harmless.
		return "nca_fallback"
	}
	return "nca_" + hex.EncodeToString(b)
}
