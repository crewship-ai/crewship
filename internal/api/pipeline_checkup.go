package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Checkup (ODCH-RT-06): the checks `crewship routine doctor` composes in the
// CLI, plus what only the server knows (failures in a row, the circuit
// breaker, a waiting decision, untested changes), as one answer.
//
//	GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/checkup
//
// Each check is ok, warn or bad. A check the data cannot prove is not listed
// as passed.

type checkupCheck struct {
	Key    string `json:"key"`
	Level  string `json:"level"` // ok | warn | bad
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// Tab is the tab of the routine page that fixes it, when one does.
	Tab string `json:"tab,omitempty"`
}

type checkupResponse struct {
	Slug      string         `json:"slug"`
	CheckedAt string         `json:"checked_at"`
	Passed    int            `json:"passed"`
	Warned    int            `json:"warned"`
	Failed    int            `json:"failed"`
	Total     int            `json:"total"`
	Checks    []checkupCheck `json:"checks"`
}

func (h *PipelineHandler) GetCheckup(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "checkup: load routine", err)
		return
	}
	checks := h.routineCheckup(r.Context(), p)
	// Worst first, then in the order they were made.
	rank := map[string]int{"bad": 0, "warn": 1, "ok": 2}
	sort.SliceStable(checks, func(i, j int) bool { return rank[checks[i].Level] < rank[checks[j].Level] })
	resp := checkupResponse{Slug: p.Slug, CheckedAt: time.Now().UTC().Format(time.RFC3339), Total: len(checks), Checks: checks}
	for _, c := range checks {
		switch c.Level {
		case "ok":
			resp.Passed++
		case "warn":
			resp.Warned++
		default:
			resp.Failed++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// walkStrings calls fn for every string value under the key `key`, anywhere in
// the steps tree (a foreach body included).
func walkDefinitionKey(v any, key string, fn func(string)) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			walkDefinitionKey(e, key, fn)
		}
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && k == key {
				fn(s)
				continue
			}
			walkDefinitionKey(e, key, fn)
		}
	}
}

func (h *PipelineHandler) routineCheckup(ctx context.Context, p *pipeline.Pipeline) []checkupCheck {
	var out []checkupCheck
	add := func(key, level, title, detail, tab string) {
		out = append(out, checkupCheck{Key: key, Level: level, Title: title, Detail: detail, Tab: tab})
	}
	var def map[string]any
	_ = json.Unmarshal([]byte(p.DefinitionJSON), &def)

	// State of the routine itself.
	switch p.Status {
	case "disabled":
		add("off", "bad", "Turned off", "It does not start until an Owner or Admin turns it on.", "settings")
	case "proposed":
		add("proposed", "bad", "Waiting for approval", "A Manager or above approves it before it can run; not the proposer.", "overview")
	default:
		add("runnable", "ok", "Can run", "It is approved and turned on.", "")
	}

	// Agents the steps run as must exist in the author crew.
	slugs := map[string]bool{}
	walkDefinitionKey(def["steps"], "agent_slug", func(s string) {
		if s = strings.TrimSpace(s); s != "" && !strings.Contains(s, "{{") {
			slugs[s] = true
		}
	})
	if len(slugs) > 0 {
		var missing []string
		for s := range slugs {
			var n int
			err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND crew_id = ? AND slug = ? AND deleted_at IS NULL`,
				p.WorkspaceID, p.AuthorCrewID, s).Scan(&n)
			if err == nil && n == 0 {
				missing = append(missing, s)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			add("agents", "bad", "An agent is missing", fmt.Sprintf("The crew has no agent %s.", strings.Join(missing, ", ")), "steps")
		} else {
			add("agents", "ok", "Agents exist and can run", "Every agent step points to an agent in the crew.", "")
		}
	}

	// Credentials the routine declares need an active credential of that type.
	if raw, ok := def["credentials_required"].([]any); ok && len(raw) > 0 {
		var missing []string
		for _, e := range raw {
			m, _ := e.(map[string]any)
			t, _ := m["type"].(string)
			t = strings.ToUpper(strings.TrimSpace(t))
			if t == "" {
				continue
			}
			var n int
			err := h.db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM credentials
				WHERE workspace_id = ? AND deleted_at IS NULL AND status = 'ACTIVE' AND (UPPER(type) = ? OR UPPER(provider) = ?)`,
				p.WorkspaceID, t, t).Scan(&n)
			if err == nil && n == 0 {
				missing = append(missing, t)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			add("credentials", "bad", "A credential is missing", fmt.Sprintf("No active credential of type %s.", strings.Join(missing, ", ")), "steps")
		} else {
			add("credentials", "ok", "Connections and credentials", "Everything the steps declare has an active credential.", "")
		}
	}

	// Cost cap against the author's estimate.
	capUSD, _ := def["max_cost_usd"].(float64)
	est, _ := def["estimated_cost_usd"].(float64)
	switch {
	case capUSD > 0 && est > 0 && capUSD < est*1.5:
		add("cost", "warn", "Cost cap is tight", fmt.Sprintf("The cap $%.2f is under 1.5 times the estimate $%.2f.", capUSD, est), "settings")
	case capUSD <= 0:
		add("cost", "warn", "No cost cap", "A runaway run is not stopped by cost. Set a limit per run.", "settings")
	default:
		add("cost", "ok", "Cost", "The cap leaves room over the estimate.", "")
	}

	// Newest runs: failures in a row, a decision waiting, a missing credential.
	type runRow struct{ status, outcome, errMsg string }
	var runs []runRow
	rows, err := h.db.QueryContext(ctx, `
		SELECT status, COALESCE(outcome,''), COALESCE(error_message,'') FROM pipeline_runs
		WHERE workspace_id = ? AND pipeline_id = ? AND mode = 'run' ORDER BY started_at DESC, id DESC LIMIT 20`, p.WorkspaceID, p.ID)
	if err == nil {
		for rows.Next() {
			var r runRow
			if rows.Scan(&r.status, &r.outcome, &r.errMsg) == nil {
				runs = append(runs, r)
			}
		}
		rows.Close()
	}
	failed := func(r runRow) bool { return r.status == "failed" || r.status == "interrupted" || r.outcome == "FAILED" }
	streak := 0
	for _, r := range runs {
		if r.status == "running" || r.status == "queued" || r.status == "waiting" {
			continue
		}
		if !failed(r) {
			break
		}
		streak++
	}
	if len(runs) > 0 && failed(runs[0]) && strings.Contains(strings.ToLower(runs[0].errMsg), "credential") {
		first := strings.SplitN(runs[0].errMsg, "\n", 2)[0]
		if len(first) > 160 {
			first = first[:160]
		}
		add("missing-credential", "bad", "The last run could not use a credential", first, "steps")
	}
	if streak >= 2 {
		add("failures-in-a-row", "warn", fmt.Sprintf("%d failures in a row", streak), "The newest finished runs all ended without finishing.", "")
	} else if len(runs) > 0 {
		add("failures-in-a-row", "ok", "No failures in a row", "The newest finished run did not fail.", "")
	}
	for _, r := range runs {
		if r.status == "waiting" {
			add("decision-waiting", "warn", "A decision is waiting", "A run is paused until a person decides.", "")
			break
		}
	}

	// The circuit breaker on the routine's schedules.
	srows, err := h.db.QueryContext(ctx, `
		SELECT name, enabled, consecutive_failures, COALESCE(max_consecutive_failures, 5), COALESCE(disabled_reason,'')
		FROM pipeline_schedules WHERE workspace_id = ? AND target_pipeline_id = ? AND deleted_at IS NULL`, p.WorkspaceID, p.ID)
	if err == nil {
		for srows.Next() {
			var name, reason string
			var enabled, fails, max int
			if srows.Scan(&name, &enabled, &fails, &max, &reason) != nil {
				continue
			}
			switch {
			case enabled == 0 && reason == "circuit_breaker":
				add("breaker:"+name, "bad", fmt.Sprintf("Turned off after %d failures", fails), fmt.Sprintf("The schedule %q stopped itself; turn it back on once the cause is fixed.", name), "triggers")
			case fails > 0 && enabled == 1:
				add("breaker:"+name, "warn", fmt.Sprintf("%d of %d failures before the schedule turns itself off", fails, max), fmt.Sprintf("The schedule %q.", name), "triggers")
			}
		}
		srows.Close()
	}

	// Changes that are not live and have no passing test (ODCH-RT-13).
	var draftDoc string
	if err := h.db.QueryRowContext(ctx, `SELECT document_json FROM pipeline_drafts WHERE workspace_id = ? AND slug = ?`, p.WorkspaceID, p.Slug).Scan(&draftDoc); err == nil {
		var doc struct {
			Definition json.RawMessage `json:"definition"`
		}
		hash := ""
		if json.Unmarshal([]byte(draftDoc), &doc) == nil && len(doc.Definition) > 0 {
			hash = definitionHashHex(doc.Definition)
		}
		tested := map[string]*pipeline.TestResult{}
		if hash != "" {
			tested, _ = h.store.TestResults(ctx, p.WorkspaceID, []string{hash})
		}
		switch t := tested[hash]; {
		case t == nil:
			add("untested-changes", "warn", "Changes not tested", "The changes are not live and have no test run yet.", "versions")
		case !t.Passed:
			add("untested-changes", "bad", "Changes failed their test", "The last test run of the changes did not pass.", "versions")
		default:
			add("untested-changes", "ok", "Changes tested", "The changes passed a test run and can be published.", "")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		h.logger.Warn("checkup: draft lookup", "error", err)
	}
	return out
}
