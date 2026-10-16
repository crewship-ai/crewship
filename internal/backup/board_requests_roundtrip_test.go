package backup_test

import (
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/pages"
	"testing"
)

func TestBoardRequestDetachedHistoryRoundtrip(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := source.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('job-board',?,'job-board','Board','u_admin','{}')`, ws)
	exec(`INSERT INTO page_boards(page_id,seq,format) VALUES('job-board',0,?)`, pages.BoardFormat)
	for _, state := range []string{"queued", "ready_to_insert"} {
		exec(`INSERT INTO page_board_requests(id,workspace_id,board_page_id,original_board_id,human_user_id,kind,base_seq,submission_id,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,status,run_id,mermaid_source,created_at,updated_at) VALUES(?,?,'job-board','job-board','u_admin','diagram',0,?,'fingerprint','Private original instruction','[]','[]','{"elements":[],"placements":[]}','{}','mermaid','converter','LIVE RECEIPT',?,'historical-run','graph TD; A-->B','2020-01-01T00:00:00.000Z','2020-01-01T00:00:00.000Z')`, state, ws, state, state)
	}
	exec(`DELETE FROM pages WHERE id='job-board'`)
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			dump, err := backup.DumpWorkspace(t.Context(), source, ws)
			if err != nil {
				t.Fatal(err)
			}
			if len(dump.Tables["page_board_requests"]) != 2 {
				t.Fatalf("detached history not exported: %+v", dump.Tables["page_board_requests"])
			}
			if fork {
				if err = backup.RemapIDs(t.Context(), source, dump); err != nil {
					t.Fatal(err)
				}
			}
			target := openMigratedDB(t)
			if err = backup.RestoreDump(t.Context(), target, dump); err != nil {
				t.Fatal(err)
			}
			var safe, ready int
			if err = target.QueryRow(`SELECT COUNT(*) FROM page_board_requests WHERE board_page_id IS NULL AND receipt_json='' AND instruction='Private original instruction' AND run_id='historical-run' AND mermaid_source='graph TD; A-->B'`).Scan(&safe); err != nil || safe != 2 {
				t.Fatalf("history/authority restore=%d %v", safe, err)
			}
			if err = target.QueryRow(`SELECT COUNT(*) FROM page_board_requests WHERE status='ready_to_insert'`).Scan(&ready); err != nil || ready != 1 {
				t.Fatalf("ready result lost %d %v", ready, err)
			}
			if err = target.QueryRow(`SELECT COUNT(*) FROM page_board_requests WHERE status='failed'`).Scan(&safe); err != nil || safe != 1 {
				t.Fatalf("queued work resumed %d %v", safe, err)
			}
		})
	}
}
