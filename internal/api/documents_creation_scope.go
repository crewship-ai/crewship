package api

import (
	"context"
	"database/sql"
)

// Standalone creation cannot reuse a selected-job principal. The source scope
// checks are retained for installed selected-request schemas; an absent feature
// has no selected authority, while an unsupported installed schema fails closed.
func (h *PageHandler) checkDocumentCreationRunScope(ctx context.Context, a *artifactRunAuthority) error {
	q := h.pageAuthReader(ctx)
	var standalone bool
	if err := q.QueryRowContext(ctx, `SELECT slides_request_origin IS NULL FROM artifact_run_principals WHERE run_id=?`, a.RunID).Scan(&standalone); err != nil || !standalone {
		return errDocumentAccess
	}
	for _, feature := range []struct {
		table     string
		required  []string
		predicate string
	}{
		{"slides_requests", []string{"id", "page_id", "original_run_id"}, "original_run_id=?"},
		{"document_requests", []string{"id", "page_id", "run_id"}, "run_id=?"},
		{"page_artifact_run_delegations", []string{"page_id", "run_id"}, ""},
	} {
		columns, present, err := documentCreationScopeColumns(ctx, q, feature.table)
		if err != nil {
			return errDocumentAccess
		}
		if !present {
			continue
		}
		for _, name := range feature.required {
			if !columns[name] {
				return errDocumentAccess
			}
		}
		predicates := []string{feature.predicate}
		if feature.table == "page_artifact_run_delegations" {
			predicates = nil
			for _, column := range []string{"slides_request_id", "document_request_id"} {
				if columns[column] {
					predicates = append(predicates, "run_id=? AND "+column+" IS NOT NULL")
				}
			}
		}
		for _, predicate := range predicates {
			var associated bool
			// Table and column names come exclusively from the fixed source-feature list.
			if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+feature.table+" WHERE "+predicate+")", a.RunID).Scan(&associated); err != nil || associated {
				return errDocumentAccess
			}
		}
	}
	return nil
}

func documentCreationScopeColumns(ctx context.Context, q pageAuthQueryer, table string) (map[string]bool, bool, error) {
	var kind string
	err := q.QueryRowContext(ctx, `SELECT type FROM sqlite_schema WHERE name=? COLLATE NOCASE AND type IN ('table','view')`, table).Scan(&kind)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if kind != "table" {
		return nil, true, errDocumentAccess
	}
	rows, err := q.QueryContext(ctx, `SELECT lower(name) FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, true, err
		}
		columns[name] = true
	}
	return columns, true, rows.Err()
}
