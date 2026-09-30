package api

// Admin › Security across workspaces. An instance admin sees and sets the
// Keeper for every workspace on the server, whichever workspace they happen to
// sit in and whether or not they belong to it:
//
//	GET /api/v1/admin/instance/keeper/governance   every workspace's watchdog settings + the instance defaults
//	PUT /api/v1/admin/instance/keeper/governance   one, several or all workspaces; dry_run previews
//	GET /api/v1/admin/instance/keeper/requests     the decision log across workspaces, rows carry their workspace
//	GET /api/v1/admin/instance/keeper/health       the rolling decision window of every workspace
//
// A save for several workspaces is one transaction: it lands in all of them or
// in none, and each workspace it changes gets its own instance audit entry
// saying what went from what to what. A save for all workspaces also becomes
// the instance defaults a workspace created later starts from.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/keeper/health"
)

type InstanceKeeperHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

func NewInstanceKeeperHandler(db *sql.DB, logger *slog.Logger, j journal.Emitter) *InstanceKeeperHandler {
	if j == nil {
		j = noopEmitter{}
	}
	return &InstanceKeeperHandler{db: db, logger: logger, journal: j}
}

type instanceWorkspaceRef struct {
	ID   string `json:"workspace_id"`
	Name string `json:"workspace_name"`
	Slug string `json:"workspace_slug"`
}

// workspaces lists every live workspace by name. There is no cap: the matrix
// and the panel need them all, and a list that silently stopped at 100 would
// be the admin-wide version of the bug this surface fixes.
func (h *InstanceKeeperHandler) workspaces(ctx context.Context) ([]instanceWorkspaceRef, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, name, slug FROM workspaces WHERE deleted_at IS NULL
		ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []instanceWorkspaceRef
	for rows.Next() {
		var w instanceWorkspaceRef
		if err := rows.Scan(&w.ID, &w.Name, &w.Slug); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// errUnknownWorkspace carries the reference nobody matched.
type errUnknownWorkspace struct{ ref string }

func (e errUnknownWorkspace) Error() string { return "no workspace " + e.ref + " on this instance" }

// pick resolves ids or slugs, in the order given, without duplicates.
func pick(all []instanceWorkspaceRef, refs []string) ([]instanceWorkspaceRef, error) {
	byRef := make(map[string]instanceWorkspaceRef, len(all)*2)
	for _, w := range all {
		byRef[w.ID] = w
		byRef[w.Slug] = w
	}
	seen := map[string]bool{}
	var out []instanceWorkspaceRef
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		w, ok := byRef[ref]
		if !ok {
			return nil, errUnknownWorkspace{ref}
		}
		if !seen[w.ID] {
			seen[w.ID] = true
			out = append(out, w)
		}
	}
	return out, nil
}

// ── Governance ──────────────────────────────────────────────────────────────

type instanceGovernanceRow struct {
	instanceWorkspaceRef
	Configured bool `json:"configured"`
	governance.Settings
	EffectiveSecondApprover effectiveSecondApprover `json:"effective_second_approver"`
}

type instanceGovernanceDefaults struct {
	// Configured is false until an instance admin saves for all workspaces;
	// the settings are then the built-in opt-out.
	Configured bool `json:"configured"`
	governance.Settings
}

type instanceGovernanceList struct {
	Defaults   instanceGovernanceDefaults `json:"defaults"`
	Workspaces []instanceGovernanceRow    `json:"workspaces"`
}

// ListGovernance is GET /api/v1/admin/instance/keeper/governance.
func (h *InstanceKeeperHandler) ListGovernance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	all, err := h.workspaces(ctx)
	if err != nil {
		h.fail(w, "list workspaces", err)
		return
	}
	d, dFound, err := governance.Defaults(ctx, h.db)
	if err != nil {
		h.fail(w, "defaults", err)
		return
	}
	out := instanceGovernanceList{
		Defaults:   instanceGovernanceDefaults{Configured: dFound, Settings: d},
		Workspaces: make([]instanceGovernanceRow, 0, len(all)),
	}
	for _, ws := range all {
		s, found, err := governance.Get(ctx, h.db, ws.ID)
		if err != nil {
			h.fail(w, "governance", err)
			return
		}
		out.Workspaces = append(out.Workspaces, instanceGovernanceRow{
			instanceWorkspaceRef: ws, Configured: found, Settings: s,
			EffectiveSecondApprover: resolveEffectiveSecondApprover(s),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type instanceGovernancePutBody struct {
	// Workspaces names the targets by id or slug. Exclusive with All.
	Workspaces []string `json:"workspaces"`
	// All targets every workspace and also sets the instance defaults.
	All bool `json:"all"`
	// DryRun computes and returns the changes without writing anything: the
	// console's "you are about to overwrite …" dialog is this response.
	DryRun bool `json:"dry_run"`
	// Set is the partial update, the same fields and bounds as the
	// per-workspace PUT /api/v1/admin/keeper/governance.
	Set keeperGovernancePutBody `json:"set"`
}

type governanceFieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type instanceGovernanceChange struct {
	instanceWorkspaceRef
	// Changes is empty when the workspace already had these values; such a
	// workspace is left alone.
	Changes  []governanceFieldChange `json:"changes"`
	Warnings []string                `json:"warnings,omitempty"`
}

type instanceGovernancePutResponse struct {
	Applied bool `json:"applied"`
	// Changed is how many workspaces the save changes (or, on a dry run, would).
	Changed         int                        `json:"changed"`
	Workspaces      []instanceGovernanceChange `json:"workspaces"`
	DefaultsUpdated bool                       `json:"defaults_updated"`
}

// PutGovernance is PUT /api/v1/admin/instance/keeper/governance.
func (h *InstanceKeeperHandler) PutGovernance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body instanceGovernancePutBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch {
	case body.All && len(body.Workspaces) > 0:
		replyError(w, http.StatusBadRequest, "send either all or workspaces, not both")
		return
	case !body.All && len(body.Workspaces) == 0:
		replyError(w, http.StatusBadRequest, "choose the workspaces to change, or all")
		return
	case reflect.DeepEqual(body.Set, keeperGovernancePutBody{}):
		replyError(w, http.StatusBadRequest, "nothing to change")
		return
	}

	every, err := h.workspaces(ctx)
	if err != nil {
		h.fail(w, "list workspaces", err)
		return
	}
	targets := every
	if !body.All {
		if targets, err = pick(every, body.Workspaces); err != nil {
			replyError(w, http.StatusNotFound, err.Error())
			return
		}
		if len(targets) == 0 {
			replyError(w, http.StatusBadRequest, "choose the workspaces to change, or all")
			return
		}
	}
	// A person or a vault credential belongs to one workspace; setting either
	// across several would point most of them at something they cannot use.
	if len(targets) > 1 || body.All {
		if body.Set.SecurityContactUserID != nil {
			replyError(w, http.StatusBadRequest, "security_contact_user_id is set per workspace — choose one workspace")
			return
		}
		if body.Set.GovModelCredentialID != nil {
			replyError(w, http.StatusBadRequest, "gov_model_credential_id is set per workspace — choose one workspace")
			return
		}
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	type planned struct {
		change instanceGovernanceChange
		after  governance.Settings
	}
	plan := make([]planned, 0, len(targets))
	resp := instanceGovernancePutResponse{Workspaces: make([]instanceGovernanceChange, 0, len(targets))}
	for _, ws := range targets {
		cur, _, err := governance.Get(ctx, tx, ws.ID)
		if err != nil {
			h.fail(w, "governance", err)
			return
		}
		after, err := mergeGovernancePatch(cur, body.Set)
		if err != nil {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("workspace %s (%s): %s", ws.Name, ws.Slug, err.Error()))
			return
		}
		if len(targets) == 1 {
			if status, msg := checkGovernanceRefs(ctx, h.db, ws.ID, body.Set, after); status != 0 {
				if status == http.StatusInternalServerError {
					h.fail(w, "reference check", errors.New(msg))
					return
				}
				replyError(w, status, msg)
				return
			}
		}
		c := instanceGovernanceChange{instanceWorkspaceRef: ws, Changes: diffGovernance(cur, after)}
		if len(c.Changes) > 0 {
			resp.Changed++
			c.Warnings = governanceWarnings(ctx, h.db, h.logger, ws.ID, body.Set)
		}
		plan = append(plan, planned{change: c, after: after})
		resp.Workspaces = append(resp.Workspaces, c)
	}

	var defaultsAfter governance.Settings
	if body.All {
		d, _, err := governance.Defaults(ctx, tx)
		if err != nil {
			h.fail(w, "defaults", err)
			return
		}
		if defaultsAfter, err = mergeGovernancePatch(d, body.Set); err != nil {
			replyError(w, http.StatusBadRequest, "instance defaults: "+err.Error())
			return
		}
		resp.DefaultsUpdated = true
	}

	if body.DryRun {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	actor := ""
	if u := UserFromContext(ctx); u != nil {
		actor = u.ID
	}
	for _, p := range plan {
		if len(p.change.Changes) == 0 {
			continue
		}
		if err := governance.Upsert(ctx, tx, p.change.ID, p.after, actor); err != nil {
			h.fail(w, "upsert", err)
			return
		}
		if err := auditInstanceTx(ctx, r, tx, "instance.keeper_governance_updated", "workspace", p.change.ID, p.change.ID, map[string]any{
			"workspace_name": p.change.Name,
			"changes":        p.change.Changes,
			"targets":        len(targets),
			"all":            body.All,
		}); err != nil {
			h.fail(w, "audit", err)
			return
		}
	}
	if body.All {
		if err := governance.SetDefaults(ctx, tx, defaultsAfter); err != nil {
			h.fail(w, "set defaults", err)
			return
		}
		if err := auditInstanceTx(ctx, r, tx, "instance.keeper_defaults_updated", "instance", "", "", map[string]any{
			"set": body.Set,
		}); err != nil {
			h.fail(w, "audit", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		h.fail(w, "commit", err)
		return
	}
	resp.Applied = true

	// The workspace journal is where a workspace's own people look; each one
	// the save changed hears about it there too.
	for _, p := range plan {
		if len(p.change.Changes) == 0 {
			continue
		}
		if _, jerr := h.journal.Emit(ctx, journal.Entry{
			WorkspaceID: p.change.ID,
			Type:        journal.EntryKeeperDecision,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorUser,
			ActorID:     actor,
			Summary:     "keeper watchdog governance updated by an instance administrator",
			Payload:     map[string]any{"changes": p.change.Changes, "targets": len(targets), "all": body.All},
		}); jerr != nil {
			h.logger.Warn("instance keeper governance: journal emit failed", "error", jerr, "workspace_id", p.change.ID)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// diffGovernance lists the fields that differ, by their wire name, so the
// console, the CLI and the audit entry all speak the same names the PUT takes.
// The watch spec is reported by length only: the text can be long and the
// audit trail need not carry it.
func diffGovernance(before, after governance.Settings) []governanceFieldChange {
	out := []governanceFieldChange{}
	bv, av := reflect.ValueOf(before), reflect.ValueOf(after)
	t := bv.Type()
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		b, a := bv.Field(i).Interface(), av.Field(i).Interface()
		if name == "watch_presets" {
			b, a = presetList(before.WatchPresets), presetList(after.WatchPresets)
		}
		if reflect.DeepEqual(b, a) {
			continue
		}
		if name == "watch_spec" {
			b, a = fmt.Sprintf("%d characters", len(before.WatchSpec)), fmt.Sprintf("%d characters", len(after.WatchSpec))
		}
		out = append(out, governanceFieldChange{Field: name, Before: b, After: a})
	}
	return out
}

// presetList treats nil and empty alike: both mean no presets.
func presetList(p []string) []string {
	if len(p) == 0 {
		return []string{}
	}
	return p
}

// ── Decision log ────────────────────────────────────────────────────────────

type instanceKeeperRequest struct {
	keeperLogEntry
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
}

type instanceKeeperCounts struct {
	Allow    int `json:"allow"`
	Deny     int `json:"deny"`
	Escalate int `json:"escalate"`
	Pending  int `json:"pending"`
}

type instanceKeeperWorkspaceCount struct {
	instanceWorkspaceRef
	Count int `json:"count"`
}

type instanceKeeperRequestList struct {
	Items []instanceKeeperRequest `json:"items"`
	// Total and Counts describe everything the filter matches, not the page.
	Total  int                  `json:"total"`
	Counts instanceKeeperCounts `json:"counts"`
	// ByWorkspace counts every workspace's requests regardless of the
	// filter: it is the panel next to the list, which keeps showing how much
	// each workspace holds while the list is narrowed to some of them.
	ByWorkspace []instanceKeeperWorkspaceCount `json:"by_workspace"`
}

// ListRequests is GET /api/v1/admin/instance/keeper/requests
// ?workspace=<id|slug>[,…]&request_type=<type>&decision=<ALLOW|DENY|ESCALATE|PENDING>&limit=&offset=
// No workspace, or workspace=all, means every workspace.
func (h *InstanceKeeperHandler) ListRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	every, err := h.workspaces(ctx)
	if err != nil {
		h.fail(w, "list workspaces", err)
		return
	}

	where := []string{"1=1"}
	var args []any
	if raw := strings.TrimSpace(q.Get("workspace")); raw != "" && raw != "all" {
		ws, err := pick(every, strings.Split(raw, ","))
		if err != nil {
			replyError(w, http.StatusNotFound, err.Error())
			return
		}
		ph := make([]string, len(ws))
		for i, x := range ws {
			ph[i] = "?"
			args = append(args, x.ID)
		}
		where = append(where, "a.workspace_id IN ("+strings.Join(ph, ",")+")")
	}
	if t := strings.TrimSpace(q.Get("request_type")); t != "" {
		where = append(where, "kr.request_type = ?")
		args = append(args, t)
	}
	if d := strings.ToUpper(strings.TrimSpace(q.Get("decision"))); d != "" {
		if d == "PENDING" {
			where = append(where, "(kr.decision IS NULL OR kr.decision = 'PENDING')")
		} else {
			where = append(where, "kr.decision = ?")
			args = append(args, d)
		}
	}
	limit, offset := 200, 0
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, 1000)
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		offset = v
	}
	cond := strings.Join(where, " AND ")

	out := instanceKeeperRequestList{Items: []instanceKeeperRequest{}, ByWorkspace: []instanceKeeperWorkspaceCount{}}
	// The join to agents is what places a request in a workspace; a request
	// whose agent is gone has no workspace to show under and is left out.
	from := `FROM keeper_requests kr
		JOIN agents a ON a.id = kr.requesting_agent_id
		JOIN workspaces wsp ON wsp.id = a.workspace_id AND wsp.deleted_at IS NULL
		LEFT JOIN credentials c ON c.id = kr.credential_id`

	counts, err := h.db.QueryContext(ctx, `SELECT COALESCE(kr.decision, 'PENDING'), COUNT(*) `+from+` WHERE `+cond+` GROUP BY 1`, args...)
	if err != nil {
		h.fail(w, "count", err)
		return
	}
	for counts.Next() {
		var d string
		var n int
		if err := counts.Scan(&d, &n); err != nil {
			counts.Close()
			h.fail(w, "count scan", err)
			return
		}
		out.Total += n
		switch d {
		case "ALLOW":
			out.Counts.Allow += n
		case "DENY":
			out.Counts.Deny += n
		case "ESCALATE":
			out.Counts.Escalate += n
		default:
			out.Counts.Pending += n
		}
	}
	counts.Close()

	rows, err := h.db.QueryContext(ctx, `
		SELECT
			kr.id, kr.requesting_agent_id, a.name,
			COALESCE(kr.requesting_crew_id,''), COALESCE(kr.credential_id,''),
			CASE WHEN kr.credential_id IS NULL THEN '' ELSE COALESCE(c.name,'Unknown') END,
			kr.intent, kr.request_type, kr.command,
			kr.decision, kr.reason, kr.risk_score, kr.exit_code,
			kr.ollama_prompt, kr.ollama_raw_response,
			kr.created_at, kr.decided_at, kr.judge_profile,
			wsp.id, wsp.name
		`+from+` WHERE `+cond+`
		ORDER BY kr.created_at DESC, kr.id DESC
		LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		h.fail(w, "query", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var e instanceKeeperRequest
		if err := rows.Scan(
			&e.ID, &e.AgentID, &e.AgentName,
			&e.CrewID, &e.CredentialID, &e.CredName,
			&e.Intent, &e.RequestType, &e.Command,
			&e.Decision, &e.Reason, &e.RiskScore, &e.ExitCode,
			&e.OllamaPrompt, &e.OllamaRawResponse,
			&e.CreatedAt, &e.DecidedAt, &e.JudgeProfile,
			&e.WorkspaceID, &e.WorkspaceName,
		); err != nil {
			h.fail(w, "scan", err)
			return
		}
		out.Items = append(out.Items, e)
	}
	if err := rows.Err(); err != nil {
		h.fail(w, "rows", err)
		return
	}

	per := map[string]int{}
	byRows, err := h.db.QueryContext(ctx, `
		SELECT a.workspace_id, COUNT(*) FROM keeper_requests kr
		JOIN agents a ON a.id = kr.requesting_agent_id GROUP BY a.workspace_id`)
	if err != nil {
		h.fail(w, "by workspace", err)
		return
	}
	for byRows.Next() {
		var id string
		var n int
		if err := byRows.Scan(&id, &n); err != nil {
			byRows.Close()
			h.fail(w, "by workspace scan", err)
			return
		}
		per[id] = n
	}
	byRows.Close()
	for _, ws := range every {
		out.ByWorkspace = append(out.ByWorkspace, instanceKeeperWorkspaceCount{instanceWorkspaceRef: ws, Count: per[ws.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Health ──────────────────────────────────────────────────────────────────

type instanceKeeperHealthRow struct {
	keeperHealthResponse
	WorkspaceName string `json:"workspace_name"`
	WorkspaceSlug string `json:"workspace_slug"`
}

// Health is GET /api/v1/admin/instance/keeper/health: the window of every
// workspace, an empty one included — zero samples is its own answer.
func (h *InstanceKeeperHandler) Health(w http.ResponseWriter, r *http.Request) {
	every, err := h.workspaces(r.Context())
	if err != nil {
		h.fail(w, "list workspaces", err)
		return
	}
	out := make([]instanceKeeperHealthRow, 0, len(every))
	for _, ws := range every {
		out = append(out, instanceKeeperHealthRow{
			keeperHealthResponse: keeperHealthFor(health.Default, ws.ID),
			WorkspaceName:        ws.Name, WorkspaceSlug: ws.Slug,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": out})
}

func (h *InstanceKeeperHandler) fail(w http.ResponseWriter, what string, err error) {
	h.logger.Error("instance keeper: "+what, "error", err)
	replyError(w, http.StatusInternalServerError, "internal error")
}

// auditInstanceTx is auditInstance inside a transaction, so the audit entry
// and the change it records commit together or not at all.
func auditInstanceTx(ctx context.Context, r *http.Request, tx *sql.Tx, action, entityType, entityID, targetWorkspaceID string, metadata map[string]any) error {
	userID := ""
	if u := UserFromContext(ctx); u != nil {
		userID = u.ID
	}
	b, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	meta := string(b)
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO instance_audit_logs (id, user_id, action, entity_type, entity_id, target_workspace_id, metadata, ip_address, user_agent, created_at)
		VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullable(userID), action, entityType, nullable(entityID), nullable(targetWorkspaceID), meta,
		nullable(clientIP(r)), nullable(r.UserAgent()), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
