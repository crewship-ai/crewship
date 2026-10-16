package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Inline chat images reuse this file route, including its live authorization.
func TestInlineRunImageDownloadDeniesUnauthorizedViewer(t *testing.T) {
	h := NewProxyHandler(setupTestDB(t), newTestLogger(), "/not-used")
	user := seedTestUser(t, h.db)
	workspace := seedTestWorkspace(t, h.db, user)
	crew := seedCrewRow(t, h.db, "image-crew", workspace, "Images", "images")
	agent := seedAgentRow(t, h.db, "image-agent", workspace, crew, "Image Agent", "image-agent", "AGENT")
	for _, tc := range []struct {
		name, role, ws string
		status         int
	}{
		{"no role", "", workspace, http.StatusForbidden},
		{"different workspace", "OWNER", "another-workspace", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/agents/"+agent+"/files/download?path=runs/run-1/chart.png", nil)
			req.SetPathValue("agentId", agent)
			req = withWorkspaceUser(req, user, tc.ws, tc.role)
			result := httptest.NewRecorder()
			h.AgentFileDownload(result, req)
			if result.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", result.Code, tc.status, result.Body.String())
			}
		})
	}
}

func TestInlineRunImageGuestSharedChatCannotReadAgentFiles(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	crew := seedCrewRow(t, db, "shared-image-crew", workspace, "Images", "images")
	seedAgentRow(t, db, "shared-image-agent", workspace, crew, "Image Agent", "image-agent", "AGENT")
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('image-guest','image-guest@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('image-guest-member',?,'image-guest','VIEWER')`, workspace)
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('shared-image-chat',?,'shared-image-agent',?,'group')`, workspace, owner)
	execOrFatal(t, db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('shared-image-chat','image-guest','member')`)
	store := access.Store{DB: db}
	member, err := store.Membership(t.Context(), "image-guest", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "image-guest", workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "shared-image-agent", Operation: "chat"}}); err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-inline-image-router-secret-2026"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), "image-guest", "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken("image-guest", session.ID, "Guest", "image-guest@example.test")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/download?path=runs/run-1/chart.png"} {
		req := httptest.NewRequest("GET", "/api/v1/agents/shared-image-agent/files"+suffix, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("generic file route: %d %s", rec.Code, rec.Body.String())
		}
	}
}
