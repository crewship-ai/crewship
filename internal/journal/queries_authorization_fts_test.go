package journal

import (
	"strings"
	"testing"
)

func TestAuthorizationAuditFTSSummaryBoundary(t *testing.T) {
	for _, typ := range []EntryType{EntryMemberAdded, EntryMemberCapabilitiesChanged, EntryPageGrantAdded, EntryPageGrantRemoved, "page.folder_acl_changed", EntryMemberAccessChanged} {
		t.Run(string(typ), func(t *testing.T) {
			db := openTestDBWithFTS(t)
			defer db.Close()
			w := NewWriter(db, quietLogger(), WriterOptions{})
			defer w.Close()
			emit := func(workspace string, kind EntryType) string {
				id, err := w.EmitSync(t.Context(), Entry{WorkspaceID: workspace, Type: kind, ActorType: ActorUser, Summary: "publicneedle", Payload: map[string]any{"secret": "privateneedle", "subject_user_id": "publicpayloadneedle"}})
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			id := emit("ws_test", typ)
			var before string
			if err := db.QueryRow(`SELECT payload FROM journal_entries WHERE id=?`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			emit("ws_test", EntryExecCommand)
			if _, err := db.Exec(`INSERT INTO workspaces(id)VALUES('ws_other')`); err != nil {
				t.Fatal(err)
			}
			emit("ws_other", typ)
			for _, tc := range []struct {
				query string
				want  int
			}{
				{"", 1}, {"publicneedle", 1}, {"privateneedle", 0}, {"publicpayloadneedle", 0},
				{"publicneedle privateneedle", 0}, {"payload:privateneedle", 0},
				{"summary:publicneedle OR payload:privateneedle", 0}, {`" OR payload:privateneedle`, 0},
				{"NEAR(publicneedle privateneedle)", 0}, {"{payload}:privateneedle", 0}, {"-summary:privateneedle", 0},
			} {
				q := Query{WorkspaceID: "ws_test", Types: []EntryType{typ}, FTSQuery: tc.query}
				rows, _, err := List(t.Context(), db, q)
				if err != nil {
					t.Fatalf("native FTS query %q: %v", tc.query, err)
				}
				n, err := Count(t.Context(), db, q)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != tc.want || n != int64(tc.want) {
					t.Errorf("protected FTS query%q List%d/Count%d want%d", tc.query, len(rows), n, tc.want)
				}
			}
			for _, query := range []string{"privateneedle", "publicpayloadneedle", "publicneedle privateneedle"} {
				q := Query{WorkspaceID: "ws_test", Types: []EntryType{EntryExecCommand}, FTSQuery: query}
				rows, _, err := List(t.Context(), db, q)
				n, countErr := Count(t.Context(), db, q)
				if err != nil || countErr != nil || len(rows) != 1 || n != 1 {
					t.Fatalf("ordinary payload FTS changed: %q %d/%d %v %v", query, len(rows), n, err, countErr)
				}
			}
			q := Query{WorkspaceID: "ws_test", FTSQuery: "publicneedle", Limit: 1}
			first, cursor, err := List(t.Context(), db, q)
			if err != nil || len(first) != 1 || cursor == "" {
				t.Fatalf("first search page: %d %v", len(first), err)
			}
			q.Cursor = cursor
			second, _, err := List(t.Context(), db, q)
			n, countErr := Count(t.Context(), db, q)
			if err != nil || countErr != nil || len(second) != 1 || first[0].ID == second[0].ID || n != 2 {
				t.Fatalf("search pagination/count changed: %d/%d %v %v", len(second), n, err, countErr)
			}
			// applyFTS also supports callers without a workspace filter. Do not add
			// an empty-workspace predicate inside either native MATCH branch.
			global := Query{Types: []EntryType{typ}, FTSQuery: "publicneedle"}
			conds, args, from := applyFTS(global, []string{"entry_type=?"}, []any{string(typ)})
			var total int
			if err := db.QueryRow(`SELECT COUNT(*) `+from+` WHERE `+strings.Join(conds, " AND "), args...).Scan(&total); err != nil || total != 2 {
				t.Fatalf("global FTS helper scope changed: %d %v", total, err)
			}
			var after string
			if err := db.QueryRow(`SELECT payload FROM journal_entries WHERE id=?`, id).Scan(&after); err != nil || after != before {
				t.Fatal("search changed canonical audit payload")
			}
		})
	}
}
