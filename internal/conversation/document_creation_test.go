package conversation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Use the migrated schema and a real captured receipt, without HTTP admission.
func creationStore(t *testing.T) (*Store, *chataudience.Receipt, documents.CreationIntent, Message) {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('creation-ws','Workspace','creation-ws')`,
		`INSERT INTO users(id,email) VALUES('creation-human','creation@example.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('creation-member','creation-ws','creation-human','MEMBER')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('creation-agent','creation-ws','Agent','creation-agent')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('creation-chat','creation-ws','creation-agent','creation-human','private')`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), db, "creation-chat", "creation-human", nil)
	if err != nil || receipt == nil {
		t.Fatalf("capture: %+v %v", receipt, err)
	}
	s := NewStore(t.TempDir(), logging.New("error", "json", nil), WithDB(db))
	t.Cleanup(s.Close)
	intent := documents.CreationIntent{SubmissionID: "explicit-consent"}
	msg := Message{ID: "creation-message", AgentID: receipt.AgentID, Role: RoleUser, Content: "Write a complete document", AuthorUserID: receipt.UserID, Metadata: map[string]any{documents.CreationMetadataKey: map[string]any{"submission_id": intent.SubmissionID}}}
	return s, receipt, intent, msg
}

func creationCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDocumentCreationTrustedAppendAndPersistence(t *testing.T) {
	s, r, intent, msg := creationStore(t)
	ordinary := msg
	ordinary.ID = "ordinary-message"
	if err := s.Append(t.Context(), r.ChatID, ordinary); err != nil {
		t.Fatal(err)
	}
	if n := creationCount(t, s.db, `SELECT COUNT(*) FROM document_creation_consents`); n != 0 {
		t.Fatal("ordinary metadata minted consent")
	}
	msg.ChatID = "untrusted-wrong-chat"
	if err := s.AppendDocumentCreation(t.Context(), r.ChatID, msg, intent, r); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Read(t.Context(), r.ChatID, 0, 0)
	if err != nil || len(messages) != 2 || messages[1].ChatID != r.ChatID || messages[1].Timestamp.IsZero() {
		t.Fatalf("canonical transcript: %+v %v", messages, err)
	}
	duplicate := msg
	duplicate.ID = "duplicate-message"
	if err := s.AppendDocumentCreation(t.Context(), r.ChatID, duplicate, intent, r); !errors.Is(err, ErrDocumentCreationConsentUnconfirmed) {
		t.Fatalf("duplicate submission: %v", err)
	}
	if n := creationCount(t, s.db, `SELECT COUNT(*) FROM conversation_messages WHERE id=?`, duplicate.ID); n != 0 {
		t.Fatal("duplicate left mirror")
	}
	s.Close()
	reopened := NewStore(s.basePath, logging.New("error", "json", nil), WithDB(s.db))
	t.Cleanup(reopened.Close)
	messages, err = reopened.Read(t.Context(), r.ChatID, 0, 0)
	if err != nil || len(messages) != 3 || messages[2].ID != duplicate.ID {
		t.Fatalf("saved transcripts after reopen: %+v %v", messages, err)
	}
	if n := creationCount(t, s.db, `SELECT COUNT(*) FROM document_creation_consents WHERE message_id=? AND run_id IS NULL`, msg.ID); n != 1 {
		t.Fatal("persisted unbound consent missing")
	}
}

func TestDocumentCreationAppendRejectsForgedActorAndReceipt(t *testing.T) {
	for _, change := range []string{"no receipt", "assistant", "different author", "different agent", "different session", "different intent", "forged receipt", "stale receipt"} {
		t.Run(change, func(t *testing.T) {
			s, r, intent, msg := creationStore(t)
			receipt := *r
			supplied := &receipt
			session := r.ChatID
			switch change {
			case "no receipt":
				supplied = nil
			case "assistant":
				msg.Role = RoleAssistant
			case "different author":
				msg.AuthorUserID = "another-human"
			case "different agent":
				msg.AgentID = "another-agent"
			case "different session":
				session = "another-chat"
			case "different intent":
				intent.SubmissionID = "changed"
			case "forged receipt":
				receipt.MemberID = "replacement"
			case "stale receipt":
				if _, err := s.db.Exec(`UPDATE workspace_members SET access_revision=access_revision+1`); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.AppendDocumentCreation(t.Context(), session, msg, intent, supplied); err == nil {
				t.Fatal("untrusted append admitted")
			}
			if n := creationCount(t, s.db, `SELECT COUNT(*) FROM document_creation_consents`); n != 0 {
				t.Fatal("untrusted append minted consent")
			}
			if n := creationCount(t, s.db, `SELECT COUNT(*) FROM conversation_messages`); n != 0 {
				t.Fatal("untrusted append left SQL mirror")
			}
		})
	}
}

func TestDocumentCreationAppendFailuresDoNotMintConsent(t *testing.T) {
	s, r, intent, msg := creationStore(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.AppendDocumentCreation(canceled, r.ChatID, msg, intent, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled append: %v", err)
	}
	if messages, err := s.Read(t.Context(), r.ChatID, 0, 0); err != nil || len(messages) != 0 {
		t.Fatalf("canceled transcript: %+v %v", messages, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_creation_consent BEFORE INSERT ON document_creation_consents BEGIN SELECT RAISE(ABORT,'synthetic consent SQL fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendDocumentCreation(t.Context(), r.ChatID, msg, intent, r); !errors.Is(err, ErrDocumentCreationConsentUnconfirmed) || !strings.Contains(err.Error(), "synthetic consent SQL fault") {
		t.Fatalf("saved-message status or SQL fault lost: %v", err)
	}
	if messages, err := s.Read(t.Context(), r.ChatID, 0, 0); err != nil || len(messages) != 1 || messages[0].ID != msg.ID {
		t.Fatalf("durable transcript lost: %+v %v", messages, err)
	}
	if n := creationCount(t, s.db, `SELECT COUNT(*) FROM document_creation_consents`); n != 0 {
		t.Fatal("SQL fault minted consent")
	}
	if n := creationCount(t, s.db, `SELECT COUNT(*) FROM conversation_messages`); n != 0 {
		t.Fatal("SQL fault left mirror")
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_creation_consent`); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(t.Context(), r.ChatID, msg); err != nil {
		t.Fatal(err)
	}
	if n := creationCount(t, s.db, `SELECT COUNT(*) FROM document_creation_consents`); n != 0 {
		t.Fatal("ordinary replay repaired authority")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("not a transcript directory"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := NewStore(blocked, logging.New("error", "json", nil), WithDB(s.db))
	t.Cleanup(broken.Close)
	msg.ID = "jsonl-failure"
	if err := broken.AppendDocumentCreation(t.Context(), r.ChatID, msg, intent, r); err == nil {
		t.Fatal("JSONL fault ignored")
	}
	if n := creationCount(t, s.db, `SELECT COUNT(*) FROM document_creation_consents`); n != 0 {
		t.Fatal("JSONL fault minted consent")
	}
}
