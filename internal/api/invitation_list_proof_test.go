package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInvitationListingDoesNotDiscloseProof(t *testing.T) {
	db := setupTestDB(t)
	inviter, workspace := seedInviter(t, db)
	seedInvitation(t, db, "list-proof", workspace, inviter, "listed@example.test", "ADMIN", time.Now().Add(time.Hour), nil)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('list-viewer','list-viewer@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('list-viewer-member',?,'list-viewer','VIEWER')`, workspace)
	router, err := NewRouter(db, "invitation-list-viewer-test-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	token := mintTokenFor(t, db, "list-viewer", "listviewer"+strings.Repeat("0", 40))
	request := httptest.NewRequest("GET", "/api/v1/workspaces/"+workspace+"/invitations", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var listed []invitationResponse
	if json.Unmarshal(response.Body.Bytes(), &listed) != nil || len(listed) != 1 {
		t.Fatal(response.Body.String())
	}
	if listed[0].Token != "" {
		t.Fatal("listing disclosed invitation redemption proof")
	}
	var stored string
	if err := db.QueryRow(`SELECT token FROM workspace_invitations WHERE id='list-proof'`).Scan(&stored); err != nil || stored != invitationTokenHash("tok-list-proof") {
		t.Fatal("listing mutated stored proof", err)
	}
}

func TestInvitationIssuerReceivesProofOnlyOnCreation(t *testing.T) {
	db := setupTestDB(t)
	inviter, workspace := seedInviter(t, db)
	handler := NewWorkspaceHandler(db, newTestLogger())
	for _, role := range []string{"VIEWER", "OWNER"} {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"new-invite@example.test","role":"MEMBER"}`)), inviter, workspace, role)
		rr := httptest.NewRecorder()
		handler.CreateInvitation(rr, req)
		want := 201
		if role == "VIEWER" {
			want = 403
		}
		if rr.Code != want {
			t.Fatal(role, rr.Code, rr.Body.String())
		}
		if role == "OWNER" {
			var invitation invitationResponse
			if json.Unmarshal(rr.Body.Bytes(), &invitation) != nil || invitation.Token == "" {
				t.Fatal("issuer missing creationproof")
			}
		}
	}
}
