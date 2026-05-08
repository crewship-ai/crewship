package seeddata

// RoutineDef defines a seed routine — a workspace-scoped declarative
// AI workflow recipe. Mirrors how IssueDef seeds demo missions: gives
// a fresh dev-server an immediate population of believable, working
// routines so users see the feature populated rather than an empty
// list.
//
// Definition is the full DSL JSON tree (parsed and validated by the
// pipeline package on save). AgentSlug + CrewSlug get resolved to IDs
// during seed; the runtime author = the seed admin user.
type RoutineDef struct {
	Slug        string                 // workspace-unique kebab-case identifier
	Name        string                 // human-readable display name
	Description string                 // one-line summary shown in lists
	CrewSlug    string                 // crew that owns this routine (resolves to author_crew_id)
	Definition  map[string]interface{} // parsed DSL JSON
}

// agentSlugRef is a tiny helper marker for readability — the seeder
// keeps the slug as-is in the definition and doesn't try to resolve
// to an agent ID. The pipeline executor's runtime resolveAgentID
// handles the slug→ID lookup at first invocation, scoped to the
// author crew. So if the named agent is renamed later, the routine
// gets a clean error rather than a silent wrong-agent execution.
func agentSlugRef(slug string) string { return slug }

// Routines is the seed list. Five starter recipes covering the main
// step types (agent_run, http, transform, multi-step needs) so a
// fresh workspace has both inspectable examples and ready-to-invoke
// routines.
//
// Design principles:
//   - Each routine is independently runnable with empty inputs (defaults set)
//   - Validation gates are present so agents can't silently leak credentials
//   - All credentials_required fields set to anthropic only; works on dev2 default
//   - estimated_cost_usd kept low so cost cap doesn't trip
var Routines = []RoutineDef{
	{
		Slug:        "summarize-text",
		Name:        "Summarize text",
		Description: "Take any input text and return a concise 3-bullet summary.",
		CrewSlug:    "engineering",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "summarize-text",
			"display_name":       "Summarize text",
			"description":        "Take any input text and return a concise 3-bullet summary.",
			"estimated_cost_usd": 0.001,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "text",
					"type":        "string",
					"required":    false,
					"default":     "Crewship is a workspace-as-a-product platform that lets AI crews orchestrate background work via declarative routines, replacing the fragmented stack of Ansible + Cron + Slack bots + custom scripts.",
					"description": "Text to summarize",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "summary", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "summarize",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("tomas"),
					"complexity": "fast",
					"prompt":     "Summarize the following text in exactly 3 concise bullet points. Each bullet on its own line, starting with '- '.\n\nText:\n{{ inputs.text }}",
					"validation": map[string]interface{}{
						"min_length":       30,
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},
	{
		Slug:        "fetch-and-summarize",
		Name:        "Fetch URL and summarize",
		Description: "Fetch the contents of a URL via HTTP, then summarize in 3 bullets.",
		CrewSlug:    "research",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "fetch-and-summarize",
			"display_name":       "Fetch URL and summarize",
			"description":        "Fetch the contents of a URL via HTTP, then summarize in 3 bullets.",
			"estimated_cost_usd": 0.002,
			// Narrow allowlist on the seed routine so the demo doesn't
			// double as an SSRF lab. Workspace admins can broaden via
			// the routine editor; we leave production allowlisting to
			// explicit operator decision rather than defaulting to "*".
			"egress_targets": []string{"httpbin.org"},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "url",
					"type":        "string",
					"required":    false,
					"default":     "https://httpbin.org/json",
					"description": "URL to fetch",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "summary", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":   "fetch",
					"type": "http",
					"http": map[string]interface{}{
						"method":             "GET",
						"url":                "{{ inputs.url }}",
						"max_response_bytes": 200000,
						"success_codes":      []int{200},
					},
					"timeout_seconds": 30,
				},
				{
					"id":         "summarize",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("filip"),
					"complexity": "fast",
					"needs":      []string{"fetch"},
					"prompt":     "Summarize the following web content in 3 concise bullet points:\n\n{{ steps.fetch.output }}",
					"validation": map[string]interface{}{
						"min_length":       20,
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},
	{
		Slug:        "pr-review-structured",
		Name:        "PR review (structured)",
		Description: "Review a PR diff and produce structured feedback (summary + issues + suggestions).",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "pr-review-structured",
			"display_name":       "PR review (structured)",
			"description":        "Review a PR diff and produce structured feedback (summary + issues + suggestions).",
			"estimated_cost_usd": 0.005,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "diff",
					"type":        "string",
					"required":    false,
					"default":     "diff --git a/foo.go b/foo.go\n+func Add(a, b int) int { return a + b }\n",
					"description": "Unified diff to review",
				},
				{
					"name":     "language",
					"type":     "string",
					"required": false,
					"default":  "go",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "review", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "review",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("eva"),
					"complexity": "moderate",
					"prompt":     "Review the following {{ inputs.language }} diff. Return a JSON object with three keys: summary (string, 1 sentence), issues (array of {file, line, severity, message}), suggestions (array of strings). Do NOT include any prose outside the JSON.\n\n{{ inputs.diff }}",
					"validation": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":     "object",
							"required": []string{"summary", "issues", "suggestions"},
							"properties": map[string]interface{}{
								"summary":     map[string]interface{}{"type": "string"},
								"issues":      map[string]interface{}{"type": "array"},
								"suggestions": map[string]interface{}{"type": "array"},
							},
						},
						"on_validation_fail": "escalate_tier",
					},
				},
			},
		},
	},
	{
		Slug:        "daily-status-digest",
		Name:        "Daily status digest",
		Description: "Compose a markdown digest of the day's work — useful as a Slack/email summary.",
		CrewSlug:    "engineering",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "daily-status-digest",
			"display_name":       "Daily status digest",
			"description":        "Compose a markdown digest of the day's work — useful as a Slack/email summary.",
			"estimated_cost_usd": 0.002,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":     "team",
					"type":     "string",
					"required": false,
					"default":  "engineering",
				},
				{
					"name":     "date",
					"type":     "string",
					"required": false,
					"default":  "today",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "digest", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "compose",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("nela"),
					"complexity": "fast",
					"prompt":     "Compose a markdown daily status digest for the {{ inputs.team }} team for {{ inputs.date }}. Include 3 sections: ## Done, ## In progress, ## Blockers. Use believable but generic items (e.g., 'shipped routine schedules feature', 'reviewing PR #281'). Keep it to under 200 words.",
					"validation": map[string]interface{}{
						"min_length":       100,
						"max_length":       2000,
						"must_contain":     []string{"##"},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},
	{
		Slug:        "incident-triage",
		Name:        "Incident alert triage",
		Description: "Categorize an alert text and suggest the next on-call action.",
		CrewSlug:    "devops",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "incident-triage",
			"display_name":       "Incident alert triage",
			"description":        "Categorize an alert text and suggest the next on-call action.",
			"estimated_cost_usd": 0.003,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":     "alert",
					"type":     "string",
					"required": false,
					"default":  "PagerDuty: high CPU on web-prod-3, 95% sustained 10 min, p95 latency tripled",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "triage", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "categorize",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("ondrej"),
					"complexity": "fast",
					"prompt":     "You are an on-call engineer. Triage this alert and respond with JSON: {category: 'capacity'|'latency'|'error_rate'|'security'|'other', severity: 'critical'|'high'|'medium'|'low', suggested_action: <2 sentence first-step recommendation>}. Alert: {{ inputs.alert }}",
					"validation": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":     "object",
							"required": []string{"category", "severity", "suggested_action"},
						},
						"on_validation_fail": "escalate_tier",
					},
				},
			},
		},
	},
}
