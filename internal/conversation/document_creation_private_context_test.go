package conversation

import (
	"github.com/crewship-ai/crewship/internal/chataudience"
	"testing"
)

// A trusted operator's ordinary chat access is not authority to create a
// private output inside another human's conversation.
func TestDocumentCreationConsentRequiresConversationAuthor(t *testing.T) {
	s, _, intent, msg := creationStore(t)
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('foreign-admin','foreign-admin@example.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('foreign-admin-member','creation-ws','foreign-admin','ADMIN')`,
	} {
		if _, err := s.db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), s.db, "creation-chat", "foreign-admin", nil)
	if err != nil || receipt == nil {
		t.Fatalf("ordinary operator baseline: %v %v", receipt, err)
	}
	msg.AuthorUserID = receipt.UserID
	if err := s.AppendDocumentCreation(t.Context(), receipt.ChatID, msg, intent, receipt); err == nil {
		t.Fatal("operator minted private output consent inside another author's conversation")
	}
	if n := creationCount(t, s.db, `SELECT COUNT(*) FROM document_creation_consents`); n != 0 {
		t.Fatal("foreign conversation consent persisted")
	}
}
