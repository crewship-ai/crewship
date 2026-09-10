package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// RoutineCalendar derives future occurrences from schedules. Past entries are
// actual runs, never an extrapolation of today's cron into yesterday's history.
func (h *PipelineHandler) RoutineCalendar(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, 403, "Forbidden")
		return
	}
	start, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	if err != nil {
		replyError(w, 400, "from must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err != nil || !end.After(start) || end.Sub(start) > 32*24*time.Hour {
		replyError(w, 400, "calendar interval must be between zero and 32 days")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	events := make([]routineCalendarEvent, 0)
	truncated := false
	schedules, err := pipeline.NewScheduleStore(h.db).List(r.Context(), ws)
	if err != nil {
		replyError(w, 500, "load schedules")
		return
	}
	now := time.Now()
	from := start.Add(-time.Second)
	if from.Before(now) {
		from = now
	}
	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		p, err := h.store.GetByID(r.Context(), s.TargetPipelineID)
		if err != nil || p.Status == "disabled" || p.Status == "proposed" {
			continue
		}
		inputs, err := planPresetInputs(s.InputsJSON)
		if err != nil {
			replyError(w, 500, "read schedule inputs")
			return
		}
		// Bound dense schedules per routine as well as the total response.
		occurrences, err := pipeline.NextOccurrences(s.CronExpr, s.Timezone, 1001, from)
		if err != nil {
			replyError(w, 500, "compute schedule occurrences")
			return
		}
		for _, at := range occurrences {
			if at.IsZero() || !at.Before(end) {
				break
			}
			if len(events) >= 1000 {
				truncated = true
				break
			}
			events = append(events, routineCalendarEvent{
				ID: s.ID + ":" + at.UTC().Format(time.RFC3339), Kind: "planned",
				At: at.UTC().Format(time.RFC3339), Slug: p.Slug, Name: p.Name,
				Inputs: &inputs, ScheduleID: s.ID, Timezone: s.Timezone, PinnedVersion: s.TargetPipelineVersion,
			})
		}
		if len(occurrences) > 0 && !occurrences[len(occurrences)-1].IsZero() && occurrences[len(occurrences)-1].Before(end) {
			truncated = true
		}
	}
	// Filter before limiting: the generic pending list caps at 200 and falls
	// back to 50 for larger requests, hiding later months in busy workspaces.
	pending, err := h.db.QueryContext(r.Context(), `SELECT q.id,q.pipeline_slug,COALESCE(p.name,q.pipeline_slug),q.fire_at,q.pinned_version,q.inputs_json
 FROM pending_runs q LEFT JOIN pipelines p ON p.id=q.pipeline_id AND p.workspace_id=q.workspace_id
 WHERE q.workspace_id=? AND q.status='pending' AND julianday(q.fire_at)>=julianday(?) AND julianday(q.fire_at)<julianday(?)
 ORDER BY julianday(q.fire_at),q.id LIMIT 1001`, ws, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		replyError(w, 500, "load pending runs")
		return
	}
	for pending.Next() {
		var id, slug, name, at, inputsJSON string
		var version *int
		if err := pending.Scan(&id, &slug, &name, &at, &version, &inputsJSON); err != nil {
			pending.Close()
			replyError(w, 500, "read pending runs")
			return
		}
		inputs, err := planPresetInputs(inputsJSON)
		if err != nil {
			pending.Close()
			replyError(w, 500, "read pending inputs")
			return
		}
		// One budget for the whole response, not one per source: the planned
		// loop above already counts against len(events), and a separate
		// counter here let a busy workspace return a thousand of each.
		if len(events) >= 1000 {
			truncated = true
			break
		}
		events = append(events, routineCalendarEvent{ID: id, Kind: "pending", Inputs: &inputs, At: at, Slug: slug, Name: name, PinnedVersion: version})
	}
	if err := pending.Err(); err != nil {
		pending.Close()
		replyError(w, 500, "read pending runs")
		return
	}
	pending.Close()
	rows, err := h.db.QueryContext(r.Context(), `SELECT r.id,r.pipeline_slug,COALESCE(p.name,r.pipeline_slug),r.started_at,r.status,COALESCE(r.outcome,'') FROM pipeline_runs r LEFT JOIN pipelines p ON p.id=r.pipeline_id WHERE r.workspace_id=? AND julianday(r.started_at)>=julianday(?) AND julianday(r.started_at)<julianday(?) ORDER BY r.started_at LIMIT 1001`, ws, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		replyError(w, 500, "load calendar runs")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, slug, name, at, status, outcome string
		if err := rows.Scan(&id, &slug, &name, &at, &status, &outcome); err != nil {
			replyError(w, 500, "read calendar runs")
			return
		}
		if len(events) >= 1000 {
			truncated = true
			break
		}
		events = append(events, routineCalendarEvent{ID: id, Kind: "run", At: at, Slug: slug, Name: name, Status: status, Outcome: outcome})
	}
	if rows.Err() != nil {
		replyError(w, 500, "read calendar runs")
		return
	}
	writeJSON(w, 200, routineCalendarResponse{Events: events, Truncated: truncated})
}

var planPresetScrubber = scrubber.New()
var planSensitiveKey = regexp.MustCompile(`(?i)password|secret|token|api.?key|authorization|private.?key`)
var planFileKey = regexp.MustCompile(`(?i)(?:^|[_-])(?:files?|attachments?|documents?)(?:$|[_-])`)
var planCamelKey = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// Read-only display projection: preserve safe primitives and field counts,
// never structured contents or recognizable credentials. It cannot be used
// to replay a run. Legacy empty/null means no overrides; malformed JSON errors.
func planPresetInputs(raw string) (map[string]any, error) {
	var inputs map[string]any
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
			return nil, err
		}
	}
	if inputs == nil {
		inputs = map[string]any{}
	}

	for key, value := range inputs {
		record, _ := value.(map[string]any)
		kind, _ := record["type"].(string)
		_, credentialRef := record["credential_ref"]
		_, filename := record["filename"]
		text, isText := value.(string)
		lowerText := strings.ToLower(text)
		marker := ""
		switch {
		case strings.Contains(strings.ToLower(key), "credential") || credentialRef || kind == "credential":
			marker = "credential"
		case planSensitiveKey.MatchString(key) || kind == "redacted":
			marker = "redacted"
		case planFileKey.MatchString(planCamelKey.ReplaceAllString(key, "${1}_${2}")) || kind == "file" || filename || strings.HasPrefix(lowerText, "data:") || strings.HasPrefix(lowerText, "file:") || strings.HasPrefix(lowerText, "blob:"):
			marker = "file"
		case isText && (strings.HasPrefix(lowerText, "credential:") || strings.HasPrefix(lowerText, "vault:")):
			marker = "credential"
		case isText && planPresetScrubber.ContainsSecret(text):
			marker = "redacted"
		}
		if marker != "" {
			inputs[key] = map[string]any{"type": marker}
			continue
		}
		switch value.(type) {
		case map[string]any:
			inputs[key] = map[string]any{}
		case []any:
			inputs[key] = []any{}
		}
	}
	return inputs, nil
}
