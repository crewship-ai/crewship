package api

// Pages — `on_failure` and the thing that notices (docs/prd/pages.md §4 rule 4).
//
//	4. on_failure: {issue: crew/<slug>} opens an issue on the owning crew.
//	   A page that quietly stops updating must generate work for a human.
//
// WHAT NOTICES A LAPSE, AND WHY IT IS A SWEEPER.
// Freshness is computed, never stored (§4): the read path asks
// pages.Evaluator on every render, so a stale panel LOOKS stale the moment
// anybody opens the page. That is exactly why a lapse needs its own producer —
// nobody opening the page is the case the rule exists for. There are only
// three candidates for who notices:
//
//	a timer on the push       — a panel that stopped pushing fires no timers;
//	a lazy check on read      — nobody reads a page nobody is looking at, and
//	                            "your monitoring works when you watch it" is
//	                            the Pushgateway failure in a new hat;
//	a sweep                   — costs a query a minute and notices whether or
//	                            not anybody is watching.
//
// So: a sweep, every minute (pageSweepInterval). One minute because the
// smallest SLA anyone writes is measured in tens of seconds and the largest
// detection lag this can add is one interval — a panel with a 30s SLA is
// reported between 30s and 90s after it goes quiet, which is inside the
// resolution anybody acts on. It is not a second freshness authority: the
// verdict comes from the same pages.Evaluator the read path uses, against the
// same injected clock, so the sweeper cannot decide a panel is stale while the
// page says it is fine.
//
// IF IT MISSES A TICK — a restart, a long GC, a deploy, or a replica that
// never had the sweeper wired — nothing is lost and nothing double-fires. The
// sweep is EDGE-TRIGGERED AGAINST DURABLE STATE, not against the previous
// tick: it compares the panel's verdict now with whether a page_panel_alerts
// row is open now. A missed tick delays the issue by one interval; it cannot
// drop it, because the next tick sees the same lapse and the same absent
// alert row. The same property makes two replicas safe — the INSERT's
// ON CONFLICT DO NOTHING is what arbitrates, so the second one opens nothing.
//
// WHY A LAPSE FIRES ONCE AND NOT EVERY TICK.
// The alert row IS the "already told somebody" flag, and it is a row rather
// than a field on the panel because it has to be created by the same statement
// that decides to create it. The row is deleted when the panel recovers, so
// the NEXT lapse is a new edge and opens a new issue. A panel dead for a week
// produces one issue, one notification and two journal entries (lapsed,
// recovered) — not ten thousand.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// pageSweepInterval is how often panels are checked. See the file header for
// why one minute, and for what happens when a tick is missed.
const pageSweepInterval = time.Minute

// slaAlertGateKey is the page_panel_alerts key for the freshness lapse. The
// other members of that namespace are the wake gates ("wake:<n>").
const slaAlertGateKey = "sla"

// PageSweepResult is what one sweep did, for the log line and for the tests.
type PageSweepResult struct {
	Checked   int
	Lapsed    int
	Recovered int
	// Issues counts the panels whose lapse opened an issue — always <= Lapsed,
	// because a panel with no on_failure block still reports its lapse.
	Issues int
}

// panelFreshnessRow is one panel as the sweep reads it.
type panelFreshnessRow struct {
	rowID       string
	panelID     string
	pageID      string
	pageSlug    string
	workspaceID string
	ownerCrewID string
	slaSeconds  int
	createdAt   time.Time
	producer    string
	fault       string
	last        *pages.Observation
	alertOpen   bool
}

// SweepPanelFreshness runs one pass. Exported so the test drives it directly
// against an owned clock instead of waiting for a ticker.
func (h *PageHandler) SweepPanelFreshness(ctx context.Context) (PageSweepResult, error) {
	var res PageSweepResult
	rows, err := h.loadPanelFreshness(ctx)
	if err != nil {
		return res, err
	}
	evaluator := h.evaluator()
	now := evaluator.Now().UTC()
	// The page spec is where on_failure lives; parse each page's once per
	// sweep rather than once per panel.
	specs := map[string]map[string]string{}

	for _, row := range rows {
		res.Checked++
		verdict := evaluator.Evaluate(pages.PanelState{
			Last:  row.last,
			SLA:   time.Duration(row.slaSeconds) * time.Second,
			Fault: row.fault,
		})
		lapsed, reason := panelHasLapsed(row, verdict, now)

		switch {
		case lapsed && !row.alertOpen:
			opened, err := h.reportPanelLapse(ctx, row, verdict, reason, specs, now)
			if err != nil {
				// One panel's failure must not end the sweep: the next panel
				// may be the one somebody is waiting on.
				h.logger.Error("pages: reporting a panel lapse failed",
					"page", row.pageSlug, "panel", row.panelID, "error", err)
				continue
			}
			res.Lapsed++
			if opened {
				res.Issues++
			}
		case !lapsed && row.alertOpen && verdict.State == pages.StateFresh:
			// Recovery is only ever declared from `fresh`. A panel that went
			// from stale to never_produced (its whole ring aged out) has not
			// recovered — it has got worse, and clearing the alert there would
			// re-open an issue the moment it went stale again.
			h.clearPanelAlert(ctx, row.workspaceID, &panelRecord{
				RowID: row.rowID, PanelID: row.panelID, OwnerCrewID: row.ownerCrewID,
			}, slaAlertGateKey, "data arrived inside the SLA again")
			res.Recovered++
		}
	}
	return res, nil
}

// panelHasLapsed decides whether this panel owes somebody an issue.
//
// `fresh` never does. `stale` and `failed` always do — those are §4's own
// words for "the data is not to be trusted", and a failed push is a producer
// that ran and said so.
//
// `never_produced` is the interesting one, and it lapses only once the panel
// has been ALIVE longer than its own SLA. A page saved thirty seconds ago
// whose producer runs hourly is not broken, and opening an issue on the crew
// for it would teach everyone to ignore these. A panel that has existed for
// three times its SLA and has never once reported is a producer that was
// never wired up — which is precisely the silent failure §4 exists to catch,
// and nothing else in the system would ever say so.
func panelHasLapsed(row panelFreshnessRow, verdict pages.Verdict, now time.Time) (bool, string) {
	switch verdict.State {
	case pages.StateFresh:
		return false, ""
	case pages.StateFailed:
		reason := verdict.Reason
		if reason == "" {
			reason = "the panel is reporting a failure"
		}
		return true, reason
	case pages.StateStale:
		return true, fmt.Sprintf("no data for %s; the panel's SLA is %s",
			verdict.Age.Round(time.Second), (time.Duration(row.slaSeconds) * time.Second))
	case pages.StateNeverProduced:
		sla := time.Duration(row.slaSeconds) * time.Second
		if row.createdAt.IsZero() || now.Sub(row.createdAt) < sla {
			return false, ""
		}
		return true, fmt.Sprintf("the panel has never received data, and it was declared %s ago with an SLA of %s",
			now.Sub(row.createdAt).Round(time.Second), sla)
	}
	return false, ""
}

// reportPanelLapse records the edge exactly once and, when the panel declares
// on_failure, opens the issue.
//
// Reporting happens whether or not on_failure is declared. §10b.6 gives the
// page owner a `pages.stale` notification and §4 rule 4 gives the crew an
// issue, and they answer different questions: one is awareness, the other is
// work. A panel with no on_failure block still owes its owner the first.
func (h *PageHandler) reportPanelLapse(ctx context.Context, row panelFreshnessRow, verdict pages.Verdict,
	reason string, specs map[string]map[string]string, now time.Time) (bool, error) {
	crewSlug := h.onFailureCrew(ctx, row, specs)

	var (
		issueID    string
		identifier string
		opened     bool
	)
	if crewSlug != "" {
		res, err := openPanelAlertIssue(ctx, h.db, h.logger, pageAlertIssue{
			WorkspaceID: row.workspaceID,
			PageID:      row.pageID,
			PageSlug:    row.pageSlug,
			PanelID:     row.panelID,
			PanelRowID:  row.rowID,
			GateKey:     slaAlertGateKey,
			CrewSlug:    crewSlug,
			Title: wakeIssueTitle(fmt.Sprintf("%s/%s stopped reporting",
				row.pageSlug, row.panelID)),
			Body: fmt.Sprintf(
				"Panel **%s** on page **%s** is **%s**: %s.\n\n"+
					"Its producer is `%s`. Look at the page — `crewship page get %s` — and either fix the producer "+
					"or change what the page claims to show.\n\n"+
					"_Opened by `on_failure: {issue: crew/%s}` on the page spec (docs/prd/pages.md §4 rule 4). "+
					"One issue per lapse: this will not be re-opened while it stands, and the alert clears when "+
					"the panel reports inside its SLA again._",
				row.panelID, row.pageSlug, verdict.State, reason, row.producer, row.pageSlug, crewSlug),
			Priority: "high",
			Now:      now,
		})
		if err != nil {
			return false, err
		}
		if !res.Opened {
			// Another replica, or another sweep, got there first.
			return false, nil
		}
		issueID, identifier, opened = res.IssueID, res.IssueIdentifier, true
	} else {
		// No on_failure. The alert row is still written, because "we have
		// already reported this lapse" is what makes the notification fire
		// once rather than every minute.
		ins, err := h.db.ExecContext(ctx, `
			INSERT INTO page_panel_alerts (panel_id, gate_key, opened_at, crew_id)
			VALUES (?, ?, ?, NULL)
			ON CONFLICT (panel_id, gate_key) DO NOTHING`,
			row.rowID, slaAlertGateKey, now.Format(time.RFC3339))
		if err != nil {
			return false, err
		}
		if n, _ := ins.RowsAffected(); n == 0 {
			return false, nil
		}
	}

	// The journal entry is the notification: notifyroute maps
	// page.panel.stale onto notify.CategoryPagesStale, which had no producer
	// at all before this. It is emitted for BOTH branches, so a page owner
	// hears about a lapse whether or not a crew was given the work.
	if h.journal != nil {
		payload := map[string]any{
			"page":        row.pageSlug,
			"page_id":     row.pageID,
			"panel":       row.panelID,
			"verdict":     string(verdict.State),
			"reason":      reason,
			"sla_seconds": row.slaSeconds,
			"producer":    row.producer,
		}
		if verdict.Age > 0 {
			payload["age_seconds"] = int(verdict.Age.Seconds())
		}
		if opened {
			payload["issue_id"] = issueID
			payload["issue_identifier"] = identifier
			payload["crew"] = crewSlug
		}
		if _, err := h.journal.Emit(ctx, journal.Entry{
			WorkspaceID: row.workspaceID,
			CrewID:      row.ownerCrewID,
			Type:        journal.EntryPagePanelStale,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			ActorID:     "pages",
			Summary:     fmt.Sprintf("panel %s/%s is %s: %s", row.pageSlug, row.panelID, verdict.State, reason),
			Payload:     payload,
		}); err != nil {
			h.logger.Warn("pages: journal entry for a panel lapse was not written",
				"page", row.pageSlug, "panel", row.panelID, "error", err)
		}
	}
	return opened, nil
}

// onFailureCrew reads `on_failure: {issue: crew/<slug>}` out of the page spec,
// parsing each page's spec at most once per sweep.
func (h *PageHandler) onFailureCrew(ctx context.Context, row panelFreshnessRow, specs map[string]map[string]string) string {
	byPanel, ok := specs[row.pageID]
	if !ok {
		byPanel = map[string]string{}
		var specJSON string
		if err := h.db.QueryRowContext(ctx, `SELECT spec_json FROM pages WHERE id = ?`, row.pageID).Scan(&specJSON); err == nil {
			var doc pages.Document
			if json.Unmarshal([]byte(specJSON), &doc) == nil {
				for i := range doc.Spec.Panels {
					if crew, err := pages.OnFailureCrewSlug(doc.Spec.Panels[i].OnFailure); err == nil && crew != "" {
						byPanel[doc.Spec.Panels[i].ID] = crew
					}
				}
			}
		}
		specs[row.pageID] = byPanel
	}
	return byPanel[row.panelID]
}

// loadPanelFreshness reads every panel with its newest payload and its alert
// state.
//
// It reads no payload BYTES — only the timestamp, the producer's own verdict
// and whether an alert is open — so the sweep's cost does not grow with what
// producers push. The two LEFT JOINs are §10b.4's "when the ground moves", and
// they are here for the same reason loadPanels has them: a panel whose
// producer was deleted is failed, and failed is a lapse.
func (h *PageHandler) loadPanelFreshness(ctx context.Context) ([]panelFreshnessRow, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT pp.id, pp.panel_id, pp.sla_seconds, pp.created_at, pp.owner_crew_id,
		       pp.producer_kind, pp.producer_ref,
		       p.id, p.slug, p.workspace_id,
		       c.deleted_at IS NOT NULL,
		       CASE pp.producer_kind
		            WHEN 'routine' THEN pl.id IS NOT NULL
		            WHEN 'agent'   THEN ag.id IS NOT NULL
		            ELSE 1
		       END,
		       COALESCE(d.produced_at, ''), COALESCE(d.state, ''),
		       a.panel_id IS NOT NULL
		  FROM page_panels pp
		  JOIN pages p ON p.id = pp.page_id
		  JOIN crews c ON c.id = pp.owner_crew_id
		  LEFT JOIN pipelines pl
		         ON pp.producer_kind = 'routine' AND pl.workspace_id = p.workspace_id
		        AND pl.slug = pp.producer_ref AND pl.deleted_at IS NULL
		  LEFT JOIN agents ag
		         ON pp.producer_kind = 'agent' AND ag.workspace_id = p.workspace_id
		        AND ag.slug = pp.producer_ref AND ag.deleted_at IS NULL
		  LEFT JOIN (SELECT panel_id, MAX(seq) AS seq FROM page_panel_data GROUP BY panel_id) n
		         ON n.panel_id = pp.id
		  LEFT JOIN page_panel_data d ON d.panel_id = n.panel_id AND d.seq = n.seq
		  LEFT JOIN page_panel_alerts a ON a.panel_id = pp.id AND a.gate_key = ?`, slaAlertGateKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []panelFreshnessRow
	for rows.Next() {
		var (
			r             panelFreshnessRow
			createdAt     string
			producerKind  string
			producerRef   string
			crewGone      bool
			producerAlive bool
			producedAt    string
			pushState     string
		)
		if err := rows.Scan(&r.rowID, &r.panelID, &r.slaSeconds, &createdAt, &r.ownerCrewID,
			&producerKind, &producerRef, &r.pageID, &r.pageSlug, &r.workspaceID,
			&crewGone, &producerAlive, &producedAt, &pushState, &r.alertOpen); err != nil {
			return nil, err
		}
		r.createdAt = parsePageTime(createdAt)
		r.producer = producerKind + "/" + producerRef
		switch {
		case crewGone:
			r.fault = "the owning crew no longer exists"
		case !producerAlive:
			r.fault = fmt.Sprintf("producer %s %q no longer exists", producerKind, producerRef)
		}
		if producedAt != "" {
			r.last = &pages.Observation{
				ProducedAt: parsePageTime(producedAt),
				Push:       pages.PushState(pushState),
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StartPanelFreshnessSweeper runs SweepPanelFreshness on a ticker until ctx is
// cancelled. Returns immediately; the goroutine exits on ctx.Done().
//
// interval <= 0 falls back to pageSweepInterval. No immediate first tick: at
// boot the panels have not had a chance to be pushed yet, and a sweep racing
// the first push of a freshly restarted instance would open issues about a
// lapse that is really a cold start.
func (h *PageHandler) StartPanelFreshnessSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = pageSweepInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				res, err := h.SweepPanelFreshness(ctx)
				if err != nil {
					h.logger.Warn("pages: freshness sweep failed", "error", err)
					continue
				}
				if res.Lapsed > 0 || res.Recovered > 0 {
					h.logger.Info("pages: freshness sweep",
						"checked", res.Checked, "lapsed", res.Lapsed,
						"issues_opened", res.Issues, "recovered", res.Recovered)
				}
			}
		}
	}()
}

// ensure database/sql stays imported for the row scan helpers above even if
// the query shape changes.
var _ = sql.ErrNoRows
