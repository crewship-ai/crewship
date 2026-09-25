package seeddata

// storyRoutines are the customer-facing, local-data demos. The check is a
// deterministic script; the model only writes the proposed human response.
// No external account is read and no message is sent.
var storyRoutines = []RoutineDef{
	{
		Slug:        "harbor-leads-review",
		Name:        "Check customer inquiries",
		Description: "Find the unanswered Harbor Goods inquiry, prepare a reply, and ask a person to approve the draft. Demo data only; nothing is sent.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "harbor-leads-review",
			"display_name":       "Check customer inquiries",
			"description":        "Check three sample inquiries. Publish the finding, draft a reply, and wait for a person to approve it. No email is sent.",
			"estimated_cost_usd": 0.01,
			"max_cost_usd":       1.0,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				AnthropicCredentialRequirement(),
			},
			"inputs": []map[string]interface{}{
				{"name": "follow_up_after_hours", "type": "string", "required": false, "default": "24", "description": "Flag an unanswered sample inquiry after this many hours."},
			},
			"steps": []map[string]interface{}{
				{
					"id": "check", "type": "script", "timeout_seconds": 30,
					"script": map[string]interface{}{
						"path": "scripts/harbor-goods/check_leads.py", "interpreter": "python3",
						"args": []string{"--after-hours", "{{ inputs.follow_up_after_hours }}"},
					},
				},
				transformOf("state", "check", ".state"),
				transformOf("headline", "check", ".headline"),
				transformOf("lead_id", "check", ".lead_id"),
				transformOf("customer", "check", ".customer"),
				transformOf("age", "check", ".age"),
				transformOf("next_step", "check", ".next_step"),
				{
					"id": "publish-finding", "type": "crewship", "action": "page.write",
					"needs": []string{"state", "headline", "lead_id", "customer", "age", "next_step"},
					"args": map[string]interface{}{
						"page": "leads-at-risk", "panel": "finding",
						"data": map[string]interface{}{
							"items": []map[string]interface{}{
								{"name": "Result", "state": "{{ steps.state.output }}", "label": "{{ steps.headline.output }}"},
								{"name": "Customer", "state": "{{ steps.state.output }}", "label": "{{ steps.customer.output }} · {{ steps.lead_id.output }} · {{ steps.age.output }}"},
								{"name": "Next step", "state": "ok", "label": "{{ steps.next_step.output }}"},
							},
						},
					},
				},
				{
					"id": "draft", "type": "agent_run", "agent_slug": agentSlugRef("morgan"),
					"complexity": "fast", "needs": []string{"check"}, "timeout_seconds": 300,
					"prompt": "You are writing a proposed reply for a HUMAN to review. This is a fictional Harbor Goods demo. " +
						"Use only the facts in this check: {{ steps.check.output }}. Write a friendly two-sentence reply to Ava Martin " +
						"about her quote request. Apologize for the delay and say the team will prepare a quote. " +
						"Do not invent a price or delivery date. Do not send anything. Output only the draft text.",
					"validation": map[string]interface{}{
						"min_length": 25, "must_not_contain": []string{"API_KEY=", "Bearer ", "sent your email"},
					},
				},
				{
					"id": "approve-draft", "type": "wait", "needs": []string{"draft", "publish-finding"},
					"wait": map[string]interface{}{
						"kind": "approval", "approval_title": "Approve draft reply for Ava Martin · L-103",
						"approval_prompt": "Review this draft for the fictional customer. Approval marks it ready to send; this demo does not send email.\n\n{{ steps.draft.output }}",
					},
					"timeout_seconds": 86400,
				},
				{
					"id": "publish-decision", "type": "crewship", "action": "page.write", "needs": []string{"approve-draft"},
					"args": map[string]interface{}{
						"page": "leads-at-risk", "panel": "decision",
						"data": map[string]interface{}{
							"verdict": "Draft approved, ready to send",
							"blocks": []map[string]interface{}{
								{"kind": "paragraph", "text": "A person approved the proposed reply for Ava Martin. This demo has not sent an email."},
								{"kind": "paragraph", "text": "Source: sample inquiries in /crew/shared/demo/harbor-goods/leads.json. A real workspace can connect its own mail, sheet, CRM or form source."},
							},
						},
					},
				},
				{
					"id": "tell-operator", "type": "notify", "needs": []string{"publish-decision"},
					"notify": map[string]interface{}{
						"to": "trigger", "title": "Ava Martin's draft is ready",
						"body":     "The draft for sample inquiry L-103 was approved. No email was sent. Open Leads at Risk to see the result.",
						"priority": "medium", "category": "routines.completed",
					},
				},
			},
		},
	},
}
