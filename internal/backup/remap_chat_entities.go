package backup

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

// remapChatEntityHistory moves historical object/actor references with a fork.
// Run and trigger message IDs retain their historical meaning; imported
// operation authority is explicitly removed rather than admitted on this host.
func remapChatEntityHistory(dump *DBDump, ids map[string]map[string]string) {
	// Keep the typed origin and its already-remapped chat/workspace edges, but
	// never transplant an admission receipt. A present invalid origin must fail
	// closed in the execution resolver rather than become autonomous work.
	for _, row := range dump.Tables["execution_origins"] {
		row["human_authority"] = "{}"
	}

	for _, row := range dump.Tables["agent_operations"] {
		if row["kind"] == "issue" {
			if old, ok := row["entity_id"].(string); ok {
				if newID := ids["missions"][old]; newID != "" {
					row["entity_id"] = newID
				}
			}
		}
		if row["actor_type"] == "agent" {
			if old, ok := row["actor_id"].(string); ok {
				if newID := ids["agents"][old]; newID != "" {
					row["actor_id"] = newID
				}
			}
		}
		row["human_authority"] = "{}"
		raw, _ := row["access_snapshot"].(string)
		snapshot, e := chatentities.ParseDeletionAccessSnapshot(json.RawMessage(raw))
		if e != nil || !uniqueSnapshotKeys(raw) {
			row["access_snapshot"] = "{}"
			continue
		}
		workspace := ids["workspaces"][snapshot.WorkspaceID]
		if workspace == "" || row["workspace_id"] != workspace {
			row["access_snapshot"] = "{}"
			continue
		}
		snapshot.WorkspaceID = workspace
		b, _ := json.Marshal(snapshot)
		row["access_snapshot"] = string(b)
	}
}

func uniqueSnapshotKeys(raw string) bool {
	d := json.NewDecoder(bytes.NewBufferString(raw))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		tok, e = d.Token()
		if e != nil {
			return false
		}
		key, ok := tok.(string)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
	}
	return len(seen) == 3
}

// Reallocated local sequences must retain the source's chronological order.
// Copy the slice so restoring never mutates the caller's bundle.
func orderedChatEntityRefs(rows []map[string]any) []map[string]any {
	ordered := append([]map[string]any(nil), rows...)
	sequence := func(row map[string]any) int64 {
		switch value := row["chat_seq"].(type) {
		case int64:
			return value
		case int:
			return int64(value)
		case float64:
			return int64(value)
		case json.Number:
			n, _ := value.Int64()
			return n
		default:
			return 0
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return sequence(ordered[i]) < sequence(ordered[j]) })
	return ordered
}
