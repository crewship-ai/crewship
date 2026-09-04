package seeddata

// packRoutines are the routines the demo packs (packs.go) seed. They are
// kept apart from routineLibrary because they are a different kind of thing:
// the library holds RECIPES (transformations that a fast model reproduces
// byte-for-byte), these hold WATCHES — a deterministic script that reads a
// real source, an agent that judges only what the script found, and a
// notification plus a Page write that a human reads in the morning.
//
// Two conventions every pack routine keeps, because `crewship seed verify`
// depends on them:
//
//   - The script step emits JSON with a `panel` object whose values are
//     already status.v1 states and labels. A transform step can only
//     project `.field`; it cannot decide what "critical" means, so the script
//     is the one place that knows the rule.
//   - The agent's report ends with one machine-readable line,
//     `COUNTS: key=n key=n …`, whose numbers must agree with the list above
//     it and with the script's own counts. That line is what turns "the
//     agent wrote something" into a checkable claim.
var packRoutines = []RoutineDef{
	// ───────────────────────────────────────────────────────────────
	// ci-watch — token-zero probe (wake gate)
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "ci-probe",
		Name:        "CI probe (wake gate)",
		Description: "Agentless probe over the scheduled GitHub Actions workflows: is any of them red or stale? Zero tokens. Used as --wake-slug for ci-nightly-triage.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":  "1.0",
			"name":         "ci-probe",
			"display_name": "CI probe (wake gate)",
			"description": "Agentless probe for the nightly CI watch. Finds out whether any scheduled workflow is red or stale. " +
				"Zero tokens. Used as the wake gate for ci-nightly-triage: while it returns false, the expensive triage never starts.",
			"agentless":          true,
			"estimated_cost_usd": 0.0,
			"egress_targets":     []string{"api.github.com"},
			"credentials_required": []map[string]interface{}{
				{"type": "CLI_TOKEN", "scope": "any"},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "repo",
					"type":        "string",
					"required":    false,
					"default":     "crewship-ai/crewship",
					"description": "GitHub repository owner/name.",
				},
				{
					"name":        "max_stale_hours",
					"type":        "string",
					"required":    false,
					"default":     "48",
					"description": "Hours without a run after which a daily workflow counts as stale. 48 h tolerates one missed run.",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "wake", "type": "boolean"},
			},
			"steps": []map[string]interface{}{
				{
					"id":              "probe",
					"type":            "script",
					"timeout_seconds": 180,
					"script": map[string]interface{}{
						"path":        "scripts/ci_probe.py",
						"interpreter": "python3",
						"args": []string{
							"--repo", "{{ inputs.repo }}",
							"--max-stale-hours", "{{ inputs.max_stale_hours }}",
						},
						"env": map[string]string{
							"GH_TOKEN": "{{ secrets.CLI_TOKEN }}",
						},
					},
				},
				{
					"id":    "wake",
					"type":  "transform",
					"needs": []string{"probe"},
					"transform": map[string]interface{}{
						"input":      "{{ steps.probe.output }}",
						"expression": ".wake",
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// ci-watch — agent triage, only after the gate wakes
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "ci-nightly-triage",
		Name:        "Nightly CI — triage",
		Description: "When the probe finds a red or stale scheduled workflow, read its logs, separate known flaky from real regressions, publish one summary to the inbox and the CI watch page.",
		CrewSlug:    "ops",
		Definition: map[string]interface{}{
			"dsl_version":  "1.0",
			"name":         "ci-nightly-triage",
			"display_name": "Nightly CI — triage",
			"description": "When the probe finds a red or stale scheduled workflow, walk its logs, separate known flaky from real regression, " +
				"and send ONE summary to the inbox. Runs behind the ci-probe wake gate, so on a green night it does not run at all.",
			"estimated_cost_usd": 0.15,
			"max_cost_usd":       3.0,
			"concurrency_key":    "ci-nightly-triage",
			"max_concurrent":     1,
			"egress_targets":     []string{"api.github.com"},
			"credentials_required": []map[string]interface{}{
				{"type": "CLI_TOKEN", "scope": "any"},
				AnthropicCredentialRequirement(),
			},
			"guardrails": map[string]interface{}{
				"input": map[string]interface{}{
					"prompt_injection": map[string]interface{}{"action": "sanitize"},
				},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "repo",
					"type":        "string",
					"required":    false,
					"default":     "crewship-ai/crewship",
					"description": "GitHub repository owner/name.",
				},
				{
					"name":        "max_stale_hours",
					"type":        "string",
					"required":    false,
					"default":     "48",
					"description": "Stale threshold — must match the value on the wake probe, or the two steps contradict each other.",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "report", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":              "probe",
					"type":            "script",
					"timeout_seconds": 180,
					"script": map[string]interface{}{
						"path":        "scripts/ci_probe.py",
						"interpreter": "python3",
						"args": []string{
							"--repo", "{{ inputs.repo }}",
							"--max-stale-hours", "{{ inputs.max_stale_hours }}",
						},
						"env": map[string]string{
							"GH_TOKEN": "{{ secrets.CLI_TOKEN }}",
						},
					},
				},
				transformOf("red_state", "probe", ".panel.red_state"),
				transformOf("red_label", "probe", ".panel.red_label"),
				transformOf("stale_state", "probe", ".panel.stale_state"),
				transformOf("stale_label", "probe", ".panel.stale_label"),
				transformOf("checked_label", "probe", ".panel.checked_label"),
				{
					"id":              "triage",
					"type":            "agent_run",
					"agent_slug":      agentSlugRef("morgan"),
					"needs":           []string{"probe"},
					"complexity":      "smart",
					"model_override":  "claude-sonnet-5",
					"timeout_seconds": 1200,
					"prompt": "You are the nightly CI watch for the repository {{ inputs.repo }}. The probe found this:\n\n" +
						"{{ steps.probe.output }}\n\n" +
						"Go through EVERY entry with status RED or STALE and decide what it is. Follow the skill 'ci-triage' for the procedure:\n\n" +
						"For RED entries: download the failed job logs (`gh run view <run_id> --repo {{ inputs.repo }} --log-failed`) and classify by the skill 'known-flaky' as REGRESSION / FLAKY / INFRA / UNCLEAR. " +
						"For a REGRESSION find the commit that most likely caused it (`gh api` over the commits between the last green and the first red run) — written as a suspicion with a reason, never as a verdict.\n\n" +
						"For STALE entries: this is a silent failure, not a test. Find out WHY the workflow did not run — a removed or changed cron, a disabled workflow (`gh api repos/{{ inputs.repo }}/actions/workflows`), broken YAML, or GitHub switching it off after 60 days of repository inactivity. State the concrete cause, not 'it did not run'.\n\n" +
						"NEVER re-run anything, never change the repository, never open an issue or comment on a PR. You are a reader and a messenger.\n\n" +
						"If a log cannot be downloaded (network policy, 403), say so and classify that entry UNCLEAR — do not guess.\n\n" +
						"Answer ONLY in markdown in this exact shape — it is the body of a notification a person reads in the morning:\n\n" +
						"## Nightly CI — <count> to handle\n\n" +
						"### Regressions (act now)\n- **<workflow>** — <what exactly failed>. Suspected commit: <sha> (<why>). [run](<url>)\n\n" +
						"### Flaky (known, not blocking)\n- **<workflow>** — <test/job>, matches known signature <which>.\n\n" +
						"### Stale (workflow not running)\n- **<workflow>** — <concrete cause>, last run <when>.\n\n" +
						"### Unclear\n- **<workflow>** — <what you saw and what is missing to decide>\n\n" +
						"Leave empty sections out. End with exactly one line:\n" +
						"COUNTS: regressions=<n> flaky=<n> infra=<n> stale=<n> unclear=<n>",
					"validation": map[string]interface{}{
						"min_length":       20,
						"must_contain":     []string{"COUNTS:"},
						"must_not_contain": []string{"API_KEY=", "Bearer ", "ghp_", "github_pat_"},
					},
					"on_fail": "abort",
				},
				{
					"id":    "post",
					"type":  "notify",
					"needs": []string{"triage"},
					"notify": map[string]interface{}{
						"to":       "workspace",
						"title":    "Nightly CI — something to handle",
						"body":     "{{ steps.triage.output }}",
						"priority": "high",
						"category": "routines.completed",
					},
				},
				{
					"id":     "page-status",
					"type":   "crewship",
					"action": "page.write",
					"needs":  []string{"triage", "red_state", "red_label", "stale_state", "stale_label", "checked_label"},
					"args": map[string]interface{}{
						"page":  "ci-watch",
						"panel": "status",
						"data": map[string]interface{}{
							"items": []map[string]interface{}{
								{"name": "red runs", "state": "{{ steps.red_state.output }}", "label": "{{ steps.red_label.output }}"},
								{"name": "stale workflows", "state": "{{ steps.stale_state.output }}", "label": "{{ steps.stale_label.output }}"},
								{"name": "coverage", "state": "ok", "label": "{{ steps.checked_label.output }}"},
							},
						},
					},
				},
				{
					"id":     "page-summary",
					"type":   "crewship",
					"action": "page.write",
					// Sequenced behind page-status so a run that fails authority
					// fails on the FIRST panel, not twice.
					"needs": []string{"page-status"},
					"args": map[string]interface{}{
						"page":  "ci-watch",
						"panel": "summary",
						"data": map[string]interface{}{
							// narrative.v1 refuses URLs in prose, and the triage report
							// links every run. The page carries the probe's verdict;
							// the full report with links is the inbox notification.
							"verdict": "{{ steps.red_label.output }}",
							"blocks": []map[string]interface{}{
								{"kind": "paragraph", "text": "{{ steps.stale_label.output }}"},
								{"kind": "paragraph", "text": "{{ steps.checked_label.output }}"},
								{"kind": "list", "text": "The triage with regressions, flaky and stale causes is the inbox notification 'Nightly CI — something to handle'."},
								{"kind": "list", "text": "Wake gate: schedule ci-nightly-triage with --wake-slug ci-probe and it costs nothing on a green night."},
							},
						},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// docs-drift — deterministic scan, agent judgement
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "docs-drift-audit",
		Name:        "Docs drift audit",
		Description: "Scan curated documentation ↔ code pairs for phantoms and gaps, have the agent confirm or reject each candidate with a path and a line, publish one summary.",
		CrewSlug:    "quality",
		Definition: map[string]interface{}{
			"dsl_version":  "1.0",
			"name":         "docs-drift-audit",
			"display_name": "Docs drift audit",
			"description": "Weekly audit of the curated documentation ↔ code pairs: a deterministic scan lists candidates for drift " +
				"(phantoms and gaps) in both directions, and the agent decides for each one whether it is a real defect or " +
				"just another name for the same thing. Sends one summary with concrete paths and lines.",
			"estimated_cost_usd": 0.4,
			"max_cost_usd":       5.0,
			"concurrency_key":    "docs-drift-audit",
			"max_concurrent":     1,
			"egress_targets":     []string{"github.com"},
			"credentials_required": []map[string]interface{}{
				{"type": "CLI_TOKEN", "scope": "any"},
				AnthropicCredentialRequirement(),
			},
			"guardrails": map[string]interface{}{
				"input": map[string]interface{}{
					"prompt_injection": map[string]interface{}{"action": "sanitize"},
				},
			},
			"inputs": []map[string]interface{}{
				{
					"name":        "repo",
					"type":        "string",
					"required":    false,
					"default":     "crewship-ai/crewship",
					"description": "GitHub repository owner/name.",
				},
				{
					"name":        "branch",
					"type":        "string",
					"required":    false,
					"default":     "main",
					"description": "Branch the documentation is judged against. Always main — drift against a feature branch is meaningless.",
				},
				{
					"name":        "max_candidates",
					"type":        "string",
					"required":    false,
					"default":     "25",
					"description": "Cap on candidates per category and pair. Truncation is always admitted in the report.",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "report", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":              "scan",
					"type":            "script",
					"timeout_seconds": 600,
					"script": map[string]interface{}{
						"path":        "scripts/docs_audit.sh",
						"interpreter": "bash",
						"env": map[string]string{
							"GH_TOKEN":       "{{ secrets.CLI_TOKEN }}",
							"REPO":           "{{ inputs.repo }}",
							"BRANCH":         "{{ inputs.branch }}",
							"MAX_CANDIDATES": "{{ inputs.max_candidates }}",
						},
					},
				},
				transformOf("scan_state", "scan", ".panel.state"),
				transformOf("scan_label", "scan", ".panel.label"),
				transformOf("sha_label", "scan", ".panel.sha_label"),
				transformOf("total", "scan", ".total_candidates"),
				transformOf("pairs", "scan", ".pairs"),
				{
					"id":              "review",
					"type":            "agent_run",
					"agent_slug":      agentSlugRef("jordan"),
					"needs":           []string{"scan"},
					"complexity":      "smart",
					"model_override":  "claude-sonnet-5",
					"timeout_seconds": 2400,
					"prompt": "You are the documentation auditor of the repository {{ inputs.repo }} (branch {{ inputs.branch }}). The deterministic scan returned this:\n\n" +
						"{{ steps.scan.output }}\n\n" +
						"The repository is checked out under /crew/shared/work/ — read the real files from it, do not rely on the candidate list alone.\n\n" +
						"Candidates are NOT findings. The scan compares YAML/JSON tags in Go against identifiers in backticks and is deliberately dumb. " +
						"Your job is to decide, for EVERY candidate, what it is — follow the skill 'docs-drift'.\n\n" +
						"For PHANTOMS (documented, not in the code) verify the key really is absent: `grep -rn '\\\"key\\\"' <package>` and beyond the listed file. " +
						"Common false positives: an enum value instead of a key, a database column instead of a manifest field, a key in another package.\n\n" +
						"For GAPS (in the code, docs silent) verify the field is relevant to a manifest author at all. Internal fields, wire format between server and client, or a field covered on another docs page are not defects.\n\n" +
						"A CONFIRMED finding must have: a path and line in the documentation OR in the code, one sentence on what the documentation claims or omits, and why it hurts a reader. Without that it is not a finding but an impression.\n\n" +
						"FIX nothing, commit nothing, open no issue or PR. You are a reader and a messenger.\n\n" +
						"Answer ONLY in markdown:\n\n" +
						"## Docs drift — <n> confirmed of <m> candidates\n\n" +
						"### <path/to/page.mdx>\n- **`<key>`** — <what is wrong and why it matters>. Code: `<file>:<line>`. Docs: `<file>:<line>`.\n\n" +
						"### Rejected (why these are not findings)\n- **`<key>`** — <one-sentence reason>\n\n" +
						"Keep the Rejected section to the 10 most instructive; summarise the rest with a number. If any pair was truncated (field `truncated`), SAY SO explicitly — otherwise the report would look complete. End with exactly one line:\n" +
						"COUNTS: candidates=<n> confirmed=<n> rejected=<n> truncated=<n>",
					"validation": map[string]interface{}{
						"min_length":       30,
						"must_contain":     []string{"COUNTS:"},
						"must_not_contain": []string{"API_KEY=", "Bearer ", "ghp_", "github_pat_", "x-access-token:"},
					},
					"on_fail": "abort",
				},
				{
					"id":    "post",
					"type":  "notify",
					"needs": []string{"review"},
					"notify": map[string]interface{}{
						"to":       "workspace",
						"title":    "Docs drift — weekly audit",
						"body":     "{{ steps.review.output }}",
						"priority": "medium",
						"category": "routines.completed",
					},
				},
				{
					"id":     "page-status",
					"type":   "crewship",
					"action": "page.write",
					"needs":  []string{"review", "scan_state", "scan_label", "sha_label"},
					"args": map[string]interface{}{
						"page":  "docs-drift",
						"panel": "status",
						"data": map[string]interface{}{
							"items": []map[string]interface{}{
								{"name": "scan", "state": "{{ steps.scan_state.output }}", "label": "{{ steps.scan_label.output }}"},
								{"name": "repository", "state": "ok", "label": "{{ steps.sha_label.output }}"},
							},
						},
					},
				},
				{
					"id":     "page-summary",
					"type":   "crewship",
					"action": "page.write",
					"needs":  []string{"page-status", "total", "pairs"},
					"args": map[string]interface{}{
						"page":  "docs-drift",
						"panel": "summary",
						"data": map[string]interface{}{
							"verdict": "{{ steps.scan_label.output }}",
							"blocks": []map[string]interface{}{
								{"kind": "paragraph", "text": "{{ steps.total.output }} candidates across {{ steps.pairs.output }} documentation-to-code pairs. Candidates are not findings: the reviewed list with paths and lines is the inbox notification 'Docs drift — weekly audit'."},
								{"kind": "list", "text": "Phantom: the documentation describes a key the code does not have. Gap: the code has a field the documentation never mentions."},
								{"kind": "list", "text": "The pairs are curated in config/docs_map.json on the quality crew's shared volume; keep them narrow so the report stays readable."},
							},
						},
					},
				},
			},
		},
	},

	// ───────────────────────────────────────────────────────────────
	// site-replica — deterministic acceptance of the crew's build
	// ───────────────────────────────────────────────────────────────
	{
		Slug:        "site-replica-audit",
		Name:        "Site replica — acceptance",
		Description: "Run the deterministic acceptance checks over the crew's site replica and publish the verdict. No agent, no tokens.",
		CrewSlug:    "engineering",
		Definition: map[string]interface{}{
			"dsl_version":  "1.0",
			"name":         "site-replica-audit",
			"display_name": "Site replica — acceptance",
			"description": "Runs the deterministic acceptance checks (self-contained, metadata, one h1, section coverage against the analyst's " +
				"content map) over /crew/shared/site-replica and publishes the verdict to the inbox and the page. It does not judge " +
				"whether the replica looks right — that stays with the human who opens the file.",
			"estimated_cost_usd": 0.0,
			"egress_targets":     []string{},
			"inputs": []map[string]interface{}{
				{
					"name":        "dir",
					"type":        "string",
					"required":    false,
					"default":     "/crew/shared/site-replica",
					"description": "Directory holding index.html and content-map.json.",
				},
				{
					"name":        "min_coverage",
					"type":        "string",
					"required":    false,
					"default":     "0.7",
					"description": "Share of inventoried sections that must appear in the replica.",
				},
			},
			"outputs": []map[string]interface{}{
				{"name": "verdict", "type": "string"},
			},
			"steps": []map[string]interface{}{
				{
					"id":              "check",
					"type":            "script",
					"timeout_seconds": 120,
					"script": map[string]interface{}{
						"path":        "scripts/replica_check.py",
						"interpreter": "python3",
						"args":        []string{"--dir", "{{ inputs.dir }}", "--min-coverage", "{{ inputs.min_coverage }}"},
					},
				},
				transformOf("state", "check", ".panel.state"),
				transformOf("label", "check", ".panel.label"),
				transformOf("verdict", "check", ".panel.verdict"),
				transformOf("passed", "check", ".passed"),
				transformOf("failed", "check", ".failed"),
				{
					"id":    "post",
					"type":  "notify",
					"needs": []string{"check", "verdict", "label"},
					"notify": map[string]interface{}{
						"to":       "workspace",
						"title":    "Site replica — acceptance {{ steps.verdict.output }}",
						"body":     "**{{ steps.label.output }}**\n\n```json\n{{ steps.check.output }}\n```",
						"priority": "medium",
						"category": "routines.completed",
					},
				},
				{
					"id":     "page-status",
					"type":   "crewship",
					"action": "page.write",
					"needs":  []string{"check", "state", "label"},
					"args": map[string]interface{}{
						"page":  "site-replica",
						"panel": "acceptance",
						"data": map[string]interface{}{
							"items": []map[string]interface{}{
								{"name": "replica", "state": "{{ steps.state.output }}", "label": "{{ steps.label.output }}"},
							},
						},
					},
				},
				{
					"id":     "page-verdict",
					"type":   "crewship",
					"action": "page.write",
					"needs":  []string{"page-status", "verdict", "passed", "failed"},
					"args": map[string]interface{}{
						"page":  "site-replica",
						"panel": "verdict",
						"data": map[string]interface{}{
							"verdict": "{{ steps.verdict.output }} — {{ steps.label.output }}",
							"blocks": []map[string]interface{}{
								{"kind": "paragraph", "text": "{{ steps.passed.output }} checks passed, {{ steps.failed.output }} failed. The check reads the crew's shared volume: index.html and the analyst's content-map.json."},
								{"kind": "list", "text": "Open index.html from the engineering crew's files to judge the look by eye; this panel only reports the mechanical bar."},
								{"kind": "list", "text": "Start the build by asking Alex to copy the site; the lead delegates analysis, data, build and test inside the crew."},
							},
						},
					},
				},
			},
		},
	},
}

// transformOf is a `.field` projection of a JSON step output into its own
// step, so a later crewship/notify step can template the value as a string.
func transformOf(id, from, expression string) map[string]interface{} {
	return map[string]interface{}{
		"id":    id,
		"type":  "transform",
		"needs": []string{from},
		"transform": map[string]interface{}{
			"input":      "{{ steps." + from + ".output }}",
			"expression": expression,
		},
	}
}
