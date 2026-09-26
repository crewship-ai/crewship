package seeddata

// Every project uses the same small contract: inspect, optionally draft with
// AI, then resolve. A workspace-wide concurrency key protects all three doors.
func businessRoutines() []RoutineDef {
	var out []RoutineDef
	for _, s := range Stories {
		base := func(suffix, name string, steps []map[string]interface{}, ai bool) RoutineDef {
			def := map[string]interface{}{"dsl_version": "1.0", "name": "demo-" + s.Slug + "-" + suffix, "display_name": name, "description": s.Problem + " Local demo data only.", "concurrency_key": "demo-story-" + s.Slug, "max_concurrent": 1, "steps": steps, "egress_targets": []string{}}
			if ai {
				def["credentials_required"] = []map[string]interface{}{AnthropicCredentialRequirement()}
				def["max_cost_usd"] = 1.0
			}
			return RoutineDef{Slug: "demo-" + s.Slug + "-" + suffix, Name: name, Description: s.Problem, CrewSlug: s.Crew, AuthorAgentSlug: s.Agent, Definition: def}
		}
		script := func(id, action string) map[string]interface{} {
			return map[string]interface{}{"id": id, "type": "script", "timeout_seconds": 20, "script": map[string]interface{}{"path": "demo/business/story.py", "interpreter": "python3", "args": []string{s.Slug, action}}}
		}
		publish := func(id, panel, ref string) map[string]interface{} {
			return map[string]interface{}{"id": id, "type": "crewship", "action": "page.write", "args": map[string]interface{}{"page": "demo-" + s.Slug, "panel": panel, "data": ref}}
		}
		comment := func(id, ref string) map[string]interface{} {
			return map[string]interface{}{"id": id, "type": "crewship", "action": "issue.comment", "args": map[string]interface{}{"identifier": "{{ steps.inspect.output.issue }}", "body": ref}}
		}
		notify := func(id, title, body string) map[string]interface{} {
			return map[string]interface{}{"id": id, "type": "notify", "notify": map[string]interface{}{"to": "trigger", "title": title, "body": body, "priority": "medium", "category": "routines.completed"}}
		}
		out = append(out, base("check", s.CheckLabel, []map[string]interface{}{
			script("inspect", "check"), publish("records", "records", "{{ steps.inspect.output.records }}"), publish("finding", "finding", "{{ steps.inspect.output.summary }}"),
			comment("note", "{{ steps.inspect.output.comment }}"), notify("notify", s.Project+": check completed", "{{ steps.inspect.output.comment }}"),
		}, false))
		draft := map[string]interface{}{"id": "agent", "type": "agent_run", "agent_slug": s.Agent, "complexity": "fast", "timeout_seconds": 120, "if": "{{ steps.inspect.output.pending }}", "prompt": "Use the demo-business skill. Prepare a short draft for a human to review. Use only this evidence: {{ steps.inspect.output.evidence }}. Task: " + s.Result + ". " + s.DraftInstruction + " Do not send anything, edit files, change issues or call tools. Do not ask the reader to approve anything: Crewship provides the decision controls separately. Wrap the final proposed text in exactly one <demo-draft>...</demo-draft> block. Keep progress commentary outside that block. Never invent prices, dates or completed actions.", "validation": map[string]interface{}{"min_length": 20}, "retry": map[string]interface{}{"max_attempts": 3, "retry_on": `error.contains("live run in progress elsewhere")`, "backoff": map[string]interface{}{"min_ms": 3000, "max_ms": 5000}}}
		save := script("save", "draft")
		save["if"] = "{{ steps.inspect.output.pending }}"
		save["script"].(map[string]interface{})["args"] = []string{s.Slug, "draft", "--draft", "{{ steps.agent.output }}"}
		pubDraft := publish("draft", "draft", "{{ steps.save.output.summary }}")
		pubDraft["if"] = "{{ steps.inspect.output.pending }}"
		// Page write acknowledgements are not run outcomes. Report only after
		// saving and publishing the draft; the script refuses a missing draft.
		report := script("report", "draft-result")
		handoff := map[string]interface{}{"id": "handoff", "type": "transform", "transform": map[string]interface{}{"input": "{{ steps.report.output }}", "expression": ".handoff"}}
		out = append(out, base("draft", "Draft with AI · "+s.Project, []map[string]interface{}{script("inspect", "check"), draft, save, pubDraft, report, handoff}, true))
		steps := []map[string]interface{}{script("inspect", "prepare")}
		waiting := publish("waiting", "proposal", "{{ steps.inspect.output.summary }}")
		steps = append(steps, waiting)
		if s.Approval {
			steps = append(steps, map[string]interface{}{"id": "decision", "type": "wait", "if": "{{ steps.inspect.output.pending }}", "timeout_seconds": 3600, "wait": map[string]interface{}{"kind": "approval", "approval_title": s.ResolveLabel + " · " + s.Project, "approval_prompt": "{{ steps.inspect.output.draft_source }}:\n\n{{ steps.inspect.output.draft }}\n\nApprove saves a local demo artifact. Nothing is sent to an external service.", "decision_form": map[string]interface{}{"fields": []interface{}{}, "actions": []map[string]interface{}{{"id": "approve", "label": "Approve demo action", "approved": true}, {"id": "keep_open", "label": "Keep open", "approved": true}}}}})
		}
		complete := script("complete", "complete")
		if s.Approval {
			complete["script"].(map[string]interface{})["args"] = []string{s.Slug, "complete", "--decision", "{{ steps.decision.output.action_id }}"}
		}
		steps = append(steps, complete)
		// Persist acknowledgements between Issue transitions. Retrying after any
		// partial failure can repeat the same status but cannot reopen a DONE Issue.
		for _, phase := range []struct{ status, condition, mark string }{{"IN_PROGRESS", "start_issue", "mark-progress"}, {"DONE", "finish_issue", "mark-done"}} {
			condition := "{{ steps.complete.output." + phase.condition + " }}"
			steps = append(steps, map[string]interface{}{"id": "issue-" + phase.status, "type": "crewship", "action": "issue.update", "if": condition, "args": map[string]interface{}{"identifier": "{{ steps.inspect.output.issue }}", "status": phase.status}})
			ack := script(phase.mark, phase.mark)
			ack["if"] = condition
			steps = append(steps, ack)
		}

		result := publish("outcome", "outcome", "{{ steps.complete.output.summary }}")
		steps = append(steps, result)
		resolvedRecords := publish("resolved-records", "resolved-records", "{{ steps.complete.output.records }}")
		steps = append(steps, resolvedRecords)
		note := comment("note", "{{ steps.complete.output.comment }}")
		note["if"] = "{{ steps.inspect.output.pending }}"
		steps = append(steps, note)
		steps = append(steps, notify("notify", s.Project+": decision recorded", "Open the project Page and its Issue to see the result. All delivery stays inside this demo workspace."))
		out = append(out, base("resolve", s.ResolveLabel, steps, false))
	}
	return out
}
