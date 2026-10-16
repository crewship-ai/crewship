package api

import "testing"

func TestDocumentRequestHistorySurvivesIdentityDeletion(t *testing.T) {
	for _, identity := range []struct{ table, id, column string }{{"chats", "artifact-chat", "chat_id"}, {"agents", "artifact-agent", "agent_id"}, {"users", "former-doc-human", "human_user_id"}} {
		t.Run(identity.table, func(t *testing.T) {
			h, workspace, user, page := documentRequestFixture(t)
			id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
			execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('former-doc-human','former-doc@example.test','Former human')`)
			execOrFatal(t, h.db, `UPDATE document_requests SET human_user_id='former-doc-human',status='succeeded',result_revision=4,checkpoint_id='retained-result',run_id='historical-run' WHERE id=?`, id)
			execOrFatal(t, h.db, `INSERT INTO document_checkpoints(workspace_id,page_id,id,rev,schema_version,snapshot,projection,actor,created_at) VALUES(?,?,'retained-result',4,2,X'01','{}','user:former-doc-human','2026-10-15T18:00:00Z')`, workspace, page)
			for _, state := range []string{"queued", "running"} {
				execOrFatal(t, h.db, `INSERT INTO document_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,receipt_json,status,run_id,created_at,updated_at) VALUES(?,?,'former-doc-human','artifact-chat','artifact-agent',0,?,'digest','Pending work','LIVE AUTHORITY',?,'active-run','2026-10-15T18:00:00Z','2026-10-15T18:00:00Z')`, state, page, state, state)
			}
			execOrFatal(t, h.db, `DELETE FROM `+identity.table+` WHERE id=?`, identity.id)
			var count int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_requests WHERE id=? AND status='succeeded' AND result_revision=4 AND checkpoint_id='retained-result' AND run_id='historical-run' AND `+identity.column+` IS NULL`, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("result history lost=%d: %v", count, err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_checkpoints WHERE page_id=? AND id='retained-result' AND actor='user:former-doc-human'`, page).Scan(&count); err != nil || count != 1 {
				t.Fatalf("checkpoint attribution lost=%d: %v", count, err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_requests WHERE id IN ('queued','running') AND status='failed' AND run_id IS NULL AND receipt_json='{}' AND ` + identity.column + ` IS NULL`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("deleted identity work remains active=%d: %v", count, err)
			}
		})
	}
}
