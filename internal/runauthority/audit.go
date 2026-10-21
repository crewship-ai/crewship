package runauthority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// OriginAudit is a read-only inventory, not an execution authorization or an
// exact deployment-impact prediction. Counts cover active candidate records;
// classifications cover only the bounded inspected sample.
type OriginAudit struct {
	ReadOnly      bool                `json:"read_only"`
	Records       []OriginAuditRecord `json:"records"`
	Counts        []OriginAuditCount  `json:"counts"`
	MissingTables []string            `json:"missing_tables"`
	Truncated     bool                `json:"truncated"`
}
type OriginAuditRecord struct {
	WorkspaceID    string `json:"workspace_id"`
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	Classification string `json:"classification"`
	Reason         string `json:"reason"`
}
type OriginAuditCount struct {
	WorkspaceID    string `json:"workspace_id"`
	Kind           string `json:"kind"`
	Candidates     int64  `json:"candidates"`
	Inspected      int64  `json:"inspected"`
	Blocked        int64  `json:"blocked"`
	NeedsReview    int64  `json:"needs_review"`
	ReceiptCurrent int64  `json:"receipt_current"`
}

// AuditExecutionOrigins accepts a read-only connection and never migrates,
// updates statuses, captures authority or re-admits work. Missing provenance
// columns are explicitly review cases, rather than inferred autonomy.
func AuditExecutionOrigins(ctx context.Context, db *sql.DB, limit int) (OriginAudit, error) {
	out := OriginAudit{ReadOnly: true, Records: []OriginAuditRecord{}, Counts: []OriginAuditCount{}, MissingTables: []string{}}
	if limit < 1 || limit > 1000 {
		return out, fmt.Errorf("execution origin audit limit must be 1–1000")
	}
	sources := []struct{ table, predicate string }{
		{"assignments", "status IN ('PENDING','QUEUED','RUNNING')"},
		{"missions", "mission_type='orchestration' AND status NOT IN ('COMPLETED','FAILED','CANCELLED')"},
		{"pipeline_runs", "status IN ('queued','running','waiting','interrupted')"},
		{"pending_runs", "(status='pending' OR (status='fired' AND COALESCE(fired_run_id,'')=''))"},
		{"recurring_issues", "enabled=1"},
	}
	originColumns, err := auditTableColumns(ctx, db, "execution_origins")
	if err != nil {
		return out, err
	}
	originTable := len(originColumns) > 0
	for _, source := range sources {
		columns, err := auditTableColumns(ctx, db, source.table)
		if err != nil {
			return out, err
		}
		if len(columns) == 0 {
			out.MissingTables = append(out.MissingTables, source.table)
			continue
		}
		if !columns["workspace_id"] || !columns["id"] {
			return out, fmt.Errorf("execution origin audit unsupported schema: %s", source.table)
		}
		rows, err := db.QueryContext(ctx, "SELECT workspace_id,COUNT(*) FROM "+source.table+" WHERE "+source.predicate+" GROUP BY workspace_id ORDER BY workspace_id")
		if err != nil {
			return out, err
		}
		indexes := map[string]int{}
		for rows.Next() {
			var count OriginAuditCount
			count.Kind = source.table
			if err = rows.Scan(&count.WorkspaceID, &count.Candidates); err != nil {
				rows.Close()
				return out, err
			}
			indexes[count.WorkspaceID] = len(out.Counts)
			out.Counts = append(out.Counts, count)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return out, err
		}
		rows.Close()
		remaining := limit - len(out.Records)
		if remaining == 0 {
			for _, index := range indexes {
				if out.Counts[index].Candidates > 0 {
					out.Truncated = true
				}
			}
			continue
		}
		origin := "''"
		if columns["chat_origin_id"] {
			origin = "COALESCE(chat_origin_id,'')"
		}
		rows, err = db.QueryContext(ctx, "SELECT id,workspace_id,"+origin+" FROM "+source.table+" WHERE "+source.predicate+" ORDER BY workspace_id,id LIMIT ?", remaining+1)
		if err != nil {
			return out, err
		}
		type candidate struct{ id, ws, origin string }
		candidates := []candidate{}
		for rows.Next() {
			var c candidate
			if err = rows.Scan(&c.id, &c.ws, &c.origin); err != nil {
				rows.Close()
				return out, err
			}
			candidates = append(candidates, c)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return out, err
		}
		rows.Close()
		if len(candidates) > remaining {
			out.Truncated = true
			candidates = candidates[:remaining]
		}
		for _, c := range candidates {
			record := OriginAuditRecord{WorkspaceID: c.ws, Kind: source.table, ID: c.id, Classification: "needs_review", Reason: "null_origin_autonomy_unverified"}
			switch {
			case !columns["chat_origin_id"]:
				record.Reason = "pre_migration_origin_unknown"
				if source.table == "recurring_issues" {
					record.Classification = "blocked"
					record.Reason = "recurring_agent_execution_requires_reauthorization_after_upgrade"
				}
			case c.origin == "":
				if source.table == "recurring_issues" {
					record.Classification = "blocked"
					record.Reason = "recurring_agent_execution_origin_missing"
				}
			case !originTable:
				record.Reason = "origin_schema_unavailable"
			default:
				_, validationErr := ValidateOriginReceipt(ctx, db, c.origin, c.ws)
				switch {
				case validationErr == nil:
					record.Classification = "receipt_current"
					record.Reason = "executor_and_action_permissions_not_checked"
				case errors.Is(validationErr, ErrRequesterStale):
					record.Classification = "blocked"
					record.Reason = "requester_authority_stale"
				case errors.Is(validationErr, ErrUnavailable) || errors.Is(validationErr, ErrIdentityMismatch):
					record.Classification = "blocked"
					record.Reason = "origin_missing_or_invalid"
				default:
					if ctx.Err() != nil {
						return out, ctx.Err()
					}
					record.Reason = "origin_validation_unavailable"
				}
			}
			out.Records = append(out.Records, record)
			index := indexes[c.ws]
			count := &out.Counts[index]
			count.Inspected++
			switch record.Classification {
			case "blocked":
				count.Blocked++
			case "receipt_current":
				count.ReceiptCurrent++
			default:
				count.NeedsReview++
			}
		}
	}
	return out, nil
}
func auditTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}
