package backup

// origins.go — provenance for disaster-recovery restores (#1716).
//
// A restore run with --as-workspace / --as-crew forks the bundle under a
// new identity: a new workspace id, a slug taken from the flag, and (via
// RemapIDs) a new primary key for every row. It deliberately skips the
// docker phase, because the crews it just created have no containers yet
// and the manifest's crew slugs still address the SOURCE crew's
// containers. The operator is then told to provision the new crews and
// re-run restore to land the files — and that re-run is refused, because
// the fork's identity matches neither the bundle's workspace id nor its
// slug.
//
// The missing fact is the fork itself. RecordRestoreOrigin writes it at
// the moment it happens; allowRestore and the --files-only resume read it
// back. Nothing else authorises a cross-identity restore: without a row
// here, the guard behaves exactly as it did before.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RestoreOriginCrew is one entry of the bundle-slug -> local-crew map.
type RestoreOriginCrew struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// RestoreOrigin is a workspace's provenance: the bundle it was forked
// from, and which local crew each of that bundle's crews became.
type RestoreOrigin struct {
	WorkspaceID  string
	BundleSHA256 string
	BundlePath   string
	// CrewsByBundleSlug maps the crew slug AS RECORDED IN THE BUNDLE to
	// the crew row this instance created for it. --as-crew renames the
	// crew, so for a forked crew-scope bundle the key and the value's
	// Slug differ, and only this map relates them.
	CrewsByBundleSlug map[string]RestoreOriginCrew
	RestoredAt        time.Time
	RestoredBy        string
}

// RecordRestoreOrigin persists (or replaces) a workspace's restore
// provenance. Called inside the restore's own transaction so a rolled
// back restore cannot leave behind a claim that a workspace came from a
// bundle it does not actually hold.
//
// Replace-on-conflict rather than insert-once: restoring a second bundle
// over the same forked workspace makes the newer bundle its origin, and
// keeping the older row would authorise a resume that lands sections
// from a bundle whose rows are no longer there.
func RecordRestoreOrigin(ctx context.Context, tx dbExecer, o RestoreOrigin) error {
	if o.WorkspaceID == "" || o.BundleSHA256 == "" {
		return fmt.Errorf("backup: RecordRestoreOrigin: workspace id and bundle sha256 required")
	}
	crews := o.CrewsByBundleSlug
	if crews == nil {
		crews = map[string]RestoreOriginCrew{}
	}
	encoded, err := json.Marshal(crews)
	if err != nil {
		return fmt.Errorf("backup: encode restore origin crew map: %w", err)
	}
	at := o.RestoredAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO backup_restore_origins
		    (workspace_id, bundle_sha256, bundle_path, crew_slug_map, restored_at, restored_by)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
		    bundle_sha256 = excluded.bundle_sha256,
		    bundle_path   = excluded.bundle_path,
		    crew_slug_map = excluded.crew_slug_map,
		    restored_at   = excluded.restored_at,
		    restored_by   = excluded.restored_by`,
		o.WorkspaceID, o.BundleSHA256, o.BundlePath, string(encoded),
		tsformat.Format(at), o.RestoredBy)
	if err != nil {
		return fmt.Errorf("backup: record restore origin: %w", err)
	}
	return nil
}

// LookupRestoreOrigin returns the provenance recorded for a workspace,
// or (nil, nil) when there is none — which is the case for every
// workspace that was not created by a rewritten restore, and therefore
// the case in which the tenant guard keeps its original behaviour.
//
// A missing backup_restore_origins table is reported as "no origin"
// rather than as an error: the guard must fail closed, and an instance
// that has not run the migration has no provenance to honour.
func LookupRestoreOrigin(ctx context.Context, db dbQueryer, workspaceID string) (*RestoreOrigin, error) {
	if workspaceID == "" {
		return nil, nil
	}
	var (
		o         RestoreOrigin
		crewMap   string
		restoreAt string
	)
	err := db.QueryRowContext(ctx, `
		SELECT workspace_id, bundle_sha256, bundle_path, crew_slug_map, restored_at, restored_by
		  FROM backup_restore_origins
		 WHERE workspace_id = ?`, workspaceID).
		Scan(&o.WorkspaceID, &o.BundleSHA256, &o.BundlePath, &crewMap, &restoreAt, &o.RestoredBy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || isMissingTableErr(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup: lookup restore origin: %w", err)
	}
	o.CrewsByBundleSlug = map[string]RestoreOriginCrew{}
	if crewMap != "" {
		// A malformed map is not fatal to authorisation — the digest
		// match is what authorises — but it does mean the resume cannot
		// address the right crews, so surface it rather than silently
		// restoring into whatever the manifest names.
		if err := json.Unmarshal([]byte(crewMap), &o.CrewsByBundleSlug); err != nil {
			return nil, fmt.Errorf("backup: decode restore origin crew map for workspace %s: %w", workspaceID, err)
		}
	}
	if t, perr := time.Parse(time.RFC3339Nano, restoreAt); perr == nil {
		o.RestoredAt = t
	}
	return &o, nil
}

// isMissingTableErr reports the driver's "no such table" so a pre-migration
// instance reads as "no provenance" instead of erroring the whole guard.
func isMissingTableErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// dbExecer / dbQueryer keep origins.go usable from both a *sql.DB and a
// *sql.Tx without importing either concretely at the call sites.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type dbQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
