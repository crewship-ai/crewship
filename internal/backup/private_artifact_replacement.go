package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

// Recovery is offline. Inspect the existing database before prepareDataDir can
// remove it; do not migrate it or follow a database symlink during this check.
func rejectPrivateArtifactRecoveryTarget(ctx context.Context, dataDir string) error {
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
	return nil
}
