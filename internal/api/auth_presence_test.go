package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestAuthWsTokenRefusesAgentContextAndUnprovenHuman(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	for _, kind := range []string{"unknown", "acting", "internal-header", "internal-context"} {
		t.Run(kind, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/ws-token", nil)
			ctx := withUser(r.Context(), &AuthUser{ID: user})
			if kind != "unknown" {
				ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
			}
			switch kind {
			case "acting":
				r.Header.Set(actingAgentSlugHeader, "agent")
			case "internal-header":
				r.Header.Set("X-Internal-Token", "credential")
			case "internal-context":
				ctx = context.WithValue(ctx, ctxInternalTokenWS, "workspace")
			}
			r = r.WithContext(ctx)
			rr := httptest.NewRecorder()
			h.WsToken(rr, r)
			if rr.Code != 403 {
				t.Fatalf("agent/unproven context minted ticket: %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}
