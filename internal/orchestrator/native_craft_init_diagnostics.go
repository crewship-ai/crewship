package orchestrator

import "log/slog"

// CLI self-report only, never tool execution, document authority or a save receipt.
// Child-controlled metadata must never be logged directly by a private task.
type nativeCraftInitDiagnostic struct{ emitted bool }

func (d *nativeCraftInitDiagnostic) observe(logger *slog.Logger, req AgentRunRequest, event AgentEvent) {
	if d.emitted || !req.nativeCraftTools || !req.strictContentPrivacy || req.mooringDocumentTools || event.Type != "system" {
		return
	}
	meta, ok := event.Metadata.(map[string]any)
	if !ok || meta["subtype"] != "init" {
		return
	}
	d.emitted = true
	if logger == nil {
		return
	}
	reported, connected := false, false
	for _, server := range initMetaObjects(meta["mcp_servers"]) {
		if server["name"] == "crewship-craft" {
			reported = true
			connected = server["status"] == "connected"
			break
		}
	}
	// Unknown or absent shapes are not evidence of an observed empty catalogue.
	tools, inventory := meta["tools"].([]string)
	read, acquire, renew, apply, release := false, false, false, false, false
	for _, tool := range tools {
		switch tool {
		case "mcp__crewship-craft__craft_read":
			read = true
		case "mcp__crewship-craft__craft_acquire":
			acquire = true
		case "mcp__crewship-craft__craft_renew":
			renew = true
		case "mcp__crewship-craft__craft_apply":
			apply = true
		case "mcp__crewship-craft__craft_release":
			release = true
		}
	}
	logger.Info("native Craft CLI init self-report",
		"craft_server_reported", reported,
		"craft_server_connected", connected,
		"tool_inventory_reported", inventory,
		"craft_read_reported", read,
		"craft_acquire_reported", acquire,
		"craft_renew_reported", renew,
		"craft_apply_reported", apply,
		"craft_release_reported", release)
}

func (d *nativeCraftInitDiagnostic) report(logger *slog.Logger, req AgentRunRequest) {
	if logger == nil || !req.nativeCraftTools || !req.strictContentPrivacy || req.mooringDocumentTools {
		return
	}
	logger.Info("native Craft CLI init observation finished", "init_observed", d.emitted)
}
