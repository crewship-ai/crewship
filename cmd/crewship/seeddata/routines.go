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

// Routines is the seed list — the "recipe library" of a fresh workspace.
//
// THE RECIPE PHILOSOPHY (why these exist and how they're built):
//
// A routine is authored once by a strong model (Opus) and then executed
// cheaply and repeatedly on a fast model (Haiku) with a near-100%
// IDENTICAL output. That only works when the task is a TRANSFORMATION
// (input fully determines output), the output is CANONICAL (sorted,
// fixed JSON schema, closed label set, fixed template), and a double
// gate locks it down:
//
//   - validation  → locks the SHAPE (json schema, must_contain, length)
//   - outcomes    → a stronger grader agent checks SEMANTIC equality
//     against a rubric; step on_fail: escalate_tier means
//     if the fast tier ever drifts off-rubric the run
//     escalates to a smarter model rather than shipping a
//     wrong answer. The goal, though, is Haiku stability.
//
// The recipes below span the determinism classes: pure extraction /
// normalization, closed-set classification, validation / linting,
// redaction, decision tables, structured review, faithful
// summarization, and multi-step / orchestration. The 17 eval-* scenarios
// in eval_scenarios.go are the matching regression harness.
//
// Alongside the deterministic recipes, three routines exist for the
// live-workspace demo loop rather than determinism. None of them ship
// with a cron schedule — the demo seed intentionally has zero scheduled
// routines; wire one by hand (`crewship routine schedules create`) to
// see the loop fire:
//   - morning-briefing    — lead briefing (agent routine whose
//     completion lands an inbox notification)
//   - feed-watch-probe    — agentless token-zero wake gate (http +
//     transform + code:expr), suitable as a schedule's wake gate
//   - feed-change-report  — the intended wake-gated target that only
//     runs (and only spends tokens) when the probe fires
//
// Design conventions (kept identical across every recipe):
//   - Each routine runs with empty inputs (sensible defaults set)
//   - Every agent_run validation carries must_not_contain creds guards
//   - estimated_cost_usd kept low so the cost cap never trips
//   - Deterministic recipes pin complexity: fast (Haiku); graded ones
//     add on_fail: escalate_tier so drift escalates instead of shipping
var Routines = []RoutineDef{
	// ───────────────────────────────────────────────────────────────
	// 1. summarize-text — base demo (light generative)
	// ───────────────────────────────────────────────────────────────
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
					"agent_slug": agentSlugRef("alex"),
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

	// ───────────────────────────────────────────────────────────────
	// 2. fetch-and-extract — http → agent extraction → JSON (DAG)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "fetch-and-extract",
		Name:        "Fetch URL and extract JSON",
		Description: "Fetch a URL over HTTP, then extract a canonical JSON object from the body.",
		CrewSlug:    "engineering",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "fetch-and-extract",
			"display_name":       "Fetch URL and extract JSON",
			"description":        "Fetch a URL over HTTP, then extract a canonical JSON object from the body.",
			"estimated_cost_usd": 0.002,
			// Narrow allowlist on the seed routine so the demo doesn't
			// double as an SSRF lab. Workspace admins broaden via the editor.
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
				{"name": "data", "type": "string"},
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
					"id":         "extract",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("sam"),
					"complexity": "fast",
					"needs":      []string{"fetch"},
					"prompt":     "Extract the key/value fields from the JSON body below into a single flat JSON object with string values, keys sorted alphabetically. Output ONLY the JSON object, no prose.\n\n{{ steps.fetch.output }}",
					"validation": map[string]interface{}{
						"must_contain":     []string{"{", "}"},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 3. extract-contacts — pure extraction → sorted JSON (class A)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "extract-contacts",
		Name:        "Extract contacts (sorted JSON)",
		Description: "Pull every email + phone from free text into a canonical, sorted JSON object.",
		CrewSlug:    "engineering",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "extract-contacts",
			"display_name":       "Extract contacts (sorted JSON)",
			"description":        "Pull every email + phone from free text into a canonical, sorted JSON object.",
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
					"default":     "Reach Dana at dana@example.com or +1-202-555-0173. Backup: ops@example.org, +1-202-555-0199. Spam: dana@example.com (dup).",
					"description": "Free text to extract contacts from",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "contacts", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "extract",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("sam"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Extract all email addresses and phone numbers from the text. Output ONLY a JSON object with exactly two keys: \"emails\" (array of unique lowercase email strings, sorted alphabetically) and \"phones\" (array of unique phone strings in the exact form they appear, sorted alphabetically). No prose, no code fences.\n\nText:\n{{ inputs.text }}",
					"validation": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":     "object",
							"required": []string{"emails", "phones"},
							"properties": map[string]interface{}{
								"emails": map[string]interface{}{"type": "array"},
								"phones": map[string]interface{}{"type": "array"},
							},
						},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 4. incident-timeline — structured extraction → timeline JSON
	//    (class A, realistic framing: raw incident log → canonical
	//    chronology, grader-checked)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "incident-timeline",
		Name:        "Incident timeline (canonical JSON)",
		Description: "Turn a raw incident log excerpt into a canonical, chronologically-ordered JSON timeline.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "incident-timeline",
			"display_name":       "Incident timeline (canonical JSON)",
			"description":        "Turn a raw incident log excerpt into a canonical, chronologically-ordered JSON timeline.",
			"estimated_cost_usd": 0.003,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":     "log",
					"type":     "string",
					"required": false,
					"default": "2026-06-30T09:12:04Z WARN [gateway] p99 latency 2.4s\n" +
						"2026-06-30T09:13:11Z ERROR [auth-svc] token refresh failed: upstream 503\n" +
						"2026-06-30T09:13:40Z ERROR [auth-svc] circuit breaker open\n" +
						"2026-06-30T09:15:02Z INFO [ops] rollback of auth-svc 1.4.2 initiated",
					"description": "Raw incident log excerpt (one event per line)",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "timeline", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "extract",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("riley"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Convert the incident log into a JSON array. Each line becomes one object with exactly these keys: \"time\" (the ISO 8601 timestamp), \"level\" (uppercase, e.g. ERROR/WARN/INFO), \"component\" (the bracketed component name without brackets), \"event\" (the remaining message text). Preserve the original line order. Output ONLY the JSON array, no prose, no code fences.\n\nLog:\n{{ inputs.log }}",
					"validation": map[string]interface{}{
						"must_contain":     []string{"[", "]"},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
					"outcomes": map[string]interface{}{
						"grader_agent_slug": agentSlugRef("morgan"),
						"max_iterations":    2,
						"on_fail":           "escalate_tier",
						"criteria": []map[string]interface{}{
							{"name": "complete", "rule": "Every line of the source log appears exactly once in the timeline, in the original order."},
							{"name": "errors_captured", "rule": "Every ERROR line from the source is present with level ERROR."},
							{"name": "no_invention", "rule": "No timeline entry states an event that is not in the source log."},
						},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 5. classify-ticket — closed-set classification + grader (class B)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "classify-ticket",
		Name:        "Classify support ticket",
		Description: "Classify a ticket into fixed category / priority / sentiment label sets (grader-checked).",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "classify-ticket",
			"display_name":       "Classify support ticket",
			"description":        "Classify a ticket into fixed category / priority / sentiment label sets (grader-checked).",
			"estimated_cost_usd": 0.002,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "ticket",
					"type":        "string",
					"required":    false,
					"default":     "Subject: Charged twice this month! I was billed $40 instead of $20 and I'm really frustrated. Please refund the difference ASAP.",
					"description": "Raw support ticket text",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "classification", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "classify",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("casey"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Classify the support ticket. Output ONLY a JSON object with exactly these keys and allowed values:\n- \"category\": one of billing | bug | feature_request | account | other\n- \"priority\": one of low | medium | high | critical\n- \"sentiment\": one of positive | neutral | negative\nNo prose, no code fences.\n\nTicket:\n{{ inputs.ticket }}",
					"validation": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":     "object",
							"required": []string{"category", "priority", "sentiment"},
						},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
					"outcomes": map[string]interface{}{
						"grader_agent_slug": agentSlugRef("jordan"),
						"max_iterations":    2,
						"on_fail":           "escalate_tier",
						"criteria": []map[string]interface{}{
							{"name": "valid_labels", "rule": "Each of category, priority, sentiment holds exactly one value from its allowed set."},
							{"name": "category_matches", "rule": "The chosen category is the best fit for the ticket content."},
							{"name": "sentiment_matches", "rule": "The sentiment reflects the customer's tone in the ticket."},
						},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 6. morning-briefing — lead briefing (agent routine)
	//    Unscheduled by default; its completion lands a notification,
	//    so wiring it to a cron shows the "your crew briefs you every
	//    morning" loop end-to-end.
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "morning-briefing",
		Name:        "Morning briefing",
		Description: "The crew lead compiles a morning briefing from real workspace activity and lands it in your inbox.",
		CrewSlug:    "engineering",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "morning-briefing",
			"display_name":       "Morning briefing",
			"description":        "The crew lead compiles a morning briefing from real workspace activity and lands it in your inbox.",
			"estimated_cost_usd": 0.004,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "focus",
					"type":        "string",
					"required":    false,
					"default":     "overnight activity and today's priorities",
					"description": "What the briefing should focus on",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "briefing", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "compose",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("alex"),
					"complexity": "fast",
					"prompt":     "Compile the Engineering crew's morning briefing, focused on {{ inputs.focus }}. Ground every statement in evidence you can actually see: the crew journal, shared notes under /crew/shared, and your own memory of recent work. If you find no recent activity, say so in one line and lay out a sensible plan for the day instead — do NOT invent events. Format as markdown with exactly three sections: ## Overnight, ## Today, ## Needs attention. Keep it under 200 words.",
					"validation": map[string]interface{}{
						"min_length":       80,
						"max_length":       2500,
						"must_contain":     []string{"##"},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 7. normalize-dates — normalization → ISO 8601 sorted (class C)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "normalize-dates",
		Name:        "Normalize dates to ISO 8601",
		Description: "Find every date in the text and output them normalized to YYYY-MM-DD, sorted ascending.",
		CrewSlug:    "engineering",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "normalize-dates",
			"display_name":       "Normalize dates to ISO 8601",
			"description":        "Find every date in the text and output them normalized to YYYY-MM-DD, sorted ascending.",
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
					"default":     "Kickoff was on March 3rd, 2024. Review 04/15/2024. Launch: 2024-05-01. Retro on 1 June 2024.",
					"description": "Text containing dates in mixed formats",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "dates", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "normalize",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("robin"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Find every calendar date in the text and convert each to ISO 8601 (YYYY-MM-DD). Output ONLY a JSON array of the unique normalized date strings, sorted ascending. No prose, no code fences.\n\nText:\n{{ inputs.text }}",
					"validation": map[string]interface{}{
						"must_contain":     []string{"[", "]", "-"},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 8. redact-secrets — redaction (class F, security-relevant)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "redact-secrets",
		Name:        "Redact secrets",
		Description: "Mask API keys, tokens, passwords and emails in text, replacing each with [REDACTED].",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "redact-secrets",
			"display_name":       "Redact secrets",
			"description":        "Mask API keys, tokens, passwords and emails in text, replacing each with [REDACTED].",
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
					"default":     "Deploy with API_KEY=sk-ant-abc123 and token ghp_DEADBEEF. Login user admin / pass Hunter2. Notify ops@example.com.",
					"description": "Text that may contain secrets",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "redacted", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "redact",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("casey"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Rewrite the text, replacing every secret value (API keys, access tokens, passwords, and email addresses) with the literal token [REDACTED]. Keep all other words exactly as-is. Output ONLY the rewritten text.\n\nText:\n{{ inputs.text }}",
					"validation": map[string]interface{}{
						"must_contain": []string{"[REDACTED]"},
						// The whole point: no secret material may survive in the output.
						"must_not_contain": []string{"sk-ant-", "ghp_", "Hunter2", "@example.com"},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 9. json-schema-validate — validation/linting (class E)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "json-schema-validate",
		Name:        "Validate JSON against required keys",
		Description: "Check a JSON object for required keys and report {valid, missing, extra} canonically.",
		CrewSlug:    "engineering",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "json-schema-validate",
			"display_name":       "Validate JSON against required keys",
			"description":        "Check a JSON object for required keys and report {valid, missing, extra} canonically.",
			"estimated_cost_usd": 0.001,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "json",
					"type":        "string",
					"required":    false,
					"default":     "{\"name\":\"widget\",\"price\":12.5,\"color\":\"blue\"}",
					"description": "JSON object to validate",
				},
				{
					"name":        "required_keys",
					"type":        "string",
					"required":    false,
					"default":     "name,price,sku",
					"description": "Comma-separated required keys",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "report", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "validate",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("sam"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Given the JSON object and the comma-separated required keys, output ONLY a JSON object with exactly: \"valid\" (boolean — true iff no required key is missing), \"missing\" (array of required keys absent from the object, sorted), \"extra\" (array of object keys not in the required list, sorted). No prose, no code fences.\n\nObject:\n{{ inputs.json }}\n\nRequired keys: {{ inputs.required_keys }}",
					"validation": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":     "object",
							"required": []string{"valid", "missing", "extra"},
						},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 10. pr-review-structured — structured review + grader (class I)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "pr-review-structured",
		Name:        "PR review (structured)",
		Description: "Review a diff and produce structured feedback (summary + issues + suggestions), grader-checked.",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "pr-review-structured",
			"display_name":       "PR review (structured)",
			"description":        "Review a diff and produce structured feedback (summary + issues + suggestions), grader-checked.",
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
					"default":     "diff --git a/auth.go b/auth.go\n+func Login(u, p string) bool { return u == \"admin\" && p == \"admin\" }\n",
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
					"agent_slug": agentSlugRef("casey"),
					"complexity": "moderate",
					"on_fail":    "escalate_tier",
					"prompt":     "Review the following {{ inputs.language }} diff. Output ONLY a JSON object with three keys: \"summary\" (string, 1 sentence), \"issues\" (array of {file, line, severity, message}), \"suggestions\" (array of strings). No prose outside the JSON.\n\n{{ inputs.diff }}",
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
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
					"outcomes": map[string]interface{}{
						"grader_agent_slug": agentSlugRef("jordan"),
						"max_iterations":    2,
						"on_fail":           "escalate_tier",
						"criteria": []map[string]interface{}{
							{"name": "flags_real_issue", "rule": "The issues array identifies at least one genuine problem present in the diff (e.g. hardcoded credentials)."},
							{"name": "well_formed", "rule": "summary is one sentence; each issue has file, line, severity, message."},
						},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 11. invoice-extract — nested extraction → JSON (stretch, class A)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "invoice-extract",
		Name:        "Extract invoice fields",
		Description: "Parse an invoice into canonical JSON with nested line items, grader-checked.",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "invoice-extract",
			"display_name":       "Extract invoice fields",
			"description":        "Parse an invoice into canonical JSON with nested line items, grader-checked.",
			"estimated_cost_usd": 0.003,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "invoice",
					"type":        "string",
					"required":    false,
					"default":     "ACME Corp — Invoice INV-2024-08 — Date: 2024-08-14\n3 x Widget @ $12.50 = $37.50\n2 x Gadget @ $5.00 = $10.00\nTotal due: $47.50 USD",
					"description": "Raw invoice text",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "invoice_json", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "extract",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("casey"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Extract the invoice into ONLY a JSON object with exactly: \"vendor\" (string), \"invoice_number\" (string), \"date\" (ISO 8601 YYYY-MM-DD), \"currency\" (3-letter code), \"total\" (number), \"line_items\" (array of {description, quantity, unit_price, amount} in document order). Numbers as JSON numbers, not strings. No prose, no code fences.\n\nInvoice:\n{{ inputs.invoice }}",
					"validation": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":     "object",
							"required": []string{"vendor", "invoice_number", "date", "currency", "total", "line_items"},
							"properties": map[string]interface{}{
								"line_items": map[string]interface{}{"type": "array"},
								"total":      map[string]interface{}{"type": "number"},
							},
						},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
					"outcomes": map[string]interface{}{
						"grader_agent_slug": agentSlugRef("jordan"),
						"max_iterations":    2,
						"on_fail":           "escalate_tier",
						"criteria": []map[string]interface{}{
							{"name": "totals_consistent", "rule": "total equals the sum of all line_items amount values."},
							{"name": "line_items_complete", "rule": "Every line item in the source appears once with correct quantity, unit_price and amount."},
						},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 12. routing-decision — decision table (stretch, class H)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "routing-decision",
		Name:        "Routing decision (rule table)",
		Description: "Apply a fixed rule table to inputs and return the single resulting route + reason.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "routing-decision",
			"display_name":       "Routing decision (rule table)",
			"description":        "Apply a fixed rule table to inputs and return the single resulting route + reason.",
			"estimated_cost_usd": 0.001,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "type",
					"type":        "string",
					"required":    false,
					"default":     "billing",
					"description": "Item type",
				},
				{
					"name":        "severity",
					"type":        "string",
					"required":    false,
					"default":     "low",
					"description": "Severity",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "decision", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "route",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("morgan"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Apply this rule table IN ORDER (first match wins) and output ONLY a JSON object {\"route\": <team>, \"reason\": <which rule matched>}:\n1. severity == \"critical\"  -> route \"ops\"\n2. type == \"billing\"        -> route \"finance\"\n3. type == \"bug\"            -> route \"engineering\"\n4. otherwise                 -> route \"support\"\nNo prose, no code fences.\n\ntype = {{ inputs.type }}\nseverity = {{ inputs.severity }}",
					"validation": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":     "object",
							"required": []string{"route", "reason"},
						},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 13. extractive-summary — faithful summary + grader (stretch, class J)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "extractive-summary",
		Name:        "Extractive summary with citations",
		Description: "Summarize a source where every bullet must be backed by a verbatim quote (faithfulness-graded).",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "extractive-summary",
			"display_name":       "Extractive summary with citations",
			"description":        "Summarize a source where every bullet must be backed by a verbatim quote (faithfulness-graded).",
			"estimated_cost_usd": 0.003,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "source",
					"type":        "string",
					"required":    false,
					"default":     "The deploy was rolled back at 09:40 after error rates hit 12%. Root cause was a missing migration on the auth service. A fix is scheduled for the next maintenance window on Friday.",
					"description": "Source text to summarize faithfully",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "summary", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "summarize",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("casey"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Summarize the source in up to 3 bullets. Each bullet MUST be followed by a verbatim quote from the source in the form [quote: \"...\"]. Do NOT state anything not present in the source. Output only the bullets.\n\nSource:\n{{ inputs.source }}",
					"validation": map[string]interface{}{
						"min_length":       40,
						"must_contain":     []string{"[quote:"},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
					"outcomes": map[string]interface{}{
						"grader_agent_slug": agentSlugRef("jordan"),
						"max_iterations":    2,
						"on_fail":           "escalate_tier",
						"criteria": []map[string]interface{}{
							{"name": "every_claim_quoted", "rule": "Every bullet is followed by a quote that appears verbatim in the source."},
							{"name": "no_hallucination", "rule": "No bullet states a fact that is not present in the source."},
						},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 14. diff-risk-score — structured review → JSON (stretch, class I)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "diff-risk-score",
		Name:        "Diff risk score",
		Description: "Score a git diff for change size and risk into a canonical JSON object.",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "diff-risk-score",
			"display_name":       "Diff risk score",
			"description":        "Score a git diff for change size and risk into a canonical JSON object.",
			"estimated_cost_usd": 0.002,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "diff",
					"type":        "string",
					"required":    false,
					"default":     "diff --git a/auth/session.go b/auth/session.go\n@@\n-func validate(t string) bool {\n+func validate(t string) bool { // TODO skip check\n+    return true\n",
					"description": "Unified diff",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "risk", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "score",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("casey"),
					"complexity": "fast",
					"on_fail":    "escalate_tier",
					"prompt":     "Analyze the diff and output ONLY a JSON object with exactly: \"files_changed\" (integer), \"additions\" (integer), \"deletions\" (integer), \"risk\" (one of low | medium | high), \"reasons\" (array of short strings). Touching auth/security/sessions is high risk. No prose, no code fences.\n\n{{ inputs.diff }}",
					"validation": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":     "object",
							"required": []string{"files_changed", "additions", "deletions", "risk", "reasons"},
						},
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 15. feed-watch-probe — agentless wake gate (token-zero monitoring)
	//     http fetch → transform reduce → code:expr compare. Unscheduled
	//     by default; wire it as a schedule's wake gate for
	//     feed-change-report and, with the stable seed defaults, it
	//     emits false — the rail shows recurring zero-token SKIPPED wake
	//     checks — the crew literally sleeps until the feed changes.
	//     On camera: lower expected_items (or point url at a live feed)
	//     and the next tick wakes the crew.
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "feed-watch-probe",
		Name:        "Feed watch probe (agentless)",
		Description: "Token-zero wake gate: fetch a JSON feed, count its items deterministically, emit true only when the count drifts from baseline.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "feed-watch-probe",
			"display_name":       "Feed watch probe (agentless)",
			"description":        "Token-zero wake gate: fetch a JSON feed, count its items deterministically, emit true only when the count drifts from baseline.",
			"estimated_cost_usd": 0.0,
			"agentless":          true,
			// Narrow allowlist, same rationale as fetch-and-extract:
			// the seed probe watches a stable public endpoint; swap url
			// (and broaden egress) to watch your own status page/feed.
			"egress_targets": []string{"httpbin.org"},
			"inputs": []map[string]interface{}{
				{
					"name":        "url",
					"type":        "string",
					"required":    false,
					"default":     "https://httpbin.org/json",
					"description": "JSON feed to watch",
				},
				{
					"name":        "expected_items",
					"type":        "number",
					"required":    false,
					"default":     2.0,
					"description": "Baseline item count — the probe fires when the feed drifts from this",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "changed", "type": "string"},
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
					// Deterministic reduce, no LLM: project the item list
					// out of the payload…
					"id":    "items",
					"type":  "transform",
					"needs": []string{"fetch"},
					"transform": map[string]interface{}{
						"input":      "{{ steps.fetch.output }}",
						"expression": ".slideshow.slides",
					},
				},
				{
					// …then count it.
					"id":    "count",
					"type":  "transform",
					"needs": []string{"items"},
					"transform": map[string]interface{}{
						"input":      "{{ steps.items.output }}",
						"expression": "length",
					},
				},
				{
					"id":    "probe",
					"type":  "code",
					"needs": []string{"count"},
					"code": map[string]interface{}{
						// runtime: expr — the deterministic, token-zero
						// CodeRunner. true = feed drifted from baseline →
						// the gated schedule wakes its target routine.
						"runtime": "expr",
						"code":    "{{ steps.count.output }} != {{ inputs.expected_items }}",
					},
					"timeout_seconds": 15,
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 15b. feed-change-report — the wake-gated target (agent routine)
	//      Only runs when feed-watch-probe says the feed changed:
	//      re-fetches the feed and has an agent brief the crew. Its
	//      completion lands a notification — "the crew woke up because
	//      something actually happened".
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "feed-change-report",
		Name:        "Feed change report",
		Description: "Wake-gated by the feed-watch probe: re-fetch the changed feed and brief the crew on what's in it now.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "feed-change-report",
			"display_name":       "Feed change report",
			"description":        "Wake-gated by the feed-watch probe: re-fetch the changed feed and brief the crew on what's in it now.",
			"estimated_cost_usd": 0.003,
			"egress_targets":     []string{"httpbin.org"},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "url",
					"type":        "string",
					"required":    false,
					"default":     "https://httpbin.org/json",
					"description": "The watched feed (keep in sync with feed-watch-probe's url)",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "report", "type": "string"},
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
					"id":         "report",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("riley"),
					"complexity": "fast",
					"needs":      []string{"fetch"},
					"prompt":     "The feed we monitor changed since its last baseline. Current JSON payload:\n\n{{ steps.fetch.output }}\n\nWrite a concise change report for the crew: at most 3 bullets describing what the feed contains now and anything that looks new or unusual, then a single 'Suggested action:' line. Plain markdown, under 120 words.",
					"validation": map[string]interface{}{
						"min_length":       40,
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 15c. workspace-digest — agentless ops digest (#1422 item 4)
	//      query(pipeline_runs) -> transform(extract summary_md) ->
	//      notify(workspace). Zero LLM spend, zero egress: the query
	//      step reads pipeline_runs directly, deterministically. Ships
	//      unscheduled like feed-watch-probe/feed-change-report — wire
	//      a cadence with `crewship digest enable` (wraps `routine
	//      schedules create --slug workspace-digest`).
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "workspace-digest",
		Name:        "Workspace digest",
		Description: "Token-zero ops digest: run counts, cost, and top failures over a trailing window, posted to the workspace inbox (fans out to Slack/email via your notification channel prefs).",
		CrewSlug:    "ops",
		Definition:  WorkspaceDigestDefinition,
	},

	// ───────────────────────────────────────────────────────────────
	// 16. approval-gate-demo — wait(approval) HITL (meta/process)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "approval-gate-demo",
		Name:        "Approval gate demo",
		Description: "Draft an action, pause for human approval, then emit the final go-ahead.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "approval-gate-demo",
			"display_name":       "Approval gate demo",
			"description":        "Draft an action, pause for human approval, then emit the final go-ahead.",
			"estimated_cost_usd": 0.001,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{"name": "action", "type": "string", "required": false, "default": "Restart the auth-svc pods in production"},
			},
			"outputs": []map[string]interface{}{
				{"name": "result", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":         "draft",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("morgan"),
					"complexity": "fast",
					"prompt":     "Draft a one-paragraph change plan for this action, including the rollback step: {{ inputs.action }}",
					"validation": map[string]interface{}{
						"min_length":       30,
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
				{
					"id":    "approve",
					"type":  "wait",
					"needs": []string{"draft"},
					"wait": map[string]interface{}{
						"kind":            "approval",
						"approval_prompt": "Approve this production action?\n\n{{ steps.draft.output }}",
					},
					"timeout_seconds": 86400,
				},
				{
					"id":         "execute",
					"type":       "agent_run",
					"agent_slug": agentSlugRef("morgan"),
					"complexity": "fast",
					"needs":      []string{"approve"},
					"prompt":     "Approval received. Emit a single confirmation line that the action is proceeding: {{ inputs.action }}",
					"validation": map[string]interface{}{
						"min_length":       10,
						"must_not_contain": []string{"API_KEY=", "Bearer "},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 17. cost-spike-probe — agentless wake-gate probe (token-zero)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "cost-spike-probe",
		Name:        "Cost spike probe (agentless)",
		Description: "Token-zero probe: emit true when spend exceeds a threshold. Usable as a schedule wake-gate.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "cost-spike-probe",
			"display_name":       "Cost spike probe (agentless)",
			"description":        "Token-zero probe: emit true when spend exceeds a threshold. Usable as a schedule wake-gate.",
			"estimated_cost_usd": 0.0,
			"agentless":          true,
			"egress_targets":     []string{},
			"inputs": []map[string]interface{}{
				{"name": "spend_usd", "type": "number", "required": false, "default": 3.0},
				{"name": "threshold_usd", "type": "number", "required": false, "default": 5.0},
			},
			"outputs": []map[string]interface{}{
				{"name": "spike", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":   "probe",
					"type": "code",
					"code": map[string]interface{}{
						// runtime: expr — the deterministic, token-zero CodeRunner.
						// The body is rendered (inputs substituted) then evaluated
						// as a single comparison emitting true/false. No shell, no
						// container, no LLM — the agentless wake-gate contract.
						"runtime": "expr",
						"code":    "{{ inputs.spend_usd }} > {{ inputs.threshold_usd }}",
					},
					"timeout_seconds": 15,
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// 18. consistency-sweep — call_pipeline fan-out (meta: Haiku=Opus)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "consistency-sweep",
		Name:        "Consistency sweep",
		Description: "Run the core deterministic recipes back-to-back so their outputs can be diffed across tiers.",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":        "1.0",
			"name":               "consistency-sweep",
			"display_name":       "Consistency sweep",
			"description":        "Run the core deterministic recipes back-to-back so their outputs can be diffed across tiers.",
			"estimated_cost_usd": 0.006,
			"egress_targets":     []string{},
			"credentials_required": []map[string]interface{}{
				{"type": "anthropic", "scope": "any"},
			},
			"inputs":  []map[string]interface{}{},
			"outputs": []map[string]interface{}{{"name": "swept", "type": "string"}},
			"steps": []map[string]interface{}{
				{
					"id":            "contacts",
					"type":          "call_pipeline",
					"pipeline_slug": "extract-contacts",
				},
				{
					// No `needs`: the three probes call independent recipes and
					// don't consume each other's output. Chaining them would let
					// one early failure skip the rest and hide exactly the
					// cross-recipe drift this sweep exists to surface — so they
					// fan out and each runs (and can fail) on its own.
					"id":            "ticket",
					"type":          "call_pipeline",
					"pipeline_slug": "classify-ticket",
				},
				{
					"id":            "dates",
					"type":          "call_pipeline",
					"pipeline_slug": "normalize-dates",
				},
			},
		},
	},
}

// WorkspaceDigestDefinition is the DSL for the "workspace-digest" seed
// routine (#1422 item 4), exported so `crewship digest enable` can save it
// on demand for a workspace that predates this template (or nuked it) —
// one source of truth for both the seeder and the CLI wrapper.
//
// Three deterministic steps, zero LLM spend, zero network egress:
//  1. query pipeline_runs over a trailing 24h window
//  2. transform: extract the pre-rendered `summary_md` field
//  3. notify the workspace — lands in the inbox and fans out to Slack/
//     email/etc per each recipient's notification channel preferences
//     (issue #1412's existing category × channel matrix, untouched)
//
// agentless: true is accurate here (not "agentless(-ish)" hedging) — query
// and transform are both server-side/deterministic and notify never
// touches an LLM either, so the whole routine is genuinely token-zero.
// window_hours on the query step is NOT template-substituted — like
// HTTPStep.MaxResponseBytes / step timeout_seconds, it's a static
// per-step config value set at author time, not a per-run variable (the
// DSL template engine only resolves string-typed fields). 24h is the
// digest's fixed lookback; author a second routine (or a copy with a
// different window_hours) for a different cadence.
var WorkspaceDigestDefinition = map[string]interface{}{
	"dsl_version":        "1.0",
	"name":               "workspace-digest",
	"display_name":       "Workspace digest",
	"description":        "Token-zero ops digest: run counts, cost, and top failures over a trailing window, posted to the workspace inbox.",
	"estimated_cost_usd": 0.0,
	"agentless":          true,
	"outputs": []map[string]interface{}{
		{"name": "summary_md", "type": "string"},
	},
	"steps": []map[string]interface{}{
		{
			"id":   "stats",
			"type": "query",
			"query": map[string]interface{}{
				"source":       "pipeline_runs",
				"window_hours": 24,
			},
		},
		{
			"id":    "summary",
			"type":  "transform",
			"needs": []string{"stats"},
			"transform": map[string]interface{}{
				"input":      "{{ steps.stats.output }}",
				"expression": ".summary_md",
			},
		},
		{
			"id":    "post",
			"type":  "notify",
			"needs": []string{"summary"},
			"notify": map[string]interface{}{
				"to":    "workspace",
				"title": "Workspace digest",
				"body":  "{{ steps.summary.output }}",
			},
		},
	},
}

// canonicalJSONRecipes maps a recipe slug → the id of the agent_run step
// whose JSON output should be canonicalised. Authored once here rather
// than inlined into every Definition literal so the recipes stay readable
// and the canonicalisation policy lives in one place.
var canonicalJSONRecipes = map[string]string{
	"extract-contacts":     "extract",
	"incident-timeline":    "extract",
	"classify-ticket":      "classify",
	"normalize-dates":      "normalize",
	"json-schema-validate": "validate",
	"invoice-extract":      "extract",
	"routing-decision":     "route",
	"diff-risk-score":      "score",
	"fetch-and-extract":    "extract",
}

// init appends a final `@json` transform step to every deterministic
// JSON recipe. An LLM's JSON output is only SEMANTICALLY stable on a
// fast tier — its whitespace and key order drift run-to-run (e.g. Haiku
// emitted both `{"a":1}` and `{"a": 1}` for the same input). The
// transform parses that output (stripping any code fence) and
// re-serialises it canonically (compact, alphabetically-sorted keys),
// so the routine's FINAL output is byte-identical every run and across
// tiers — the property that makes these recipes hard, reproducible test
// scenarios. The agent_run step keeps its own validation/grader; this
// only normalises the bytes that flow out of the routine.
func init() {
	for i := range Routines {
		stepID, ok := canonicalJSONRecipes[Routines[i].Slug]
		if !ok {
			continue
		}
		steps, ok := Routines[i].Definition["steps"].([]map[string]interface{})
		if !ok {
			panic("seeddata: routine " + Routines[i].Slug + " has unexpected steps type for canonicalisation")
		}
		// Confirm the target step actually exists before wiring `needs` +
		// `{{ steps.<id>.output }}` to it. A stale canonicalJSONRecipes entry
		// (e.g. a recipe step renamed) would otherwise silently produce a
		// canonical step pointing at nothing and fail only at run time —
		// panic here so the mistake surfaces at init/build instead.
		foundStep := false
		for _, s := range steps {
			if id, _ := s["id"].(string); id == stepID {
				foundStep = true
				break
			}
		}
		if !foundStep {
			panic("seeddata: routine " + Routines[i].Slug + " canonicalisation targets unknown step id " + stepID)
		}
		Routines[i].Definition["steps"] = append(steps, map[string]interface{}{
			"id":    "canonical",
			"type":  "transform",
			"needs": []string{stepID},
			"transform": map[string]interface{}{
				"input":      "{{ steps." + stepID + ".output }}",
				"expression": "@json",
			},
		})
	}
}
