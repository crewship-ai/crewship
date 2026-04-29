package api

// MCP-config JSON-blob parsing + the one-shot migrations that
// turn a stringified mcp_config blob into proper crew_mcp_servers /
// agent_mcp_servers rows. Extracted from crew_integrations.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type parsedMCPServer struct {
	name        string
	displayName string
	transport   string
	endpoint    *string
	command     *string
	argsJSON    *string
	envJSON     *string
}

// parseMCPConfigBlob parses an mcp_config_json blob into a slice of
// parsedMCPServer values. Returns nil (no error) when the blob is empty
// or contains no servers.

func parseMCPConfigBlob(mcpJSON string) ([]parsedMCPServer, error) {
	if mcpJSON == "" {
		return nil, nil
	}

	var config struct {
		MCPServers map[string]struct {
			Command   string            `json:"command"`
			Args      []string          `json:"args"`
			Env       map[string]string `json:"env"`
			URL       string            `json:"url"`
			Type      string            `json:"type"`
			Transport string            `json:"transport"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mcpJSON), &config); err != nil {
		return nil, fmt.Errorf("parse mcp_config_json: %w", err)
	}
	if len(config.MCPServers) == 0 {
		return nil, nil
	}

	servers := make([]parsedMCPServer, 0, len(config.MCPServers))
	for name, srv := range config.MCPServers {
		transport := "stdio"
		if srv.Transport == "streamable-http" || srv.Type == "http" || (srv.Command == "" && srv.URL != "") {
			transport = "streamable-http"
		}

		var argsJSON *string
		if len(srv.Args) > 0 {
			b, _ := json.Marshal(srv.Args)
			s := string(b)
			argsJSON = &s
		}

		var envJSON *string
		if len(srv.Env) > 0 {
			b, _ := json.Marshal(srv.Env)
			s := string(b)
			envJSON = &s
		}

		var endpoint *string
		if srv.URL != "" {
			endpoint = &srv.URL
		}

		var command *string
		if srv.Command != "" {
			command = &srv.Command
		}

		// strings.Title is deprecated in Go 1.18; cases.Title handles
		// Unicode word boundaries correctly. NoLower preserves any
		// uppercase the caller already supplied in `name`.
		displayName := strings.ReplaceAll(name, "-", " ")
		displayName = cases.Title(language.English, cases.NoLower).String(displayName)

		servers = append(servers, parsedMCPServer{
			name:        name,
			displayName: displayName,
			transport:   transport,
			endpoint:    endpoint,
			command:     command,
			argsJSON:    argsJSON,
			envJSON:     envJSON,
		})
	}
	return servers, nil
}

// insertCrewMCPServersFromBlob inserts parsed MCP servers into crew_mcp_servers
// using INSERT OR IGNORE for idempotency (duplicates by crew_id+name are skipped).

func insertCrewMCPServersFromBlob(ctx context.Context, tx *sql.Tx, crewID string, servers []parsedMCPServer) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, srv := range servers {
		id := generateCUID()
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO crew_mcp_servers
				(id, crew_id, name, display_name, transport, endpoint, command, args_json, env_json, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, crewID, srv.name, srv.displayName, srv.transport, srv.endpoint, srv.command, srv.argsJSON, srv.envJSON, now, now); err != nil {
			return fmt.Errorf("insert crew server %q: %w", srv.name, err)
		}
	}
	return nil
}

// verifyAllServersExist checks that all server names from the parsed blob
// exist in crew_mcp_servers for the given crew. Returns true when the count
// matches.

func verifyAllServersExist(ctx context.Context, tx *sql.Tx, crewID string, servers []parsedMCPServer) (bool, error) {
	args := make([]any, 0, len(servers)+1)
	args = append(args, crewID)
	placeholders := ""
	for _, srv := range servers {
		if placeholders != "" {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, srv.name)
	}
	var matching int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id = ? AND name IN ("+placeholders+")",
		args...).Scan(&matching); err != nil {
		return false, err
	}
	return matching == len(servers), nil
}

// MigrateJSONBlobToCrewServers converts a crew's mcp_config_json blob into
// individual crew_mcp_servers rows.  It is idempotent (INSERT OR IGNORE) and
// clears the blob after successful migration.

func MigrateJSONBlobToCrewServers(ctx context.Context, db *sql.DB, logger *slog.Logger, crewID, workspaceID, mcpJSON string) error {
	servers, err := parseMCPConfigBlob(mcpJSON)
	if err != nil || len(servers) == 0 {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := insertCrewMCPServersFromBlob(ctx, tx, crewID, servers); err != nil {
		return err
	}

	// Clear the JSON blob only if all configured server names exist in the table.
	allExist, err := verifyAllServersExist(ctx, tx, crewID, servers)
	if err != nil {
		return fmt.Errorf("count matching crew servers: %w", err)
	}
	if allExist {
		if _, err := tx.ExecContext(ctx, `UPDATE crews SET mcp_config_json = NULL WHERE id = ?`, crewID); err != nil {
			return fmt.Errorf("clear mcp_config_json: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	logger.Info("migrated crew MCP config from JSON blob to tables", "crew_id", crewID, "servers", len(servers))
	return nil
}

// MigrateJSONBlobToAgentServers converts an agent's mcp_config_json blob into
// crew_mcp_servers rows (owned by the agent's crew) plus agent_mcp_bindings
// that link the agent to each server.  It is idempotent and clears the blob
// after successful migration.

func MigrateJSONBlobToAgentServers(ctx context.Context, db *sql.DB, logger *slog.Logger, agentID, crewID, workspaceID, mcpJSON string) error {
	servers, err := parseMCPConfigBlob(mcpJSON)
	if err != nil || len(servers) == 0 {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := insertCrewMCPServersFromBlob(ctx, tx, crewID, servers); err != nil {
		return err
	}

	// Resolve actual server IDs and create agent bindings.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, srv := range servers {
		var resolvedServerID string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM crew_mcp_servers WHERE crew_id = ? AND name = ?`,
			crewID, srv.name).Scan(&resolvedServerID); err != nil {
			return fmt.Errorf("resolve crew server id %q: %w", srv.name, err)
		}

		bindingID := generateCUID()
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO agent_mcp_bindings
				(id, agent_id, mcp_server_id, mcp_server_scope, enabled, created_at)
			VALUES (?, ?, ?, 'crew', 1, ?)`,
			bindingID, agentID, resolvedServerID, now); err != nil {
			return fmt.Errorf("insert agent binding for server %q: %w", srv.name, err)
		}
	}

	// Clear the JSON blob only if all configured server names exist in crew_mcp_servers.
	allExist, err := verifyAllServersExist(ctx, tx, crewID, servers)
	if err != nil {
		return fmt.Errorf("count matching crew servers for agent: %w", err)
	}
	if allExist {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET mcp_config_json = NULL WHERE id = ?`, agentID); err != nil {
			return fmt.Errorf("clear agent mcp_config_json: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit agent migration: %w", err)
	}

	logger.Info("migrated agent MCP config from JSON blob to tables", "agent_id", agentID, "crew_id", crewID, "servers", len(servers))
	return nil
}
