package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ErrPrivateArtifactTargetReplacement prevents restored SQL from discarding
// audience proof for newer private output retained outside the backup stores.
var ErrPrivateArtifactTargetReplacement = errors.New("backup: target contains retained private artifact conversations; restore into a separate empty target")

type privateArtifactTargetReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// workspaceIDs is empty only for offline recovery, which replaces the whole DB.
// Inspect optional source tables as well as the durable marker: older targets
// can still carry consent or creation proof before their marker migration.
func rejectPrivateArtifactTargetReplacement(ctx context.Context, target privateArtifactTargetReader, workspaceIDs []string) error {
	if err := rejectMooringDeliveryTargetReplacement(ctx, target, workspaceIDs); err != nil {
		return err
	}
	scope := ""
	args := make([]any, len(workspaceIDs))
	if len(workspaceIDs) > 0 {
		scope = " AND " + equalityOrIN(quoteIdent("workspace_id"), len(workspaceIDs))
		for i, id := range workspaceIDs {
			args[i] = id
		}
	}
	var marker bool
	if err := target.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('chats') WHERE name='private_artifact_author_user_id')`).Scan(&marker); err != nil {
		return err
	}
	if marker {
		var protected bool
		if err := target.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chats WHERE private_artifact_author_user_id IS NOT NULL`+scope+`)`, args...).Scan(&protected); err != nil {
			return err
		}
		if protected {
			return ErrPrivateArtifactTargetReplacement
		}
	}
	for _, table := range []string{"document_creation_consents", "document_creations"} {
		var exists bool
		if err := target.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		var protected bool
		if err := target.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+quoteIdent(table)+` WHERE 1=1`+scope+`)`, args...).Scan(&protected); err != nil {
			return err
		}
		if protected {
			return ErrPrivateArtifactTargetReplacement
		}
	}
	return nil
}

// Runtime delivery metadata is deliberately absent from portable bundles.
// Dropping the target's rows cannot withdraw a lease already held by a runtime,
// and would erase the expiry evidence needed by subsequent revocation barriers.
// Editor lease rows also retain non-ABA generation tombstones after release.
// Refuse replacement until a qualified retirement protocol can prove it safe.
var ErrMooringDeliveryTargetReplacement = errors.New("backup: target contains issued Mooring delivery metadata; restore into a separate empty target")

func rejectMooringDeliveryTargetReplacement(ctx context.Context, target privateArtifactTargetReader, workspaceIDs []string) error {
	args := make([]any, len(workspaceIDs))
	scope := ""
	if len(workspaceIDs) > 0 {
		scope = " WHERE " + equalityOrIN(quoteIdent("workspace_id"), len(workspaceIDs))
		for i, id := range workspaceIDs {
			args[i] = id
		}
	}
	for _, table := range []string{"mooring_delivery_gates", "mooring_delivery_barriers", "mooring_edit_leases", "mooring_edit_lease_workspaces"} {
		var exists bool
		if err := target.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		var issued bool
		if err := target.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+quoteIdent(table)+scope+`)`, args...).Scan(&issued); err != nil {
			return err
		}
		if issued {
			return ErrMooringDeliveryTargetReplacement
		}
	}
	return nil
}

// Preflight before service maintenance or file preparation. The writer transaction
// repeats this check immediately before deleting rows to close admission races.
func rejectPrivateArtifactWorkspaceReplacement(ctx context.Context, db *sql.DB, bundleID, bundleSlug string, discardWorkbooks bool) error {
	if bundleID == "" {
		return fmt.Errorf("backup: --replace requires the bundle to carry a workspace row; this one does not")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids, err := resolveTargetWorkspaceIDs(ctx, tx, bundleID, bundleSlug)
	if err != nil || len(ids) == 0 {
		return err
	}
	if err := rejectPrivateArtifactTargetReplacement(ctx, tx, ids); err != nil {
		return err
	}
	if discardWorkbooks {
		return nil
	}
	workbooks, err := targetWorkbookPages(ctx, tx, ids)
	if err != nil {
		return err
	}
	if len(workbooks) > 0 {
		return workbookReplacementError(workbooks)
	}
	return nil
}

// ErrWorkbookTargetReplacement refuses a replacement that would destroy
// experimental workbooks. No bundle contains a workbook, so the loss would be
// permanent; the operator acknowledges it with --discard-workbooks.
var ErrWorkbookTargetReplacement = errors.New("backup: target holds workbooks, which backups never contain")

type workbookTargetQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// targetWorkbookPages lists the IDs of the Pages holding a workbook in the
// target workspaces. An installation without the workbook schema has none.
// The IDs are opaque on purpose: a slug is usually derived from a private
// workbook's title, and restore is an instance-admin operation.
func targetWorkbookPages(ctx context.Context, q workbookTargetQuerier, workspaceIDs []string) ([]string, error) {
	if len(workspaceIDs) == 0 {
		return nil, nil
	}
	return workbookPageIDs(ctx, q, workspaceIDs)
}

// workbookPageIDs lists workbook Page IDs in workspaceIDs, or in the whole
// database when workspaceIDs is empty (offline recovery replaces all of it).
func workbookPageIDs(ctx context.Context, q workbookTargetQuerier, workspaceIDs []string) ([]string, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='gridcraft_heads')`).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	scope := "1=1"
	args := make([]any, len(workspaceIDs))
	if len(workspaceIDs) > 0 {
		scope = equalityOrIN("h.workspace_id", len(workspaceIDs))
		for i, id := range workspaceIDs {
			args[i] = id
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT h.page_id FROM gridcraft_heads h WHERE `+scope+` ORDER BY h.page_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func workbookReplacementError(ids []string) error {
	return workbookDestructionError(ids, "--replace", "restore under a new slug with --as-workspace, or acknowledge the loss with --discard-workbooks (API: discard_workbooks)")
}

func workbookDestructionError(ids []string, operation, remedy string) error {
	const shown = 20
	named := ids
	more := ""
	if len(named) > shown {
		named = named[:shown]
		more = fmt.Sprintf(" and %d more", len(ids)-shown)
	}
	return fmt.Errorf("%w; %s would destroy them (workbook page IDs: %s%s). Save their contents first (crewship workbook read), %s", ErrWorkbookTargetReplacement, operation, strings.Join(named, ", "), more, remedy)
}

// Recovery is offline. Inspect the existing database before prepareDataDir can
// remove it; do not migrate it or follow a database symlink during this check.
// A forced recovery replaces the whole database, so every workbook on the
// target would be lost unless discardWorkbooks acknowledges it.
func rejectPrivateArtifactRecoveryTarget(ctx context.Context, dataDir string, discardWorkbooks bool) error {
	path := filepath.Join(dataDir, "crewship.db")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect target database: %v", ErrPrivateArtifactTargetReplacement, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: target database is not a regular file", ErrPrivateArtifactTargetReplacement)
	}
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(5000)"}}
	uri.RawQuery = query.Encode()
	target, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return fmt.Errorf("%w: cannot inspect target database: %v", ErrPrivateArtifactTargetReplacement, err)
	}
	defer target.Close()
	target.SetMaxOpenConns(1)
	if err := rejectPrivateArtifactTargetReplacement(ctx, target, nil); err != nil {
		if errors.Is(err, ErrPrivateArtifactTargetReplacement) {
			return err
		}
		return fmt.Errorf("%w: cannot verify target classification: %v", ErrPrivateArtifactTargetReplacement, err)
	}
	if discardWorkbooks {
		return nil
	}
	workbooks, err := workbookPageIDs(ctx, target, nil)
	if err != nil {
		return fmt.Errorf("%w: cannot inspect target workbooks: %v", ErrWorkbookTargetReplacement, err)
	}
	if len(workbooks) > 0 {
		return workbookDestructionError(workbooks, "recover --force", "or acknowledge the loss with --discard-workbooks")
	}
	return nil
}
