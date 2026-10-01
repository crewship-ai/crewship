// Package backupplan is the backend service behind Admin › Backups:
// instance-level backup plans, the scheduler that runs them in their
// timezone (busy window, one catch-up after downtime, bounded concurrency),
// the runs it records with their phases, and the overview built from plans,
// runs and the bundle catalog.
//
// Backups run here, in a server goroutine — never through an agent or a
// model. The bundle itself is written by internal/backup through an
// Executor, so the instance-scope creator (Track B) plugs in without this
// package changing.
package backupplan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/robfig/cron/v3"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Scopes.
const (
	ScopeInstance   = "instance"
	ScopeWorkspaces = "workspaces"
)

// Cadences.
const (
	CadenceDaily   = "daily"
	CadenceWeekly  = "weekly"
	CadenceMonthly = "monthly"
	CadenceCustom  = "custom"
)

// Environment cadences: which data runs also take complete environments.
const (
	EnvEvery   = "every"
	EnvWeekly  = "weekly"
	EnvMonthly = "monthly"
)

// Plan is one backup plan (GET/POST/PUT /api/v1/admin/instance/backups/plans).
// The JSON is the console's BackupPlan, plus counts_as_protection.
type Plan struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Preset       string   `json:"preset"`
	Scope        string   `json:"scope"`
	WorkspaceIDs []string `json:"workspace_ids"`
	Contents     []string `json:"contents"`
	EnvMode      string   `json:"env_mode"`
	Cadence      string   `json:"cadence"`
	TimeOfDay    string   `json:"time_of_day"`
	Weekday      *int     `json:"weekday"`
	Monthday     *int     `json:"monthday"`
	CronExpr     *string  `json:"cron_expr"`
	Timezone     string   `json:"timezone"`
	EnvCadence   string   `json:"env_cadence"`
	KeepMin      int      `json:"keep_min"`
	KeepDaily    int      `json:"keep_daily"`
	KeepWeekly   int      `json:"keep_weekly"`
	KeepMonthly  int      `json:"keep_monthly"`
	Destinations []string `json:"destinations"`
	RecipientIDs []string `json:"recipient_ids"`
	BusyWait     int      `json:"busy_wait_minutes"`
	BusyRetry    int      `json:"busy_retry_minutes"`
	HoldCap      int      `json:"hold_cap_minutes"`
	StaleAlert   int      `json:"stale_alert_hours"`
	Enabled      bool     `json:"enabled"`
	NextRunAt    *string  `json:"next_run_at"`
	LastRunAt    *string  `json:"last_run_at"`
	// CountsAsProtection is false for custom plans: a bundle that carries
	// only some categories never counts as protecting a workspace.
	CountsAsProtection bool   `json:"counts_as_protection"`
	CreatedAt          string `json:"created_at,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
}

// Policy is the plan's keep rules as a retention policy.
func (p *Plan) Policy() backup.RetentionPolicy {
	return backup.RetentionPolicy{KeepMin: p.KeepMin, Daily: p.KeepDaily, Weekly: p.KeepWeekly, Monthly: p.KeepMonthly}
}

// Location is the plan's timezone (validated on save).
func (p *Plan) Location() *time.Location {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Resolution is what the plan's bundles carry.
func (p *Plan) Resolution() (backup.ContentsResolution, error) {
	return backup.ResolveContents(p.Preset, p.Contents, p.EnvMode)
}

func intPtr(n int) *int { return &n }

// Defaults returns a new plan of the preset with the design's defaults:
// complete recovery = the whole instance, data daily 03:00 and complete
// environments weekly; a workspace backup = every category of the selected
// workspaces, daily; custom = daily, no environments, the categories chosen.
func Defaults(preset string) Plan {
	p := Plan{
		Preset: preset, Scope: ScopeWorkspaces, WorkspaceIDs: []string{}, Contents: []string{},
		EnvMode: backup.EnvModeFiles, Cadence: CadenceDaily, TimeOfDay: "03:00", Timezone: "UTC",
		EnvCadence: EnvWeekly, KeepMin: 3, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12,
		Destinations: []string{"local"}, RecipientIDs: []string{},
		BusyWait: 120, BusyRetry: 15, HoldCap: 20, StaleAlert: 36, Enabled: true,
	}
	switch preset {
	case backup.PresetComplete:
		p.Name, p.Scope, p.EnvMode = "Complete recovery", ScopeInstance, backup.EnvModeComplete
	case backup.PresetWorkspace:
		p.Name = "Workspace backup"
	case backup.PresetCustom:
		p.Name, p.KeepWeekly, p.KeepMonthly = "Custom backup", 0, 0
	}
	return p
}

// ValidationError is a 400: the plan as sent cannot be saved.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// cronParser is the parser routines use (internal/pipeline/schedules.go):
// five fields plus @descriptors.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

var timeOfDayRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// WorkspaceLister names the live workspaces a plan may cover.
type WorkspaceLister interface {
	WorkspaceIDs(ctx context.Context) ([]string, error)
}

// Normalize fixes what the preset decides and validates the rest. It changes
// p in place: complete recovery always covers the whole instance; workspace
// and custom plans cover workspaces (scope=instance there means every
// workspace); contents are kept only for custom plans; a cron expression only
// for the custom cadence.
func Normalize(ctx context.Context, p *Plan, ws WorkspaceLister, rr RecipientResolver) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 120 {
		return invalid("name is required (at most 120 characters)")
	}
	switch p.Preset {
	case backup.PresetComplete:
		p.Scope, p.WorkspaceIDs, p.Contents = ScopeInstance, []string{}, []string{}
	case backup.PresetWorkspace, backup.PresetCustom:
		switch p.Scope {
		case ScopeWorkspaces:
		case ScopeInstance, "":
			p.Scope, p.WorkspaceIDs = ScopeWorkspaces, []string{}
		default:
			return invalid("scope must be instance or workspaces")
		}
	default:
		return invalid("preset must be complete, workspace or custom")
	}
	if p.WorkspaceIDs == nil {
		p.WorkspaceIDs = []string{}
	}
	if len(p.WorkspaceIDs) > 0 && ws != nil {
		live, err := ws.WorkspaceIDs(ctx)
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, id := range live {
			known[id] = true
		}
		seen := map[string]bool{}
		var ids []string
		for _, id := range p.WorkspaceIDs {
			id = strings.TrimSpace(id)
			if !known[id] {
				return invalid("no workspace %q on this instance", id)
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		p.WorkspaceIDs = ids
	}
	if p.EnvMode != backup.EnvModeFiles && p.EnvMode != backup.EnvModeComplete {
		return invalid("env_mode must be files or complete")
	}
	if p.Preset == backup.PresetCustom {
		var kept []string
		for _, k := range p.Contents {
			if !backup.IsCategory(k) {
				return invalid("unknown contents category %q", k)
			}
			if k != backup.CategoryEnv { // environments follow env_mode
				kept = append(kept, k)
			}
		}
		if len(kept) == 0 && p.EnvMode != backup.EnvModeComplete {
			return invalid("a custom plan needs at least one contents category")
		}
		if kept == nil {
			kept = []string{}
		}
		p.Contents = kept
	} else {
		p.Contents = []string{}
	}
	if err := validateSchedule(p); err != nil {
		return err
	}
	switch p.EnvCadence {
	case EnvEvery, EnvWeekly, EnvMonthly:
	default:
		return invalid("env_cadence must be every, weekly or monthly")
	}
	if p.KeepMin < 1 {
		return invalid("keep_min must be at least 1: no rule may delete the last copy")
	}
	if p.KeepDaily < 0 || p.KeepWeekly < 0 || p.KeepMonthly < 0 {
		return invalid("keep_daily, keep_weekly and keep_monthly cannot be negative")
	}
	if p.BusyWait < 0 || p.BusyRetry < 1 || p.HoldCap < 1 || p.StaleAlert < 1 {
		return invalid("busy_wait_minutes must be >= 0; busy_retry_minutes, hold_cap_minutes and stale_alert_hours >= 1")
	}
	var dest []string
	for _, d := range p.Destinations {
		if d = strings.TrimSpace(d); d != "" {
			dest = append(dest, d)
		}
	}
	if len(dest) == 0 {
		dest = []string{"local"}
	}
	p.Destinations = dest
	var rids []string
	for _, r := range p.RecipientIDs {
		if r = strings.TrimSpace(r); r != "" {
			rids = append(rids, r)
		}
	}
	if len(rids) == 0 {
		return invalid("recipient_ids is required: every backup is encrypted, and a plan names who can open its bundles")
	}
	p.RecipientIDs = rids
	if rr != nil {
		if _, _, err := rr.Resolve(ctx, rids); err != nil {
			return invalid("%v", err)
		}
	}
	p.CountsAsProtection = p.Preset != backup.PresetCustom
	return nil
}

func validateSchedule(p *Plan) error {
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	if p.Timezone == "Local" {
		return invalid("timezone must be an IANA zone such as Europe/Prague, not Local")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return invalid("timezone %q is not a valid IANA zone", p.Timezone)
	}
	switch p.Cadence {
	case CadenceDaily, CadenceWeekly, CadenceMonthly:
		if !timeOfDayRe.MatchString(p.TimeOfDay) {
			return invalid("time_of_day must be HH:MM (24-hour)")
		}
		p.CronExpr = nil
	case CadenceCustom:
		if p.CronExpr == nil || strings.TrimSpace(*p.CronExpr) == "" {
			return invalid("cron_expr is required for the custom cadence")
		}
		expr := strings.TrimSpace(*p.CronExpr)
		if strings.HasPrefix(expr, "CRON_TZ=") || strings.HasPrefix(expr, "TZ=") {
			return invalid("cron_expr must not carry a timezone; set timezone instead")
		}
		if _, err := cronParser.Parse(expr); err != nil {
			return invalid("cron_expr is not a valid cron expression: %v", err)
		}
		p.CronExpr = &expr
		if !timeOfDayRe.MatchString(p.TimeOfDay) {
			p.TimeOfDay = "03:00"
		}
	default:
		return invalid("cadence must be daily, weekly, monthly or custom")
	}
	switch p.Cadence {
	case CadenceWeekly:
		if p.Weekday == nil {
			p.Weekday = intPtr(0)
		}
		if *p.Weekday < 0 || *p.Weekday > 6 {
			return invalid("weekday must be 0 (Sunday) to 6")
		}
	case CadenceMonthly:
		if p.Monthday != nil && (*p.Monthday < 1 || *p.Monthday > 31) {
			return invalid("monthday must be 1 to 31, or null for the first Sunday")
		}
	}
	if p.Cadence != CadenceWeekly {
		if p.Weekday != nil && (*p.Weekday < 0 || *p.Weekday > 6) {
			return invalid("weekday must be 0 (Sunday) to 6")
		}
	}
	if p.Cadence != CadenceMonthly && p.Monthday != nil && (*p.Monthday < 1 || *p.Monthday > 31) {
		return invalid("monthday must be 1 to 31")
	}
	return nil
}

// RecipientResolver turns a plan's recipient ids into age recipients and
// the names a run lists. An id is either an age public key (age1…) or the
// id of a row in backup_recipients (created by the recipients API when that
// exists; tolerated when it does not).
type RecipientResolver interface {
	Resolve(ctx context.Context, ids []string) ([]age.Recipient, []string, error)
}

// DBRecipients resolves ids against backup_recipients, and age1… keys as is.
type DBRecipients struct{ DB *sql.DB }

func (r DBRecipients) Resolve(ctx context.Context, ids []string) ([]age.Recipient, []string, error) {
	if len(ids) == 0 {
		return nil, nil, errors.New("no recipients: every backup is encrypted")
	}
	var recs []age.Recipient
	var names []string
	for _, id := range ids {
		key, name := id, ""
		if !strings.HasPrefix(id, "age1") {
			if r.DB == nil {
				return nil, nil, fmt.Errorf("unknown recipient %q", id)
			}
			err := r.DB.QueryRowContext(ctx, `SELECT public_key, name FROM backup_recipients WHERE id = ?`, id).Scan(&key, &name)
			if err != nil {
				return nil, nil, fmt.Errorf("unknown recipient %q (give an age1… public key or a recipient id)", id)
			}
		}
		rec, err := age.ParseX25519Recipient(strings.TrimSpace(key))
		if err != nil {
			return nil, nil, fmt.Errorf("recipient %q is not a valid age public key: %v", id, err)
		}
		recs = append(recs, rec)
		if name == "" {
			name = shortKey(key)
		}
		names = append(names, name)
	}
	return recs, names, nil
}

func shortKey(k string) string {
	if len(k) > 14 {
		return k[:7] + "…" + k[len(k)-3:]
	}
	return k
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}
