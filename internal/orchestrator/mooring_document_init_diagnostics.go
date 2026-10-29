package orchestrator

import "log/slog"

// This is CLI-reported readiness, never authority or proof of a tool call/save.
// Private runs must not send raw init metadata to the generic journal: names,
// errors, paths and other child-controlled strings can contain document data.
type mooringDocumentInitDiagnostic struct{ emitted bool }

func (d *mooringDocumentInitDiagnostic) observe(logger *slog.Logger, req AgentRunRequest, event AgentEvent) {
	if d.emitted || !req.strictContentPrivacy || !req.mooringDocumentTools || event.Type != "system" {
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
	serverReported, connected, read, submit := false, false, false, false
	for _, server := range initMetaObjects(meta["mcp_servers"]) {
		if server["name"] == "crewship-document" {
			serverReported = true
			connected = server["status"] == "connected"
			break
		}
	}
	// The Claude adapter supplies []string; absence/unknown shapes are not
	// reported as a positively observed empty inventory.
	tools, inventoryReported := meta["tools"].([]string)
	for _, tool := range tools {
		read = read || tool == "mcp__crewship-document__doc_read"
		submit = submit || tool == "mcp__crewship-document__doc_submit"
	}
	logger.Info("mooring document CLI init reported",
		"run_id", req.RunID,
		"document_server_reported", serverReported,
		"document_server_connected", connected,
		"tool_inventory_reported", inventoryReported,
		"doc_read_reported", read,
		"doc_submit_reported", submit)
}
