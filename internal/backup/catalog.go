package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CatalogEntry is one row in the backup_catalog table — a pointer to
// a bundle on disk with the minimal metadata the admin list view
// needs. Fetching these avoids re-parsing every manifest on each page
// load, which matters once an install accumulates a few hundred
// backups.
type CatalogEntry struct {
	ID            string
	FilePath      string
	Scope         string
	Slug          string
	WorkspaceID   string
	CreatedAt     time.Time
	CreatedBy     string
	Size          int64
	SHA256        string
	Encrypted     bool
	FormatVersion int
}

// UpsertCatalogEntry inserts a row for the bundle at path. If the row
// already exists (admin created the same bundle twice via a retry) we
// overwrite the mutable fields — size, checksum, timestamp — so the
// catalog never drifts from reality.
func UpsertCatalogEntry(ctx context.Context, db *sql.DB, e CatalogEntry) error {
	if db == nil {
		return nil
	}
	if e.ID == "" {
		id, err := newCatalogID()
		if err != nil {
			return err
		}
		e.ID = id
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO backup_catalog
  (id, file_path, scope, slug, workspace_id, created_at, created_by,
   size, sha256, encrypted, format_version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(file_path) DO UPDATE SET
  scope          = excluded.scope,
  slug           = excluded.slug,
  workspace_id   = excluded.workspace_id,
  created_at     = excluded.created_at,
  created_by     = excluded.created_by,
  size           = excluded.size,
  sha256         = excluded.sha256,
  encrypted      = excluded.encrypted,
  format_version = excluded.format_version
`,
		e.ID, e.FilePath, e.Scope, e.Slug, e.WorkspaceID,
		e.CreatedAt.UTC().Format(time.RFC3339), e.CreatedBy,
		e.Size, e.SHA256, boolToInt(e.Encrypted), e.FormatVersion,
	)
	if err != nil {
		return fmt.Errorf("backup: upsert catalog: %w", err)
	}
	return nil
}

// DeleteCatalogEntry removes the row keyed by path. Safe to call for
// a bundle that was never catalogued (legacy pre-v49 ones) — the
// DELETE is a no-op then.
func DeleteCatalogEntry(ctx context.Context, db *sql.DB, path string) error {
	if db == nil {
		return nil
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM backup_catalog WHERE file_path = ?`, path); err != nil {
		return fmt.Errorf("backup: delete catalog: %w", err)
	}
	return nil
}

// ListCatalog returns the catalogued bundles sorted by created_at
// descending (newest first, matching the CLI). Optional workspaceID
// filter scopes the result to one workspace when non-empty.
func ListCatalog(ctx context.Context, db *sql.DB, workspaceID string) ([]CatalogEntry, error) {
	if db == nil {
		return nil, nil
	}
	const baseQuery = `
SELECT id, file_path, scope, COALESCE(slug, ''), COALESCE(workspace_id, ''),
       created_at, COALESCE(created_by, ''), size, sha256, encrypted, format_version
FROM backup_catalog`
	// Two distinct queries keep the driver's parameter plane typed —
	// []any with an optional first element is easy to get wrong when
	// the WHERE clause grows.
	var rows *sql.Rows
	var err error
	if workspaceID != "" {
		rows, err = db.QueryContext(ctx,
			baseQuery+` WHERE workspace_id = ? ORDER BY created_at DESC`,
			workspaceID)
	} else {
		rows, err = db.QueryContext(ctx,
			baseQuery+` ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, fmt.Errorf("backup: list catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CatalogEntry
	for rows.Next() {
		var e CatalogEntry
		var enc int
		var createdAt string
		if err := rows.Scan(&e.ID, &e.FilePath, &e.Scope, &e.Slug, &e.WorkspaceID,
			&createdAt, &e.CreatedBy, &e.Size, &e.SHA256, &enc, &e.FormatVersion); err != nil {
			return nil, fmt.Errorf("backup: scan catalog row: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("backup: parse created_at %q for %s: %w", createdAt, e.ID, err)
		}
		e.CreatedAt = parsed
		e.Encrypted = enc == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// BackfillCatalogFromDir walks the default bundles directory and
// upserts a catalog row for every bundle that ListBackups recognises.
// Used once at startup so installs upgraded to v49 see their existing
// bundles in the admin UI without a manual `backup list` run. The
// walk is idempotent via the UNIQUE(file_path) constraint, so running
// it on every boot stays cheap.
//
// Errors reading individual bundles are logged via the supplied
// logger (if any) but do not abort — a single corrupted file should
// not block startup.
func BackfillCatalogFromDir(ctx context.Context, db *sql.DB, dir string, logger func(string)) error {
	if db == nil || dir == "" {
		return nil
	}
	entries, err := ListBackups(ctx, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, le := range entries {
		manifest, err := Inspect(ctx, le.Path)
		if err != nil {
			if logger != nil {
				logger(fmt.Sprintf("backup catalog backfill: skip %s: %v", le.Path, err))
			}
			continue
		}
		entry := CatalogEntry{
			FilePath:      le.Path,
			Scope:         string(manifest.Scope),
			Size:          le.Size,
			SHA256:        manifest.Checksums.PayloadSHA256,
			Encrypted:     manifest.Encryption.Enabled,
			FormatVersion: manifest.FormatVersion,
			CreatedAt:     manifest.CreatedAt,
			CreatedBy:     manifest.CreatedBy.Email,
		}
		if manifest.Contents.Workspace != nil {
			entry.WorkspaceID = manifest.Contents.Workspace.ID
			entry.Slug = manifest.Contents.Workspace.Slug
		}
		if manifest.Scope == ScopeCrew && len(manifest.Contents.Crews) > 0 {
			entry.Slug = manifest.Contents.Crews[0].Slug
		}
		if err := UpsertCatalogEntry(ctx, db, entry); err != nil && logger != nil {
			logger(fmt.Sprintf("backup catalog backfill: upsert %s: %v", le.Path, err))
		}
	}
	return nil
}

// newCatalogID returns a random 128-bit hex string. We intentionally
// do not reuse the CUID generator from internal/api — it lives behind
// a dependency boundary we are not willing to invert.
//
// Propagates rand.Read errors rather than swallowing them: if the OS
// entropy source is unavailable we MUST abort the catalogue write —
// continuing with a fixed/empty ID risks duplicate rows and later
// constraint failures on backup_catalog.id PRIMARY KEY.
func newCatalogID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("backup: generate catalog id: %w", err)
	}
	return "bk_" + hex.EncodeToString(b[:]), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CatalogEntryFromResult builds a CatalogEntry from a completed
// CreateResult plus the manifest that was embedded in the bundle.
// Separating it from CreateBackup keeps the runner free of DB-layout
// concerns — the caller (CLI / REST handler) calls UpsertCatalogEntry
// once a successful CreateResult comes back.
func CatalogEntryFromResult(res *CreateResult, m *Manifest, _ string) CatalogEntry {
	e := CatalogEntry{
		FilePath:      res.Path,
		Scope:         string(m.Scope),
		Size:          res.Size,
		SHA256:        res.SHA256,
		Encrypted:     m.Encryption.Enabled,
		FormatVersion: m.FormatVersion,
		CreatedAt:     m.CreatedAt,
		CreatedBy:     m.CreatedBy.Email,
	}
	if m.Contents.Workspace != nil {
		e.WorkspaceID = m.Contents.Workspace.ID
		e.Slug = m.Contents.Workspace.Slug
	}
	if m.Scope == ScopeCrew && len(m.Contents.Crews) > 0 {
		e.Slug = m.Contents.Crews[0].Slug
	}
	// Bundle path base-name fallback when a crew scope bundle has no
	// crews summary for some reason — extremely unlikely but keeps the
	// catalog row non-empty in the slug slot.
	if e.Slug == "" {
		base := filepath.Base(res.Path)
		e.Slug = strings.TrimSuffix(base, ".tar.zst")
	}
	return e
}
