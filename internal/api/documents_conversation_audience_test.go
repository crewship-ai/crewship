package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/conversation"
)

// Real creation admission/SQL, production Router authentication and production
// chat audience resolution. Only Node preparation and transcript IPC are fixture
// adapters. No provider/model run, browser or deployed acceptance is asserted.
func TestPrivateDocumentConversationOutputAudience(t *testing.T) {
	pages, workspace, author, _, create := documentCreationFixture(t)
	created := create("conversation-audience", documentCreationBody)
	creationExpect(t, created, http.StatusCreated)
	var result documentCreationResult
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var chat, agent string
	var private bool
	if err := pages.db.QueryRow(`SELECT c.source_chat_id,c.agent_id,p.private_audience FROM document_creations c JOIN pages p ON p.id=c.page_id WHERE c.page_id=? AND c.workspace_id=?`, result.PageID, workspace).Scan(&chat, &agent, &private); err != nil || !private {
		t.Fatalf("missing private creation provenance: private=%v err=%v", private, err)
	}
	const admin, owner = "conversation-private-admin", "conversation-private-owner"
	pagesSeedUser(t, pages, workspace, admin, "conversation-admin@example.test", "ADMIN")
	pagesSeedUser(t, pages, workspace, owner, "conversation-owner@example.test", "OWNER")
	// Pin the contrast: this is protected Document output, not merely an
	// ordinary conversation whose legacy administrative access is under review.
	if status := pagesGetStatus(t, pages, workspace, admin, "ADMIN", result.Slug).Code; status != http.StatusNotFound {
		t.Fatalf("private Page baseline=%d want404", status)
	}
	var parts conversation.PartAccumulator
	parts.Add("tool_result", created.Body.String(), map[string]any{"tool_name": "doc_create", "tool_id": "fixture-create"})
	message := conversation.Message{ID: "private-create-reply", ChatID: chat, AgentID: agent, Role: conversation.RoleAssistant, Parts: parts.Parts(), Timestamp: time.Now().UTC()}
	socket := covIPCJSON(t, map[string]any{"/chats/" + chat + "/messages": map[string]any{"chat_id": chat, "messages": []conversation.Message{message}}})
	// Use a per-test key: no reusable credential is stored in this fixture.
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(keyBytes)
	router, err := NewRouter(pages.db, secret, newTestLogger(), WithSocketPath(socket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, user     string
		allowed, audit bool
	}{
		{"author", author, true, false}, {"other ADMIN", admin, false, false}, {"foreign OWNER", owner, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := sessions.NewDBStore(pages.db).Create(t.Context(), tc.user, "fixture", "127.0.0.1", auth.RefreshTokenTTL)
			if err != nil {
				t.Fatal(err)
			}
			token, err := validator.IssueAccessToken(tc.user, session.ID, "Fixture", "fixture@example.test")
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/chats/"+chat+"/messages?workspace_id="+workspace, nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if tc.allowed {
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), result.Title) || !strings.Contains(response.Body.String(), result.PageID) {
					t.Fatalf("authorized output not delivered: status=%d", response.Code)
				}
			} else {
				if response.Code != http.StatusOK && response.Code != http.StatusNotFound && response.Code != http.StatusForbidden {
					t.Fatalf("unexpected denial/setup status=%d", response.Code)
				}
				if strings.Contains(response.Body.String(), result.Title) || strings.Contains(response.Body.String(), result.PageID) || strings.Contains(response.Body.String(), result.Slug) {
					t.Errorf("other ADMIN received private Document title/link through conversation view (status=%d)", response.Code)
				}
			}
			if tc.audit {
				var count int
				if err := pages.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE workspace_id=? AND user_id=? AND entity_type='chat' AND entity_id=? AND action='artifact.private_owner_accessed' AND json_extract(metadata,'$.owner_user_id')=? AND json_extract(metadata,'$.output_source')='conversation'`, workspace, tc.user, chat, author).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count == 0 {
					t.Error("foreign OWNER private Document conversation read lacks required artifact audit")
				}
			}
		})
	}
}
