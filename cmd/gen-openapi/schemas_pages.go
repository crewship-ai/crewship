package main

// pagesSchemaCatalog describes the /api/v1/pages surface — docs/prd/pages.md
// §11 (the routes), §11b (the wire decisions), §4 (freshness) and §7
// (permissions).
//
// The shapes here are the WIRE, not the tables. Three differences are
// deliberate and are the ones a client gets wrong if the document does not say
// so:
//
//   - `sla_seconds` is an integer (§11b.3). `sla: 30s` is YAML sugar the CLI
//     converts; nothing on the wire carries the duration string.
//   - provenance is a nested object (§11b.4), because flat fields would
//     collide with payload keys the moment a producer emits `produced_at`
//     itself.
//   - `state` has FOUR members (§11b.8) and the server sends all four. A
//     client that infers `never_produced` from an absent field is a client
//     that will disagree with the next one.
//   - a panel the caller may not see is a SEALED PLACEHOLDER (§11b.14), not an
//     omission, and the index carries a freshness rollup per row (§11b.15).
//
// The panel data body has no schema beyond "an object": it is the producer's
// payload, validated against the panel's own declared schema
// (internal/pages/schemas), and describing it here as anything narrower would
// document one of the five panel kinds as if it were all of them.
func pagesSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	arr := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	timeString := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }

	provenance := obj(map[string]any{
		"producer": map[string]any{"type": "string",
			"description": "The panel's declared producer, `<kind>/<ref>`. Server-attached (§4 rule 5): a producer never claims it."},
		"run_id": map[string]any{"type": "string",
			"description": "The pipeline run that produced the payload. A script or webhook producer has no run, and carries the push's own server-side reference (`push:<panel>:<seq>`) instead."},
		"produced_at": timeString(),
	})

	panelSpec := map[string]any{
		"id": map[string]any{"type": "string",
			"description": "The author-chosen panel id. It is the address a producer pushes to, so it is stable across edits."},
		"schema": map[string]any{"type": "string",
			"enum":        []string{"metric.v1", "series.v1", "status.v1", "table.v1", "narrative.v1", "embed.v1"},
			"description": "The set is CLOSED — a new panel kind is a server release. series.v1, narrative.v1 and embed.v1 are reserved and not yet producible."},
		"title": str(),
		"icon": map[string]any{"type": "string",
			"enum": []string{"memory", "cpu", "disk", "network", "container",
				"database", "queue", "clock", "calendar", "money", "people",
				"deploy", "alert"},
			"description": "Optional. The panel's glyph, from a CLOSED set (internal/pages/icons.go) — a name the client has no glyph for would render a blank header, which is a quieter failure than an unknown schema. Absent means the icon the panel's schema implies. There is no per-icon colour: colour on a panel means state."},
		"owner": map[string]any{"type": "string",
			"description": "`crew/<slug>`. This is the ACL, not a label: a viewer outside the crew never receives the panel (§7.1 rule 2)."},
		"producer": map[string]any{"type": "string",
			"description": "`<kind>/<ref>`, kind ∈ {routine, script, agent, webhook}. Only the declared producer may write the panel's payload (§7.1 rule 4)."},
		"sla_seconds": map[string]any{"type": "integer",
			"description": "Required and greater than zero: a panel without an SLA does not validate, and there is no default that means \"never mind\" (§4 rule 1)."},
		"span":   map[string]any{"type": "integer", "description": "1–12 columns of the grid. Absent means 12."},
		"public": boolean(),
	}

	panel := obj(mergeProps(panelSpec, map[string]any{
		"state": map[string]any{"type": "string",
			"enum":        []string{"fresh", "stale", "failed", "never_produced"},
			"description": "Computed SERVER-side on every read (§4 rule 2, §11b.8). fresh and stale are functions of the clock and are never stored."},
		"reason": map[string]any{"type": "string",
			"description": "Why a failed panel failed. Internal vocabulary — never rendered on a public page (§7.3.2b)."},
		"data":       map[string]any{"description": "The last payload, exactly as the producer sent it. Absent when nothing has ever been pushed."},
		"provenance": provenance,
	}))

	// §11b.14: a panel the viewer may not see is serialised as EXACTLY this —
	// no schema, no payload, no producer, no SLA. The slot keeps its width so
	// the page has the same shape for everyone, and `sealed` is present rather
	// than inferred, so a serialisation bug cannot be read as a permission
	// decision.
	sealedPanel := obj(map[string]any{
		"panel_id":        str(),
		"span":            integer(),
		"sealed":          map[string]any{"type": "boolean", "enum": []any{true}},
		"owner_crew_name": str(),
	})

	page := obj(map[string]any{
		"id": str(), "slug": str(), "name": str(), "description": str(),
		"owner": map[string]any{"type": "string",
			"description": "`user/<id>` or `crew/<slug>` — exactly one of the two (§7.1 rule 1)."},
		"panels": map[string]any{"type": "array",
			"items":       map[string]any{"oneOf": []any{panel, sealedPanel}},
			"description": "Every panel on the page, in spec order. A panel owned by a crew the caller does not belong to arrives as the sealed placeholder — decided server-side, before serialisation, never hidden client-side (§7.1 rule 5, §11b.14)."},
		"created_at": timeString(), "updated_at": timeString(),
	})

	pageRow := obj(map[string]any{
		"id": str(), "slug": str(), "name": str(), "description": str(),
		"owner": str(), "owner_crew_slug": str(),
		"panel_count": map[string]any{"type": "integer",
			"description": "Every panel on the page, sealed ones included — the grid draws a placeholder for those, so a count that skipped them would disagree with what the page renders."},
		"panel_states": obj(map[string]any{
			"fresh": integer(), "stale": integer(), "failed": integer(), "never_produced": integer(),
		}),
		"state": map[string]any{"type": "string",
			"enum":        []string{"fresh", "stale", "failed", "never_produced"},
			"description": "The page's worst panel."},
		"last_produced_at": map[string]any{"type": "string", "format": "date-time",
			"description": "Newest produced_at across the page's visible panels. NOT updated_at, which §10 defines as the SPEC's modification time — a page edited an hour ago whose data last arrived a week ago must not read as \"updated today\"."},
		"created_at": timeString(), "updated_at": timeString(),
	})

	// The write half carries two fields the read half does not echo: the
	// sensor (§5, §4 rule 4). They are stored in the page spec and read from
	// there by the gate compiler and the freshness sweeper; a panel document
	// coming BACK does not repeat them, and documenting them on the response
	// would promise a round-trip that `page export` is the door for.
	writePanelSpec := mergeProps(panelSpec, map[string]any{
		"wake": map[string]any{"type": "array",
			"items": obj(map[string]any{
				"when": map[string]any{"type": "string",
					"description": "The threshold, in one of two forms: `any(state == \"critical\")` / `all(state == \"ok\")` over a status.v1 panel, or `value > 90` over a metric.v1 panel. Not an expression language; a predicate the panel's schema cannot satisfy is refused at save time rather than never matching (§5)."},
				"for": map[string]any{"type": "string",
					"description": "Go duration. The condition must hold this long, continuously, before the gate fires, so one bad scrape wakes nobody. Max 24h."},
				"agent": map[string]any{"type": "string",
					"description": "`crew/<slug>` — the crew woken when the gate fires. A crew and never a single agent."},
				"writes": map[string]any{"type": "string",
					"description": "The panel the woken agent is expected to write. Must exist on this page. A declaration, not a grant: the agent still needs produce authority on it."},
			}),
			"description": "Thresholds that turn this panel into a sensor (§5). Each compiles to an `automations` row owned by the page spec. Max 4 per panel."},
		"on_failure": obj(map[string]any{
			"issue": map[string]any{"type": "string",
				"description": "`crew/<slug>` — the crew an SLA lapse or a producer failure opens an issue on, once per lapse (§4 rule 4)."},
		}),
	})

	writeBody := obj(map[string]any{
		"slug": str(), "name": str(), "description": str(),
		"panels": map[string]any{"type": "array", "items": obj(writePanelSpec),
			"description": "The parsed spec (§11b.2). The CLI parses the YAML document and sends this; the server validates it and checks that every declared owner crew and producer routine or agent resolves (§10b.1)."},
	})

	return map[string]DomainSchema{
		"GET /api/v1/pages": {
			Response: arr(pageRow),
		},
		"GET /api/v1/pages/{slug}": {
			Response: page,
		},
		"POST /api/v1/pages": {
			Request: writeBody, Response: page,
		},
		"PATCH /api/v1/pages/{slug}": {
			Request: writeBody, Response: page,
		},
		// The single write path (§11). The body IS the payload: there is no
		// field for a producer, a run id or a timestamp, because those are
		// attached server-side from the token and the server's clock (§4
		// rule 5, §7.1b). A payload over 64 KiB is refused with the rejection
		// envelope, 422 and never a 500 (§10, §10b.3); the producer's own
		// verdict rides on `?state=failed`.
		//
		// A push over the RATE limit answers 429 with a Retry-After header and
		// {error, reason, scope, retry_after_secs, page, panel} (§10b.3, the
		// pattern pipelines_exec.go already uses). `scope` is "panel" or
		// "workspace": one says this producer is too fast, the other says the
		// workspace is, and they need different fixes.
		"PUT /api/v1/pages/{slug}/panels/{panelId}/data": {
			Request: anyObject(),
			Response: obj(map[string]any{
				"accepted": boolean(), "page": str(), "panel": str(),
				"seq":        integer(),
				"state":      map[string]any{"type": "string", "enum": []string{"fresh", "stale", "failed", "never_produced"}},
				"provenance": provenance,
				"rejected": map[string]any{"type": "boolean",
					"description": "Never present on success. A refused push answers 422 with the rejection envelope instead: {rejected, kind, message, detail{bytes_attempted, bytes_limit}}."},
			}),
		},
	}
}

// mergeProps returns a fresh map holding both property sets; the panel spec is
// shared between the read shape and the write shape and neither may mutate it.
func mergeProps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
