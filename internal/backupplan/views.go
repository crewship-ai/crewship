package backupplan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// IncompleteView is one gap as the console reads it.
type IncompleteView struct {
	Kind        string  `json:"kind"`
	Count       int     `json:"count"`
	WorkspaceID *string `json:"workspace_id"`
	Detail      string  `json:"detail"`
}

// RunView is one row of GET /api/v1/admin/instance/backups/runs — the
// console's BackupRun, with the bundle's catalog facts joined in, plus a few
// fields the console does not read yet (phase, environments, due_at,
// retry_of, categories, catalog_id).
type RunView struct {
	ID            string           `json:"id"`
	PlanID        *string          `json:"plan_id"`
	PlanName      *string          `json:"plan_name"`
	Trigger       string           `json:"trigger"`
	Note          *string          `json:"note"`
	Scope         string           `json:"scope"`
	WorkspaceID   *string          `json:"workspace_id"`
	WorkspaceName *string          `json:"workspace_name"`
	Kind          string           `json:"kind"`
	Status        string           `json:"status"`
	RetriedAt     *string          `json:"retried_at"`
	Phases        []Phase          `json:"phases"`
	BundlePath    *string          `json:"bundle_path"`
	SizeBytes     *int64           `json:"size_bytes"`
	Error         *string          `json:"error"`
	Incomplete    []IncompleteView `json:"incomplete"`
	StartedAt     string           `json:"started_at"`
	EndedAt       *string          `json:"ended_at"`
	ProofLevel    int              `json:"proof_level"`
	DrillResult   *string          `json:"drill_result"`
	DrillAt       *string          `json:"drill_at"`
	DrillNote     *string          `json:"drill_note"`
	Pinned        bool             `json:"pinned"`
	Recipients    []string         `json:"recipients"`
	FormatVersion *int             `json:"format_version"`
	Restorable    *string          `json:"restorable"`
	Legacy        bool             `json:"legacy"`

	Phase        string   `json:"phase"`
	Environments bool     `json:"environments"`
	DueAt        *string  `json:"due_at"`
	RetryOf      *string  `json:"retry_of"`
	Categories   []string `json:"categories"`
	CatalogID    *string  `json:"catalog_id"`
	// HoldMs is how long writes were held for the consistent copy (the
	// quiet window of an instance run); null when nothing was held.
	HoldMs *int64 `json:"hold_ms"`
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optTS(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

// Restorability of a bundle format on this server.
func restorable(format int) *string {
	var s string
	switch {
	case format <= 0:
		return nil
	case format > backup.FormatVersion:
		s = "unsupported"
	case format >= backup.MinSupportedFormatVersion:
		s = "direct"
	default:
		s = "converter"
	}
	return &s
}

// drillNote pulls a one-line note out of a drill report, when it has one.
func drillNote(raw json.RawMessage) *string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	for _, k := range []string{"note", "summary"} {
		if s, ok := m[k].(string); ok && s != "" {
			return &s
		}
	}
	return nil
}

type lookups struct {
	planNames map[string]string
	wsNames   map[string]string
	catalog   map[string]backup.CatalogEntry
	names     func([]string) []string
}

func loadLookups(ctx context.Context, db *sql.DB, rr RecipientResolver) (*lookups, error) {
	l := &lookups{planNames: map[string]string{}, wsNames: map[string]string{}, catalog: map[string]backup.CatalogEntry{}}
	plans, err := ListPlans(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, p := range plans {
		l.planNames[p.ID] = p.Name
	}
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM workspaces`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		l.wsNames[id] = name
	}
	_ = rows.Close()
	cat, err := backup.ListCatalog(ctx, db, "")
	if err != nil {
		return nil, err
	}
	for _, e := range cat {
		l.catalog[e.FilePath] = e
	}
	cache := map[string]string{}
	l.names = func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			if n, ok := cache[id]; ok {
				out = append(out, n)
				continue
			}
			n := shortKey(id)
			if rr != nil {
				if _, names, err := rr.Resolve(ctx, []string{id}); err == nil && len(names) == 1 {
					n = names[0]
				}
			}
			cache[id] = n
			out = append(out, n)
		}
		return out
	}
	return l, nil
}

func (l *lookups) view(r *Run) RunView {
	v := RunView{
		ID: r.ID, PlanID: optStr(r.PlanID), Trigger: r.Trigger, Note: optStr(r.Note), Scope: r.Scope,
		WorkspaceID: optStr(r.WorkspaceID), Kind: r.Kind, Status: r.Status, RetriedAt: optTS(r.RetriedAt),
		Phases: r.Phases, BundlePath: optStr(r.BundlePath), SizeBytes: r.Size, Error: optStr(r.Error),
		Incomplete: []IncompleteView{}, StartedAt: ts(r.StartedAt), EndedAt: optTS(r.EndedAt),
		Recipients: l.names(r.Recipients), Phase: r.Phase, Environments: r.Environments, DueAt: optTS(r.DueAt),
		RetryOf: optStr(r.RetryOf), Categories: r.Categories, CatalogID: optStr(r.CatalogID), HoldMs: r.HoldMs,
	}
	if v.Phases == nil {
		v.Phases = []Phase{}
	}
	if r.PlanID != "" {
		if n, ok := l.planNames[r.PlanID]; ok {
			v.PlanName = &n
		}
	}
	if r.WorkspaceID != "" {
		if n, ok := l.wsNames[r.WorkspaceID]; ok {
			v.WorkspaceName = &n
		}
	}
	for _, it := range r.Incomplete {
		v.Incomplete = append(v.Incomplete, IncompleteView{Kind: it.Kind, Count: it.Count, WorkspaceID: optStr(it.Workspace), Detail: it.Detail})
	}
	if e, ok := l.catalog[r.BundlePath]; ok && r.BundlePath != "" {
		v.ProofLevel, v.Pinned = e.ProofLevel, e.Pinned
		v.DrillResult, v.DrillAt, v.DrillNote = optStr(e.DrillResult), optTS(e.DrillAt), drillNote(e.DrillReport)
		fv := e.FormatVersion
		v.FormatVersion, v.Restorable = &fv, restorable(fv)
	}
	return v
}

// GetRunView returns one run as the console reads it — the same shape as an
// item of ListRunViews. ErrNotFound when there is no such run.
func GetRunView(ctx context.Context, db *sql.DB, rr RecipientResolver, id string) (*RunView, error) {
	r, err := GetRun(ctx, db, id)
	if err != nil {
		return nil, err
	}
	l, err := loadLookups(ctx, db, rr)
	if err != nil {
		return nil, err
	}
	v := l.view(r)
	return &v, nil
}

// ListRunViews returns the runs as the console reads them, newest first.
func ListRunViews(ctx context.Context, db *sql.DB, rr RecipientResolver, f RunFilter) ([]RunView, error) {
	runs, err := ListRuns(ctx, db, f)
	if err != nil {
		return nil, err
	}
	l, err := loadLookups(ctx, db, rr)
	if err != nil {
		return nil, err
	}
	out := make([]RunView, 0, len(runs))
	for _, r := range runs {
		out = append(out, l.view(r))
	}
	return out, nil
}

// ─── Next runs and the calendar ─────────────────────────────────────────────

// NextRun is one entry of GET …/plans/{id}/next.
type NextRun struct {
	At           string `json:"at"`
	Environments bool   `json:"environments"`
}

// NextRuns is the plan's next n due times after now (none when disabled).
func NextRuns(p *Plan, now time.Time, n int) []NextRun {
	out := []NextRun{}
	if !p.Enabled {
		return out
	}
	for _, o := range NextOccurrences(p, now, n) {
		out = append(out, NextRun{At: ts(o.At), Environments: o.Environments})
	}
	return out
}

// CalendarEntry is one mark on a calendar day.
type CalendarEntry struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`   // data | environments
	Status string `json:"status"` // done | skipped | catchup | failed | planned
}

// CalendarDay is one date in the plan's timezone.
type CalendarDay struct {
	Date    string          `json:"date"`
	Entries []CalendarEntry `json:"entries"`
}

// MaxCalendarDays bounds a calendar request.
const MaxCalendarDays = 400

// runCalendarStatus: done (catchup when it was a catch-up), skipped, failed.
// A run still in progress shows nothing yet.
func runCalendarStatus(r *Run) string {
	switch r.Status {
	case StatusDone, StatusIncomplete:
		if r.Trigger == TriggerCatchup {
			return "catchup"
		}
		return "done"
	case StatusSkipped:
		return "skipped"
	case StatusFailed, StatusInterrupted:
		if r.RetriedAt != nil {
			return "" // its retry speaks for it
		}
		return "failed"
	}
	return ""
}

// envCalendarStatus: the run's environments phase — done, failed (the phase
// failed or some crew's environment was not captured), else the run's own
// status (a skipped or failed run took no environments either); a phase
// that never finished is skipped.
func envCalendarStatus(r *Run, runStatus string) string {
	if runStatus == "failed" || runStatus == "skipped" {
		return runStatus
	}
	if i := phaseIndex(r, PhaseEnvironments); i >= 0 {
		switch r.Phases[i].Status {
		case "done":
			return runStatus
		case "failed":
			return "failed"
		}
	}
	// No environments phase that finished: nothing says they were taken.
	return "skipped"
}

var calendarRank = map[string]int{"failed": 4, "skipped": 3, "catchup": 2, "done": 1, "planned": 0}

// Calendar lays the plan's runs (from backup_runs) and its planned due times
// (from the schedule, after now) over the dates from..to (YYYY-MM-DD in the
// plan's timezone). Runs of one due time over several workspaces fold into
// one mark with the worst status. An environments mark follows the run's
// environments phase.
func Calendar(ctx context.Context, db *sql.DB, p *Plan, from, to string, now time.Time) ([]CalendarDay, error) {
	loc := p.Location()
	start, err := time.ParseInLocation("2006-01-02", from, loc)
	if err != nil {
		return nil, invalid("from must be YYYY-MM-DD")
	}
	endDay, err := time.ParseInLocation("2006-01-02", to, loc)
	if err != nil {
		return nil, invalid("to must be YYYY-MM-DD")
	}
	if endDay.Before(start) {
		return nil, invalid("to is before from")
	}
	end := endDay.AddDate(0, 0, 1)
	if end.Sub(start) > MaxCalendarDays*24*time.Hour {
		return nil, invalid("a calendar spans at most %d days", MaxCalendarDays)
	}
	since := start.Add(-24 * time.Hour)
	runs, err := ListRuns(ctx, db, RunFilter{PlanID: p.ID, Since: &since})
	if err != nil {
		return nil, err
	}
	type key struct {
		at   time.Time
		kind string
	}
	marks := map[key]string{}
	put := func(at time.Time, kind, status string) {
		if at.Before(start) || !at.Before(end) || status == "" {
			return
		}
		k := key{at.UTC(), kind}
		if cur, ok := marks[k]; !ok || calendarRank[status] > calendarRank[cur] {
			marks[k] = status
		}
	}
	for _, r := range runs {
		at := r.StartedAt
		if r.DueAt != nil {
			at = *r.DueAt
		}
		status := runCalendarStatus(r)
		put(at, "data", status)
		if r.Environments && status != "" {
			put(at, "environments", envCalendarStatus(r, status))
		}
	}
	if p.Enabled {
		from := now
		if start.After(from) {
			from = start
		}
		for _, o := range NextOccurrences(p, from, 5000) {
			if !o.At.Before(end) {
				break
			}
			if _, done := marks[key{o.At, "data"}]; !done {
				put(o.At, "data", "planned")
				if o.Environments {
					put(o.At, "environments", "planned")
				}
			}
		}
	}
	byDate := map[string][]CalendarEntry{}
	for k, status := range marks {
		d := k.at.In(loc).Format("2006-01-02")
		byDate[d] = append(byDate[d], CalendarEntry{At: ts(k.at), Kind: k.kind, Status: status})
	}
	var days []CalendarDay
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		entries := byDate[date]
		if entries == nil {
			entries = []CalendarEntry{}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].At != entries[j].At {
				return entries[i].At < entries[j].At
			}
			return entries[i].Kind < entries[j].Kind
		})
		days = append(days, CalendarDay{Date: date, Entries: entries})
	}
	if days == nil {
		days = []CalendarDay{}
	}
	return days, nil
}

// PreviewContents is POST …/plans/preview-contents.
func PreviewContents(preset string, contents []string, envMode string) (backup.ContentsResolution, error) {
	switch preset {
	case backup.PresetComplete, backup.PresetWorkspace, backup.PresetCustom:
	default:
		return backup.ContentsResolution{}, invalid("preset must be complete, workspace or custom")
	}
	if envMode == "" {
		envMode = backup.EnvModeFiles
	}
	if envMode != backup.EnvModeFiles && envMode != backup.EnvModeComplete {
		return backup.ContentsResolution{}, invalid("env_mode must be files or complete")
	}
	res, err := backup.ResolveContents(preset, contents, envMode)
	if err != nil {
		return res, invalid("%v", err)
	}
	return res, nil
}

func describeKeep(p *Plan) string {
	s := fmt.Sprintf("keep %d checked", p.KeepMin)
	if p.KeepDaily > 0 {
		s += fmt.Sprintf(", %d daily", p.KeepDaily)
	}
	if p.KeepWeekly > 0 {
		s += fmt.Sprintf(", %d weekly", p.KeepWeekly)
	}
	if p.KeepMonthly > 0 {
		s += fmt.Sprintf(", %d monthly", p.KeepMonthly)
	}
	return s
}

var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// describeWhen is the console's describePlanWhen, in Go.
func describeWhen(p *Plan) string {
	var when string
	switch p.Cadence {
	case CadenceDaily:
		when = "daily " + p.TimeOfDay
	case CadenceWeekly:
		wd := 0
		if p.Weekday != nil {
			wd = *p.Weekday
		}
		when = fmt.Sprintf("weekly on %s %s", weekdayNames[wd], p.TimeOfDay)
	case CadenceMonthly:
		if p.Monthday != nil {
			when = fmt.Sprintf("monthly on day %d %s", *p.Monthday, p.TimeOfDay)
		} else {
			when = "monthly, first Sunday " + p.TimeOfDay
		}
	default:
		if p.CronExpr != nil {
			when = "cron " + *p.CronExpr
		}
	}
	if p.EnvMode != backup.EnvModeComplete {
		return when
	}
	env := p.EnvCadence
	if env == EnvEvery {
		env = "with every run"
	}
	return "data " + when + " · environments " + env
}
