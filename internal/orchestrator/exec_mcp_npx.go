package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

func nodeJSLauncher(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	switch parts[0] {
	case "npx", "npm":
		return parts[0]
	default:
		return ""
	}
}

// isNpxCommand returns true if the command's first token is exactly "npx" or "npm".
func isNpxCommand(cmd string) bool {
	return nodeJSLauncher(cmd) != ""
}

// filterNpxServers checks whether npx/npm is available in the container and removes
// stdio servers that require them if missing. Returns the filtered list.
func filterNpxServers(ctx context.Context, container provider.ContainerProvider, containerID string, servers []MCPServerConfig, logger *slog.Logger) []MCPServerConfig {
	// 1. Check if any server uses npx/npm — if none, return unchanged.
	hasNodeLauncher := false
	for _, s := range servers {
		if s.Transport == "stdio" && isNpxCommand(s.Command) {
			hasNodeLauncher = true
			break
		}
	}
	if !hasNodeLauncher {
		return servers
	}

	// 2. Collect unique launchers needed (only "npx" or "npm" are allowed).
	// Value meaning: true = confirmed available, false = confirmed missing.
	// Launchers with probe errors are removed from the map (kept by default).
	launchers := map[string]bool{}
	for _, s := range servers {
		if s.Transport == "stdio" {
			if l := nodeJSLauncher(s.Command); l != "" {
				launchers[l] = false
			}
		}
	}

	// 3. Probe each launcher with a fixed, safe command (no interpolation).
	probeCommands := map[string][]string{
		"npx": {"sh", "-c", "command -v npx >/dev/null 2>&1 && echo ok"},
		"npm": {"sh", "-c", "command -v npm >/dev/null 2>&1 && echo ok"},
	}
	for launcher := range launchers {
		probe, ok := probeCommands[launcher]
		if !ok {
			// Unknown launcher — should not happen due to nodeJSLauncher filter,
			// but keep the server by removing from the map (not filtering it out).
			delete(launchers, launcher)
			continue
		}
		cfg := provider.ExecConfig{
			ContainerID: containerID,
			Cmd:         probe,
			User:        "1001:1001",
		}
		result, err := container.Exec(ctx, cfg)
		if err != nil {
			// Exec failure (container not ready, timeout, etc.) — don't drop the
			// server; remove from map so it won't be filtered out.
			logger.Warn("probe exec failed, keeping servers that require "+launcher,
				"error", err,
				"container_id", containerID[:min(12, len(containerID))])
			delete(launchers, launcher)
			continue
		}
		output, _ := io.ReadAll(result.Reader)
		result.Reader.Close()
		if strings.TrimSpace(string(output)) != "" {
			launchers[launcher] = true
		}
	}

	// If all remaining launchers available, return unchanged.
	allAvailable := true
	for _, available := range launchers {
		if !available {
			allAvailable = false
			break
		}
	}
	if allAvailable {
		return servers
	}

	// 4. Filter out servers whose launcher is confirmed missing.
	var skipped []string
	var filtered []MCPServerConfig
	for _, s := range servers {
		if s.Transport == "stdio" {
			if l := nodeJSLauncher(s.Command); l != "" {
				if available, probed := launchers[l]; probed && !available {
					skipped = append(skipped, s.Name)
					continue
				}
			}
		}
		filtered = append(filtered, s)
	}
	if len(skipped) == 0 {
		return servers
	}
	logger.Warn("launcher not found in container, skipping stdio MCP servers",
		"skipped_servers", skipped,
		"container_id", containerID[:min(12, len(containerID))])
	return filtered
}

// filterMergedMCPConfigNpx parses a merged .mcp.json config, checks if npx is available
// in the container, and removes stdio servers that require npx/npm if it's missing.
// Returns the (possibly filtered) JSON string and a list of skipped server names.
func filterMergedMCPConfigNpx(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	mcpJSON string,
	logger *slog.Logger,
) (string, []string) {
	if mcpJSON == "" {
		return mcpJSON, nil
	}

	type serverEntry struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type wrapper struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	var w wrapper
	if err := json.Unmarshal([]byte(mcpJSON), &w); err != nil {
		return mcpJSON, nil
	}

	// Build MCPServerConfig slice so we can reuse filterNpxServers.
	var configs []MCPServerConfig
	nameOrder := make([]string, 0, len(w.MCPServers))
	parseFailed := make(map[string]bool)
	for name, raw := range w.MCPServers {
		nameOrder = append(nameOrder, name)
		var entry serverEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			parseFailed[name] = true
			continue
		}
		configs = append(configs, MCPServerConfig{Name: name, Transport: entry.Type, Command: entry.Command})
	}

	filtered := filterNpxServers(ctx, container, containerID, configs, logger)

	// If nothing was removed, return original JSON unchanged.
	if len(filtered) == len(configs) {
		return mcpJSON, nil
	}

	// Build set of kept names and collect skipped names.
	// Preserve entries that failed to parse — they weren't filtered by npx logic.
	kept := make(map[string]bool, len(filtered))
	for _, s := range filtered {
		kept[s.Name] = true
	}
	var skipped []string
	for _, name := range nameOrder {
		if !kept[name] && !parseFailed[name] {
			delete(w.MCPServers, name)
			skipped = append(skipped, name)
		}
	}
	if len(w.MCPServers) == 0 {
		return "", skipped
	}
	out := map[string]interface{}{"mcpServers": w.MCPServers}
	b, err := json.Marshal(out)
	if err != nil {
		logger.Error("failed to re-marshal MCP config after npx filtering", "error", err)
		return mcpJSON, nil
	}
	return string(b), skipped
}

// buildMCPConfig converts resolved MCP server configs into Claude Code's --mcp-config JSON format.
// Supports both HTTP (remote) and stdio (local npm/pip) MCP servers.
