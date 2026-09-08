package database

import (
	"fmt"
	"strings"
	"testing"
)

// A composite primary key's second column and a partial index are not enough
// for these cleanup queries, even though the generic FK inventory counts them.
func TestWorkspaceConversationCleanupIndexes(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	for _, tc := range []struct{ table, column string }{
		{"workspace_conversation_agents", "agent_id"},
		{"workspace_conversations", "workspace_id"},
	} {
		t.Run(tc.table, func(t *testing.T) {
			rows, err := db.Query(fmt.Sprintf("EXPLAIN QUERY PLAN SELECT rowid FROM %s WHERE %s=?", tc.table, tc.column), "removed-parent")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			searched := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(detail, "SEARCH") && strings.Contains(detail, tc.column+"=?") {
					searched = true
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !searched {
				t.Fatalf("parent cleanup of %s still scans %s", tc.column, tc.table)
			}
		})
	}
}
