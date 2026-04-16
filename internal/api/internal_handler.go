package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// buildEthosBlock returns the [CREWSHIP ETHOS] system prompt block.
// This block is non-overridable and injected for every agent, with role-specific variations.
func buildEthosBlock(agentRole string) string {
	var roleText string
	switch agentRole {
	case "LEAD":
		roleText = `You are a crew member with orchestration responsibility on the Crewship -- ` +
			`an expedition with a shared purpose. You are not a boss -- you are an equal ` +
			`colleague who carries the soul and mission of the expedition to the whole team, ` +
			`and that is how the ship sails towards adventure. Your crew trusts you because ` +
			`you are one of them, just with a different task.`
	// COORDINATOR role is deprecated (2026-04-16); see orchestrator.BuildCoordinatorContext.
	// Ethos text retained so existing COORDINATOR agents still get a coherent system prompt.
	case "COORDINATOR":
		roleText = `You are a workspace member with coordination responsibility on the Crewship -- ` +
			`connecting the expeditions of all crews towards one shared goal. You are not above ` +
			`anyone -- you are an equal who sees the bigger picture and helps crews align ` +
			`their efforts towards the common adventure.`
	default: // AGENT
		roleText = `You are part of a crew on the Crewship -- an expedition with a shared purpose ` +
			`that transcends any individual. Your work matters because it contributes to ` +
			`something greater than yourself.`
	}
	return "[CREWSHIP ETHOS]\n" + roleText
}

// WriteAuditLog records an action in the audit_logs table. It is safe to call from any goroutine.
func WriteAuditLog(ctx context.Context, db *sql.DB, action, entityType, entityID, userID, workspaceID string, metadata map[string]interface{}) {
	now := time.Now().UTC().Format(time.RFC3339)
	metaJSON := "{}"
	if metadata != nil {
		if b, err := json.Marshal(metadata); err == nil {
			metaJSON = string(b)
		}
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_logs (id, workspace_id, user_id, action, entity_type, entity_id, metadata, created_at)
		VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, userID, action, entityType, entityID, metaJSON, now)
	if err != nil {
		slog.Warn("audit log write failed", "error", err, "action", action)
	}
}

// decryptCredential is a shared helper that decrypts an encrypted credential value.
func decryptCredential(encValue string) (string, error) {
	return encryption.Decrypt(encValue)
}

// MCP credential auto-resolution functions are in internal_mcp.go
