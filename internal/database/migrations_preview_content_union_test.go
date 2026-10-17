package database

import (
	"fmt"
	"strings"
	"testing"
)

// Preview already contains published Artifact/Board tables. Joining the typed
// Docs foundation must preserve exclusivity in both directions, including moves.
func TestPreviewNativeContentExclusion(t *testing.T) {
	db := artifactsFoundationDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('union-owner','union-owner@example.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('union-ws','Union','union-ws')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	tables := map[string]string{"artifact": "page_artifacts", "board": "page_boards", "document": "documents", "slides": "slides_heads"}
	inserts := map[string]string{
		"artifact": `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`,
		"board":    `INSERT INTO page_boards(page_id,seq,format) VALUES(?,0,'crewship-board-excalidraw-0.18.1-v1')`,
		"document": `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('union-ws',?,3,X'73656e74696e656c',X'5b5d')`,
		"slides":   `INSERT INTO slides_heads(workspace_id,page_id,schema_version,head_rev,order_version,canonical_json,content_hash) VALUES('union-ws',?,1,1,1,X'7b7d','unchanged-hash')`,
	}
	for _, legacy := range []string{"artifact", "board"} {
		for _, native := range []string{"document", "slides"} {
			for _, pair := range [][2]string{{legacy, native}, {native, legacy}} {
				for _, move := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s_then_%s/move_%t", pair[0], pair[1], move), func(t *testing.T) {
						tx, err := db.Begin()
						if err != nil {
							t.Fatal(err)
						}
						defer tx.Rollback()
						for _, id := range []string{"union-target", "union-source"} {
							if _, err = tx.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,'union-ws',?,?,'union-owner','{}')`, id, id, id); err != nil {
								t.Fatal(err)
							}
						}
						if _, err = tx.Exec(inserts[pair[0]], "union-target"); err != nil {
							t.Fatal(err)
						}
						if move {
							if _, err = tx.Exec(inserts[pair[1]], "union-source"); err != nil {
								t.Fatal(err)
							}
						}
						snapshot := func() string {
							var parts []string
							for _, kind := range []string{"artifact", "board", "document", "slides"} {
								var ids string
								if e := tx.QueryRow(`SELECT COALESCE(group_concat(page_id,'|'),'') FROM (SELECT page_id FROM ` + tables[kind] + ` ORDER BY page_id)`).Scan(&ids); e != nil {
									t.Fatal(e)
								}
								parts = append(parts, kind+":"+ids)
							}
							return strings.Join(parts, ";")
						}
						before := snapshot()
						if move {
							_, err = tx.Exec(`UPDATE ` + tables[pair[1]] + ` SET page_id='union-target' WHERE page_id='union-source'`)
						} else {
							_, err = tx.Exec(inserts[pair[1]], "union-target")
						}
						if err == nil {
							t.Error("second content type must be refused")
						}
						if after := snapshot(); after != before {
							t.Error("rejected attachment changed retained content identities")
						}
						for _, table := range []string{"document_updates", "document_receipts", "slides_revisions", "slides_receipts", "page_artifact_revisions"} {
							var count int
							if e := tx.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); e != nil || count != 0 {
								t.Errorf("unexpected write to %s: count=%d err=%v", table, count, e)
							}
						}
					})
				}
			}
		}
	}
}
