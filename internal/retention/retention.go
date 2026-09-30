// Package retention is the one place that knows every per-workspace data
// retention window: what each is called, where it is stored, what it defaults
// to, and how many rows the next sweep would delete under a given value.
//
// Six windows predate this package and keep their columns on workspaces
// (run_retention_days, approvals_retention_days, audit_log_retention_days,
// credential_audit_retention_days, page_retention_days and the
// versions_retention_days key of memory_config). Their sweeps stay where they
// are; this package reads and writes the columns in place and translates each
// column's own NULL/0 convention into one API convention: an integer number of
// days, or nil for "keep forever".
//
// Three windows are new (inbox_days, chats_days, keeper_decisions_days). They
// live in retention_settings, default to forever for every workspace that
// exists or is created later, and are swept by Sweep* in this package — only
// once an administrator sets them.
package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Key names one retention window. The string is the wire name the API, the
// CLI and the instance audit all use.
type Key string

const (
	RoutineRuns     Key = "routine_runs_days"
	Approvals       Key = "approvals_days"
	Audit           Key = "audit_days"
	CredentialAudit Key = "credential_audit_days"
	MemoryVersions  Key = "memory_versions_days"
	PagePanelData   Key = "page_panel_data_days"
	Inbox           Key = "inbox_days"
	Chats           Key = "chats_days"
	KeeperDecisions Key = "keeper_decisions_days"
)

// MinDays and MaxDays bound every window an administrator sets. Ten years is
// the ceiling memory_config already used; a longer window is "forever" for
// every practical purpose and, where a window can be forever, says so.
const (
	MinDays = 1
	MaxDays = 3650
)

// Credential audit's product default. It is duplicated from
// api.DefaultCredentialAuditRetentionDays because this package cannot import
// internal/api; a test in internal/api pins the two together.
const DefaultCredentialAuditDays = 90

// KeyInfo describes one window for the console and the CLI.
type KeyInfo struct {
	Key   Key    `json:"key"`
	Label string `json:"label"`
	// DefaultDays is what a workspace nobody configured keeps; nil is forever.
	DefaultDays *int `json:"default_days"`
	// ForeverAllowed is false where the storage cannot express "keep forever":
	// routine runs, memory versions and panel data always age out.
	ForeverAllowed bool   `json:"forever_allowed"`
	MinDays        int    `json:"min_days"`
	MaxDays        int    `json:"max_days"`
	Detail         string `json:"detail"`
}

func days(n int) *int { return &n }

// Keys lists every window in the order the console shows them.
var Keys = []KeyInfo{
	{Key: RoutineRuns, Label: "Routine runs", DefaultDays: days(pipeline.DefaultRunRetentionDays),
		Detail: fmt.Sprintf("Finished runs older than this are deleted daily; the last %d runs of every routine, runs waiting on an approval and runs another run replays are always kept.", pipeline.DefaultKeepLastNRunsPerPipeline)},
	{Key: Approvals, Label: "Approvals", DefaultDays: days(harbormaster.DefaultApprovalsRetentionDays), ForeverAllowed: true,
		Detail: "Decided approvals older than this are deleted daily. Pending approvals and autonomy gates are never deleted."},
	{Key: Audit, Label: "Audit log", DefaultDays: nil, ForeverAllowed: true,
		Detail: "Workspace audit entries older than this are deleted daily. Kept forever unless set: retention obligations are the operator's."},
	{Key: CredentialAudit, Label: "Credential audit", DefaultDays: days(DefaultCredentialAuditDays), ForeverAllowed: true,
		Detail: "Credential reads older than this are deleted daily. Last used time and address stay on the credential."},
	{Key: MemoryVersions, Label: "Memory version history", DefaultDays: days(memory.DefaultRetentionDays),
		Detail: "Older memory versions are deleted daily. The instance-wide pass also ages versions out after 30 days, keeping the latest few per file, so a longer window only keeps those."},
	{Key: PagePanelData, Label: "Page panel data", DefaultDays: days(pages.DefaultPageRetentionDays),
		Detail: fmt.Sprintf("Panel values older than this are dropped on the panel's next push; the newest value always stays, and no panel keeps more than %d.", pages.RingMaxPayloads)},
	{Key: Inbox, Label: "Inbox items", DefaultDays: nil, ForeverAllowed: true,
		Detail: "Resolved items, and read items that block nothing, older than this are deleted daily. Open items are never deleted."},
	{Key: Chats, Label: "Chats", DefaultDays: nil, ForeverAllowed: true,
		Detail: "Chats idle longer than this are deleted daily with their messages, unless they hold open work: a delegation, an unanswered escalation, an unresolved inbox item, a pending approval, an unfinished routine run or a recent access grant."},
	{Key: KeeperDecisions, Label: "Keeper decisions", DefaultDays: nil, ForeverAllowed: true,
		Detail: "Decided Keeper requests older than this are deleted daily with their ledger. Pending and escalated requests and requests backing a live credential lease are never deleted."},
}

// Info returns the description of one key.
func Info(k Key) (KeyInfo, bool) {
	for _, i := range Keys {
		if i.Key == k {
			return i, true
		}
	}
	return KeyInfo{}, false
}

func knownKeys() string {
	names := make([]string, 0, len(Keys))
	for _, i := range Keys {
		names = append(names, string(i.Key))
	}
	return strings.Join(names, ", ")
}

// settingsKeys are the windows stored in retention_settings.
var settingsKeys = []Key{Inbox, Chats, KeeperDecisions}

func isSettingsKey(k Key) bool {
	for _, s := range settingsKeys {
		if s == k {
			return true
		}
	}
	return false
}

// Windows maps every key to its value in days; nil is forever.
type Windows map[Key]*int

// ProductDefaults is what a workspace keeps when nobody configured anything.
func ProductDefaults() Windows {
	w := Windows{}
	for _, i := range Keys {
		if i.DefaultDays != nil {
			w[i.Key] = days(*i.DefaultDays)
		} else {
			w[i.Key] = nil
		}
	}
	return w
}

// Equal reports whether two window values are the same.
func Equal(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// DB is a *sql.DB or a *sql.Tx.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ErrWorkspaceNotFound is returned by Load for an unknown workspace.
var ErrWorkspaceNotFound = errors.New("retention: workspace not found")

// Load returns every window of one workspace as it takes effect today.
func Load(ctx context.Context, db DB, workspaceID string) (Windows, error) {
	var runs, approvals, audit, cred, page sql.NullInt64
	var memCfg string
	err := db.QueryRowContext(ctx, `
		SELECT run_retention_days, approvals_retention_days, audit_log_retention_days,
		       credential_audit_retention_days, page_retention_days, COALESCE(memory_config, '')
		  FROM workspaces WHERE id = ?`, workspaceID).
		Scan(&runs, &approvals, &audit, &cred, &page, &memCfg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("retention: load %s: %w", workspaceID, err)
	}
	w := Windows{}
	// run_retention_days and page_retention_days: NULL or <= 0 is the default
	// (pipeline.SweepAllWorkspacesRunRetention, pages.RetentionAge).
	w[RoutineRuns] = positiveOr(runs, pipeline.DefaultRunRetentionDays)
	w[PagePanelData] = positiveOr(page, pages.DefaultPageRetentionDays)
	// approvals and credential audit: NULL is the default, 0 is forever.
	w[Approvals] = nullDefaultZeroForever(approvals, harbormaster.DefaultApprovalsRetentionDays)
	w[CredentialAudit] = nullDefaultZeroForever(cred, DefaultCredentialAuditDays)
	// audit log: NULL and 0 are both forever.
	if audit.Valid && audit.Int64 > 0 {
		w[Audit] = days(int(audit.Int64))
	} else {
		w[Audit] = nil
	}
	w[MemoryVersions] = days(memoryRetentionDays(memCfg))
	for _, k := range settingsKeys {
		w[k] = nil
	}
	rows, err := db.QueryContext(ctx, `SELECT key, days FROM retention_settings WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("retention: load settings %s: %w", workspaceID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var d sql.NullInt64
		if err := rows.Scan(&k, &d); err != nil {
			return nil, fmt.Errorf("retention: scan settings: %w", err)
		}
		if !isSettingsKey(Key(k)) {
			continue
		}
		if d.Valid && d.Int64 > 0 {
			w[Key(k)] = days(int(d.Int64))
		}
	}
	return w, rows.Err()
}

func positiveOr(v sql.NullInt64, def int) *int {
	if v.Valid && v.Int64 > 0 {
		return days(int(v.Int64))
	}
	return days(def)
}

func nullDefaultZeroForever(v sql.NullInt64, def int) *int {
	switch {
	case !v.Valid:
		return days(def)
	case v.Int64 <= 0:
		return nil
	default:
		return days(int(v.Int64))
	}
}

// memoryRetentionDays mirrors memory.extractRetentionDays: a positive number
// (fractions round up) or the default.
func memoryRetentionDays(memCfg string) int {
	if memCfg == "" {
		return memory.DefaultRetentionDays
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(memCfg), &cfg); err != nil {
		return memory.DefaultRetentionDays
	}
	if v, ok := cfg["versions_retention_days"].(float64); ok && v > 0 {
		return int(math.Ceil(v))
	}
	return memory.DefaultRetentionDays
}

// ValidationError is a refused value; its message is safe to show.
type ValidationError struct{ msg string }

func (e ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return ValidationError{msg: fmt.Sprintf(format, args...)}
}

// ParsePatch reads {"key": days | null, …}: only the keys being changed.
func ParsePatch(raw map[string]json.RawMessage) (Windows, error) {
	if len(raw) == 0 {
		return nil, invalid("nothing to change: send at least one window")
	}
	out := Windows{}
	for name, v := range raw {
		info, ok := Info(Key(name))
		if !ok {
			return nil, invalid("unknown retention window %q; known: %s", name, knownKeys())
		}
		d, err := parseDays(info, v)
		if err != nil {
			return nil, err
		}
		out[info.Key] = d
	}
	return out, nil
}

func parseDays(info KeyInfo, v json.RawMessage) (*int, error) {
	s := strings.TrimSpace(string(v))
	if s == "null" || s == "" {
		if !info.ForeverAllowed {
			return nil, invalid("%s cannot be forever; choose %d to %d days", info.Key, MinDays, MaxDays)
		}
		return nil, nil
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		return nil, invalid("%s must be a whole number of days or null for forever", info.Key)
	}
	if f != math.Trunc(f) {
		return nil, invalid("%s must be a whole number of days, got %v", info.Key, f)
	}
	if f < MinDays || f > MaxDays {
		return nil, invalid("%s must be between %d and %d days, got %v", info.Key, MinDays, MaxDays, f)
	}
	return days(int(f)), nil
}

// Store writes one window of one workspace into wherever it lives.
func Store(ctx context.Context, db DB, workspaceID string, k Key, d *int, actor string, now time.Time) error {
	info, ok := Info(k)
	if !ok {
		return invalid("unknown retention window %q", k)
	}
	if d == nil && !info.ForeverAllowed {
		return invalid("%s cannot be forever", k)
	}
	if d != nil && (*d < MinDays || *d > MaxDays) {
		return invalid("%s must be between %d and %d days", k, MinDays, MaxDays)
	}
	// forever as the column spells it: an explicit 0 where the column has a
	// separate "not configured" NULL.
	orZero := func() any {
		if d == nil {
			return 0
		}
		return *d
	}
	var err error
	switch k {
	case RoutineRuns:
		_, err = db.ExecContext(ctx, `UPDATE workspaces SET run_retention_days = ? WHERE id = ?`, *d, workspaceID)
	case PagePanelData:
		_, err = db.ExecContext(ctx, `UPDATE workspaces SET page_retention_days = ? WHERE id = ?`, *d, workspaceID)
	case Approvals:
		_, err = db.ExecContext(ctx, `UPDATE workspaces SET approvals_retention_days = ? WHERE id = ?`, orZero(), workspaceID)
	case Audit:
		_, err = db.ExecContext(ctx, `UPDATE workspaces SET audit_log_retention_days = ? WHERE id = ?`, orZero(), workspaceID)
	case CredentialAudit:
		_, err = db.ExecContext(ctx, `UPDATE workspaces SET credential_audit_retention_days = ? WHERE id = ?`, orZero(), workspaceID)
	case MemoryVersions:
		err = storeMemoryDays(ctx, db, workspaceID, *d)
	default:
		var v any
		if d != nil {
			v = *d
		}
		var actorArg any
		if actor != "" {
			actorArg = actor
		}
		_, err = db.ExecContext(ctx, `
			INSERT INTO retention_settings (workspace_id, key, days, updated_at, updated_by) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(workspace_id, key) DO UPDATE SET days = excluded.days, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			workspaceID, string(k), v, tsformat.Format(now.UTC()), actorArg)
	}
	if err != nil {
		return fmt.Errorf("retention: store %s for %s: %w", k, workspaceID, err)
	}
	return nil
}

// storeMemoryDays sets memory_config.versions_retention_days, keeping every
// other key. A corrupt document is replaced, the same recovery the memory
// config PATCH applies.
func storeMemoryDays(ctx context.Context, db DB, workspaceID string, d int) error {
	var raw sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT memory_config FROM workspaces WHERE id = ?`, workspaceID).Scan(&raw); err != nil {
		return err
	}
	doc := map[string]any{}
	if raw.Valid && raw.String != "" {
		if err := json.Unmarshal([]byte(raw.String), &doc); err != nil || doc == nil {
			doc = map[string]any{}
		}
	}
	doc["versions_retention_days"] = d
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE workspaces SET memory_config = ? WHERE id = ?`, string(b), workspaceID)
	return err
}

// DefaultsSettingKey is the app_settings row holding the instance defaults:
// the windows last saved for all workspaces. A workspace created later starts
// from them (ApplyDefaults); a key never saved for all keeps its product
// default.
const DefaultsSettingKey = "retention.defaults"

// Defaults returns the windows a new workspace starts from and which keys an
// administrator has set for all workspaces (the rest are product defaults).
func Defaults(ctx context.Context, db DB) (Windows, []Key, error) {
	stored, err := storedDefaults(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	w := ProductDefaults()
	configured := make([]Key, 0, len(stored))
	for k, v := range stored {
		w[k] = v
		configured = append(configured, k)
	}
	sort.Slice(configured, func(i, j int) bool { return configured[i] < configured[j] })
	return w, configured, nil
}

func storedDefaults(ctx context.Context, db DB) (Windows, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, DefaultsSettingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Windows{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("retention: defaults: %w", err)
	}
	var m map[string]*int
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("retention: defaults: decode: %w", err)
	}
	out := Windows{}
	for name, v := range m {
		info, ok := Info(Key(name))
		if !ok {
			continue
		}
		if v == nil && !info.ForeverAllowed {
			continue
		}
		if v != nil && (*v < MinDays || *v > MaxDays) {
			continue
		}
		out[info.Key] = v
	}
	return out, nil
}

// MergeDefaults stores patch over the saved instance defaults.
func MergeDefaults(ctx context.Context, db DB, patch Windows, now time.Time) error {
	stored, err := storedDefaults(ctx, db)
	if err != nil {
		return err
	}
	for k, v := range patch {
		stored[k] = v
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("retention: set defaults: %w", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		DefaultsSettingKey, string(b), now.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("retention: set defaults: %w", err)
	}
	return nil
}

// ApplyDefaults gives a newly created workspace the windows last saved for all
// workspaces. Keys never saved for all are left alone, so the workspace keeps
// the product default — forever, for inbox, chats and Keeper decisions. Call
// it in the transaction that inserts the workspace.
func ApplyDefaults(ctx context.Context, db DB, workspaceID string) error {
	stored, err := storedDefaults(ctx, db)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, info := range Keys {
		v, ok := stored[info.Key]
		if !ok {
			continue
		}
		if err := Store(ctx, db, workspaceID, info.Key, v, "", now); err != nil {
			return err
		}
	}
	return nil
}
