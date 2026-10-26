package orchestrator

import "log/slog"

// CLI self-report only: never a host authorization, read receipt or Saved.
// Keep at most 32 opaque IDs in memory for result pairing; never log them or
// any child-controlled name, arguments, text, result, path or error string.
type mooringDocumentToolDiagnostic struct {
	tracked                    map[string]uint8 // 1 = read, 2 = submit, 0 = consumed/ambiguous
	readCalls, submitCalls     int
	readResults, submitResults int
	readErrors, submitErrors   int
	incomplete                 bool
}

func (d *mooringDocumentToolDiagnostic) observe(req AgentRunRequest, event AgentEvent) {
	if !req.strictContentPrivacy || !req.mooringDocumentTools {
		return
	}
	meta, ok := event.Metadata.(map[string]any)
	if !ok {
		return
	}
	if event.Type == "tool_call" {
		name, _ := meta["tool_name"].(string)
		if name != "mcp__crewship-document__doc_read" && name != "mcp__crewship-document__doc_submit" {
			return
		}
		if d.readCalls+d.submitCalls >= 32 {
			d.incomplete = true
			return
		}
		submit := name == "mcp__crewship-document__doc_submit"
		if submit {
			d.submitCalls++
		} else {
			d.readCalls++
		}
		id, _ := meta["tool_id"].(string)
		if len(id) > 0 && len(id) <= 256 {
			if d.tracked == nil {
				d.tracked = make(map[string]uint8)
			}
			if _, seen := d.tracked[id]; seen {
				d.tracked[id] = 0
				d.incomplete = true
				return
			}
			d.tracked[id] = 1
			if submit {
				d.tracked[id] = 2
			}
		} else {
			d.incomplete = true
		}
		return
	}
	if event.Type != "tool_result" {
		return
	}
	id, _ := meta["tool_use_id"].(string)
	kind := d.tracked[id]
	if kind == 0 {
		return
	}
	d.tracked[id] = 0 // Retain the tombstone: reused IDs cannot pair twice.
	failed, _ := meta["is_error"].(bool)
	if kind == 2 {
		d.submitResults++
		if failed {
			d.submitErrors++
		}
	} else {
		d.readResults++
		if failed {
			d.readErrors++
		}
	}
}

func (d *mooringDocumentToolDiagnostic) report(logger *slog.Logger, req AgentRunRequest) {
	if logger == nil || !req.strictContentPrivacy || !req.mooringDocumentTools {
		return
	}
	logger.Info("mooring document CLI tool summary reported", "run_id", req.RunID,
		"read_calls", d.readCalls, "submit_calls", d.submitCalls,
		"read_results", d.readResults, "submit_results", d.submitResults,
		"read_errors", d.readErrors, "submit_errors", d.submitErrors,
		"incomplete", d.incomplete)
}
