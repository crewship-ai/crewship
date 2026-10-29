package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// An agent's tool profile, MCP servers, crew and role decide what it may
// reach, so a change to any of them is a permission change: its compliance
// row and chained journal entry commit with it on the writer transaction, and
// an audit failure rolls the change back. Unlike appendSettingsAudit the actor
// may be absent: the internal hire path runs on behalf of an agent, and is
// recorded as a system actor exactly as the best-effort writer recorded it.
func appendAgentAudit(ctx context.Context, tx *sql.Tx, emitter journal.Emitter, workspaceID, userID, action, agentID string, metadata map[string]any) (func(), error) {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		raw = []byte("{}")
	}
	var userArg any
	actorType := journal.ActorSystem
	if userID != "" {
		userArg, actorType = userID, journal.ActorUser
	}
	auditID, stamp := generateCUID(), time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,'AGENT',?,?,?)`,
		auditID, workspaceID, userArg, action, agentID, string(raw), stamp)
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, errors.Join(err, errors.New("agent compliance audit not inserted"))
	}
	// The entry names the agent only in its payload and refs, not agent_id:
	// that column follows the agents row (ON DELETE SET NULL), and a reverted
	// hire deletes the row its chained record describes.
	payload := map[string]any{"action": action, "entity_type": "AGENT", "entity_id": agentID}
	if metadata != nil {
		payload["metadata"] = metadata
	}
	return appendAuthorizationAudit(ctx, tx, emitter, journal.Entry{
		WorkspaceID: workspaceID, Type: classifyAuditAction(action), Severity: journal.SeverityNotice,
		ActorType: actorType, ActorID: userID, Summary: fmt.Sprintf("%s AGENT %s", action, shortenID(agentID, 12)),
		Payload: payload,
		Refs:    map[string]any{"entity_type": "AGENT", "entity_id": agentID},
	})
}

// revertAgentTimeout bounds a compensating removal that has outlived its
// request.
const revertAgentTimeout = 30 * time.Second

// revertCreatedAgent removes an agent whose creation was committed (and
// chained) but whose required follow-up row could not be written, and chains
// the removal in the same transaction. It runs detached from the request's
// cancellation: a client that disconnects mid-request must not leave a live
// agent behind without its revert record.
func revertCreatedAgent(parent context.Context, db *sql.DB, emitter journal.Emitter, workspaceID, agentID, inboxID, userID, action, cause string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), revertAgentTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if inboxID != "" {
		// Best effort inside the transaction: a missing inbox row must not
		// keep the agent alive.
		if _, err := tx.ExecContext(ctx, `DELETE FROM inbox_items WHERE id = ?`, inboxID); err != nil {
			slog.Warn("compensating inbox delete", "error", err, "inbox_id", inboxID)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id = ? AND workspace_id = ?`, agentID, workspaceID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("compensated agent not found"))
	}
	notify, err := appendAgentAudit(ctx, tx, emitter, workspaceID, userID, action, agentID, map[string]any{"cause": cause})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if notify != nil {
		notify()
	}
	return nil
}

// agentPrivileges is the access-relevant part of an agent row as the audit
// records it. The MCP configuration appears only in its redacted form.
func agentPrivileges(ctx context.Context, tx *sql.Tx, workspaceID, agentID string) (map[string]any, error) {
	var crewID, role, profile, mcp sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT crew_id, agent_role, tool_profile, mcp_config_json FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&crewID, &role, &profile, &mcp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"crew_id": crewID.String, "agent_role": role.String, "tool_profile": profile.String,
		"mcp_servers": mcpConfigAudit(mcp.String)}, nil
}

// mcpConfigAudit describes an MCP configuration without any value that could
// be a credential: per server its name, transport, the executable's base name
// and the NAMES of its environment variables and headers. Arguments, URLs and
// every env/header value are left out, since any of them may carry a token.
func mcpConfigAudit(raw string) any {
	servers := []map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return servers
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type      string         `json:"type"`
			Transport string         `json:"transport"`
			Command   string         `json:"command"`
			URL       string         `json:"url"`
			Env       map[string]any `json:"env"`
			Headers   map[string]any `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return map[string]any{"unparseable": true}
	}
	for name, s := range cfg.MCPServers {
		transport := s.Type
		if transport == "" {
			transport = s.Transport
		}
		if transport == "" && s.Command != "" {
			transport = "stdio"
		} else if transport == "" && s.URL != "" {
			transport = "http"
		}
		command := auditCommandName(s.Command)
		servers = append(servers, map[string]any{"name": name, "transport": transport, "command": command,
			"env_keys": auditKeyNames(s.Env), "header_keys": auditKeyNames(s.Headers)})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i]["name"].(string) < servers[j]["name"].(string) })
	return servers
}

// auditCommandToken is what an executable name looks like. A token outside
// it (a quote, a shell operator, an assignment) may be part of a value.
var auditCommandToken = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)

// auditCommandName is the base name of a server command's executable.
// Leading NAME=value assignments ("TOKEN=abc npx ...") are skipped; the first
// other token is recorded only when it plainly is an executable name, so a
// quoted value split by whitespace (`TOKEN="a b" npx`) yields "" rather than
// a fragment of it.
func auditCommandName(command string) string {
	for _, field := range strings.Fields(command) {
		if strings.Contains(field, "=") {
			continue
		}
		if !auditCommandToken.MatchString(field) {
			return ""
		}
		return path.Base(field)
	}
	return ""
}

func auditKeyNames(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
