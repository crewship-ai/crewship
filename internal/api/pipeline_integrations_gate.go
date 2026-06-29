package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// crewIntegrationsWildcard is the sentinel key resolveCrewIntegrations
// inserts when the workspace runs the Composio "default connector" — under
// that mode agents inherit access to ALL of the workspace's connected apps
// without a per-app MCP server row, so we cannot enumerate them cheaply on
// the run hot path. The gate treats the wildcard as "every integration is
// available" and lets the run through.
const crewIntegrationsWildcard = "*"

// gateMissingIntegrations enforces a routine's declared
// integrations_required against the integrations its author crew has
// actually connected. It returns true — having ALREADY written a 422
// Problem Details with a machine-readable `missing_integrations` array —
// when the run MUST be blocked; the caller returns immediately.
//
// Semantics:
//   - empty `required` → fast path, returns false (no DB work, no overhead).
//   - crewID == "" (no author crew to resolve against) → fail-open.
//   - resolver error → FAIL-OPEN: log a warning and ALLOW the run. A bug in
//     integration resolution must never wedge every run of every routine —
//     a forgotten integration is a soft failure the agent/operator can fix,
//     but a hard block on all runs would be a self-inflicted outage. We bias
//     to availability and lean on the explicit-missing path below for the
//     real signal.
func (h *PipelineHandler) gateMissingIntegrations(w http.ResponseWriter, r *http.Request, workspaceID, crewID, crewName string, required []string) bool {
	if len(required) == 0 {
		return false // no-op fast path
	}
	if h.db == nil || crewID == "" {
		h.logger.Warn("integration gate: no crew/db to resolve against, allowing run (fail-open)",
			"workspace_id", workspaceID, "crew_id", crewID)
		return false
	}
	available, err := resolveCrewIntegrations(r.Context(), h.db, workspaceID, crewID)
	if err != nil {
		// FAIL-OPEN — see the doc comment. Never block on a resolver bug.
		h.logger.Warn("integration gate: resolution failed, allowing run (fail-open)",
			"workspace_id", workspaceID, "crew_id", crewID, "error", err)
		return false
	}
	if available[crewIntegrationsWildcard] {
		return false
	}
	var missing []string
	for _, want := range required {
		if !available[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return false
	}

	if crewName == "" {
		crewName = lookupCrewName(r.Context(), h.db, workspaceID, crewID)
	}
	if crewName == "" {
		crewName = crewID
	}
	detail := fmt.Sprintf("routine requires integration %q not connected for crew %q", missing[0], crewName)
	if len(missing) > 1 {
		detail = fmt.Sprintf("routine requires %d integrations not connected for crew %q: %s",
			len(missing), crewName, strings.Join(missing, ", "))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":                 "about:blank",
		"title":                http.StatusText(http.StatusUnprocessableEntity),
		"status":               http.StatusUnprocessableEntity,
		"detail":               detail,
		"instance":             r.URL.Path,
		"missing_integrations": missing,
	})
	return true
}

// resolveCrewIntegrations returns the set of third-party integration slugs
// (Composio toolkits, lowercased) the crew's agents have connected.
//
// Resolution is DB-only (no Composio API call — this runs on the run hot
// path): it reads the per-agent Composio MCP server rows the bind flow
// writes (workspace_mcp_servers with icon='composio', display_name shaped
// "Composio: <toolkit> · <mode>") for every agent in the crew, and recovers
// the toolkit slug from the display name.
//
// Limitations (documented here + in docs/guides/routines.mdx):
//   - Under the workspace "default connector" (composio_settings carries a
//     default user + server) agents inherit ALL connected apps without
//     per-app rows; we cannot enumerate those without a Composio API call, so
//     we return the wildcard sentinel and the gate passes for everything.
//   - It reflects what is WIRED, not live connection health — a revoked
//     Composio account still shows available until its binding row is removed.
//   - It considers workspace-scoped Composio bindings (what the bind flow
//     produces); hypothetical crew-scoped Composio rows are not enumerated.
func resolveCrewIntegrations(ctx context.Context, db *sql.DB, workspaceID, crewID string) (map[string]bool, error) {
	out := make(map[string]bool)

	// Default-connector wildcard: when the workspace has a configured
	// default Composio user + server, agents reach every connected app, so
	// we can't (cheaply) tell what's missing — treat all as available.
	var du, ds sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT default_user_id, default_mcp_server_id FROM composio_settings WHERE workspace_id = ?`,
		workspaceID).Scan(&du, &ds)
	switch {
	case err == nil:
		if strings.TrimSpace(du.String) != "" && strings.TrimSpace(ds.String) != "" {
			out[crewIntegrationsWildcard] = true
			return out, nil
		}
	case errors.Is(err, sql.ErrNoRows):
		// No Composio settings row — fall through to explicit bindings.
	default:
		return nil, fmt.Errorf("resolve composio settings: %w", err)
	}

	// Explicit per-agent Composio bindings (workspace-scoped servers) for
	// every non-deleted agent in the crew.
	rows, err := db.QueryContext(ctx, `
		SELECT ws.display_name, ws.name
		FROM agent_mcp_bindings b
		JOIN agents a ON a.id = b.agent_id AND a.crew_id = ? AND a.deleted_at IS NULL
		JOIN workspace_mcp_servers ws ON ws.id = b.mcp_server_id
		WHERE b.enabled = 1 AND b.mcp_server_scope = 'workspace'
		  AND ws.icon = 'composio' AND ws.enabled = 1 AND ws.deleted_at IS NULL`,
		crewID)
	if err != nil {
		return nil, fmt.Errorf("resolve crew integrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var display, name string
		if err := rows.Scan(&display, &name); err != nil {
			return nil, err
		}
		if slug := composioToolkitFromServer(display, name); slug != "" {
			out[slug] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// composioToolkitFromServer recovers the Composio toolkit slug from a bound
// MCP server row. It prefers the display_name the bind flow writes
// ("Composio: <toolkit> · <mode>"); when that shape is absent it falls back
// to the server name suffix ("composio-<agentID>-<toolkit>"). Returns "" when
// neither yields a slug.
func composioToolkitFromServer(display, name string) string {
	const prefix = "Composio: "
	if strings.HasPrefix(display, prefix) {
		rest := display[len(prefix):]
		// The mode label is separated by a middle dot ("·"); strip it and
		// any surrounding whitespace to leave just the toolkit slug.
		if i := strings.Index(rest, "·"); i >= 0 {
			rest = rest[:i]
		}
		if slug := strings.ToLower(strings.TrimSpace(rest)); slug != "" {
			return slug
		}
	}
	if strings.HasPrefix(name, "composio-") {
		if i := strings.LastIndex(name, "-"); i >= 0 && i+1 < len(name) {
			return strings.ToLower(strings.TrimSpace(name[i+1:]))
		}
	}
	return ""
}

// lookupCrewName resolves a crew's display name for the gate's human
// `detail` message. Best-effort: any error (missing crew, DB down) yields ""
// and the caller falls back to the crew id.
func lookupCrewName(ctx context.Context, db *sql.DB, workspaceID, crewID string) string {
	var name string
	if err := db.QueryRowContext(ctx,
		`SELECT name FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&name); err != nil {
		return ""
	}
	return name
}
