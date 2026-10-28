package orchestrator

import "log/slog"

// CLI self-report only: these are neither host tool outcomes nor authority or
// save receipts. Capture at most 32 known call events and 32 unique opaque IDs
// (256 bytes each) in RAM. Retain consumed/ambiguous ID tombstones until finish;
// never log IDs, names, arguments, output, errors, paths or run identity.
type nativeCraftToolCounts struct{ calls, results, errors int }
type nativeCraftToolDiagnostic struct {
	tracked              map[string]uint8 // 1..5 = native tool, 0 = consumed/ambiguous
	counts               [5]nativeCraftToolCounts
	totalCalls           int
	incomplete, reported bool
}

func (d *nativeCraftToolDiagnostic) observe(req AgentRunRequest, event AgentEvent) {
	if d.reported || !req.nativeCraftTools || !req.strictContentPrivacy || req.mooringDocumentTools {
		return
	}
	if event.Type != "tool_call" && event.Type != "tool_result" {
		return
	}
	meta, ok := event.Metadata.(map[string]any)
	if !ok {
		d.incomplete = true
		return
	}
	if event.Type == "tool_call" {
		name, _ := meta["tool_name"].(string)
		id, _ := meta["tool_id"].(string)
		// A collision with any later call poisons an existing pairing, even
		// when its name is outside our catalogue or the capture budget is full.
		_, reused := d.tracked[id]
		if reused {
			d.tracked[id] = 0
			d.incomplete = true
		}
		var kind uint8
		switch name {
		case "mcp__crewship-craft__craft_read":
			kind = 1
		case "mcp__crewship-craft__craft_acquire":
			kind = 2
		case "mcp__crewship-craft__craft_renew":
			kind = 3
		case "mcp__crewship-craft__craft_apply":
			kind = 4
		case "mcp__crewship-craft__craft_release":
			kind = 5
		default:
			return
		}
		if d.totalCalls >= 32 {
			d.incomplete = true
			return
		}
		d.totalCalls++
		d.counts[kind-1].calls++
		if id == "" || len(id) > 256 {
			d.incomplete = true
			return
		}
		if d.tracked == nil {
			d.tracked = make(map[string]uint8)
		}
		if reused {
			return
		}
		d.tracked[id] = kind
		return
	}
	id, _ := meta["tool_use_id"].(string)
	if id == "" || len(id) > 256 {
		d.incomplete = true
		return
	}
	kind := d.tracked[id]
	if kind == 0 {
		// No attribution is possible; do not invent an outcome for an unknown,
		// duplicated or reused ID, or retain any new result-only IDs.
		d.incomplete = true
		return
	}
	d.tracked[id] = 0
	d.counts[kind-1].results++
	flag, present := meta["is_error"]
	failed, known := flag.(bool)
	if present && !known {
		d.incomplete = true
	} else if failed {
		d.counts[kind-1].errors++
	}
}

func (d *nativeCraftToolDiagnostic) report(logger *slog.Logger, req AgentRunRequest) {
	if logger == nil || d.reported || !req.nativeCraftTools || !req.strictContentPrivacy || req.mooringDocumentTools {
		return
	}
	d.reported = true
	for _, kind := range d.tracked {
		if kind != 0 {
			d.incomplete = true
		}
	}
	logger.Info("native Craft CLI tool self-report summary",
		"read_calls", d.counts[0].calls, "read_results", d.counts[0].results, "read_errors", d.counts[0].errors,
		"acquire_calls", d.counts[1].calls, "acquire_results", d.counts[1].results, "acquire_errors", d.counts[1].errors,
		"renew_calls", d.counts[2].calls, "renew_results", d.counts[2].results, "renew_errors", d.counts[2].errors,
		"apply_calls", d.counts[3].calls, "apply_results", d.counts[3].results, "apply_errors", d.counts[3].errors,
		"release_calls", d.counts[4].calls, "release_results", d.counts[4].results, "release_errors", d.counts[4].errors,
		"incomplete", d.incomplete)
}
