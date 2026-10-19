package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// RoutineCalendar derives future occurrences from schedules. Past entries are
// actual runs, never an extrapolation of today's cron into yesterday's history.
func (h *PipelineHandler) RoutineCalendar(w http.ResponseWriter, r *http.Request) {
	start, end, ok := calendarWindow(w, r)
	if !ok {
		return
	}
	resp, status, msg := h.routineCalendarEvents(r.Context(), WorkspaceIDFromContext(r.Context()), start, end)
	if status != http.StatusOK {
		replyError(w, status, msg)
		return
	}
	writeJSON(w, 200, resp)
}

// calendarWindow reads and checks ?from= and ?to=: RFC3339, at most 32 days.
func calendarWindow(w http.ResponseWriter, r *http.Request) (start, end time.Time, ok bool) {
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, 403, "Forbidden")
		return
	}
	start, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	if err != nil {
		replyError(w, 400, "from must be RFC3339")
		return
	}
	end, err = time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err != nil || !end.After(start) || end.Sub(start) > 32*24*time.Hour {
		replyError(w, 400, "calendar interval must be between zero and 32 days")
		return
	}
	return start, end, true
}

// routineCalendarEvents is the calendar of one workspace for [start, end): the
// planned, pending and recorded events with their agents. The status is 200,
// or the HTTP error and message the handler should answer with.
func (h *PipelineHandler) routineCalendarEvents(ctx context.Context, ws string, start, end time.Time) (routineCalendarResponse, int, string) {
	events := make([]routineCalendarEvent, 0)
	truncated := false
	schedules, err := pipeline.NewScheduleStore(h.db).List(ctx, ws)
	if err != nil {
		return routineCalendarResponse{}, 500, "load schedules"
	}
	ids := make([]string, 0, len(schedules))
	for _, s := range schedules {
		ids = append(ids, s.TargetPipelineID)
	}
	projections, err := loadRoutineProjections(ctx, h.db, ws, ids)
	if err != nil {
		h.logger.Error("project calendar schedules", "error", err)
		return routineCalendarResponse{}, 500, "load schedule routines"
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
		p, ok := projections[s.TargetPipelineID]
		if !ok || p.Status == "disabled" || p.Status == "proposed" {
			continue
		}
		inputs, err := planPresetInputs(s.InputsJSON)
		if err != nil {
			return routineCalendarResponse{}, 500, "read schedule inputs"
		}
		// Bound dense schedules per routine as well as the total response.
		occurrences, err := pipeline.NextOccurrences(s.CronExpr, s.Timezone, 1001, from)
		if err != nil {
			return routineCalendarResponse{}, 500, "compute schedule occurrences"
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
	pending, err := h.db.QueryContext(ctx, `SELECT q.id,q.pipeline_slug,COALESCE(p.name,q.pipeline_slug),q.fire_at,q.pinned_version,q.inputs_json
 FROM pending_runs q LEFT JOIN pipelines p ON p.id=q.pipeline_id AND p.workspace_id=q.workspace_id
 WHERE q.workspace_id=? AND q.status='pending' AND julianday(q.fire_at)>=julianday(?) AND julianday(q.fire_at)<julianday(?)
 ORDER BY julianday(q.fire_at),q.id LIMIT 1001`, ws, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return routineCalendarResponse{}, 500, "load pending runs"
	}
	for pending.Next() {
		var id, slug, name, at, inputsJSON string
		var version *int
		if err := pending.Scan(&id, &slug, &name, &at, &version, &inputsJSON); err != nil {
			pending.Close()
			return routineCalendarResponse{}, 500, "read pending runs"
		}
		inputs, err := planPresetInputs(inputsJSON)
		if err != nil {
			pending.Close()
			return routineCalendarResponse{}, 500, "read pending inputs"
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
		return routineCalendarResponse{}, 500, "read pending runs"
	}
	pending.Close()
	rows, err := h.db.QueryContext(ctx, `SELECT r.id,r.pipeline_slug,COALESCE(p.name,r.pipeline_slug),r.started_at,r.status,COALESCE(r.outcome,'') FROM pipeline_runs r LEFT JOIN pipelines p ON p.id=r.pipeline_id WHERE r.workspace_id=? AND julianday(r.started_at)>=julianday(?) AND julianday(r.started_at)<julianday(?) ORDER BY r.started_at LIMIT 1001`, ws, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return routineCalendarResponse{}, 500, "load calendar runs"
	}
	defer rows.Close()
	for rows.Next() {
		var id, slug, name, at, status, outcome string
		if err := rows.Scan(&id, &slug, &name, &at, &status, &outcome); err != nil {
			return routineCalendarResponse{}, 500, "read calendar runs"
		}
		if len(events) >= 1000 {
			truncated = true
			break
		}
		events = append(events, routineCalendarEvent{ID: id, Kind: "run", At: at, Slug: slug, Name: name, Status: status, Outcome: outcome})
	}
	if rows.Err() != nil {
		return routineCalendarResponse{}, 500, "read calendar runs"
	}
	if err := h.attachCalendarAgents(ctx, ws, events); err != nil {
		h.logger.Error("calendar agents", "error", err)
		return routineCalendarResponse{}, 500, "load calendar agents"
	}
	return routineCalendarResponse{Events: events, Truncated: truncated}, http.StatusOK, ""
}

// attachCalendarAgents fills each event's Agents from the agent_run steps of
// its routine's current definition, resolved to the live agents of this
// workspace (agent slugs are unique per workspace). A slug that names no live
// agent is left out: it would be a lane nobody could open.
func (h *PipelineHandler) attachCalendarAgents(ctx context.Context, ws string, events []routineCalendarEvent) error {
	slugs := map[string]struct{}{}
	for _, e := range events {
		if e.Slug != "" {
			slugs[e.Slug] = struct{}{}
		}
	}
	if len(slugs) == 0 {
		return nil
	}
	agentSlugsOf := map[string][]string{}
	wanted := map[string]struct{}{}
	for _, batch := range chunkStrings(keysOf(slugs), 400) {
		rows, err := h.db.QueryContext(ctx,
			`SELECT slug, definition_json FROM pipelines WHERE workspace_id = ? AND deleted_at IS NULL AND slug IN (`+placeholdersFor(len(batch))+`)`,
			append([]any{ws}, anySlice(batch)...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var slug, def string
			if err := rows.Scan(&slug, &def); err != nil {
				rows.Close()
				return err
			}
			dsl, perr := pipeline.Parse([]byte(def))
			if perr != nil {
				continue // an unreadable definition has no lanes; the event still shows
			}
			list := dsl.ExtractManifest().Agents
			agentSlugsOf[slug] = list
			for _, a := range list {
				wanted[a] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if len(wanted) == 0 {
		return nil
	}
	agents := map[string]routineCalendarAgent{}
	for _, batch := range chunkStrings(keysOf(wanted), 400) {
		rows, err := h.db.QueryContext(ctx,
			`SELECT id, slug, name FROM agents WHERE workspace_id = ? AND deleted_at IS NULL AND slug IN (`+placeholdersFor(len(batch))+`)`,
			append([]any{ws}, anySlice(batch)...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a routineCalendarAgent
			if err := rows.Scan(&a.ID, &a.Slug, &a.Name); err != nil {
				rows.Close()
				return err
			}
			agents[a.Slug] = a
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	for i := range events {
		for _, s := range agentSlugsOf[events[i].Slug] {
			if a, ok := agents[s]; ok {
				events[i].Agents = append(events[i].Agents, a)
			}
		}
	}
	return nil
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func chunkStrings(in []string, size int) [][]string {
	var out [][]string
	for len(in) > size {
		out = append(out, in[:size])
		in = in[size:]
	}
	if len(in) > 0 {
		out = append(out, in)
	}
	return out
}

func anySlice(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func placeholdersFor(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
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
		lowerText := strings.ToLower(strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, text)))
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
