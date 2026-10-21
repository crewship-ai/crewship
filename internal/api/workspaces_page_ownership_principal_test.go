package api

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestPrivateOwnershipRunPrincipalCannotBorrowHumanCredential(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			rig, handler := ownershipFixture(t, "documents")
			pages := NewPageHandler(rig.db, nil, newTestLogger())
			human := ownershipRequest(t, rig, method, "/", "")
			if actor, ok := handler.pageOwnershipActor(httptest.NewRecorder(), human, pages); !ok || actor == nil {
				t.Fatal("fixture must admit the current human administrator")
			}
			principal := &artifactRunAuthority{WorkspaceID: rig.wsID, HumanUserID: rig.adminID, RunID: "synthetic-custody-run"}
			run := human.WithContext(context.WithValue(human.Context(), artifactRunKey{}, principal))
			response := httptest.NewRecorder()
			actor, ok := handler.pageOwnershipActor(response, run, pages)
			if ok || actor != nil || response.Code != 401 {
				t.Fatalf("run borrowed human custody authority: admitted=%v status=%d", ok, response.Code)
			}
		})
	}
}
