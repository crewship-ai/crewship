package backup_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/pages"
)

func TestBoardBackupForkPinsAndDeletedSourcePlaceholder(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := source.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"board-page", "source-page"} {
		exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,'u_admin','{}')`, id, workspace, id, id)
	}
	exec(`INSERT INTO page_artifacts(page_id,head_rev) VALUES('source-page',2)`)
	for i := 1; i <= 2; i++ {
		exec(`INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES(?,'source-page',?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,'digest','2020-01-01T00:00:00.000Z')`, string(rune('a'+i)), i, string(rune('a'+i)))
	}
	exec(`INSERT INTO page_boards(page_id,seq,format) VALUES('board-page',3,?)`, pages.BoardFormat)
	fixture, err := os.ReadFile("../pages/testdata/board-excalidraw-0.18.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var elements []json.RawMessage
	if err = json.Unmarshal(fixture, &elements); err != nil {
		t.Fatal(err)
	}
	// Use a standalone valid shape rather than retaining links to the rest of
	// the browser fixture that this storage test does not insert.
	var standalone map[string]any
	if err = json.Unmarshal(elements[0], &standalone); err != nil {
		t.Fatal(err)
	}
	standalone["boundElements"] = nil
	elements[0], err = json.Marshal(standalone)
	if err != nil {
		t.Fatal(err)
	}
	var element struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(elements[0], &element); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,author_user_id,updated_seq) VALUES('board-page',?,1,?,'u_admin',1)`, element.ID, string(elements[0]))
	exec(`INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,author_user_id,updated_seq) VALUES('board-page','deleted-shape',2,NULL,'u_admin',3)`)
	exec(`INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,author_user_id) VALUES('placement','board-page','source-page',1,10,20,300,200,2,'u_admin')`)
	exec(`INSERT INTO page_board_mutations(board_page_id,mutation_id,author_user_id,fingerprint,result_seq,placement_id,created_at) VALUES('board-page','mutation','u_admin','fingerprint',2,'placement','2020-01-01T00:00:00.000Z')`)
	exec(`INSERT INTO users(id,email) VALUES('board-reader','board-reader@example.test')`)
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('board-reader-member',?,'board-reader','MEMBER')`, workspace)
	exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('board-page','user','board-reader','read','u_admin','2020-01-01T00:00:00.000Z')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('other-workspace','Other','other')`)
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('other-board','other-workspace','other-board','Other','u_admin','{}')`)
	exec(`INSERT INTO page_boards(page_id,seq,format) VALUES('other-board',0,?)`, pages.BoardFormat)
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if len(dump.Tables["page_boards"]) != 1 {
				t.Fatalf("workspace scope leaked: %v", dump.Tables["page_boards"])
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
			var boardID, sourceID, placementID, mutationPlacement, elementID, raw string
			var revision int
			if err = target.QueryRow(`SELECT p.board_page_id,p.source_page_id,p.source_revision,p.id,m.placement_id,e.element_id,e.element_json FROM page_board_artifact_placements p JOIN page_board_mutations m ON m.board_page_id=p.board_page_id JOIN page_board_elements e ON e.board_page_id=p.board_page_id AND e.element_json IS NOT NULL`).Scan(&boardID, &sourceID, &revision, &placementID, &mutationPlacement, &elementID, &raw); err != nil {
				t.Fatal(err)
			}
			if revision != 1 || placementID != mutationPlacement || elementID != element.ID || raw != string(elements[0]) {
				t.Fatalf("pin/receipt/scene links changed: %d %s %s %s", revision, placementID, mutationPlacement, elementID)
			}
			if fork && (boardID == "board-page" || sourceID == "source-page" || placementID == "placement") {
				t.Fatal("fork retained original resource IDs")
			}
			var tombstones int
			if err = target.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=? AND element_id='deleted-shape' AND element_json IS NULL AND element_rev=2`, boardID).Scan(&tombstones); err != nil || tombstones != 1 {
				t.Fatalf("board tombstone lost: %d %v", tombstones, err)
			}
			var grants int
			if err = target.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id=?`, sourceID).Scan(&grants); err != nil || grants != 0 {
				t.Fatalf("board grant copied to source: %d %v", grants, err)
			}
			if _, err = target.Exec(`DELETE FROM pages WHERE id=?`, sourceID); err != nil {
				t.Fatal(err)
			}
			var missing int
			if err = target.QueryRow(`SELECT COUNT(*) FROM page_board_artifact_placements WHERE board_page_id=? AND source_page_id IS NULL AND source_revision IS NULL AND x=10 AND width=300`, boardID).Scan(&missing); err != nil || missing != 1 {
				t.Fatalf("deleted source lost placeholder: %d %v", missing, err)
			}
		})
	}
}
