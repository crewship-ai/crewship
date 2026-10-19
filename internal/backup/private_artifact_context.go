package backup

import (
	"context"
	"database/sql"
	"errors"
)

const privateArtifactMarker = "private_artifact_author_user_id"

// Old artifact-era bundles excluded runtime consents. Without the retained
// classification even an unsuccessful creation cannot be distinguished from
// an ordinary chat. Refuse before any restore hook or data mutation.
func validatePrivateArtifactContextDump(dump *DBDump) error {
	if dump == nil {
		return nil
	}
	if _, artifactEra := dump.Tables["document_creations"]; !artifactEra {
		return nil
	}
	for _, row := range dump.Tables["chats"] {
		value, present := row[privateArtifactMarker]
		if !present {
			return errors.New("backup: private artifact conversation classification is missing; upgrade the source and create a fresh bundle")
		}
		if value != nil {
			if _, ok := value.(string); !ok {
				return errors.New("backup: invalid private artifact conversation classification")
			}
		}
	}
	return nil
}

// Recovery may defer encryption-dependent migrations. It must not discard the
// only source privacy proof before the classification backfill can run.
func preservePrivateArtifactContextProof(ctx context.Context, db *sql.DB) error {
	var table bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='document_creation_consents')`).Scan(&table); err != nil {
		return err
	}
	if !table {
		return nil
	}
	var column bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('chats') WHERE name='private_artifact_author_user_id')`).Scan(&column); err != nil {
		return err
	}
	query := `SELECT COUNT(*) FROM document_creation_consents`
	if column {
		query += ` s JOIN chats c ON c.id=s.chat_id WHERE c.private_artifact_author_user_id IS NULL`
	}
	var count int
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("backup: apply pending private artifact conversation classification migration with the original vault key before clearing runtime authority")
	}
	return nil
}
