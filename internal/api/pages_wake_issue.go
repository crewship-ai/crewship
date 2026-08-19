package api

// Pages — opening the issue a gate or a lapse asks for.
//
// Both halves of the sensor end here. A wake gate arrives through
// internal/automation (matched, debounced, coalesced, rate-limited) and an SLA
// lapse arrives from the sweeper in pages_on_failure.go, and both want the
// same thing: ONE issue on the owning crew, and not a second one while the
// first is still open.
//
// The "not a second one" is the whole difficulty, and it is why
// page_panel_alerts exists. A wake condition usually persists across the next
// push and a silent panel stays silent, so both producers see their trigger
// again and again. Holding "we already told somebody" in a sweeper's memory
// would lose it on restart and duplicate it across replicas; holding it in the
// database as a row whose PRIMARY KEY is the subject makes the INSERT itself
// the arbiter — the same reasoning that put the push floor in a
// WHERE NOT EXISTS rather than in a read-then-write.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// pageAlertIssue is one request to open an issue for a panel.
type pageAlertIssue struct {
	WorkspaceID string
	PageID      string
	PageSlug    string
	// PanelID is the author-chosen panel id; PanelRowID is the row it resolves
	// to. Both are carried because the caller has one and the alert table
	// needs the other.
	PanelID    string
	PanelRowID string
	// GateKey is the alert's identity within the panel: "sla", or "wake:<n>".
	GateKey  string
	CrewSlug string
	Title    string
	Body     string
	Priority string
	Now      time.Time
}

// pageAlertResult reports what happened, so the caller can journal it.
type pageAlertResult struct {
	Opened          bool
	IssueID         string
	IssueIdentifier string
	CrewID          string
}

// openPanelAlertIssue opens the issue, exactly once per open alert.
//
// The alert row and the issue are written in ONE transaction. Either both
// exist or neither does: an alert row without its issue would suppress every
// future attempt to open one (the panel would go quiet forever and nothing
// would say why), and an issue without its alert row would be re-opened on the
// next tick.
//
// Returns Opened=false with no error when an alert is already open. That is
// the normal steady state of a broken panel, not a failure.
func openPanelAlertIssue(ctx context.Context, db *sql.DB, logger *slog.Logger, req pageAlertIssue) (pageAlertResult, error) {
	var res pageAlertResult

	if err := db.QueryRowContext(ctx,
		`SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`,
		req.WorkspaceID, req.CrewSlug).Scan(&res.CrewID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The crew named by the spec is gone. Not this function's to fix,
			// and NOT silent: a gate pointing at a deleted crew is a page that
			// thinks it is monitored and is not.
			return res, fmt.Errorf("crew %q does not exist in this workspace", req.CrewSlug)
		}
		return res, err
	}

	// The crew's LEAD agent, named as the assignee. insertIssueTx sets
	// lead_agent_id by itself; the ASSIGNEE is separate and matters because
	// IssueHandler.Start refuses an issue with none — so an issue opened
	// without one arrives on the board unable to be started, which is not what
	// "wakes an agent" means.
	var leadAgentID string
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL LIMIT 1`,
		res.CrewID).Scan(&leadAgentID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()

	openedAt := req.Now.UTC().Format(time.RFC3339)
	ins, err := tx.ExecContext(ctx, `
		INSERT INTO page_panel_alerts (panel_id, gate_key, opened_at, crew_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (panel_id, gate_key) DO NOTHING`,
		req.PanelRowID, req.GateKey, openedAt, res.CrewID)
	if err != nil {
		return res, err
	}
	if n, _ := ins.RowsAffected(); n == 0 {
		// Already open. The DATABASE decided that, not a cache, so two
		// replicas sweeping the same second still produce one issue.
		return res, nil
	}

	spec := issueSpec{
		WorkspaceID: req.WorkspaceID,
		CrewID:      res.CrewID,
		Title:       req.Title,
		Description: &req.Body,
		Priority:    req.Priority,
		// The enum on missions.authored_via has no member for "a system
		// dispatcher opened this" other than 'recurring' — widening a CHECK on
		// that table means a full SQLite rebuild of the busiest table in the
		// schema, which is not worth a provenance label. 'recurring' is the
		// only value that already means "no human and no agent tool call
		// authored it", and the body says exactly which gate did.
		AuthoredVia: "recurring",
	}
	if leadAgentID != "" {
		agentKind := "agent"
		spec.AssigneeType = &agentKind
		spec.AssigneeID = &leadAgentID
	}
	issueID, identifier, err := insertIssueTx(ctx, tx, logger, spec)
	if err != nil {
		// The alert row rolls back with it, so the next trigger tries again.
		// A crew with no LEAD agent lands here (errIssueNoLeadAgent) and will
		// keep landing here until somebody hires one — loudly, in the caller's
		// log, rather than by quietly never alerting.
		return res, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE page_panel_alerts SET issue_id = ? WHERE panel_id = ? AND gate_key = ?`,
		issueID, req.PanelRowID, req.GateKey); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Opened, res.IssueID, res.IssueIdentifier = true, issueID, identifier
	return res, nil
}

// ── The automation sink ────────────────────────────────────────────────────

// wakeIssueOpener implements automation.IssueOpener for the rules Pages
// compiles. It is wired once at boot (cmd_start.go) alongside the registry.
//
// It lives in package api rather than in internal/automation because issue
// creation does: insertIssueTx is the one chokepoint every issue goes through,
// and a second implementation of "allocate the identifier, find the LEAD
// agent, validate the assignee" is how the two drift.
type wakeIssueOpener struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
	// journal records the fire. Separate from the issue itself: the issue is
	// the work, the entry is the audit, and one is not a substitute for the
	// other.
	journal journal.Emitter
	now     func() time.Time
}

// NewPagesWakeIssueOpener builds the sink. Hand it to
// automation.Registry.SetIssueOpener.
func NewPagesWakeIssueOpener(db *sql.DB, hub *ws.Hub, journal journal.Emitter, logger *slog.Logger) automation.IssueOpener {
	if logger == nil {
		logger = slog.Default()
	}
	return &wakeIssueOpener{db: db, hub: hub, logger: logger, journal: journal,
		now: func() time.Time { return time.Now().UTC() }}
}

// OpenIssue implements automation.IssueOpener.
func (o *wakeIssueOpener) OpenIssue(ctx context.Context, in automation.IssueIntent) (bool, error) {
	if in.Context["kind"] != "wake" {
		// Another feature's issue rule. Refusing it beats guessing: this
		// opener writes a page_panel_alerts row, and it has no idea what the
		// subject of somebody else's rule is.
		return false, fmt.Errorf("pages: not a page wake rule (context kind %q)", in.Context["kind"])
	}
	pageID := in.Context["page_id"]
	panelID := in.Context["panel"]
	gateKey := in.Context["gate_key"]
	if pageID == "" || panelID == "" || gateKey == "" {
		return false, errors.New("pages: wake rule carries no page, panel or gate")
	}

	var (
		panelRowID  string
		ownerCrewID string
		pageSlug    string
		wsID        string
	)
	err := o.db.QueryRowContext(ctx, `
		SELECT pp.id, pp.owner_crew_id, p.slug, p.workspace_id
		  FROM page_panels pp
		  JOIN pages p ON p.id = pp.page_id
		 WHERE pp.page_id = ? AND pp.panel_id = ?`, pageID, panelID).
		Scan(&panelRowID, &ownerCrewID, &pageSlug, &wsID)
	if errors.Is(err, sql.ErrNoRows) {
		// The panel is gone but its rule outlived it — a page deleted between
		// the match and the flush, at most a few hundred milliseconds.
		// Nothing to open and nothing to complain about.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if wsID != in.WorkspaceID {
		// A rule may only ever act inside its own workspace. This cannot
		// happen through reconcileWakeAutomations, which is exactly why it is
		// checked: the cost of being wrong here is cross-tenant.
		return false, fmt.Errorf("pages: rule workspace %q does not own page %q", in.WorkspaceID, pageID)
	}

	res, err := openPanelAlertIssue(ctx, o.db, o.logger, pageAlertIssue{
		WorkspaceID: wsID,
		PageID:      pageID,
		PageSlug:    pageSlug,
		PanelID:     panelID,
		PanelRowID:  panelRowID,
		GateKey:     gateKey,
		CrewSlug:    in.CrewSlug,
		Title:       in.Title,
		Body:        in.Body,
		Priority:    "high",
		Now:         o.now(),
	})
	if err != nil || !res.Opened {
		return false, err
	}

	if o.journal != nil {
		if _, jerr := o.journal.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			CrewID:      ownerCrewID,
			Type:        journal.EntryPageWakeFired,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			ActorID:     "pages",
			Summary: fmt.Sprintf("wake gate on %s/%s woke crew/%s (%s)",
				pageSlug, panelID, in.CrewSlug, res.IssueIdentifier),
			Payload: map[string]any{
				"page":             pageSlug,
				"page_id":          pageID,
				"panel":            panelID,
				"gate":             gateKey,
				"crew":             in.CrewSlug,
				"writes":           in.Context["writes"],
				"issue_id":         res.IssueID,
				"issue_identifier": res.IssueIdentifier,
				"automation_id":    in.AutomationID,
				"coalesced_events": in.Coalesced,
			},
		}); jerr != nil {
			o.logger.Warn("pages: journal entry for a fired wake gate was not written",
				"page", pageSlug, "panel", panelID, "error", jerr)
		}
	}
	if o.hub != nil {
		broadcastWorkspaceEvent(o.hub, wsID, "issue.created",
			map[string]string{"id": res.IssueID, "identifier": res.IssueIdentifier, "title": in.Title})
	}
	o.logger.Info("pages: wake gate fired",
		"page", pageSlug, "panel", panelID, "gate", gateKey,
		"crew", in.CrewSlug, "issue", res.IssueIdentifier,
		"coalesced_events", in.Coalesced)
	return true, nil
}

// wakeIssueTitle trims a title to something an issue list can render. Titles
// are built from the page author's own predicate text, which is short by
// construction — this is the guard against the one that is not.
func wakeIssueTitle(s string) string {
	s = strings.TrimSpace(s)
	const max = 160
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
