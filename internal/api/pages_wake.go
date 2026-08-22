package api

// Pages — wake gates (docs/prd/pages.md §5, §12 v1.1).
//
// §0 calls this the feature's entire payoff: "a cheap script pushes, a
// threshold wakes an agent, and the agent writes its analysis back onto the
// same page". Without it a Page is a read-only dashboard, which §2.1
// documents as the reason the push-to-panel genre lost to query-based tools.
//
// THE SHAPE, IN ONE PARAGRAPH.
// A `wake:` gate is not a new subsystem. It compiles, at page-save time, to
// an ordinary row in `automations` — the journal-event matcher #1836 already
// delivered — whose event type is `page.panel.updated` (which every accepted
// push already emits, from both the human and the routine paths) and whose
// action opens an issue on the crew the gate names. What this file adds to
// that substrate is the one thing the substrate cannot express: the PREDICATE
// over the pushed payload, and its `for` window.
//
// WHY THE PREDICATE IS EVALUATED HERE AND NOT IN THE MATCHER.
// automation.Matcher is exact-equality over the journal payload, deliberately:
// it runs inline on the journal write path, where a regex is a
// pathological-backtracking incident waiting to happen and a database lookup
// is forbidden outright. `any(state == "critical")` needs the decoded payload,
// and `for: 5m` needs the panel's history — neither is available there and
// neither should be. So the push path, which has both in hand already, decides
// whether each gate is ARMED and writes one boolean per armed gate into the
// journal entry it was going to emit anyway. The matcher then does what it is
// good at: exact equality on `page_id`, `panel` and `wake_<n>`.
//
// The result is that everything the substrate provides — debounced,
// coalesced, rate-limited firing, one enqueue for a storm of 200 — applies to
// wake gates for free, and there is exactly one eventing path in the process.
//
// WHAT IT COSTS A PAGE WITH NO GATES: one nil-slice check per push. The gates
// are attached to the panel record from the page spec that loadPanels already
// parses for ordering, so there is no extra query, no extra column and no
// extra parse.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// wakeAutomationIDPrefix marks every automations row Pages owns.
//
// The rows are DERIVED state: the page spec is the source of truth, and each
// save rewrites them. A deterministic id is what makes that a rewrite rather
// than an accumulation — save a page twice and you get one rule, not two —
// and the shared page segment is what lets a delete find them all without a
// page_id column on a table that belongs to another feature.
const wakeAutomationIDPrefix = "aut_pgw_"

// SetAutomationRefresh hands the handler a reload hook into the in-memory
// automation.Registry, wired at boot alongside the registry itself.
//
// Without it a gate authored just now would not fire until the registry's 60s
// tick, and the first thing its author would see after saving is nothing
// happening — which is indistinguishable from the gate being broken. The
// automations API surface takes the same hook for the same reason.
func (h *PageHandler) SetAutomationRefresh(fn func(context.Context)) *PageHandler {
	h.automationRefresh = fn
	return h
}

// refreshAutomations reloads the registry after a page save changed its rules.
func (h *PageHandler) refreshAutomations(ctx context.Context) {
	if h.automationRefresh != nil {
		h.automationRefresh(ctx)
	}
}

// ── Attaching gates to the loaded panel ────────────────────────────────────

// panelGates is one panel's compiled sensor declarations.
type panelGates struct {
	Wake []pages.WakeGate
	// OnFailureCrew is the crew slug from `on_failure: {issue: crew/<slug>}`,
	// or "" when the panel declares none.
	OnFailureCrew string
}

// attachPanelGates copies each panel's compiled gates onto its record.
//
// Called from loadPanels with the spec document it has already unmarshalled
// for ordering, so gates cost nothing to load. A gate that no longer compiles
// (the panel's schema was edited under it, say) is DROPPED rather than
// reported: this is a read path, the authoring gate refuses the same document
// on the way in, and a page must still render when something upstream of it is
// wrong (§10b.4).
func attachPanelGates(doc *pages.Document, byPanelID map[string]*panelRecord) {
	if doc == nil {
		return
	}
	for i := range doc.Spec.Panels {
		spec := doc.Spec.Panels[i]
		rec, ok := byPanelID[spec.ID]
		if !ok {
			continue
		}
		if gates, err := pages.CompileWakeGates(spec); err == nil {
			rec.Gates.Wake = gates
		}
		if crew, err := pages.OnFailureCrewSlug(spec.OnFailure); err == nil {
			rec.Gates.OnFailureCrew = crew
		}
	}
}

// ── The authoring gate ─────────────────────────────────────────────────────

// gatePlan is a validated page's sensor declarations with every crew resolved.
type gatePlan struct {
	panels []gatePlanPanel
	// refresh is the page's `refresh:` declarations (pages_refresh.go). They
	// travel with the gates because they are compiled into the SAME table, by
	// the same reconcile, inside the same transaction — a page whose spec says
	// a panel refreshes on wake, saved next to a rule that failed to write, is
	// a page that lies about what it does, exactly as it is for a gate.
	//
	// Nothing here needs resolving against the database: `refresh:` names no
	// crew, and the routine it runs is the panel's own producer, which
	// resolveReferences has already required to exist.
	refresh []pages.RefreshTrigger
}

type gatePlanPanel struct {
	panelID string
	gates   []pages.WakeGate
}

// resolveGates is the second half of the authoring gate for the sensor: the
// shape is checked by pages.ValidateGates, and every crew it names must EXIST.
//
// Same bargain resolveReferences makes for `owner` and `producer`, and for the
// same reason: a gate pointing at crew/devpos is a gate that would compile, be
// saved, match forever and wake nobody.
func (h *PageHandler) resolveGates(w http.ResponseWriter, r *http.Request, wsID string, doc *pages.Document) (*gatePlan, bool) {
	if err := pages.ValidateGates(doc); err != nil {
		writeSpecError(w, err)
		return nil, false
	}
	// `refresh:` on the same terms and for the same reason (pages_refresh.go).
	// Document.Validate already ran it on both write paths; running it again
	// here is the same bargain ValidateGates strikes on the line above —
	// nothing reaches the compiler that has not just been checked, whichever
	// door the document came through.
	if err := pages.ValidateRefresh(doc); err != nil {
		writeSpecError(w, err)
		return nil, false
	}
	// pages.ValidateGates ran above and Document.Validate runs validatePageRefresh
	// straight after it, so by here every declaration below is known to name a
	// routine producer, and `on:wake` is known to have a gate on this page it
	// could fire from.
	plan := &gatePlan{refresh: pages.RefreshTriggers(doc)}
	crewIDs := map[string]string{}
	resolve := func(slug string) (string, bool) {
		if id, ok := crewIDs[slug]; ok {
			return id, id != ""
		}
		var id string
		err := h.db.QueryRowContext(r.Context(),
			`SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, slug).Scan(&id)
		if err != nil && err != sql.ErrNoRows {
			replyInternalError(w, h.logger, "resolve gate crew", err)
			return "", false
		}
		crewIDs[slug] = id
		return id, id != ""
	}

	for i := range doc.Spec.Panels {
		spec := doc.Spec.Panels[i]
		gates, err := pages.CompileWakeGates(spec)
		if err != nil {
			// Unreachable: ValidateGates compiled the same panels a moment
			// ago. Reported rather than ignored, because "unreachable" is a
			// claim about today's call order.
			replyError(w, http.StatusBadRequest, fmt.Sprintf("panel %q: %v", spec.ID, err))
			return nil, false
		}
		onFailure, err := pages.OnFailureCrewSlug(spec.OnFailure)
		if err != nil {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("panel %q: %v", spec.ID, err))
			return nil, false
		}
		if onFailure != "" {
			if _, ok := resolve(onFailure); !ok {
				if w.Header().Get("Content-Type") == "" {
					replyError(w, http.StatusBadRequest, fmt.Sprintf(
						"panel %q declares on_failure: {issue: crew/%s}, and no such crew exists here",
						spec.ID, onFailure))
				}
				return nil, false
			}
		}
		for _, g := range gates {
			if _, ok := resolve(g.CrewSlug); !ok {
				if w.Header().Get("Content-Type") == "" {
					replyError(w, http.StatusBadRequest, fmt.Sprintf(
						"panel %q wake gate %d names agent: crew/%s, and no such crew exists here",
						spec.ID, g.Index, g.CrewSlug))
				}
				return nil, false
			}
		}
		if len(gates) == 0 {
			continue
		}
		plan.panels = append(plan.panels, gatePlanPanel{panelID: spec.ID, gates: gates})
	}
	return plan, true
}

// ── Compiling to automations rows ──────────────────────────────────────────

// wakeAutomationID is the deterministic id of one gate's rule.
//
// Derived from the page id, the panel id and the gate's ordinal, so the same
// gate keeps the same rule across saves — and so `crewship automation list`
// shows a stable row rather than a new one every time somebody edits the page.
func wakeAutomationID(pageID, panelID string, index int) string {
	return wakeAutomationPagePrefix(pageID) + shortHash(panelID, 8) + "_" + strconv.Itoa(index)
}

// wakeAutomationPagePrefix is the id prefix every rule of one page shares. The
// delete path scans on it.
func wakeAutomationPagePrefix(pageID string) string {
	return wakeAutomationIDPrefix + shortHash(pageID, 12) + "_"
}

func shortHash(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:n]
}

// buildWakeAutomation turns one compiled gate into the rule that fires it.
func buildWakeAutomation(wsID, pageID, pageSlug string, panel gatePlanPanel, g pages.WakeGate, authorUserID string) automation.Automation {
	held := ""
	if g.For > 0 {
		held = fmt.Sprintf(" held for %s", g.For)
	}
	// The issue this becomes is assigned to a crew's agent, which runs INSIDE a
	// crew container — where there is no `crewship` binary at all (the sandbox
	// image ships curl and jq). So the instruction has to name the door that
	// audience actually has: the sidecar on loopback, which stamps the agent's
	// own identity onto the push.
	writes := ""
	if g.Writes != "" {
		writes = fmt.Sprintf("\n\nWrite your analysis back onto the page with one PUT to your sidecar: "+
			"`curl -X PUT http://localhost:9119/pages/%s/%s` with the payload as the body, and the token "+
			"from $CREWSHIP_AGENT_TOKEN. The gate declares that panel as where the answer goes; it does NOT "+
			"grant you produce authority on it, so if the push is refused, that is the ACL and not a bug — "+
			"the panel has to declare `producer: agent/<your slug>`, or a human has to grant you `produce` "+
			"on it.", pageSlug, g.Writes)
	}
	return automation.Automation{
		ID:          wakeAutomationID(pageID, panel.panelID, g.Index),
		WorkspaceID: wsID,
		Name:        fmt.Sprintf("page %s/%s wake %d", pageSlug, panel.panelID, g.Index),
		Enabled:     true,
		EventType:   string(journal.EntryPagePanelUpdated),
		Matcher: automation.Matcher{
			// page_id rather than the slug: a slug is the page's address and
			// the address is immutable today, but a rule keyed on an id
			// cannot be captured by a page created with a recycled slug.
			// `panel` and the gate's own key complete the identity.
			PayloadEquals: map[string]any{
				"page_id":      pageID,
				"panel":        panel.panelID,
				g.PayloadKey(): true,
			},
		},
		ActionKind: automation.ActionKindIssue,
		Action: automation.Action{Issue: &automation.IssueAction{
			CrewSlug: g.CrewSlug,
			Title:    wakeIssueTitle(fmt.Sprintf("%s/%s: %s", pageSlug, panel.panelID, g.When)),
			Body: fmt.Sprintf(
				"Panel **%s** on page **%s** satisfied its wake gate: `%s`%s.\n\n"+
					"Look at panel `%s` on that page and decide what to do about it.%s\n\n"+
					"_Opened by a wake gate on the page spec (docs/prd/pages.md §5). "+
					"Editing or deleting this automation directly will not stop it: the page spec owns the rule "+
					"and the next save rewrites it. Remove the gate from the page instead._",
				panel.panelID, pageSlug, g.When, held, panel.panelID, writes),
			DedupeKey: wakeAlertGateKey(g.Index),
			Context: map[string]string{
				"page_id":  pageID,
				"page":     pageSlug,
				"panel":    panel.panelID,
				"gate_key": wakeAlertGateKey(g.Index),
				"writes":   g.Writes,
				"kind":     "wake",
			},
		}},
		DebounceSeconds: automation.DefaultDebounceSeconds,
		MaxPerHour:      automation.DefaultMaxPerHour,
		CreatedBy:       authorUserID,
	}
}

// wakeAlertGateKey is the page_panel_alerts key for the nth gate. 'sla' is the
// other member of that namespace (pages_on_failure.go).
func wakeAlertGateKey(index int) string { return "wake:" + strconv.Itoa(index) }

// reconcileWakeAutomations brings the page's rules in line with its spec,
// inside the SAME transaction as the page write.
//
// Atomic on purpose: a page whose spec says it wakes devops, saved next to a
// rule set that failed to write, is a page that lies about what it does. The
// SQL is here rather than behind automation.Store because that store takes a
// *sql.DB and this has to join the page's transaction; automation.Automation's
// own Validate still gates every row, so a rule this file writes is a rule
// that package would have accepted.
func reconcileWakeAutomations(ctx context.Context, tx *sql.Tx, wsID, pageID, pageSlug string, plan *gatePlan, authorUserID string, now string) error {
	wanted := map[string]automation.Automation{}
	if plan != nil {
		for _, panel := range plan.panels {
			for _, g := range panel.gates {
				a := buildWakeAutomation(wsID, pageID, pageSlug, panel, g, authorUserID)
				if err := a.Validate(); err != nil {
					return fmt.Errorf("wake gate %d on panel %q: %w", g.Index, panel.panelID, err)
				}
				wanted[a.ID] = a
			}
		}
		// `refresh:` compiles into the SAME table, through the same reconcile
		// and under the same page id prefix (pages_refresh.go). Sharing the
		// prefix is what makes the delete loop below — and deletePageWakeAutomations
		// — remove a refresh rule the spec no longer declares, without either of
		// them having to know that refresh exists.
		for _, t := range plan.refresh {
			a := buildRefreshAutomation(wsID, pageID, pageSlug, t, authorUserID)
			if err := a.Validate(); err != nil {
				return fmt.Errorf("refresh: %s on panel %q: %w", t.On, t.PanelID, err)
			}
			wanted[a.ID] = a
		}
	}

	existing, err := pageWakeAutomationIDs(ctx, tx, wsID, pageID)
	if err != nil {
		return err
	}
	for _, a := range wanted {
		matcher, err := json.Marshal(a.Matcher)
		if err != nil {
			return err
		}
		action, err := json.Marshal(a.Action)
		if err != nil {
			return err
		}
		// One statement rather than get-then-insert-or-update: the page save
		// is not the only writer of this table, and a rule that exists is a
		// rule to overwrite in place — its id is derived, so "already there"
		// always means "the same gate, saved before".
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automations (id, workspace_id, name, enabled, event_type, matcher_json,
			                         action_kind, action_config_json, debounce_seconds, max_per_hour,
			                         created_by, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
			ON CONFLICT(id) DO UPDATE SET
			    name = excluded.name,
			    enabled = 1,
			    event_type = excluded.event_type,
			    matcher_json = excluded.matcher_json,
			    action_kind = excluded.action_kind,
			    action_config_json = excluded.action_config_json,
			    deleted_at = NULL,
			    updated_at = excluded.updated_at`,
			a.ID, a.WorkspaceID, a.Name, a.EventType, string(matcher),
			a.ActionKind, string(action), a.DebounceSeconds, a.MaxPerHour,
			a.CreatedBy, now, now); err != nil {
			return err
		}
		delete(existing, a.ID)
	}
	// Whatever is left named a gate the spec no longer declares. Hard delete
	// rather than the soft delete the store uses: these rows are derived from
	// the spec, page_versions already records what the spec said, and a
	// tombstone here would be a second history of the same edit.
	for id := range existing {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM automations WHERE id = ? AND workspace_id = ?`, id, wsID); err != nil {
			return err
		}
	}
	return nil
}

// pageWakeAutomationIDs lists the rules Pages owns for one page.
type sqlQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func pageWakeAutomationIDs(ctx context.Context, q sqlQuerier, wsID, pageID string) (map[string]struct{}, error) {
	// The prefix is hex, so it carries no LIKE metacharacter and needs no
	// ESCAPE clause.
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM automations WHERE workspace_id = ? AND id LIKE ?`,
		wsID, wakeAutomationPagePrefix(pageID)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// deletePageWakeAutomations removes every rule a deleted page owned.
//
// Pages cascade to their panels, their data and their grants; `automations` is
// another feature's table with no page_id column, so it does not cascade and
// the delete path has to say so explicitly. A rule left behind would match an
// event no surviving panel can emit — harmless in effect, and exactly the kind
// of orphan that makes `automation list` untrustworthy.
func deletePageWakeAutomations(ctx context.Context, db *sql.DB, wsID, pageID string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM automations WHERE workspace_id = ? AND id LIKE ?`,
		wsID, wakeAutomationPagePrefix(pageID)+"%")
	return err
}

// ── The push path ──────────────────────────────────────────────────────────

// wakeSignals decides which of the panel's gates this push arms, and returns
// the journal-payload keys that say so.
//
// It also CLEARS the alert of any gate the push no longer satisfies, which is
// what lets the same gate fire again the next time the condition appears. A
// gate that could only ever fire once would be a monitor that stops monitoring
// after its first incident.
//
// Cost when a panel declares no gates: one length check. Cost when it declares
// gates with no `for`: the predicate, in memory, against a payload the caller
// has already decoded. Only a gate with a `for` window reads the ring, and
// only back as far as that window.
func (h *PageHandler) wakeSignals(ctx context.Context, wsID string, panel *panelRecord, payload pages.Payload, now time.Time) map[string]any {
	if len(panel.Gates.Wake) == 0 || payload == nil {
		return nil
	}

	matched := make([]bool, len(panel.Gates.Wake))
	needHistory := time.Duration(0)
	for i, g := range panel.Gates.Wake {
		matched[i] = g.When.Eval(payload)
		if matched[i] && g.For > needHistory {
			needHistory = g.For
		}
	}

	var history []wakeObservation
	if needHistory > 0 {
		history = h.wakeHistory(ctx, panel, needHistory, now)
	}

	out := map[string]any{}
	for i, g := range panel.Gates.Wake {
		armed := false
		switch {
		case !matched[i]:
		case g.For <= 0:
			armed = true
		default:
			armed = pages.WakeHeldFor(wakeSamplesFor(history, g), g.For, now)
		}
		if armed {
			out[g.PayloadKey()] = true
			continue
		}
		if !matched[i] {
			// The condition is gone: clear the alert so the NEXT crossing
			// opens a new issue. Doing it here rather than on a sweep is what
			// makes a gate edge-triggered against the DATA rather than against
			// a timer — the panel that just told us it is healthy is the best
			// evidence there will ever be.
			//
			// The cost is one indexed DELETE, usually matching nothing, per
			// non-matching push of a panel that declares a gate. It is paid
			// only by gated panels, and it buys immediate re-arming; the
			// alternative (let the sweeper notice) would leave a recovered
			// gate deaf for up to a minute, which is exactly the window an
			// outage that flaps lives in.
			h.clearPanelAlert(ctx, wsID, panel, wakeAlertGateKey(g.Index), "the wake condition no longer holds")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// wakeObservation is one stored payload, decoded once for every gate that has
// to look at it.
type wakeObservation struct {
	producedAt time.Time
	payload    pages.Payload
	// readable is false for a stored payload that no longer decodes — the
	// panel's schema was changed under its own history, say.
	readable bool
}

// wakeSamplesFor projects the decoded history through one gate's predicate.
func wakeSamplesFor(history []wakeObservation, g pages.WakeGate) []pages.WakeSample {
	out := make([]pages.WakeSample, 0, len(history))
	for _, o := range history {
		out = append(out, pages.WakeSample{
			ProducedAt: o.producedAt,
			// Evidence we cannot read breaks the run rather than being
			// skipped: a hold window built over unreadable history is not a
			// hold window, and the conservative answer is "not yet".
			Matched: o.readable && g.When.Eval(o.payload),
		})
	}
	return out
}

// wakeHistory reads the panel's ring back as far as the longest `for` window,
// plus the one payload immediately before it, newest first.
//
// That extra row is the whole reason this is not a plain range query: without
// it, "the condition has held since before the window opened" is
// indistinguishable from "the window happens to start at our oldest evidence",
// and a gate would fire a push early or a push late depending on timing.
//
// The stored payloads are decoded WITHOUT re-running the published JSON
// Schema. They passed it on the way in — that is the only door — and paying
// for the schema again, once per historical row, once per push, would make a
// `for` window cost more than the thing it is guarding.
func (h *PageHandler) wakeHistory(ctx context.Context, panel *panelRecord, window time.Duration, now time.Time) []wakeObservation {
	cutoff := now.Add(-window).UTC().Format(time.RFC3339)
	rows, err := h.db.QueryContext(ctx, `
		SELECT payload_json, produced_at, seq FROM (
		    SELECT payload_json, produced_at, seq FROM page_panel_data
		     WHERE panel_id = ? AND produced_at >= ?
		    UNION ALL
		    SELECT * FROM (
		        SELECT payload_json, produced_at, seq FROM page_panel_data
		         WHERE panel_id = ? AND produced_at < ?
		         ORDER BY seq DESC LIMIT 1
		    )
		)
		ORDER BY seq DESC`,
		panel.RowID, cutoff, panel.RowID, cutoff)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("pages: reading the wake window failed; the gate holds fire",
				"panel", panel.PanelID, "error", err)
		}
		return nil
	}
	defer rows.Close()

	schema := pages.PanelSchema(panel.Schema)
	var out []wakeObservation
	for rows.Next() {
		var raw, producedAt string
		var seq int64
		if err := rows.Scan(&raw, &producedAt, &seq); err != nil {
			return nil
		}
		stored, ok := pages.DecodeStoredPayload(schema, []byte(raw))
		out = append(out, wakeObservation{
			producedAt: parsePageTime(producedAt),
			payload:    stored,
			readable:   ok,
		})
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

// ── Panel alerts ───────────────────────────────────────────────────────────

// clearPanelAlert removes an open alert and journals the recovery. Best
// effort: nothing about a push depends on it.
func (h *PageHandler) clearPanelAlert(ctx context.Context, wsID string, panel *panelRecord, gateKey, why string) {
	res, err := h.db.ExecContext(ctx,
		`DELETE FROM page_panel_alerts WHERE panel_id = ? AND gate_key = ?`, panel.RowID, gateKey)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("pages: clearing a panel alert failed", "panel", panel.PanelID, "gate", gateKey, "error", err)
		}
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	if h.journal == nil {
		return
	}
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		CrewID:      panel.OwnerCrewID,
		Type:        journal.EntryPagePanelRecovered,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		ActorID:     "pages",
		Summary:     fmt.Sprintf("panel %s recovered: %s", panel.PanelID, why),
		Payload: map[string]any{
			"panel":    panel.PanelID,
			"gate":     gateKey,
			"reason":   why,
			"panel_id": panel.RowID,
		},
	})
}
