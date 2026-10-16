package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentCreationTrustedMessageAndRunBinding(t *testing.T) {
	pages, workspace, user, _, _ := artifactRunFixture(t)
	receipt, err := chataudience.CaptureTrusted(t.Context(), pages.db, "artifact-chat", user, nil)
	if err != nil || receipt == nil {
		t.Fatal(err)
	}
	store := conversation.NewStore(t.TempDir(), newTestLogger(), conversation.WithDB(pages.db))
	t.Cleanup(func() { store.Close() })
	intent := documents.CreationIntent{SubmissionID: "explicit-owned-consent"}
	message := conversation.Message{ID: "docs-consent-message", ChatID: "untrusted-wrong-chat", AgentID: "artifact-agent", Role: conversation.RoleUser, Content: "Write a complete document", AuthorUserID: user, Metadata: map[string]any{documents.CreationMetadataKey: map[string]any{"submission_id": intent.SubmissionID}}}
	// Ordinary metadata never grants authority, even on an actual human message.
	ordinary := message
	ordinary.ID = "ordinary-metadata-message"
	if err := store.Append(t.Context(), receipt.ChatID, ordinary); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pages.db.QueryRow(`SELECT COUNT(*) FROM document_creation_consents`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ordinary metadata minted consent: %d %v", count, err)
	}
	if err := store.AppendDocumentCreation(t.Context(), receipt.ChatID, message, intent, nil); err == nil {
		t.Fatal("nil human authority admitted")
	}
	if err := store.AppendDocumentCreation(t.Context(), receipt.ChatID, message, intent, receipt); err != nil {
		t.Fatal(err)
	}
	messages, err := store.Read(t.Context(), receipt.ChatID, 0, 0)
	if err != nil || len(messages) != 2 || messages[1].ChatID != receipt.ChatID || messages[1].Timestamp.IsZero() {
		t.Fatalf("trusted transcript canonicalization: %v %v", messages, err)
	}
	// Reusing consent on a second persisted message cannot duplicate allowance.
	duplicate := message
	duplicate.ID = "duplicate-consent-message"
	if err := store.AppendDocumentCreation(t.Context(), receipt.ChatID, duplicate, intent, receipt); err == nil {
		t.Fatal("duplicate submission minted consent")
	}
	if err := pages.db.QueryRow(`SELECT COUNT(*) FROM conversation_messages WHERE id=?`, duplicate.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed consent did not rollback mirror")
	}
	handler := NewInternalHandler(pages.db, "tok", newTestLogger())
	wireTestJournalForHandler(t, pages.db, handler)
	call := func(run string, value *documents.CreationIntent, trigger, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": run, "agent_id": receipt.AgentID, "workspace_id": workspace, "chat_id": receipt.ChatID, "human_authority": receipt, "trigger_message_id": trigger, "artifact_human_receipt": receipt, "artifact_human_message_id": trigger, "document_creation_intent": value}))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Internal-Token", token)
		rr := httptest.NewRecorder()
		handler.requireInternal(http.HandlerFunc(handler.CreateRun)).ServeHTTP(rr, req)
		return rr
	}
	for _, token := range []string{internaltoken.DeriveCrewToken("tok", workspace, "artifact-crew"), internaltoken.DeriveWorkspaceToken("tok", workspace)} {
		rr := call("crew-forged-consent", &intent, message.ID, token)
		if rr.Code != 403 {
			t.Fatalf("crew forged consent: %d %s", rr.Code, rr.Body.String())
		}
	}
	if err := pages.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='crew-forged-consent'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("forged consent reached journal")
	}
	rr := call("docs-create-run", &intent, message.ID, "tok")
	if rr.Code != 201 {
		t.Fatalf("trusted run: %d %s", rr.Code, rr.Body.String())
	}
	var originalExpiry string
	if err := pages.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id='docs-create-run'`).Scan(&originalExpiry); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // force a later wall-clock capture on lost-ACK retry
	rr = call("docs-create-run", &intent, message.ID, "tok")
	if rr.Code != 201 {
		t.Fatalf("exact admission replay: %d %s", rr.Code, rr.Body.String())
	}
	var replayExpiry string
	if err := pages.db.QueryRow(`SELECT expires_at FROM document_creation_consents WHERE run_id='docs-create-run'`).Scan(&replayExpiry); err != nil || replayExpiry != originalExpiry {
		t.Fatalf("lost ACK changed expiry: %s %s %v", originalExpiry, replayExpiry, err)
	}
	for _, value := range []*documents.CreationIntent{nil, {SubmissionID: "changed-intent"}} {
		rr = call("docs-create-run", value, message.ID, "tok")
		if rr.Code != 409 {
			t.Fatalf("run intent changed: %d %s", rr.Code, rr.Body.String())
		}
	}
	rr = call("docs-create-second-run", &intent, message.ID, "tok")
	if rr.Code != 403 {
		t.Fatalf("consent rebound: %d %s", rr.Code, rr.Body.String())
	}
	rr = call("docs-create-missing-message", &intent, "nonexistent-message", "tok")
	if rr.Code != 403 {
		t.Fatalf("missing principal was not fatal: %d %s", rr.Code, rr.Body.String())
	}
	var bound string
	if err := pages.db.QueryRow(`SELECT run_id FROM document_creation_consents WHERE message_id=?`, message.ID).Scan(&bound); err != nil || bound != "docs-create-run" {
		t.Fatalf("original binding changed: %s %v", bound, err)
	}
}

func TestDocumentCreationAppendFailuresDoNotMintConsent(t *testing.T) {
	pages, _, user, _, _ := artifactRunFixture(t)
	receipt, err := chataudience.CaptureTrusted(t.Context(), pages.db, "artifact-chat", user, nil)
	if err != nil || receipt == nil {
		t.Fatal(err)
	}
	intent := documents.CreationIntent{SubmissionID: "failure-consent"}
	message := conversation.Message{ID: "failed-consent-message", AgentID: receipt.AgentID, Role: conversation.RoleUser, Content: "Keep transcript; do not start a run", AuthorUserID: user, Timestamp: time.Now(), Metadata: map[string]any{documents.CreationMetadataKey: map[string]any{"submission_id": intent.SubmissionID}}}
	root := t.TempDir()
	store := conversation.NewStore(root, newTestLogger(), conversation.WithDB(pages.db))
	t.Cleanup(func() { store.Close() })
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.AppendDocumentCreation(canceled, receipt.ChatID, message, intent, receipt); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled append: %v", err)
	}
	if messages, err := store.Read(t.Context(), receipt.ChatID, 0, 0); err != nil || len(messages) != 0 {
		t.Fatalf("canceled append wrote transcript: %v %v", messages, err)
	}
	execOrFatal(t, pages.db, `CREATE TRIGGER reject_creation_consent BEFORE INSERT ON document_creation_consents BEGIN SELECT RAISE(ABORT,'synthetic consent SQL fault'); END`)
	if err := store.AppendDocumentCreation(t.Context(), receipt.ChatID, message, intent, receipt); err == nil || !strings.Contains(err.Error(), "synthetic consent SQL fault") || !errors.Is(err, conversation.ErrDocumentCreationConsentUnconfirmed) {
		t.Fatalf("SQL fault not reached or saved-message status lost: %v", err)
	}
	messages, err := store.Read(t.Context(), receipt.ChatID, 0, 0)
	if err != nil || len(messages) != 1 || messages[0].ID != message.ID {
		t.Fatalf("durable transcript lost: %v %v", messages, err)
	}
	var count int
	if err := pages.db.QueryRow(`SELECT COUNT(*) FROM document_creation_consents`).Scan(&count); err != nil || count != 0 {
		t.Fatal("SQL fault minted consent")
	}
	if err := pages.db.QueryRow(`SELECT COUNT(*) FROM conversation_messages WHERE id=?`, message.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("SQL fault left mirror")
	}
	execOrFatal(t, pages.db, `DROP TRIGGER reject_creation_consent`)
	// Replaying ordinary metadata cannot repair authority as a side effect.
	if err := store.Append(t.Context(), receipt.ChatID, message); err != nil {
		t.Fatal(err)
	}
	if err := pages.db.QueryRow(`SELECT COUNT(*) FROM document_creation_consents`).Scan(&count); err != nil || count != 0 {
		t.Fatal("ordinary replay minted consent")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("not a transcript directory"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := conversation.NewStore(blocked, newTestLogger(), conversation.WithDB(pages.db))
	t.Cleanup(func() { broken.Close() })
	message.ID = "jsonl-failed-consent-message"
	if err := broken.AppendDocumentCreation(t.Context(), receipt.ChatID, message, intent, receipt); err == nil {
		t.Fatal("JSONL failure ignored")
	}
	if err := pages.db.QueryRow(`SELECT COUNT(*) FROM document_creation_consents`).Scan(&count); err != nil || count != 0 {
		t.Fatal("JSONL fault minted consent")
	}
}
