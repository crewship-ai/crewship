package api

// Admin › Security across workspaces. An instance admin sees and sets the
// Keeper for every workspace on the server, whichever workspace they happen to
// sit in and whether or not they belong to it:
//
//	GET /api/v1/admin/instance/keeper/governance   every workspace's watchdog settings + the instance defaults
//	PUT /api/v1/admin/instance/keeper/governance   one, several or all existing workspaces; dry_run previews
//	GET|PUT /api/v1/admin/instance/keeper/governance/defaults   the template a new workspace copies
//	GET /api/v1/admin/instance/keeper/requests     the decision log across workspaces, rows carry their workspace
//	GET /api/v1/admin/instance/keeper/health       the rolling decision window of every workspace
//
// A save for several workspaces is one transaction: it lands in all of them or
// in none, and each workspace it changes gets its own instance audit entry
// saying what went from what to what. "All" is the scope of that save —
// every existing workspace — and nothing more.
//
// The defaults for new workspaces are a separate operation with its own
// preview and confirmation (PUT …/governance/defaults): a template a
// workspace copies when it is created, which changes no existing one.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"

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
	// All targets every existing workspace. It does not touch the defaults
	// for new workspaces; that is PUT …/governance/defaults.
	All bool `json:"all"`
	// DryRun computes and returns the changes without writing anything: the
	// console's "you are about to overwrite …" dialog is this response.
	DryRun bool `json:"dry_run"`
	// Set is the partial update, the same fields and bounds as the
	// per-workspace PUT /api/v1/admin/keeper/governance.
	Set keeperGovernancePutBody `json:"set"`
	// ExpectPreview is the preview_id of the dry run the admin confirmed. When
	// sent, the save goes through only if it would do exactly what that
	// preview showed — the same workspaces, the same changes, the same
	// defaults — and answers 409 otherwise (a workspace created meanwhile, a
	// value somebody else changed).
	ExpectPreview string `json:"expect_preview,omitempty"`
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
	Changed    int                        `json:"changed"`
	Workspaces []instanceGovernanceChange `json:"workspaces"`
	// PreviewID fingerprints what the save does (or would do): every target
	// with its whole settings before and after. Send it back as
	// expect_preview.
	PreviewID string `json:"preview_id"`
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
		before governance.Settings
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
		plan = append(plan, planned{change: c, before: cur, after: after})
		resp.Workspaces = append(resp.Workspaces, c)
	}

	fp := make([]governanceFingerprintRow, 0, len(plan))
	for _, p := range plan {
		fp = append(fp, governanceFingerprintRow{ID: p.change.ID, Before: p.before, After: p.after})
	}
	resp.PreviewID = governancePreviewID(fp, nil)
	if body.DryRun {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if body.ExpectPreview != "" && body.ExpectPreview != resp.PreviewID {
		replyError(w, http.StatusConflict, "the settings or the workspaces changed since the preview; review the changes again")
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
		if err := auditInstance(ctx, r, tx, "instance.keeper_governance_updated", "workspace", p.change.ID, p.change.ID, map[string]any{
			"workspace_name": p.change.Name,
			"changes":        p.change.Changes,
			"targets":        len(targets),
			"all":            body.All,
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

// governanceFingerprintRow is one workspace of a planned save, whole: the
// settings before and after, the watch spec's text included, which the
// changes list reports only by length.
type governanceFingerprintRow struct {
	ID     string              `json:"id"`
	Before governance.Settings `json:"before"`
	After  governance.Settings `json:"after"`
}

// governancePreviewID fingerprints a planned save: every target with its
// full before and after (or, for the defaults, their before and after). Any
// drift since the preview — a workspace added, a value changed, a rule
// rewritten to the same length — gives another id.
func governancePreviewID(rows []governanceFingerprintRow, defaults *governanceFingerprintRow) string {
	plan := struct {
		Workspaces []governanceFingerprintRow `json:"workspaces,omitempty"`
		Defaults   *governanceFingerprintRow  `json:"defaults,omitempty"`
	}{Workspaces: rows, Defaults: defaults}
	b, _ := json.Marshal(plan)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// ── Defaults for new workspaces ─────────────────────────────────────────────

type instanceDefaultsResponse struct {
	Applied bool `json:"applied"`
	// Configured is false until the defaults were ever saved; they are then
	// the built-in opt-out.
	Configured bool                    `json:"configured"`
	Defaults   governance.Settings     `json:"defaults"`
	Changes    []governanceFieldChange `json:"changes"`
	PreviewID  string                  `json:"preview_id"`
}

type instanceDefaultsPutBody struct {
	DryRun        bool                    `json:"dry_run"`
	ExpectPreview string                  `json:"expect_preview,omitempty"`
	Set           keeperGovernancePutBody `json:"set"`
}

// GetDefaults is GET /api/v1/admin/instance/keeper/governance/defaults.
func (h *InstanceKeeperHandler) GetDefaults(w http.ResponseWriter, r *http.Request) {
	d, found, err := governance.Defaults(r.Context(), h.db)
	if err != nil {
		h.fail(w, "defaults", err)
		return
	}
	writeJSON(w, http.StatusOK, instanceDefaultsResponse{Configured: found, Defaults: d, Changes: []governanceFieldChange{}})
}

// PutDefaults is PUT /api/v1/admin/instance/keeper/governance/defaults: the
// template a workspace copies when it is created. It changes no existing
// workspace. dry_run previews; expect_preview confirms exactly that preview.
func (h *InstanceKeeperHandler) PutDefaults(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body instanceDefaultsPutBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if reflect.DeepEqual(body.Set, keeperGovernancePutBody{}) {
		replyError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if body.Set.SecurityContactUserID != nil || body.Set.GovModelCredentialID != nil {
		replyError(w, http.StatusBadRequest, "a security contact and a judge credential belong to one workspace and cannot be defaults")
		return
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.fail(w, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	cur, found, err := governance.Defaults(ctx, tx)
	if err != nil {
		h.fail(w, "defaults", err)
		return
	}
	after, err := mergeGovernancePatch(cur, body.Set)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := instanceDefaultsResponse{Configured: found, Defaults: after, Changes: diffGovernance(cur, after)}
	resp.PreviewID = governancePreviewID(nil, &governanceFingerprintRow{ID: "defaults", Before: cur, After: after})
	if body.DryRun {
		resp.Defaults = cur
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if body.ExpectPreview != "" && body.ExpectPreview != resp.PreviewID {
		replyError(w, http.StatusConflict, "the defaults changed since the preview; review the changes again")
		return
	}
	if len(resp.Changes) > 0 {
		if err := governance.SetDefaults(ctx, tx, after); err != nil {
			h.fail(w, "set defaults", err)
			return
		}
		if err := auditInstance(ctx, r, tx, "instance.keeper_defaults_updated", "instance", "", "", map[string]any{
			"changes": resp.Changes,
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
	resp.Configured = found || len(resp.Changes) > 0
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
	// Total is how many rows the whole filter matches, for paging.
	Total int `json:"total"`
	// Counts are the decisions under the workspace and kind filter, leaving
	// the decision filter out: the chips that pick a decision keep their
	// numbers while one is picked.
	Counts instanceKeeperCounts `json:"counts"`
	// ByType counts every kind under the workspace filter alone, for the
	// panel's list of kinds.
	ByType map[string]int `json:"by_type"`
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

	var wsCond, typeCond, decCond string
	var wsArgs, typeArgs, decArgs []any
	if raw := strings.TrimSpace(q.Get("workspace")); raw != "" && raw != "all" {
		ws, err := pick(every, strings.Split(raw, ","))
		if err != nil {
			replyError(w, http.StatusNotFound, err.Error())
			return
		}
		ph := make([]string, len(ws))
		for i, x := range ws {
			ph[i] = "?"
			wsArgs = append(wsArgs, x.ID)
		}
		wsCond = " AND a.workspace_id IN (" + strings.Join(ph, ",") + ")"
	}
	// request_type takes several kinds, comma-separated: a credential request
	// is access or execute.
	if raw := strings.TrimSpace(q.Get("request_type")); raw != "" {
		var ph []string
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				ph = append(ph, "?")
				typeArgs = append(typeArgs, t)
			}
		}
		if len(ph) > 0 {
			typeCond = " AND kr.request_type IN (" + strings.Join(ph, ",") + ")"
		}
	}
	if d := strings.ToUpper(strings.TrimSpace(q.Get("decision"))); d != "" {
		if d == "PENDING" {
			decCond = " AND (kr.decision IS NULL OR kr.decision = 'PENDING')"
		} else {
			decCond = " AND kr.decision = ?"
			decArgs = append(decArgs, d)
		}
	}
	join := func(parts ...[]any) []any {
		var out []any
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	limit, offset := 200, 0
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, 1000)
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		offset = v
	}
	out := instanceKeeperRequestList{Items: []instanceKeeperRequest{}, ByWorkspace: []instanceKeeperWorkspaceCount{}, ByType: map[string]int{}}
	// The join to agents is what places a request in a workspace; a request
	// whose agent is gone has no workspace to show under and is left out.
	from := `FROM keeper_requests kr
		JOIN agents a ON a.id = kr.requesting_agent_id
		JOIN workspaces wsp ON wsp.id = a.workspace_id AND wsp.deleted_at IS NULL
		LEFT JOIN credentials c ON c.id = kr.credential_id`

	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) `+from+` WHERE 1=1`+wsCond+typeCond+decCond,
		join(wsArgs, typeArgs, decArgs)...).Scan(&out.Total); err != nil {
		h.fail(w, "total", err)
		return
	}
	types, err := h.db.QueryContext(ctx, `SELECT COALESCE(kr.request_type, ''), COUNT(*) `+from+` WHERE 1=1`+wsCond+` GROUP BY 1`, wsArgs...)
	if err != nil {
		h.fail(w, "by type", err)
		return
	}
	for types.Next() {
		var t string
		var n int
		if err := types.Scan(&t, &n); err != nil {
			types.Close()
			h.fail(w, "by type scan", err)
			return
		}
		out.ByType[t] = n
	}
	types.Close()

	counts, err := h.db.QueryContext(ctx, `SELECT COALESCE(kr.decision, 'PENDING'), COUNT(*) `+from+` WHERE 1=1`+wsCond+typeCond+` GROUP BY 1`, join(wsArgs, typeArgs)...)
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
		`+from+` WHERE 1=1`+wsCond+typeCond+decCond+`
		ORDER BY kr.created_at DESC, kr.id DESC
		LIMIT ? OFFSET ?`, join(wsArgs, typeArgs, decArgs, []any{limit, offset})...)
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
