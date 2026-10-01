package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRestrictedRoutesWithoutRuntimeFailClosedOnEveryPlatform(t *testing.T) {
	for _, runOperation := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, "/unavailable", strings.NewReader(`{"content":"synthetic request"}`))
		ctx := context.WithValue(req.Context(), ctxUser, &AuthUser{ID: "synthetic-actor"})
		ctx = context.WithValue(ctx, ctxWorkspaceID, "synthetic-workspace")
		response := httptest.NewRecorder()
		(&Router{}).restrictedTextExecute(response, req.WithContext(ctx), runOperation)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "runtime unavailable") {
			t.Fatalf("uninstalled runtime response: %d", response.Code)
		}
	}
}
