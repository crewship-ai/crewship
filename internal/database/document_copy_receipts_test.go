package database

import (
	"strings"
	"testing"
)

func TestDocumentCopyReceiptsRetainDeletedIdentitiesAndCannotBeRewritten(t *testing.T) {
	db := openMigratedTestDB(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('copy-ws','Copy','copy-ws')`)
	exec(`INSERT INTO users(id,email) VALUES('copy-user','copy-user@example.invalid')`)
	for _, id := range []string{"copy-source", "copy-result"} {
		exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,'copy-ws',?,?,'copy-user','{}')`, id, id, id)
	}
	insert := `INSERT INTO document_copy_receipts(workspace_id,user_id,mutation_id,request_digest,source_page_id,result_page_id,result_slug,created_at) VALUES('copy-ws',?,'same-action',?,'copy-source','copy-result','copy-result','2026-10-21T11:30:25Z')`
	exec(insert, "copy-user", strings.Repeat("a", 64))
	if _, err := db.Exec(insert, "copy-user", strings.Repeat("b", 64)); err == nil {
		t.Fatal("duplicate action overwrote its original copy")
	}
	if _, err := db.Exec(`UPDATE document_copy_receipts SET request_digest=?`, strings.Repeat("b", 64)); err == nil {
		t.Fatal("receipt identity can be rewritten")
	}
	exec(`DELETE FROM pages WHERE workspace_id='copy-ws'`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM document_copy_receipts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("deleting a Page freed an old action key: %d %v", n, err)
	}
	exec(insert, "another-human", strings.Repeat("c", 64))
	if err := db.QueryRow(`SELECT COUNT(*) FROM document_copy_receipts`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("action key is not human-scoped: %d %v", n, err)
	}
	exec(`DELETE FROM workspaces WHERE id='copy-ws'`)
	if err := db.QueryRow(`SELECT COUNT(*) FROM document_copy_receipts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("workspace deletion retained foreign receipts: %d %v", n, err)
	}
}
