package mooring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// This engine revision shipped native JSON Sheets before the workbook package.
const legacyGridCraftRevision = "e68d192282499bed81a9800d742cb9b239d367e5"

var errWorkbookAlreadyMigrated = errors.New("Sheet already migrated")

type LegacyWorkbookSource struct {
	Version, WorkbookVersion int64
	Hash                     string
	Bytes                    []byte
}

// LegacyWorkbook reads the exact source under the current Page authority.
// After conversion the download resolves through the retained source blob.
func (s *NativeStore) LegacyWorkbook(ctx context.Context, scope EditLeaseScope, write bool) (LegacyWorkbookSource, error) {
	var result LegacyWorkbookSource
	if err := s.ready(); err != nil {
		return result, err
	}
	if !s.workbookStore() {
		return result, ErrNativeProfile
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	intent := "read"
	if write {
		intent = "write"
	}
	if _, err := s.config.Authorize(ctx, tx, scope, intent); err != nil {
		return result, err
	}
	return legacyWorkbookSource(ctx, tx, scope, true)
}

func legacyWorkbookSource(ctx context.Context, tx *sql.Tx, scope EditLeaseScope, loadBytes bool) (LegacyWorkbookSource, error) {
	var source LegacyWorkbookSource
	err := tx.QueryRowContext(ctx, `
		SELECT m.source_version,m.workbook_version,m.source_hash,CASE WHEN ?4 THEN b.content ELSE x'' END
		FROM craft_workbook_migrations m
		JOIN artifact_version_blobs b ON b.workspace_id=m.workspace_id AND b.page_id=m.page_id AND b.hash=m.source_hash
		WHERE m.workspace_id=?1 AND m.page_id=?2
		UNION ALL
		SELECT v.version,0,b.hash,CASE WHEN ?4 THEN b.content ELSE x'' END
		FROM craft_native_documents d
		JOIN artifact_version_heads h ON h.workspace_id=d.workspace_id AND h.page_id=d.page_id AND h.content_type='gridcraft'
		JOIN artifact_versions v ON v.workspace_id=h.workspace_id AND v.page_id=h.page_id AND v.version=h.latest_version
		JOIN artifact_version_blobs b ON b.workspace_id=v.workspace_id AND b.page_id=v.page_id
			AND b.hash=json_extract(v.manifest_json,'$.files."document.json".hash')
		WHERE d.workspace_id=?1 AND d.page_id=?2 AND d.application_id='gridcraft' AND d.engine_revision=?3
			AND json_extract(v.manifest_json,'$.entry_point')='document.json'
			AND json_extract(v.manifest_json,'$.files."document.json".media_type')='application/json'
			AND NOT EXISTS(SELECT 1 FROM craft_workbook_migrations m WHERE m.page_id=d.page_id)
	`, scope.WorkspaceID, scope.ArtifactID, legacyGridCraftRevision, loadBytes).
		Scan(&source.Version, &source.WorkbookVersion, &source.Hash, &source.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return LegacyWorkbookSource{}, ErrNativeProfile
	}
	if err != nil {
		return LegacyWorkbookSource{}, err
	}
	if loadBytes {
		sum := sha256.Sum256(source.Bytes)
		if len(source.Bytes) == 0 || len(source.Bytes) > 32<<20 || source.Hash != hex.EncodeToString(sum[:]) {
			return LegacyWorkbookSource{}, ErrNativeProfile
		}
	}
	return source, nil
}

// MigrateLegacyWorkbook publishes the qualified file and its profile in one
// transaction. The caller has already verified the round trip of source.Bytes.
func (s *NativeStore) MigrateLegacyWorkbook(ctx context.Context, scope EditLeaseScope, source LegacyWorkbookSource, workbook []byte) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !s.workbookStore() || source.Version <= 0 {
		return 0, ErrNativeProfile
	}
	format, err := DetectWorkbook(workbook)
	if err != nil || format != "xlsx" {
		return 0, ErrWorkbookNotWorkbook
	}
	if err := s.config.Validate(ctx, workbook); err != nil {
		return 0, err
	}
	var existing int64
	inserted := false
	a := func(ctx context.Context, tx *sql.Tx) (string, error) {
		principal, err := s.config.Authorize(ctx, tx, scope, "write")
		if err != nil {
			return "", err
		}
		if inserted {
			return principal.ActorID, s.profile(ctx, tx, scope)
		}
		live, boot, expires, err := editLeaseRow(ctx, tx, scope)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		nowBoot, now, clockErr := s.config.Leases.clock()
		if clockErr != nil {
			return "", clockErr
		}
		current, err := legacyWorkbookSource(ctx, tx, scope, false)
		if err != nil {
			return "", err
		}
		if current.Version != source.Version || current.Hash != source.Hash {
			return "", artifacts.ErrVersionConflict
		}
		if current.WorkbookVersion != 0 {
			existing = current.WorkbookVersion
			return "", errWorkbookAlreadyMigrated
		}
		if editLeaseLive(live, boot, expires, nowBoot, now) {
			return "", ErrEditLeaseBusy
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO craft_workbook_migrations
			(page_id,workspace_id,source_version,source_hash,workbook_version,engine_revision,engine_sha256,editor_bundle)
			VALUES(?,?,?,?,?,?,?,?)`, scope.ArtifactID, scope.WorkspaceID, source.Version, source.Hash,
			source.Version+1, s.config.EngineRevision, s.config.EngineSHA256, s.config.EditorBundle)
		inserted = err == nil
		return principal.ActorID, err
	}
	request := s.manifest(source.Version, workbook)
	request.Message = "Converted to a workbook file"
	receipt, err := s.versions.Apply(ctx, nativeVersionKey(scope), request, a)
	if errors.Is(err, errWorkbookAlreadyMigrated) {
		return existing, nil
	}
	return receipt.Version, err
}

// HasWorkbookMigration is metadata for an already authorized workbook read.
func (s *NativeStore) HasWorkbookMigration(ctx context.Context, scope EditLeaseScope) bool {
	if !s.Workbook() {
		return false
	}
	var found bool
	_ = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM craft_workbook_migrations WHERE workspace_id=? AND page_id=?)",
		scope.WorkspaceID, scope.ArtifactID).Scan(&found)
	return found
}
