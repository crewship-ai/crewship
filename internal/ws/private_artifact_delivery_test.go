package ws

import "testing"

func TestPrivateArtifactConsentFencesExistingDelivery(t *testing.T) {
	db, _ := deliveryAuthorizationFixture(t, 3, 1)
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE workspace_members SET role='ADMIN' WHERE user_id='bench-user-1'`)
	exec(`UPDATE workspace_members SET role='OWNER' WHERE user_id='bench-user-2'`)
	auth := NewDBChannelAuthorizer(db)
	for _, channel := range []string{"session:bench-chat-0-0", "chat-hints:bench-chat-0-0"} {
		if ok, err := auth.CanDeliver(t.Context(), "bench-user-1", channel); err != nil || !ok {
			t.Fatalf("ordinary operator baseline: %v %v", ok, err)
		}
	}
	exec(`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('delivery-consent','bench-chat-0-0','bench-agent-0','user','create document','2026-10-19T10:00:00Z','bench-user-0')`)
	exec(`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('delivery-consent','bench-workspace-0','bench-user-0','bench-chat-0-0','create-document','2026-10-19T10:00:00Z')`)
	for _, channel := range []string{"session:bench-chat-0-0", "chat-hints:bench-chat-0-0"} {
		if ok, err := auth.CanDeliver(t.Context(), "bench-user-1", channel); err != nil || ok {
			t.Errorf("preexisting ADMIN delivery retained private output: %v %v", ok, err)
		}
		for _, user := range []string{"bench-user-0", "bench-user-2"} {
			if ok, err := auth.CanDeliver(t.Context(), user, channel); err != nil || !ok {
				t.Errorf("authorized delivery %s: %v %v", user, ok, err)
			}
		}
	}
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND user_id='bench-user-2' AND entity_type='chat' AND entity_id='bench-chat-0-0'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("OWNER before-frame inspection audit count=%d: %v", n, err)
	}
	exec(`CREATE TRIGGER reject_artifact_context_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if ok, err := auth.CanDeliver(t.Context(), "bench-user-2", "session:bench-chat-0-0"); ok || err == nil {
		t.Fatalf("OWNER private frame escaped failed audit: %v %v", ok, err)
	}
}
