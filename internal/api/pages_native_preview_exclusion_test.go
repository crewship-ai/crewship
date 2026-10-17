package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPreviewLegacyAttachmentRejectsNativeContent(t *testing.T) {
	for _, native := range []string{"document", "slides"} {
		for _, legacy := range []string{"artifact", "board"} {
			t.Run(native+"_rejects_"+legacy, func(t *testing.T) {
				h, _, _, workspace, user, page := artifactFixture(t)
				sentinel := []byte("private native content must remain unchanged")
				var err error
				if native == "document" {
					_, err = h.db.Exec(`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,?,3,?,?)`, workspace, page, sentinel, []byte(`[]`))
				} else {
					_, err = h.db.Exec(`INSERT INTO slides_heads(workspace_id,page_id,schema_version,head_rev,order_version,canonical_json,content_hash) VALUES(?,?,1,1,1,?,'retained-hash')`, workspace, page, sentinel)
				}
				if err != nil {
					t.Fatal(err)
				}
				var responseCode int
				var responseBody []byte
				if legacy == "artifact" {
					response := artifactSave(t, h, workspace, user, "<h1>Attempted replacement</h1>", "incompatible-native", 0)
					responseCode, responseBody = response.Code, response.Body.Bytes()
				} else {
					response := boardCall(t, h, workspace, user, "OWNER", "report", "init", `{"mutation_id":"incompatible-native"}`)
					responseCode, responseBody = response.Code, response.Body.Bytes()
				}
				if responseCode != 409 {
					t.Errorf("incompatible attachment status=%d, want409", responseCode)
				}
				if bytes.Contains(responseBody, sentinel) {
					t.Error("response disclosed private native content")
				}
				var envelope map[string]any
				if json.Unmarshal(responseBody, &envelope) != nil || envelope["error"] == nil {
					t.Error("expected a generic JSON error")
				}
				query := `SELECT snapshot FROM documents WHERE workspace_id=? AND page_id=?`
				if native == "slides" {
					query = `SELECT canonical_json FROM slides_heads WHERE workspace_id=? AND page_id=?`
				}
				var retained []byte
				if err = h.db.QueryRow(query, workspace, page).Scan(&retained); err != nil || !bytes.Equal(retained, sentinel) {
					t.Error("native content changed")
				}
				for _, table := range []string{"page_artifacts", "page_boards", "page_artifact_revisions", "document_receipts", "slides_receipts"} {
					var count int
					if err = h.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
						t.Errorf("unexpected mutation of%s: count=%d err=%v", table, count, err)
					}
				}
			})
		}
	}
}
