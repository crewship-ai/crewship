package backup

import (
	"encoding/json"
	"fmt"
)

// Forks move typed object references, while run/trigger IDs retain historical
// meaning. Receipts are history, never admission grants or pending effects.
// Original request hashes remain immutable: a forked intent with changed IDs
// conflicts safely rather than replaying the source workspace's operation.
func remapChatProposals(dump *DBDump, ids map[string]map[string]string) error {
	mapped := func(table, old string) string {
		if value := ids[table][old]; value != "" {
			return value
		}
		return old
	}
	var walk func(any, string)
	walk = func(value any, parent string) {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				walk(child, parent)
			}
		case map[string]any:
			for key, child := range node {
				table := ""
				switch key {
				case "workspace_id":
					table = "workspaces"
				case "crew_id":
					table = "crews"
				case "project_id":
					table = "projects"
				case "assignee_id":
					table = "agents" // global user IDs stay stable
				case "operation_id":
					table = "chat_proposal_operations"
				case "id":
					switch parent {
					case "entity":
						table = "chat_issue_proposals"
					case "issues":
						table = "missions"
					case "actor":
						if node["type"] == "agent" {
							table = "agents"
						}
					}
				}
				if old, ok := child.(string); ok && table != "" {
					node[key] = mapped(table, old)
				} else {
					walk(child, key)
				}
			}
		}
	}
	rewrite := func(row map[string]any, field string) error {
		raw, ok := row[field].(string)
		if !ok || raw == "" {
			return nil
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return fmt.Errorf("remap proposal %s: %w", field, err)
		}
		walk(value, field)
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		row[field] = string(data)
		return nil
	}
	for _, row := range dump.Tables["agent_operations"] {
		if old, ok := row["source_proposal_id"].(string); ok {
			row["source_proposal_id"] = mapped("chat_issue_proposals", old)
		}
	}
	for _, row := range dump.Tables["chat_issue_proposals"] {
		if old, ok := row["operation_id"].(string); ok {
			row["operation_id"] = mapped("chat_proposal_operations", old)
		}
		for _, field := range []string{"items", "outcome"} {
			if err := rewrite(row, field); err != nil {
				return err
			}
		}
	}
	for _, row := range dump.Tables["chat_proposal_operations"] {
		if old, ok := row["workspace_id"].(string); ok {
			row["workspace_id"] = mapped("workspaces", old)
		}
		if row["actor_type"] == "agent" {
			if old, ok := row["actor_id"].(string); ok {
				row["actor_id"] = mapped("agents", old)
			}
		}
		if err := rewrite(row, "result"); err != nil {
			return err
		}
	}
	return nil
}
