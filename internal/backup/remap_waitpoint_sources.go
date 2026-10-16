package backup

import (
	"encoding/json"
	"fmt"
)

// Forks need independent source identities, including the token primary key
// which the generic id-column remapper cannot see. Same-ID restores never call
// this path. Historical invocation proof and answer hashes stay unchanged;
// copying a workflow must not manufacture current execution authority.
func remapWaitpointSources(dump *DBDump, ids map[string]map[string]string) error {
	tokens := map[string]string{}
	runs := map[string]bool{}
	for _, row := range dump.Tables["pipeline_waitpoints"] {
		old, ok := row["token"].(string)
		if !ok || old == "" {
			continue
		}
		token, err := mintHexToken(32)
		if err != nil {
			return fmt.Errorf("backup: remint waitpoint identity: %w", err)
		}
		row["token"] = "tok_" + token
		tokens[old] = row["token"].(string)
		for column, table := range map[string]string{"pipeline_run_id": "pipeline_runs", "invoking_crew_id": "crews"} {
			if old, ok := row[column].(string); ok {
				if mapped := ids[table][old]; mapped != "" {
					row[column] = mapped
				}
			}
		}
		if run, ok := row["pipeline_run_id"].(string); ok {
			runs[run] = true
		}
	}
	for _, row := range dump.Tables["pipeline_runs"] {
		id, _ := row["id"].(string)
		if !runs[id] {
			continue
		}
		for column, table := range map[string]string{"invoking_agent_id": "agents", "invoking_crew_id": "crews"} {
			if old, ok := row[column].(string); ok {
				if mapped := ids[table][old]; mapped != "" {
					row[column] = mapped
				}
			}
		}
	}
	for _, row := range dump.Tables["inbox_items"] {
		if row["kind"] != "waitpoint" {
			continue
		}
		old, _ := row["source_id"].(string)
		if token := tokens[old]; token != "" {
			row["source_id"] = token
		}
		raw, _ := row["payload_json"].(string)
		var payload map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		for column, table := range map[string]string{"pipeline_run_id": "pipeline_runs", "invoking_crew_id": "crews"} {
			var old string
			if json.Unmarshal(payload[column], &old) == nil {
				if mapped := ids[table][old]; mapped != "" {
					payload[column], _ = json.Marshal(mapped)
				}
			}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("backup: remap waitpoint Inbox metadata: %w", err)
		}
		row["payload_json"] = string(encoded)
	}
	return nil
}
