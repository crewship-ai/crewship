package backupplan

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// ErrNotFound is returned for an unknown plan or run id.
var ErrNotFound = errors.New("backupplan: not found")

// ts is how every time is stored: UTC RFC 3339 with nanoseconds, so string
// order is time order.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

func tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

func decodeStrings(raw string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

const planColumns = `id, name, preset, scope, workspace_ids, contents, env_mode, cadence, time_of_day, weekday, monthday,
	cron_expr, timezone, env_cadence, keep_min, keep_daily, keep_weekly, keep_monthly, destinations, recipient_ids,
	busy_wait_minutes, busy_retry_minutes, hold_cap_minutes, stale_alert_hours, enabled, next_run_at, last_run_at,
	created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanPlan(s scanner) (*Plan, error) {
	var p Plan
	var wsIDs, contents, dest, rids string
	var weekday, monthday sql.NullInt64
	var cronExpr, next, last sql.NullString
	var enabled int
	if err := s.Scan(&p.ID, &p.Name, &p.Preset, &p.Scope, &wsIDs, &contents, &p.EnvMode, &p.Cadence, &p.TimeOfDay,
		&weekday, &monthday, &cronExpr, &p.Timezone, &p.EnvCadence, &p.KeepMin, &p.KeepDaily, &p.KeepWeekly,
		&p.KeepMonthly, &dest, &rids, &p.BusyWait, &p.BusyRetry, &p.HoldCap, &p.StaleAlert, &enabled, &next, &last,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.WorkspaceIDs, p.Contents, p.Destinations, p.RecipientIDs = decodeStrings(wsIDs), decodeStrings(contents), decodeStrings(dest), decodeStrings(rids)
	if weekday.Valid {
		p.Weekday = intPtr(int(weekday.Int64))
	}
	if monthday.Valid {
		p.Monthday = intPtr(int(monthday.Int64))
	}
	p.CronExpr, p.NextRunAt, p.LastRunAt = strPtr(cronExpr), strPtr(next), strPtr(last)
	p.Enabled = enabled == 1
	p.CountsAsProtection = p.Preset != backup.PresetCustom
	return &p, nil
}

// ListPlans returns every plan, by name.
func ListPlans(ctx context.Context, db *sql.DB) ([]*Plan, error) {
	return listPlans(ctx, db)
}

func listPlans(ctx context.Context, db queryer) ([]*Plan, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+planColumns+` FROM backup_plans ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPlan returns one plan or ErrNotFound.
func GetPlan(ctx context.Context, db *sql.DB, id string) (*Plan, error) {
	return scanPlan(db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM backup_plans WHERE id = ?`, id))
}

// queryer allows reads through either the pool or an already-owned transaction.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// queryExecer keeps deletion checks on the executor that owns the writes.
type queryExecer interface {
	queryer
	execer
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullStrPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// InsertPlan writes a new, normalized plan. p.ID is set when empty.
func InsertPlan(ctx context.Context, ex execer, p *Plan, actor string, now time.Time) error {
	if p.ID == "" {
		p.ID = newID("bp_")
	}
	p.CreatedAt, p.UpdatedAt = ts(now), ts(now)
	_, err := ex.ExecContext(ctx, `INSERT INTO backup_plans (`+planColumns+`, created_by, updated_by)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Preset, p.Scope, mustJSON(p.WorkspaceIDs), mustJSON(p.Contents), p.EnvMode, p.Cadence, p.TimeOfDay,
		nullInt(p.Weekday), nullInt(p.Monthday), nullStrPtr(p.CronExpr), p.Timezone, p.EnvCadence, p.KeepMin, p.KeepDaily,
		p.KeepWeekly, p.KeepMonthly, mustJSON(p.Destinations), mustJSON(p.RecipientIDs), p.BusyWait, p.BusyRetry,
		p.HoldCap, p.StaleAlert, boolInt(p.Enabled), nullStrPtr(p.NextRunAt), nullStrPtr(p.LastRunAt),
		p.CreatedAt, p.UpdatedAt, nullStr(actor), nullStr(actor))
	return err
}

// UpdatePlan rewrites every editable column of an existing plan.
func UpdatePlan(ctx context.Context, ex execer, p *Plan, actor string, now time.Time) error {
	p.UpdatedAt = ts(now)
	res, err := ex.ExecContext(ctx, `UPDATE backup_plans SET name=?, preset=?, scope=?, workspace_ids=?, contents=?, env_mode=?,
		cadence=?, time_of_day=?, weekday=?, monthday=?, cron_expr=?, timezone=?, env_cadence=?, keep_min=?, keep_daily=?,
		keep_weekly=?, keep_monthly=?, destinations=?, recipient_ids=?, busy_wait_minutes=?, busy_retry_minutes=?,
		hold_cap_minutes=?, stale_alert_hours=?, enabled=?, next_run_at=?, updated_at=?, updated_by=? WHERE id=?`,
		p.Name, p.Preset, p.Scope, mustJSON(p.WorkspaceIDs), mustJSON(p.Contents), p.EnvMode, p.Cadence, p.TimeOfDay,
		nullInt(p.Weekday), nullInt(p.Monthday), nullStrPtr(p.CronExpr), p.Timezone, p.EnvCadence, p.KeepMin, p.KeepDaily,
		p.KeepWeekly, p.KeepMonthly, mustJSON(p.Destinations), mustJSON(p.RecipientIDs), p.BusyWait, p.BusyRetry,
		p.HoldCap, p.StaleAlert, boolInt(p.Enabled), nullStrPtr(p.NextRunAt), p.UpdatedAt, nullStr(actor), p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlan removes a plan. Its runs stay, as history (plan_name is then
// read as unknown).
func DeletePlan(ctx context.Context, ex execer, id string) error {
	res, err := ex.ExecContext(ctx, `DELETE FROM backup_plans WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetNextRun recomputes and stores next_run_at from `after` (disabled plans
// get NULL).
func SetNextRun(p *Plan, after time.Time) {
	p.NextRunAt = nil
	if !p.Enabled {
		return
	}
	if next := NextOccurrences(p, after, 1); len(next) == 1 {
		s := ts(next[0].At)
		p.NextRunAt = &s
	}
}

// ─── Runs ───────────────────────────────────────────────────────────────────

// Run statuses.
const (
	StatusRunning     = "running"
	StatusDone        = "done"
	StatusIncomplete  = "incomplete"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
	StatusSkipped     = "skipped"
)

// Triggers.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerCatchup  = "catchup"
	TriggerDrill    = "drill"
)

// Phases a running run can be in. The first two are "not started".
const (
	PhaseQueued   = "queued"
	PhaseBusyWait = "busy_wait"
	PhaseStarting = "starting"
	PhaseCopy     = "copy"
	PhasePack     = "pack"
	PhaseEncrypt  = "encrypt"
	PhaseCheck    = "check"
	PhaseOffsite  = "off-site"
	PhaseWait     = "wait"
	// PhaseEnvironments is present only on runs that take complete
	// container environments.
	PhaseEnvironments = "environments"
)

// Phase is one persisted step of a run.
type Phase struct {
	Name      string  `json:"name"`
	StartedAt *string `json:"started_at"`
	EndedAt   *string `json:"ended_at"`
	Status    string  `json:"status"` // pending | running | done | failed | skipped
	Detail    *string `json:"detail"`
}

// Run is one backup_runs row.
type Run struct {
	ID            string
	PlanID        string
	Trigger       string
	Note          string
	Scope         string
	WorkspaceID   string
	Kind          string
	Categories    []string
	Environments  bool
	Status        string
	Phase         string
	Phases        []Phase
	DueAt         *time.Time
	NextAttemptAt *time.Time
	RetryOf       string
	RetriedAt     *time.Time
	BundlePath    string
	CatalogID     string
	Size          *int64
	Error         string
	Incomplete    []backup.IncompleteItem
	Recipients    []string
	ActorUserID   string
	StartedAt     time.Time
	EndedAt       *time.Time
	HoldMs        *int64
}

const runColumns = `id, COALESCE(plan_id,''), trigger, COALESCE(note,''), scope, COALESCE(workspace_id,''), kind, categories,
	environments, status, phase, phases, due_at, next_attempt_at, COALESCE(retry_of,''), retried_at,
	COALESCE(bundle_path,''), COALESCE(catalog_id,''), size, COALESCE(error,''), incomplete, recipients,
	COALESCE(actor_user_id,''), started_at, ended_at, hold_ms`

func optTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := parseTS(ns.String)
	if err != nil {
		return nil
	}
	return &t
}

func scanRun(s scanner) (*Run, error) {
	var r Run
	var cats, phases, incomplete, recipients, started string
	var env int
	var due, nextAttempt, retried, ended sql.NullString
	var size, hold sql.NullInt64
	if err := s.Scan(&r.ID, &r.PlanID, &r.Trigger, &r.Note, &r.Scope, &r.WorkspaceID, &r.Kind, &cats, &env, &r.Status,
		&r.Phase, &phases, &due, &nextAttempt, &r.RetryOf, &retried, &r.BundlePath, &r.CatalogID, &size, &r.Error,
		&incomplete, &recipients, &r.ActorUserID, &started, &ended, &hold); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Categories, r.Recipients = decodeStrings(cats), decodeStrings(recipients)
	r.Environments = env == 1
	r.Phases = []Phase{}
	_ = json.Unmarshal([]byte(phases), &r.Phases)
	r.Incomplete = []backup.IncompleteItem{}
	_ = json.Unmarshal([]byte(incomplete), &r.Incomplete)
	r.DueAt, r.NextAttemptAt, r.RetriedAt, r.EndedAt = optTime(due), optTime(nextAttempt), optTime(retried), optTime(ended)
	if size.Valid {
		r.Size = &size.Int64
	}
	if hold.Valid {
		r.HoldMs = &hold.Int64
	}
	t, err := parseTS(started)
	if err != nil {
		return nil, fmt.Errorf("backupplan: run %s started_at %q: %w", r.ID, started, err)
	}
	r.StartedAt = t
	return &r, nil
}

// insertRun writes a new run. With a plan and a due time it is a claim:
// the unique index refuses a second row for the same (plan, due, workspace)
// and insertRun then reports claimed=false.
func insertRun(ctx context.Context, ex execer, r *Run) (claimed bool, err error) {
	if r.ID == "" {
		r.ID = newID("br_")
	}
	if r.Phases == nil {
		r.Phases = []Phase{}
	}
	if r.Incomplete == nil {
		r.Incomplete = []backup.IncompleteItem{}
	}
	if r.Categories == nil {
		r.Categories = []string{}
	}
	if r.Recipients == nil {
		r.Recipients = []string{}
	}
	if r.Kind == "" {
		r.Kind = backup.KindFull
	}
	res, err := ex.ExecContext(ctx, `INSERT OR IGNORE INTO backup_runs (id, plan_id, trigger, note, scope, workspace_id, kind,
		categories, environments, status, phase, phases, due_at, next_attempt_at, retry_of, error, incomplete, recipients,
		actor_user_id, started_at, ended_at, bundle_path, catalog_id, size)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, nullStr(r.PlanID), r.Trigger, nullStr(r.Note), r.Scope, nullStr(r.WorkspaceID), r.Kind,
		mustJSON(r.Categories), boolInt(r.Environments), r.Status, r.Phase, mustJSON(r.Phases), tsPtr(r.DueAt),
		tsPtr(r.NextAttemptAt), nullStr(r.RetryOf), nullStr(r.Error), mustJSON(r.Incomplete), mustJSON(r.Recipients),
		nullStr(r.ActorUserID), ts(r.StartedAt), tsPtr(r.EndedAt), nullStr(r.BundlePath), nullStr(r.CatalogID), nullInt64(r.Size))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GetRun returns one run or ErrNotFound.
func GetRun(ctx context.Context, db *sql.DB, id string) (*Run, error) {
	return scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM backup_runs WHERE id = ?`, id))
}

// RunFilter narrows ListRuns.
type RunFilter struct {
	PlanID string
	// Scope instance lists instance-scope runs; workspaces lists runs of
	// workspaces (narrowed by WorkspaceIDs when set). Empty lists all.
	Scope        string
	WorkspaceIDs []string
	Limit        int
	// Since, when set, lists runs started at or after it.
	Since *time.Time
}

// ListRuns returns runs newest first.
func ListRuns(ctx context.Context, db *sql.DB, f RunFilter) ([]*Run, error) {
	var where []string
	var args []any
	if f.PlanID != "" {
		where, args = append(where, "plan_id = ?"), append(args, f.PlanID)
	}
	if f.Scope != "" {
		where, args = append(where, "scope = ?"), append(args, f.Scope)
	}
	if len(f.WorkspaceIDs) > 0 {
		where = append(where, "workspace_id IN (?"+strings.Repeat(",?", len(f.WorkspaceIDs)-1)+")")
		for _, id := range f.WorkspaceIDs {
			args = append(args, id)
		}
	}
	if f.Since != nil {
		where, args = append(where, "started_at >= ?"), append(args, ts(*f.Since))
	}
	q := `SELECT ` + runColumns + ` FROM backup_runs`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY started_at DESC, id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// saveProgress persists the run's phase list and current phase.
func saveProgress(ctx context.Context, ex execer, r *Run) error {
	_, err := ex.ExecContext(ctx, `UPDATE backup_runs SET phase = ?, phases = ?, hold_ms = ? WHERE id = ?`,
		r.Phase, mustJSON(r.Phases), nullInt64(r.HoldMs), r.ID)
	return err
}

func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// finishRun writes a run's terminal state.
func finishRun(ctx context.Context, ex execer, r *Run) error {
	_, err := ex.ExecContext(ctx, `UPDATE backup_runs SET status = ?, phase = '', phases = ?, bundle_path = ?, catalog_id = ?,
		size = ?, error = ?, incomplete = ?, ended_at = ?, hold_ms = ?, next_attempt_at = NULL WHERE id = ?`,
		r.Status, mustJSON(r.Phases), nullStr(r.BundlePath), nullStr(r.CatalogID), nullInt64(r.Size), nullStr(r.Error),
		mustJSON(r.Incomplete), tsPtr(r.EndedAt), nullInt64(r.HoldMs), r.ID)
	return err
}
