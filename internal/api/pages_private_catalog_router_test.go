//go:build linux

package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

func TestPrivateAudienceGuestCatalogThroughRouter(t *testing.T) {
	h, workspace, author, _, create := documentCreationFixture(t)
	pagesSeedUser(t, h, workspace, "router-private-guest", "router-private-guest@example.test", "MEMBER")
	execOrFatal(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='router-private-guest'`)
	const secret = "synthetic-private-catalog-router-secret"
	runner := &restricteddispatch.TextRunner{Authority: restricteddispatch.Authority{Store: access.Store{DB: h.db}}}
	workflow, err := restrictedworkflow.New(h.db, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer workflow.Close()
	router, err := NewRouter(h.db, secret, newTestLogger(), WithInternalToken("synthetic-private-catalog-host"), WithRestrictedTextRunner(runner))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	router.SetRestrictedWorkflow(workflow)
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), "router-private-guest", "catalog-test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken("router-private-guest", session.ID, "Guest", "router-private-guest@example.test")
	if err != nil {
		t.Fatal(err)
	}
	read := func() {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/workspaces/"+workspace+"/restricted-pages?workspace_id="+workspace, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 200 || response.Header().Get("X-Total-Count") != "0" {
			t.Fatalf("Guest catalog HTTP%d count%s: %s", response.Code, response.Header().Get("X-Total-Count"), response.Body.String())
		}
		if strings.Contains(response.Body.String(), "created-doc") || strings.Contains(response.Body.String(), "Created report") {
			t.Fatal("private chat document leaked through Guest catalog")
		}
	}
	read() // Calibrate the real auth middleware, Guest route and runtime first.
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, author)
	creationExpect(t, create("member", documentCreationBody), 201)
	read()
}
