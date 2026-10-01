package backupplan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Incident kinds: one open incident per (plan, kind).
const (
	IncidentFailed     = "failed"     // a run failed, was skipped SkipAlertAfter times in a row, or was interrupted without a retry
	IncidentIncomplete = "incomplete" // a run wrote a bundle with recorded gaps
	IncidentStale      = "stale"      // the plan's newest good backup is older than stale_alert_hours
	IncidentOffsite    = "offsite"    // an off-site copy could not be made or verified
	IncidentDrill      = "drill"      // a recorded drill (test restore) failed or was partial, or none is recent enough (the reminder)
)

// DrillReminderPlanID is the plan_id slot of the drill reminder: an
// instance-wide "drill" incident, kept apart from the per-plan drill
// failures (and from plan_id "", which is bundles made without a plan) so
// that recording a drill resolves the reminder without hiding a failure.
// The API speaks it as plan_id null.
const DrillReminderPlanID = "instance:drill-reminder"

// incidentPlan is the API's plan_id for a stored one: nil for none and for
// the drill reminder.
func incidentPlan(planID string) *string {
	if planID == "" || planID == DrillReminderPlanID {
		return nil
	}
	return &planID
}

// Incident is one backup_incidents row (GET …/backups/incidents): the
// console's BackupIncident plus the run it last came from.
type Incident struct {
	ID           string   `json:"id"`
	PlanID       *string  `json:"plan_id"`
	PlanName     *string  `json:"plan_name"`
	Kind         string   `json:"kind"`
	State        string   `json:"state"`
	Count        int      `json:"count"`
	FirstAt      string   `json:"first_at"`
	LastAt       string   `json:"last_at"`
	ResolvedAt   *string  `json:"resolved_at"`
	Message      string   `json:"message"`
	RunID        *string  `json:"run_id"`
	InboxItemIDs []string `json:"-"`
}

const incidentColumns = `id, plan_id, kind, state, count, first_at, last_at, resolved_at, message, run_id, inbox_item_ids`

func scanIncident(s scanner) (*Incident, error) {
	var in Incident
	var plan string
	var resolved, run sql.NullString
	var inbox string
	if err := s.Scan(&in.ID, &plan, &in.Kind, &in.State, &in.Count, &in.FirstAt, &in.LastAt, &resolved, &in.Message, &run, &inbox); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	in.PlanID = incidentPlan(plan)
	in.ResolvedAt, in.RunID = strPtr(resolved), strPtr(run)
	in.InboxItemIDs = decodeStrings(inbox)
	return &in, nil
}

// RaiseIncident opens the (plan, kind) incident, or updates the open one: a
// repeat (bump) counts once more and moves last_at; a condition that simply
// persists (bump=false, e.g. stale on every tick) only refreshes the
// message. opened reports whether the incident is new.
func RaiseIncident(ctx context.Context, db *sql.DB, planID, kind, message, runID string, now time.Time, bump bool) (*Incident, bool, error) {
	cur, err := scanIncident(db.QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM backup_incidents
		WHERE plan_id = ? AND kind = ? AND state = 'open'`, planID, kind))
	switch {
	case errors.Is(err, ErrNotFound):
		in := &Incident{ID: newID("bin_"), Kind: kind, State: "open", Count: 1, FirstAt: ts(now), LastAt: ts(now), Message: message, InboxItemIDs: []string{}}
		in.PlanID = incidentPlan(planID)
		if runID != "" {
			in.RunID = &runID
		}
		res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO backup_incidents (id, plan_id, kind, state, count, first_at, last_at, message, run_id)
			VALUES (?,?,?,'open',1,?,?,?,?)`, in.ID, planID, kind, in.FirstAt, in.LastAt, message, nullStr(runID))
		if err != nil {
			return nil, false, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Another writer opened it between the read and the insert.
			return RaiseIncident(ctx, db, planID, kind, message, runID, now, bump)
		}
		return in, true, nil
	case err != nil:
		return nil, false, err
	}
	if bump {
		cur.Count++
		cur.LastAt = ts(now)
	}
	cur.Message = message
	if runID != "" {
		cur.RunID = &runID
	}
	if _, err := db.ExecContext(ctx, `UPDATE backup_incidents SET count = ?, last_at = ?, message = ?, run_id = ? WHERE id = ?`,
		cur.Count, cur.LastAt, cur.Message, nullStrPtr(cur.RunID), cur.ID); err != nil {
		return nil, false, err
	}
	return cur, false, nil
}

// ResolveIncidents closes the plan's open incidents of the given kinds and
// returns the ones it closed.
func ResolveIncidents(ctx context.Context, db *sql.DB, planID string, kinds []string, now time.Time) ([]*Incident, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	args := []any{planID}
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := db.QueryContext(ctx, `SELECT `+incidentColumns+` FROM backup_incidents WHERE plan_id = ? AND state = 'open'
		AND kind IN (?`+strings.Repeat(",?", len(kinds)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	var open []*Incident
	for rows.Next() {
		in, err := scanIncident(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		open = append(open, in)
	}
	_ = rows.Close()
	for _, in := range open {
		at := ts(now)
		if _, err := db.ExecContext(ctx, `UPDATE backup_incidents SET state = 'resolved', resolved_at = ? WHERE id = ? AND state = 'open'`, at, in.ID); err != nil {
			return nil, err
		}
		in.State, in.ResolvedAt = "resolved", &at
	}
	return open, nil
}

// GetIncident reads one incident by id (ErrNotFound when there is none).
func GetIncident(ctx context.Context, db *sql.DB, id string) (*Incident, error) {
	return scanIncident(db.QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM backup_incidents WHERE id = ?`, id))
}

// SetIncidentInboxItems records the inbox rows delivered for an incident.
func SetIncidentInboxItems(ctx context.Context, db *sql.DB, id string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	_, err := db.ExecContext(ctx, `UPDATE backup_incidents SET inbox_item_ids = ? WHERE id = ?`, mustJSON(ids), id)
	return err
}

// IncidentFilter narrows ListIncidents.
type IncidentFilter struct {
	State string // open | resolved | "" for both
	Limit int
}

// ListIncidents returns incidents, open first, newest first, with the plan
// names filled in.
func ListIncidents(ctx context.Context, db *sql.DB, f IncidentFilter) ([]Incident, error) {
	q := `SELECT ` + incidentColumns + ` FROM backup_incidents`
	var args []any
	if f.State != "" {
		q += ` WHERE state = ?`
		args = append(args, f.State)
	}
	q += ` ORDER BY state = 'open' DESC, last_at DESC, id`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		in, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := map[string]string{}
	if plans, err := ListPlans(ctx, db); err == nil {
		for _, p := range plans {
			names[p.ID] = p.Name
		}
	}
	for i := range out {
		if out[i].PlanID != nil {
			if n, ok := names[*out[i].PlanID]; ok {
				out[i].PlanName = &n
			}
		}
	}
	return out, nil
}

// SkipAlertAfter is how many skipped runs in a row (of one plan, for one
// workspace or the instance) raise the plan's "failed" incident. A single
// skip — one night the server stayed busy — shows in Backup history and the
// nights strip without paging anyone; the second in a row does.
// SkipAlertAfterEnv overrides it (1-30).
const SkipAlertAfter = 2

// SkipAlertAfterEnv names the override of SkipAlertAfter.
const SkipAlertAfterEnv = "CREWSHIP_BACKUP_SKIP_ALERT_AFTER"

func skipAlertAfter() int {
	if raw := strings.TrimSpace(os.Getenv(SkipAlertAfterEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 30 {
			return n
		}
	}
	return SkipAlertAfter
}

// consecutiveSkips counts the newest finished runs of the plan for the same
// workspace ("" for an instance run) that ended skipped, stopping at the
// first that did not. planID "" is runs made without a plan. Runs still in
// progress are passed over.
func consecutiveSkips(ctx context.Context, db *sql.DB, planID, workspaceID string) int {
	q := `SELECT status FROM backup_runs WHERE plan_id = ? AND COALESCE(workspace_id, '') = ? AND status <> 'running'
		ORDER BY COALESCE(ended_at, started_at) DESC, rowid DESC LIMIT 100`
	args := []any{planID, workspaceID}
	if planID == "" {
		q = `SELECT status FROM backup_runs WHERE plan_id IS NULL AND COALESCE(workspace_id, '') = ? AND status <> 'running'
		ORDER BY COALESCE(ended_at, started_at) DESC, rowid DESC LIMIT 100`
		args = []any{workspaceID}
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var status string
		if rows.Scan(&status) != nil || status != StatusSkipped {
			break
		}
		n++
	}
	return n
}

// SkipReason is why a run was skipped, in the words the incident uses:
// "writes did not drain", "not enough space", "backups are held" or
// "workspace busy".
func SkipReason(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "writes did not drain"):
		return "writes did not drain"
	case strings.Contains(m, "not enough disk space"), strings.Contains(m, "not enough space"):
		return "not enough space"
	case strings.Contains(m, "held"):
		return "backups are held"
	}
	return "workspace busy"
}

// lastGoodRun is when the plan's newest done (or incomplete) run ended; nil
// when it never had one. planID "" is runs made without a plan.
func lastGoodRun(ctx context.Context, db *sql.DB, planID string) *time.Time {
	var ended sql.NullString
	q := `SELECT MAX(ended_at) FROM backup_runs WHERE status IN ('done','incomplete') AND plan_id = ?`
	args := []any{planID}
	if planID == "" {
		q, args = `SELECT MAX(ended_at) FROM backup_runs WHERE status IN ('done','incomplete') AND plan_id IS NULL`, nil
	}
	if err := db.QueryRowContext(ctx, q, args...).Scan(&ended); err != nil || !ended.Valid {
		return nil
	}
	return optTime(ended)
}

// Ago says how long ago t was, the way the inbox card reads: "32 hours ago",
// "3 days ago", "never".
func Ago(t *time.Time, now time.Time) string {
	if t == nil {
		return "never"
	}
	d := now.Sub(*t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute ago", "minutes ago")
	case d < 72*time.Hour:
		return plural(int(math.Round(d.Hours())), "hour ago", "hours ago")
	default:
		return plural(int(d.Hours()/24), "day ago", "days ago")
	}
}

// incidentLabel names what the incident is about: the plan, or "Manual".
func incidentLabel(plan *Plan) string {
	if plan == nil {
		return "Manual"
	}
	return plan.Name
}
