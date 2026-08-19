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
		"tab": map[string]any{"type": "string",
			"description": "Optional. The tab this panel renders under (internal/pages/tabs.go). Bar order is FIRST APPEARANCE in the panel list, and a panel with no tab lands on the first tab; a page where no panel declares one has no bar. Each tab carries the worst state of its own panels, and the page's freshness summary is computed over ALL tabs — a hidden tab must never be a place a panel can go stale quietly (§4)."},
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
	// The one addition to that list is `tab`, and it is there for the
	// placeholder's own reason: the page must have the same shape for everyone,
	// and a tab whose panels are all sealed to this reader must still appear on
	// their bar. It is authored page structure, exactly like `span`, and says
	// nothing about the panel's data, producer or health.
	sealedPanel := obj(map[string]any{
		"panel_id":        str(),
		"span":            integer(),
		"sealed":          map[string]any{"type": "boolean", "enum": []any{true}},
		"owner_crew_name": str(),
		"tab":             str(),
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

	// ── Grants (§7.1, §7.1b) ────────────────────────────────────────────────
	//
	// `live` is the field worth documenting rather than the columns behind it.
	// A grant is only as wide as the human who issued it, so liveness is
	// recomputed on every read against that human's CURRENT reach; a client
	// that cached `level` and skipped `live` would show a permission that
	// stopped working the moment its issuer left the crew.
	grant := obj(map[string]any{
		"subject_type":       map[string]any{"type": "string", "enum": []string{"user", "crew", "agent"}},
		"subject":            str(),
		"subject_id":         str(),
		"level":              map[string]any{"type": "string", "enum": []string{"read", "produce", "write"}},
		"panels":             arr(str()),
		"granted_by":         str(),
		"granted_by_user_id": str(),
		"granted_at":         timeString(),
		"live": map[string]any{"type": "boolean",
			"description": "Recomputed on read, never stored (§7.1b). False when the issuing human no longer reaches what the grant names — the grant stops working at the same moment."},
		"inert_reason": map[string]any{"type": "string",
			"description": "Present only when `live` is false, naming why in a sentence an operator can act on."},
	})
	grantsResponse := obj(map[string]any{
		"page":   str(),
		"grants": arr(grant),
		"changed": map[string]any{"type": "integer",
			"description": "Rows affected by an issue or a revoke. 0 on a revoke naming a subject that held no grant — a revoke that changed nothing still succeeded."},
	})

	// ── Versions and rollback (§10b.1) ──────────────────────────────────────
	version := obj(map[string]any{
		"seq": integer(), "created_at": timeString(),
		"author": str(), "author_label": str(), "name": str(),
		"panel_count": integer(),
		"current":     boolean(),
	})

	// ── Transfer (§10b.2) ───────────────────────────────────────────────────
	//
	// A bundle carries the page's SHAPE and nothing else: no payloads, no
	// grants, no tokens, and no `public` flag on a panel. Publication is a
	// property of the install, not of the document, so a bundle that could
	// carry it would publish panels nobody in the receiving workspace has
	// looked at.
	bundleRef := obj(map[string]any{
		"ref":     str(),
		"kind":    map[string]any{"type": "string", "enum": []string{"crew", "agent", "routine"}},
		"used_by": arr(str()),
		"reason":  str(),
	})
	bundleProps := map[string]any{
		"format": map[string]any{"type": "string",
			"description": "`crewship.page.bundle/v1`. An unknown format is refused rather than read optimistically."},
		"page": obj(map[string]any{
			"name": str(), "slug": str(), "description": str(), "owner": str(),
			"panels": arr(obj(panelSpec)),
		}),
		"references": map[string]any{"type": "array", "items": bundleRef,
			"description": "Every reference the page makes to something outside itself. The importer must bind each one explicitly; an unbound reference is refused (422), because guessing would hand the page to whoever happens to hold that name in the receiving workspace."},
		"metadata": obj(map[string]any{"exported_at": timeString(), "panel_count": integer()}),
	}
	bundle := obj(bundleProps)

	// ── Public links (§7.3) ─────────────────────────────────────────────────
	publicToken := obj(map[string]any{
		"id": str(),
		"token": map[string]any{"type": "string",
			"description": "Returned ONCE, by the publish call. The column holds a SHA-256 digest, so no later read can produce it."},
		"url":             str(),
		"expires_at":      timeString(),
		"show_provenance": boolean(),
		"has_password":    boolean(),
		"created_by":      str(),
		"created_at":      timeString(),
		"revoked_at":      timeString(),
		"last_seen_at":    timeString(),
		"live": map[string]any{"type": "boolean",
			"description": "Not revoked and not yet expired. Two columns and a clock is a calculation every reader would otherwise repeat, and one of them would get it wrong."},
		"panels": map[string]any{"type": "array", "items": str(),
			"description": "The panel ids this link exposes — the human-attested set (§7.3.2), so \"what does this link show\" is answerable without reading the spec."},
	})
	revoked := obj(map[string]any{
		"id": str(), "revoked": boolean(),
		"already": map[string]any{"type": "boolean",
			"description": "Present when the row was already revoked. Revoking twice succeeds; it is the state that matters, not who got there first."},
	})

	// The public READ shape. Deliberately narrower than the authenticated one:
	// no producer names, no run ids, no owner crews (§7.3.4). A stale panel
	// carries its AGE and never its reason — "last updated 3 days ago" is
	// useful to a stranger, "producer script/watch-services.sh has not run
	// since Tuesday" describes the operator's infrastructure to them.
	publicPage := obj(map[string]any{
		"slug": str(), "name": str(), "description": str(),
		"generated_at":    timeString(),
		"show_provenance": boolean(),
		"panels": arr(obj(map[string]any{
			"id": str(), "schema": str(), "title": str(), "span": integer(),
			"state":       map[string]any{"type": "string", "enum": []string{"fresh", "stale", "failed", "never_produced"}},
			"produced_at": timeString(),
			"data":        map[string]any{"description": "The payload as the producer sent it."},
			// Present only on a link published with show_provenance.
			"provenance": provenance,
		})),
	})

	// ── Webhooks (§10b.4) ───────────────────────────────────────────────────
	webhook := obj(map[string]any{
		"id": str(),
		"panel": map[string]any{"type": "string",
			"description": "A token is bound to exactly ONE panel, so a leaked token can write that panel and nothing else."},
		"name": str(),
		"token": map[string]any{"type": "string",
			"description": "Returned once, by the mint call, and stored as a digest thereafter."},
		"url":           str(),
		"created_by":    str(),
		"created_at":    timeString(),
		"revoked_at":    timeString(),
		"last_fired_at": timeString(),
		"fire_count":    integer(),
		"live":          boolean(),
	})

	// ── Actions (§8b) ───────────────────────────────────────────────────────
	actionInput := obj(map[string]any{
		"name": str(), "label": str(),
		"type":     map[string]any{"type": "string", "enum": []string{"string", "number", "boolean", "select"}},
		"required": boolean(), "default": str(),
		"options": arr(str()),
	})
	action := obj(map[string]any{
		"id": str(),
		"kind": map[string]any{"type": "string", "enum": []string{"call", "link", "toggle", "custom"},
			"description": "Closed set. A `link` carries an ENTITY reference, never a URL — a panel that could render an arbitrary link is a phishing surface a producer controls."},
		"label":   str(),
		"style":   map[string]any{"type": "string", "enum": []string{"default", "primary", "danger"}},
		"confirm": obj(map[string]any{"title": str(), "body": str()}),
		"routine": map[string]any{"type": "string",
			"description": "Read-only. The caller dispatches an ACTION ID, never a routine name, so a button cannot be redirected at something the panel author did not declare."},
		"params": anyObject(),
		"inputs": arr(actionInput),
		"target": arr(str()),
		"ref":    obj(map[string]any{"kind": str(), "id": str()}),
	})

	return map[string]DomainSchema{
		"GET /api/v1/pages": {
			Response: arr(pageRow),
		},
		"DELETE /api/v1/pages/{slug}": {
			// 204, no body. Declared so the document says "nothing comes back"
			// rather than leaving a client to guess at an empty object.
			Response: obj(map[string]any{}),
		},
		"GET /api/v1/pages/{slug}/grants": {Response: grantsResponse},
		"PUT /api/v1/pages/{slug}/grants": {
			Request: obj(map[string]any{
				"subject_type": map[string]any{"type": "string", "enum": []string{"user", "crew", "agent"}},
				"subject":      str(),
				"level":        map[string]any{"type": "string", "enum": []string{"read", "produce", "write"}},
				"panels": map[string]any{"type": "array", "items": str(),
					"description": "Optional. Restricts a produce grant to named panels; omitted means every panel on the page."},
			}),
			Response: grantsResponse,
		},
		"DELETE /api/v1/pages/{slug}/grants": {Response: grantsResponse},

		"GET /api/v1/pages/{slug}/versions": {
			Response: obj(map[string]any{
				"page": str(), "retained": integer(), "versions": arr(version),
			}),
		},
		"POST /api/v1/pages/{slug}/rollback": {
			Request: obj(map[string]any{
				"to": map[string]any{"type": "integer",
					"description": "Required. The version to restore, from the versions list."},
			}),
			Response: obj(map[string]any{
				"page": page, "rolled_back_to": integer(), "version": integer(),
				"dimmed": map[string]any{"type": "array", "items": str(),
					"description": "Panels the rollback brought back that hold no current data. A rollback never resurrects an old payload, and naming them here tells the operator who ran it rather than leaving them to find a blank panel five minutes later."},
			}),
		},

		"GET /api/v1/pages/{slug}/export": {Response: bundle},
		"POST /api/v1/pages/import": {
			Request: obj(mergeProps(bundleProps, map[string]any{
				"slug": map[string]any{"type": "string", "description": "The slug to create in this workspace."},
				"bind": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
					"description": "One entry per declared reference, mapping the bundle's name to something that exists here."},
			})),
			Response: page,
		},

		"GET /api/v1/pages/{slug}/public": {
			Response: obj(map[string]any{"page": str(), "links": arr(publicToken)}),
		},
		"POST /api/v1/pages/{slug}/public": {
			Request: obj(map[string]any{
				"expires_in_days": map[string]any{"type": "integer",
					"description": "Default 30, maximum 365. OMIT the field to take the server's default; sending a number pins one that can drift from it."},
				"password": map[string]any{"type": "string",
					"description": "8–72 bytes. bcrypt silently truncates past 72, so a longer one is refused rather than quietly shortened."},
				"show_provenance": map[string]any{"type": "boolean",
					"description": "Default false. Producer and routine names are internal vocabulary; a public page that carries them describes the operator's infrastructure to whoever holds the link."},
			}),
			Response: publicToken,
		},
		"DELETE /api/v1/pages/{slug}/public/{tokenId}": {Response: revoked},

		"GET /api/v1/pages/{slug}/webhooks": {
			Response: obj(map[string]any{"page": str(), "webhooks": arr(webhook)}),
		},
		"POST /api/v1/pages/{slug}/webhooks": {
			Request:  obj(map[string]any{"panel": str(), "name": str()}),
			Response: webhook,
		},
		"DELETE /api/v1/pages/{slug}/webhooks/{webhookId}": {Response: revoked},

		// The token in the path IS the authentication, and the body is the
		// payload with no envelope — the same bytes the CLI write path takes,
		// judged by the same schema.
		"POST /api/v1/page-webhooks/{token}": {
			Request: anyObject(),
			Response: obj(map[string]any{
				"accepted": boolean(), "panel": str(), "seq": integer(),
			}),
		},

		"GET /api/v1/public/pages/{token}": {Response: publicPage},
		"POST /api/v1/public/pages/{token}/unlock": {
			Request: obj(map[string]any{"password": str()}),
			// A correct password SERVES the page; there is no separate
			// "unlocked" acknowledgement to round-trip for.
			Response: publicPage,
		},

		"GET /api/v1/pages/{slug}/panels/{panelId}/actions": {
			Response: obj(map[string]any{"page": str(), "panel": str(), "actions": arr(action)}),
		},
		"POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}": {
			Request: obj(map[string]any{
				"inputs": map[string]any{"type": "object", "additionalProperties": true,
					"description": "The inputs the action declares. An Idempotency-Key header is bound to the RESOLVED inputs, so retrying is safe and replaying the same key with different inputs answers 409."},
			}),
			Response: obj(map[string]any{
				"status": map[string]any{"type": "string", "enum": []string{"SCHEDULED", "DEDUPED"},
					"description": "The run is QUEUED, not finished. A dispatch returns when the work is accepted; the outcome arrives on the run."},
				"pending_id": str(), "fire_at": timeString(),
				"deduped": boolean(), "coalesced": boolean(),
				"page": str(), "panel": str(), "action": str(), "routine": str(),
			}),
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
