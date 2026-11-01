package artifacts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/diskusage"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ErrStorageQuota is the aggregate (workspace or instance) capacity refusal.
// It wraps ErrVersionQuota so every existing 413 mapping keeps working, while
// callers that need to can tell it apart from a single Page's history quota.
var ErrStorageQuota = fmt.Errorf("%w: aggregate artifact storage capacity reached", ErrVersionQuota)

// ErrStorageBusy refuses admission while too many uploads are in flight. It
// is transient: nothing was written and the same request may be retried.
var ErrStorageBusy = errors.New("artifact storage admission busy; retry shortly")

// StorageBudgetError preserves the surrounding HTTP error contract while
// identifying only the private SQLite quota-trigger sentinel.
func StorageBudgetError(err error) error {
	if err == nil || errors.Is(err, ErrVersionQuota) || errors.Is(err, ErrStorageBusy) {
		return err
	}
	var sqliteError *sqlite.Error
	if errors.As(err, &sqliteError) && sqliteError.Code() == 1811 && strings.Contains(sqliteError.Error(), "artifact_storage_quota") {
		return errors.Join(ErrStorageQuota, err)
	}
	return err
}

// AggregateStorageQuota is separate from the immutable document's own budget.
// Pending bytes remain charged until their request commits or releases them.
type AggregateStorageQuota struct {
	WorkspaceUsedBytes     int64 `json:"workspace_used_bytes"`
	WorkspaceReservedBytes int64 `json:"workspace_reserved_bytes"`
	WorkspaceLimitBytes    int64 `json:"workspace_limit_bytes"`
	UsedBytes              int64 `json:"used_bytes"`
	ReservedBytes          int64 `json:"reserved_bytes"`
	LimitBytes             int64 `json:"limit_bytes"`
	OperationReserveBytes  int64 `json:"operation_reserve_bytes"`
}

// Level is the most severe of the workspace and instance capacity levels. It
// is coarse on purpose: any writer may see it, never the byte counts.
func (q AggregateStorageQuota) Level(nearPercent int) string {
	rank := map[string]int{"": 0, QuotaLevelNone: 1, QuotaLevelNear: 2, QuotaLevelCritical: 3, QuotaLevelFull: 4}
	level := quotaLevel(q.WorkspaceUsedBytes+q.WorkspaceReservedBytes, q.WorkspaceLimitBytes, nearPercent)
	if instance := quotaLevel(q.UsedBytes+q.ReservedBytes, q.LimitBytes, nearPercent); rank[instance] > rank[level] {
		level = instance
	}
	return level
}

func storageQuota(ctx context.Context, tx *sql.Tx, workspace string) (AggregateStorageQuota, error) {
	var q AggregateStorageQuota
	err := tx.QueryRowContext(ctx, `SELECT coalesce(w.used_bytes,0),coalesce(w.reserved_bytes,0),coalesce(w.max_bytes,g.workspace_max_bytes),g.used_bytes,g.reserved_bytes,g.max_bytes,g.min_free_bytes FROM artifact_storage_global g LEFT JOIN artifact_storage_workspaces w ON w.workspace_id=? WHERE g.id=1`, workspace).Scan(&q.WorkspaceUsedBytes, &q.WorkspaceReservedBytes, &q.WorkspaceLimitBytes, &q.UsedBytes, &q.ReservedBytes, &q.LimitBytes, &q.OperationReserveBytes)
	return q, err
}

const StorageUploadTTL = 2 * time.Minute
const MaxStorageUploadBytes int64 = 64 << 20
const maxStorageUploads = 8
const maxWorkspaceStorageUploads = 4

type storageUploadContextKey struct{}
type StorageUpload struct {
	db                  *sql.DB
	id, workspace, page string
	expires             time.Time
	owned               bool
}

func (u *StorageUpload) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, storageUploadContextKey{}, u)
}
func (u *StorageUpload) ExpiresAt() time.Time { return u.expires }
func (u *StorageUpload) Close() error {
	if u == nil || !u.owned {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := u.db.ExecContext(ctx, `DELETE FROM artifact_storage_reservations WHERE id=? AND workspace_id=?`, u.id, u.workspace)
	return err
}

// ReserveUpload admits bounded in-progress work before reading/validating its
// payload. A new-Page upload may be workspace-bound; only the trusted callback
// decides whether that principal may start it. It grants no publication rights.
func (s *VersionStore) ReserveUpload(ctx context.Context, k VersionKey, size int64, a VersionAuthorize) (*StorageUpload, error) {
	if size <= 0 || size > MaxStorageUploadBytes {
		return nil, ErrVersionInvalid
	}
	var upload *StorageUpload
	err := s.transactionScope(ctx, k, a, func(tx *sql.Tx, _ string) error {
		if err := expireStorageUploads(ctx, tx); err != nil {
			return err
		}
		if current, _ := ctx.Value(storageUploadContextKey{}).(*StorageUpload); current != nil {
			if err := checkStorageUpload(ctx, tx, current, k); err != nil {
				return err
			}
			copy := *current
			copy.owned = false
			upload = &copy
			return nil
		}
		var total, local int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(CASE WHEN workspace_id=? THEN 1 ELSE 0 END),0) FROM artifact_storage_reservations`, k.WorkspaceID).Scan(&total, &local); err != nil {
			return err
		}
		if total >= maxStorageUploads || local >= maxWorkspaceStorageUploads {
			return ErrStorageBusy
		}
		if err := CheckStorageHeadroom(ctx, tx, size); err != nil {
			return err
		}
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return err
		}
		now := time.Now().UTC()
		expiry := now.Add(StorageUploadTTL)
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(expiry) {
			expiry = deadline
		}
		if !expiry.After(now) {
			return context.DeadlineExceeded
		}
		upload = &StorageUpload{db: s.db, id: "asr_" + hex.EncodeToString(random), workspace: k.WorkspaceID, page: k.PageID, expires: expiry, owned: true}
		var page any
		if k.PageID != "" {
			page = k.PageID
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO artifact_storage_reservations(id,workspace_id,page_id,bytes,created_at,expires_at) VALUES(?,?,?,?,?,?)`, upload.id, k.WorkspaceID, page, size, tsformat.Format(now), tsformat.Format(expiry))
		return err
	}, true, false)
	if err != nil {
		return nil, err
	}
	return upload, nil
}
func expireStorageUploads(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM artifact_storage_reservations WHERE julianday(expires_at)<=julianday(?)`, tsformat.Format(time.Now().UTC()))
	return err
}
func checkStorageUpload(ctx context.Context, tx *sql.Tx, u *StorageUpload, k VersionKey) error {
	if u.workspace != k.WorkspaceID || u.page != "" && u.page != k.PageID {
		return ErrVersionQuota
	}
	var workspace, expiry string
	var page sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT workspace_id,page_id,expires_at FROM artifact_storage_reservations WHERE id=?`, u.id).Scan(&workspace, &page, &expiry)
	if err != nil {
		return errors.Join(ErrVersionQuota, err)
	}
	deadline, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || !time.Now().Before(deadline) || workspace != k.WorkspaceID || page.Valid && page.String != k.PageID {
		return ErrVersionQuota
	}
	return nil
}

// ConsumeStorageUpload exchanges pending allowance for the payload charge in
// the same writer transaction. Rollback restores the pending allowance too.
func ConsumeStorageUpload(ctx context.Context, tx *sql.Tx, k VersionKey) error {
	upload, _ := ctx.Value(storageUploadContextKey{}).(*StorageUpload)
	if upload == nil {
		return nil
	}
	if err := checkStorageUpload(ctx, tx, upload, k); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM artifact_storage_reservations WHERE id=?`, upload.id)
	return err
}

var storageDiskUsage = diskusage.Usage

// CheckStorageHeadroom runs under the existing database writer fence. Logical
// admission is enforced by SQL capacity charges, never by this probe alone.
// Keep one whole DB copy plus the operation reserve, and twice the outstanding
// payload allowance for SQLite pages/WAL. Reads do not call this guard.
func CheckStorageHeadroom(ctx context.Context, tx *sql.Tx, writeBytes int64) error {
	if writeBytes < 0 || writeBytes > MaxStorageUploadBytes {
		return ErrVersionInvalid
	}
	if err := expireStorageUploads(ctx, tx); err != nil {
		return err
	}
	var path string
	if err := tx.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	var pages, pageSize, reserved, reserve int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT page_count FROM pragma_page_count),(SELECT page_size FROM pragma_page_size),reserved_bytes,min_free_bytes FROM artifact_storage_global WHERE id=1`).Scan(&pages, &pageSize, &reserved, &reserve); err != nil {
		return err
	}
	if pages < 0 || pageSize <= 0 || reserved < 0 || reserve < 0 {
		return ErrStorageQuota
	}
	stats, err := storageDiskUsage(filepath.Dir(path))
	if err != nil {
		return errors.Join(ErrStorageQuota, err)
	}
	required := uint64(reserve) + uint64(pages)*uint64(pageSize) + 2*(uint64(reserved)+uint64(writeBytes))
	if stats.FreeBytes < required {
		return ErrStorageQuota
	}
	return nil
}

// WorkspaceStorageUsage is the owner/admin view of one workspace's aggregate
// artifact storage. It never carries the instance's byte counts or any other
// workspace's usage; the instance contributes only a coarse level.
type WorkspaceStorageUsage struct {
	UsedBytes     int64  `json:"used_bytes"`
	ReservedBytes int64  `json:"reserved_bytes"`
	LimitBytes    int64  `json:"limit_bytes"`
	Level         string `json:"level"`
	InstanceLevel string `json:"instance_level"`
	Retention     string `json:"retention"`
}

// WorkspaceStorage reads the accounting rows only. Callers must have already
// established workspace owner/admin authority; it does not consult Pages.
func (s *VersionStore) WorkspaceStorage(ctx context.Context, workspace string) (WorkspaceStorageUsage, error) {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return WorkspaceStorageUsage{}, ErrDisabled
	}
	if workspace == "" {
		return WorkspaceStorageUsage{}, ErrVersionInvalid
	}
	var q AggregateStorageQuota
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(w.used_bytes,0),coalesce(w.reserved_bytes,0),coalesce(w.max_bytes,g.workspace_max_bytes),g.used_bytes,g.reserved_bytes,g.max_bytes,g.min_free_bytes FROM artifact_storage_global g LEFT JOIN artifact_storage_workspaces w ON w.workspace_id=? WHERE g.id=1`, workspace).Scan(&q.WorkspaceUsedBytes, &q.WorkspaceReservedBytes, &q.WorkspaceLimitBytes, &q.UsedBytes, &q.ReservedBytes, &q.LimitBytes, &q.OperationReserveBytes)
	if err != nil {
		return WorkspaceStorageUsage{}, err
	}
	percent := s.opts.WarningPercent
	return WorkspaceStorageUsage{
		UsedBytes:     q.WorkspaceUsedBytes,
		ReservedBytes: q.WorkspaceReservedBytes,
		LimitBytes:    q.WorkspaceLimitBytes,
		Level:         quotaLevel(q.WorkspaceUsedBytes+q.WorkspaceReservedBytes, q.WorkspaceLimitBytes, percent),
		InstanceLevel: quotaLevel(q.UsedBytes+q.ReservedBytes, q.LimitBytes, percent),
		Retention:     "keep-all",
	}, nil
}
