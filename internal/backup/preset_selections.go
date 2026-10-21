package backup

import (
	"context"
	"database/sql"
)

// Preserve only selectors actually referenced by this workspace, including
// historical operators no longer in its roster. Never export all global users.
func includePresetSelectionUsers(ctx context.Context, tx *sql.Tx, workspace, where string, args []any) (string, []any, error) {
	exists, err := tableExists(ctx, tx, "access_preset_selections")
	if err != nil || !exists {
		return where, args, err
	}
	return "(" + where + `) OR id IN (SELECT selected_by FROM access_preset_selections WHERE workspace_id=? AND selected_by IS NOT NULL)`, append(args, workspace), nil
}

// A custom category subset can omit a choice's resource. Drop only dangling
// informational labels; never change canonical access_grants or add categories.
func dropDanglingPresetSelections(dump *DBDump) {
	ids := map[string]map[string]bool{}
	present := func(table string, value any) bool {
		id, ok := value.(string)
		if !ok {
			return false
		}
		if _, known := ids[table]; !known {
			ids[table] = map[string]bool{}
			for _, row := range dump.Tables[table] {
				if parent, ok := row["id"].(string); ok {
					ids[table][parent] = true
				}
			}
		}
		return ids[table][id]
	}
	rows, ok := dump.Tables["access_preset_selections"]
	if !ok {
		return
	}
	kept := []map[string]any{}
	for _, row := range rows {
		resourcePresent := row["resource_kind"] == "project" && present("projects", row["project_id"]) || row["resource_kind"] == "routine" && present("pipelines", row["routine_id"])
		if resourcePresent && present("workspace_members", row["member_id"]) && present("workspaces", row["workspace_id"]) && (row["selected_by"] == nil || present("users", row["selected_by"])) {
			kept = append(kept, row)
		}
	}
	dump.Tables["access_preset_selections"] = kept
}
