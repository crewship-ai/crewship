package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Bounds for configured aggregate limits. The floor keeps an admin from
// locking the instance out with a typo; the ceiling is far past any disk.
const (
	MinStorageLimitBytes int64 = 1 << 20
	MaxStorageLimitBytes int64 = 1 << 50
)

// ErrStorageLimit marks a rejected limit value (out of range or not set).
var ErrStorageLimit = errors.New("invalid artifact storage limit")

// StorageLimits are the configurable aggregate limits. Defaults come from the
// migration (32 GiB per workspace, 64 GiB per instance) and stay in force until
// an instance administrator changes them.
type StorageLimits struct {
	WorkspaceLimitBytes int64 `json:"workspace_limit_bytes"`
	InstanceLimitBytes  int64 `json:"instance_limit_bytes"`
}

// InstanceStorage is the instance administrator's view: limits plus the
// instance-wide counters. Only instance administrators may see it.
type InstanceStorage struct {
	StorageLimits
	UsedBytes     int64  `json:"used_bytes"`
	ReservedBytes int64  `json:"reserved_bytes"`
	Level         string `json:"level"`
	Workspaces    int    `json:"workspaces"`
	Retention     string `json:"retention"`
}

// LoadInstanceStorage reads the accounting rows only.
func LoadInstanceStorage(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nearPercent int) (InstanceStorage, error) {
	var s InstanceStorage
	err := q.QueryRowContext(ctx, `SELECT workspace_max_bytes,max_bytes,used_bytes,reserved_bytes,(SELECT count(*) FROM artifact_storage_workspaces) FROM artifact_storage_global WHERE id=1`).
		Scan(&s.WorkspaceLimitBytes, &s.InstanceLimitBytes, &s.UsedBytes, &s.ReservedBytes, &s.Workspaces)
	if err != nil {
		return s, err
	}
	if nearPercent <= 0 {
		nearPercent = 80
	}
	s.Level = quotaLevel(s.UsedBytes+s.ReservedBytes, s.InstanceLimitBytes, nearPercent)
	s.Retention = "keep-all"
	return s, nil
}

// SetStorageLimits changes the workspace and/or instance limit inside tx. A nil
// argument keeps the current value. Lowering a limit below current usage never
// removes anything: it only refuses further growth until usage falls.
func SetStorageLimits(ctx context.Context, tx *sql.Tx, workspace, instance *int64) (before, after StorageLimits, err error) {
	if workspace == nil && instance == nil {
		return before, after, fmt.Errorf("%w: send workspace_limit_bytes and/or instance_limit_bytes", ErrStorageLimit)
	}
	for name, v := range map[string]*int64{"workspace_limit_bytes": workspace, "instance_limit_bytes": instance} {
		if v != nil && (*v < MinStorageLimitBytes || *v > MaxStorageLimitBytes) {
			return before, after, fmt.Errorf("%w: %s must be between %d and %d bytes", ErrStorageLimit, name, MinStorageLimitBytes, MaxStorageLimitBytes)
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT workspace_max_bytes,max_bytes FROM artifact_storage_global WHERE id=1`).Scan(&before.WorkspaceLimitBytes, &before.InstanceLimitBytes); err != nil {
		return before, after, err
	}
	after = before
	if workspace != nil {
		after.WorkspaceLimitBytes = *workspace
	}
	if instance != nil {
		after.InstanceLimitBytes = *instance
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_global SET workspace_max_bytes=?,max_bytes=? WHERE id=1`, after.WorkspaceLimitBytes, after.InstanceLimitBytes); err != nil {
		return before, after, err
	}
	if workspace != nil {
		// Every workspace shares the one configured limit; there is no
		// per-workspace override to preserve.
		if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_workspaces SET max_bytes=?`, after.WorkspaceLimitBytes); err != nil {
			return before, after, err
		}
	}
	return before, after, nil
}

// StorageCounterDrift is one counter pair before and after a recompute.
type StorageCounterDrift struct {
	UsedBefore     int64 `json:"used_bytes_before"`
	UsedAfter      int64 `json:"used_bytes_after"`
	ReservedBefore int64 `json:"reserved_bytes_before"`
	ReservedAfter  int64 `json:"reserved_bytes_after"`
}

func (d StorageCounterDrift) drifted() bool {
	return d.UsedBefore != d.UsedAfter || d.ReservedBefore != d.ReservedAfter
}

// WorkspaceStorageDrift is one workspace whose counters disagreed with its rows.
type WorkspaceStorageDrift struct {
	WorkspaceID string `json:"workspace_id"`
	StorageCounterDrift
}

// StorageRecomputeReport says what a recompute found (and, unless DryRun,
// fixed). Only derived counters are ever written; no artifact data is touched.
type StorageRecomputeReport struct {
	DryRun            bool                    `json:"dry_run"`
	Changed           bool                    `json:"changed"`
	Instance          StorageCounterDrift     `json:"instance"`
	WorkspacesChecked int                     `json:"workspaces_checked"`
	WorkspacesDrifted int                     `json:"workspaces_drifted"`
	Workspaces        []WorkspaceStorageDrift `json:"workspaces"`
	DataDeleted       bool                    `json:"data_deleted"`
}

// RecomputeStorage rebuilds the aggregate counters from the artifact source
// tables in ONE transaction holding the writer fence, so no writer can slip a
// charge between the scan and the update. It is idempotent: a second run on a
// consistent database reports no drift and writes nothing. It never deletes
// data and never changes a limit. audit, when set, runs inside the same
// transaction (its error rolls everything back).
func RecomputeStorage(ctx context.Context, db *sql.DB, dryRun bool, audit func(context.Context, *sql.Tx, StorageRecomputeReport) error) (StorageRecomputeReport, error) {
	report := StorageRecomputeReport{DryRun: dryRun, Workspaces: []WorkspaceStorageDrift{}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return report, err
	}

	type counters struct{ used, reserved, max int64 }
	current := map[string]counters{}
	rows, err := tx.QueryContext(ctx, `SELECT workspace_id,used_bytes,reserved_bytes,max_bytes FROM artifact_storage_workspaces`)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id string
		var c counters
		if err = rows.Scan(&id, &c.used, &c.reserved, &c.max); err != nil {
			rows.Close()
			return report, err
		}
		current[id] = c
	}
	if err = rows.Close(); err == nil {
		err = rows.Err()
	}
	if err != nil {
		return report, err
	}
	var globalMax, workspaceMax int64
	if err = tx.QueryRowContext(ctx, `SELECT used_bytes,reserved_bytes,max_bytes,workspace_max_bytes FROM artifact_storage_global WHERE id=1`).
		Scan(&report.Instance.UsedBefore, &report.Instance.ReservedBefore, &globalMax, &workspaceMax); err != nil {
		return report, err
	}

	live := map[string]bool{}
	liveRows, err := tx.QueryContext(ctx, `SELECT id FROM workspaces`)
	if err != nil {
		return report, err
	}
	for liveRows.Next() {
		var id string
		if err = liveRows.Scan(&id); err != nil {
			liveRows.Close()
			return report, err
		}
		live[id] = true
	}
	if err = liveRows.Close(); err == nil {
		err = liveRows.Err()
	}
	if err != nil {
		return report, err
	}

	used := map[string]int64{}
	var total int64
	srcRows, err := tx.QueryContext(ctx, storageSourceBytesSQL)
	if err != nil {
		return report, err
	}
	for srcRows.Next() {
		var id sql.NullString
		var bytes sql.NullInt64
		if err = srcRows.Scan(&id, &bytes); err != nil {
			srcRows.Close()
			return report, err
		}
		// Orphans (workspace already gone) still count toward the instance
		// total, as the triggers and the seed do; only live workspaces get rows.
		total += bytes.Int64
		if id.Valid && live[id.String] {
			used[id.String] += bytes.Int64
		}
	}
	if err = srcRows.Close(); err == nil {
		err = srcRows.Err()
	}
	if err != nil {
		return report, err
	}
	reserved := map[string]int64{}
	var totalReserved int64
	resRows, err := tx.QueryContext(ctx, `SELECT workspace_id,coalesce(sum(bytes),0) FROM artifact_storage_reservations GROUP BY workspace_id`)
	if err != nil {
		return report, err
	}
	for resRows.Next() {
		var id string
		var bytes int64
		if err = resRows.Scan(&id, &bytes); err != nil {
			resRows.Close()
			return report, err
		}
		reserved[id] += bytes
		totalReserved += bytes
	}
	if err = resRows.Close(); err == nil {
		err = resRows.Err()
	}
	if err != nil {
		return report, err
	}

	ids := map[string]struct{}{}
	for id := range current {
		ids[id] = struct{}{}
	}
	for id := range used {
		ids[id] = struct{}{}
	}
	for id := range reserved {
		if live[id] {
			ids[id] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	report.WorkspacesChecked = len(sorted)
	report.Instance.UsedAfter, report.Instance.ReservedAfter = total, totalReserved

	type fix struct {
		id         string
		used, resv int64
		insert     bool
		limit      int64
	}
	var fixes []fix
	for _, id := range sorted {
		cur, exists := current[id]
		d := WorkspaceStorageDrift{WorkspaceID: id, StorageCounterDrift: StorageCounterDrift{UsedBefore: cur.used, UsedAfter: used[id], ReservedBefore: cur.reserved, ReservedAfter: reserved[id]}}
		if !d.drifted() {
			continue
		}
		report.Workspaces = append(report.Workspaces, d)
		limit := cur.max
		if !exists {
			limit = workspaceMax
		}
		fixes = append(fixes, fix{id: id, used: used[id], resv: reserved[id], insert: !exists, limit: limit})
	}
	report.WorkspacesDrifted = len(report.Workspaces)
	report.Changed = report.Instance.drifted() || report.WorkspacesDrifted > 0

	if report.Changed && !dryRun {
		// The capacity triggers refuse any counter increase past its limit.
		// Correcting a counter upward is bookkeeping, not admission, so lift the
		// limit for this transaction only and restore the exact values after.
		if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_global SET max_bytes=? WHERE id=1`, int64(math.MaxInt64)); err != nil {
			return report, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_workspaces SET max_bytes=?`, int64(math.MaxInt64)); err != nil {
			return report, err
		}
		for _, f := range fixes {
			if f.insert {
				if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) VALUES(?,?)`, f.id, int64(math.MaxInt64)); err != nil {
					return report, err
				}
			}
			if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_workspaces SET used_bytes=?,reserved_bytes=? WHERE workspace_id=?`, f.used, f.resv, f.id); err != nil {
				return report, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_global SET used_bytes=?,reserved_bytes=? WHERE id=1`, total, totalReserved); err != nil {
			return report, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_global SET max_bytes=? WHERE id=1`, globalMax); err != nil {
			return report, err
		}
		for id, c := range current {
			if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_workspaces SET max_bytes=? WHERE workspace_id=?`, c.max, id); err != nil {
				return report, err
			}
		}
		for _, f := range fixes {
			if f.insert {
				if _, err = tx.ExecContext(ctx, `UPDATE artifact_storage_workspaces SET max_bytes=? WHERE workspace_id=?`, f.limit, f.id); err != nil {
					return report, err
				}
			}
		}
	}
	if audit != nil && !dryRun {
		if err = audit(ctx, tx, report); err != nil {
			return report, err
		}
	}
	if dryRun {
		return report, nil
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}
