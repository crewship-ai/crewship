package backup

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
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
		token, err := mintHexToken(16)
		if err != nil {
			return fmt.Errorf("backup: remint waitpoint identity: %w", err)
		}
		// A public waitpoint id (#1910), not a capability. The fork does not
		// inherit the source's callback capability either (#2274's rule for
		// every capability table): the digest is dropped, so no URL that
		// completes the source's gate completes the copy, and the unique
		// digest index cannot collide when the fork lands beside its source.
		row["token"] = pipeline.WaitpointIDPrefix + token
		row["callback_token_hash"] = nil
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

// normalizeLegacyWaitpointRestore preserves identity across a boot migration
// and repeated same-ID restores. Make copies so a failed restore leaves the
// caller's decoded bundle intact and can be retried.
func normalizeLegacyWaitpointRestore(dump *DBDump) *DBDump {
	return normalizeWaitpointIdentities(dump, true)
}

func normalizeWaitpointIdentities(dump *DBDump, restore bool) *DBDump {
	normalized := *dump
	normalized.Tables = maps.Clone(dump.Tables)
	for _, table := range []string{"pipeline_waitpoints", "inbox_items", "inbox_item_reads"} {
		if rows, ok := dump.Tables[table]; ok {
			normalized.Tables[table] = append([]map[string]any(nil), rows...)
		}
	}
	for i, row := range normalized.Tables["pipeline_waitpoints"] {
		old, _ := row["token"].(string)
		ws, _ := row["workspace_id"].(string)
		if old == "" || ws == "" {
			continue
		}
		updated := maps.Clone(row)
		if !strings.HasPrefix(old, pipeline.WaitpointIDPrefix) {
			id := pipeline.LegacyWaitpointPublicID(ws, old)
			updated["token"] = id
		}
		// Restoring an older snapshot must not reactivate a rotated callback.
		// Additive restore leaves the digest of an existing current row alone.
		if restore || old != updated["token"] {
			updated["callback_token_hash"] = nil
		}
		normalized.Tables["pipeline_waitpoints"][i] = updated
	}
	inboxIDs := map[string]string{}
	for i, row := range normalized.Tables["inbox_items"] {
		if row["kind"] != "waitpoint" {
			continue
		}
		old, _ := row["source_id"].(string)
		ws, _ := row["workspace_id"].(string)
		id := pipeline.PublicWaitpointID(ws, old)
		if id == "" || id == old {
			continue
		}
		updated := maps.Clone(row)
		updated["source_id"] = id
		if oldID, ok := row["id"].(string); ok {
			newID := strings.ReplaceAll(oldID, old, id)
			updated["id"] = newID
			inboxIDs[oldID] = newID
		}
		normalized.Tables["inbox_items"][i] = updated
	}
	for i, row := range normalized.Tables["inbox_item_reads"] {
		if oldID, ok := row["inbox_item_id"].(string); ok && inboxIDs[oldID] != "" {
			updated := maps.Clone(row)
			updated["inbox_item_id"] = inboxIDs[oldID]
			normalized.Tables["inbox_item_reads"][i] = updated
		}
	}
	return &normalized
}
