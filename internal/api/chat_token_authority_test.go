package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func chatTokenScopeCases() []struct {
	name   string
	scopes any
	allow  bool
} {
	return []struct {
		name   string
		scopes any
		allow  bool
	}{
		{"unrestricted session or legacy token", nil, true},
		{"explicit no scopes", stringSet{}, false},
		{"agent write", stringSet{"agents:write": {}}, false},
		{"crew write", stringSet{"crews:write": {}}, false},
		{"workspace admin", stringSet{"workspace:admin": {}}, false},
		{"resource wildcard", stringSet{"agents:*": {}}, false},
		{"multiple resource scopes", stringSet{"agents:write": {}, "crews:write": {}, "workspace:admin": {}}, false},
		{"global wildcard", stringSet{"*": {}}, true},
		{"malformed fence", []string{"*"}, false},
	}
}

func TestChatTokenAuthorityWSTicketNeverDropsLimitedScope(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	validator := newTestJWTValidator(t)
	h := NewAuthHandler(db, newTestLogger(), validator, sessions.NewDBStore(db), true)
	for _, tc := range chatTokenScopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := withUser(t.Context(), &AuthUser{ID: user, TokenID: "scope-test-token"})
			if tc.scopes != nil {
				ctx = context.WithValue(ctx, ctxTokenScopes, tc.scopes)
			}
			rr := httptest.NewRecorder()
			h.WsToken(rr, httptest.NewRequest("GET", "/api/v1/ws-token", nil).WithContext(ctx))
			if !tc.allow {
				if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "scoped_token_chat_unavailable") || strings.Contains(rr.Body.String(), `"token":`) {
					t.Fatalf("limited authority minted ticket: %d %s", rr.Code, rr.Body.String())
				}
				return
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("unrestricted ticket refused: %d %s", rr.Code, rr.Body.String())
			}
			var body struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			claims, err := validator.ValidateWS(body.Token)
			if err != nil || claims.ID != user {
				t.Fatalf("invalid preserved ticket: %+v %v", claims, err)
			}
		})
	}
}

func TestChatTokenAuthoritySteeringNeverDropsLimitedScope(t *testing.T) {
	bed := setupReactionsTestBed(t)
	for _, tc := range chatTokenScopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeSteerer{}
			h := NewSteerHandler(bed.h.db, fake, newTestLogger())
			ctx := withUser(t.Context(), &AuthUser{ID: bed.userID})
			if tc.scopes != nil {
				ctx = context.WithValue(ctx, ctxTokenScopes, tc.scopes)
			}
			req := httptest.NewRequest("POST", "/api/v1/chats/"+bed.chatID+"/steer", strings.NewReader(`{"message":"Create another issue"}`)).WithContext(ctx)
			req.SetPathValue("chatId", bed.chatID)
			rr := httptest.NewRecorder()
			h.Steer(rr, req)
			if !tc.allow {
				if rr.Code != http.StatusForbidden || fake.calls != 0 || !strings.Contains(rr.Body.String(), "scoped_token_chat_unavailable") {
					t.Fatalf("limited token reached chat: %d calls=%d %s", rr.Code, fake.calls, rr.Body.String())
				}
				return
			}
			if rr.Code != http.StatusAccepted || fake.calls != 1 {
				t.Fatalf("unrestricted steer refused: %d calls=%d %s", rr.Code, fake.calls, rr.Body.String())
			}
		})
	}
}
