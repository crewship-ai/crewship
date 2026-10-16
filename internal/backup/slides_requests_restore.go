package backup

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func selectedRestoreInt(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= float64(math.MaxInt64) || math.Trunc(v) != v {
			return 0, fmt.Errorf("backup: invalid selected request integer")
		}
		return int64(v), nil
	case json.Number:
		return v.Int64()
	default:
		return 0, fmt.Errorf("backup: selected request integer required")
	}
}

func selectedBindingRow(row map[string]any) (slides.Key, slides.StoredSelectedBinding, error) {
	key := slides.Key{}
	key.WorkspaceID, _ = row["workspace_id"].(string)
	key.PageID, _ = row["page_id"].(string)
	var binding slides.StoredSelectedBinding
	var err error
	binding.BaseRevision, err = selectedRestoreInt(row["base_rev"])
	if err != nil || binding.BaseRevision < 1 || key.WorkspaceID == "" || key.PageID == "" {
		return key, binding, fmt.Errorf("backup: invalid selected request Page/base")
	}
	schema, err := selectedRestoreInt(row["base_schema_version"])
	if err != nil || schema > math.MaxInt32 || !slides.SupportedSchema(int(schema)) {
		return key, binding, fmt.Errorf("backup: invalid selected request deck schema")
	}
	binding.BaseSchemaVersion = int(schema)
	binding.BaseContentHash, _ = row["base_content_hash"].(string)
	binding.Fingerprint, _ = row["fingerprint"].(string)
	binding.InputJSON, _ = row["input_json"].(string)
	binding.VerifiedJSON, _ = row["verified_json"].(string)
	return key, binding, nil
}

// Prepare a private copy: restore never changes the caller's original bundle.
// Runtime principals/delegations are excluded independently by backup intent.
func prepareSelectedRestoreDump(source *DBDump) (*DBDump, error) {
	if len(source.Tables["slides_requests"]) == 0 {
		return source, nil
	}
	out := *source
	out.Tables = make(map[string][]map[string]any, len(source.Tables))
	for table, rows := range source.Tables {
		out.Tables[table] = rows
	}
	for _, table := range []string{"slides_requests", "slides_request_events"} {
		out.Tables[table] = make([]map[string]any, 0, len(source.Tables[table]))
		for _, original := range source.Tables[table] {
			row := make(map[string]any, len(original))
			for key, value := range original {
				row[key] = value
			}
			out.Tables[table] = append(out.Tables[table], row)
		}
	}
	out.Tables["slides_request_receipts"] = nil
	now := tsformat.Format(time.Now())
	for _, row := range out.Tables["slides_requests"] {
		key, binding, err := selectedBindingRow(row)
		if err != nil {
			return nil, err
		}
		if _, _, err = slides.DecodeStoredSelectedBinding(key, binding); err != nil {
			return nil, fmt.Errorf("backup: invalid selected request binding: %w", err)
		}
		protocol, err := selectedRestoreInt(row["protocol_version"])
		if err != nil || protocol != slides.SelectedRequestProtocolVersion {
			return nil, fmt.Errorf("backup: unsupported selected request protocol")
		}
		version, err := selectedRestoreInt(row["version"])
		if err != nil || version < 1 || version > slides.MaxSelectedRequestEvents {
			return nil, fmt.Errorf("backup: invalid selected request version")
		}
		restored, err := selectedRestoreInt(row["restored"])
		if err != nil || (restored != 0 && restored != 1) {
			return nil, fmt.Errorf("backup: invalid selected restored marker")
		}
		status, _ := row["status"].(string)
		active := status == "queued" || status == "running"
		if !active && status != "succeeded" && status != "failed" && status != "cancelled" && status != "interrupted" {
			return nil, fmt.Errorf("backup: invalid selected request status")
		}
		if restored == 1 {
			if active || row["audience_json"] != "{}" || row["credential_id"] != "" || row["credential_hash"] != "" {
				return nil, fmt.Errorf("backup: restored selected request has live authority")
			}
			for _, column := range []string{"run_id", "human_user_id", "agent_id", "chat_id"} {
				if row[column] != nil && row[column] != "" {
					return nil, fmt.Errorf("backup: restored selected request has live identity")
				}
			}
		}
		if active {
			if version >= slides.MaxSelectedRequestEvents {
				return nil, fmt.Errorf("backup: selected terminal event budget exceeded")
			}
			row["status"], row["version"], row["error"], row["updated_at"] = "interrupted", version+1, slides.SelectedRestoreMessage, now
			out.Tables["slides_request_events"] = append(out.Tables["slides_request_events"], map[string]any{"workspace_id": key.WorkspaceID, "page_id": key.PageID, "request_id": row["id"], "version": version + 1, "kind": "interrupted", "actor": "system:backup-restore", "detail": slides.SelectedRestoreMessage, "mutation_id": nil, "created_at": now})
		}
		row["restored"], row["run_id"], row["human_user_id"], row["agent_id"], row["chat_id"] = int64(1), nil, nil, nil, nil
		row["audience_json"], row["credential_id"], row["credential_hash"] = "{}", "", ""
	}
	return &out, nil
}
