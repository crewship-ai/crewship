package backup

import "encoding/json"

// A fork is a new historical namespace. Creation run IDs participate in a
// global unique key even though their live authority is intentionally absent
// from backups. Rewrite matching provenance together; never create principals.
func remapArtifactCreationRuns(dump *DBDump) {
	runs := map[string]string{}
	for _, row := range dump.Tables["page_artifact_creations"] {
		old, _ := row["run_id"].(string)
		if old != "" {
			if _, ok := runs[old]; !ok {
				runs[old] = newRemapCUID()
			}
		}
	}
	for _, ref := range []struct{ table, column string }{
		{"page_artifact_creations", "run_id"},
		{"page_board_write_audit", "actor_run_id"},
		{"page_board_elements", "author_run_id"},
		{"page_board_mutations", "author_run_id"},
		{"page_artifact_revisions", "author_run_id"},
		{"page_artifact_mutation_receipts", "author_run_id"},
		{"page_artifact_requests", "run_id"},
		{"page_board_requests", "run_id"},
		{"journal_entries", "trace_id"},
		{"eval_runs", "trace_id"},
		{"message_feedback", "trace_id"},
	} {
		for _, row := range dump.Tables[ref.table] {
			old, _ := row[ref.column].(string)
			if mapped, ok := runs[old]; ok {
				row[ref.column] = mapped
			}
		}
	}
	// Artifact publication journal payloads carry the same run provenance as
	// author_run_id. Rewrite that structured field, never arbitrary prose.
	for _, row := range dump.Tables["journal_entries"] {
		raw, _ := row["payload"].(string)
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		old, _ := payload["run_id"].(string)
		if mapped, ok := runs[old]; ok {
			payload["run_id"] = mapped
			if encoded, err := json.Marshal(payload); err == nil {
				row["payload"] = string(encoded)
			}
		}
	}
}
