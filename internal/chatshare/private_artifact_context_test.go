package chatshare

import (
	"errors"
	"testing"
	"time"
)

func TestPrivateArtifactConversationCannotPublishThroughChatShare(t *testing.T) {
	db := shareTestDB(t)
	store := NewStore(db)
	grant, token, err := store.Create(t.Context(), "ws", "agent", "chat", "owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-existing link must lose access before the first model output, not
	// only after a Document has been successfully created.
	for _, query := range []string{
		`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('private-artifact-consent','chat','agent','user','create document','2026-10-19T10:00:00Z','owner')`,
		`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('private-artifact-consent','ws','owner','chat','private-document','2026-10-19T10:00:00Z')`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	for _, actor := range []string{"owner", "admin"} {
		if _, _, err := store.Create(t.Context(), "ws", "agent", "chat", actor, time.Hour); !errors.Is(err, ErrDenied) {
			t.Errorf("%s published protected context: %v", actor, err)
		}
	}
	if _, err := store.Validate(t.Context(), grant.ID, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("old link retained protected context: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE document_creation_consents SET revoked_at='2026-10-19T10:01:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Validate(t.Context(), grant.ID, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoking execution consent reopened transcript: %v", err)
	}
}
