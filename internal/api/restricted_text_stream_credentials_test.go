package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

type queuedCredentialText struct {
	run func(context.Context, func(string, string) error) error
}

func (q queuedCredentialText) Execute(ctx context.Context, _, _, _, _ string, send func(string, string) error) error {
	return q.run(ctx, send)
}
func (q queuedCredentialText) ExecuteRun(ctx context.Context, a, b, c, d string, send func(string, string) error) error {
	return q.Execute(ctx, a, b, c, d, send)
}

func (q queuedCredentialText) ExecuteWithProjectFiles(ctx context.Context, a, b, c, d string, _ []string, send func(string, string) error) error {
	return q.Execute(ctx, a, b, c, d, send)
}
func (q queuedCredentialText) ExecuteRunWithProjectFiles(ctx context.Context, a, b, c, d string, _ []string, send func(string, string) error) error {
	return q.Execute(ctx, a, b, c, d, send)
}

func TestRestrictedTextStreamCredentialQueuedFrames(t *testing.T) {
	for _, credential := range []string{"session", "token"} {
		for _, variant := range []string{"chat", "run", "chat-files", "run-files", "chat-before-first", "run-before-first"} {
			run := strings.HasPrefix(variant, "run")
			t.Run(credential+"/"+variant, func(t *testing.T) {
				db := setupTestDB(t)
				owner := seedTestUser(t, db)
				workspace := seedTestWorkspace(t, db, owner)
				user := "credential-text-guest"
				execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,'credential-text@example.test')`, user)
				execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('credential-text-member',?,?,'VIEWER')`, user, workspace)
				execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('credential-text-crew',?,'Fixture','credential-text')`, workspace)
				seedAgentRow(t, db, "credential-text-agent", workspace, "credential-text-crew", "Fixture", "credential-text-agent", "AGENT")
				store := access.Store{DB: db}
				member, e := store.Membership(t.Context(), user, workspace)
				if e != nil {
					t.Fatal(e)
				}
				_, e = store.Replace(t.Context(), owner, user, workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "credential-text-agent", Operation: "chat"}, {Kind: "agent", ID: "credential-text-agent", Operation: "run"}})
				if e != nil {
					t.Fatal(e)
				}
				execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('credential-text-chat',?,'credential-text-agent',?,'private')`, workspace, user)
				router, err := NewRouter(db, "test-secret-for-jwt-signing-32chars!!", newTestLogger(), WithInternalToken("synthetic-text-internal"))
				if err != nil {
					t.Fatal(err)
				}
				var bearer string
				var revoke func()
				if credential == "session" {
					s, e := router.sessionsStore.Create(t.Context(), user, "", "", time.Hour)
					if e != nil {
						t.Fatal(e)
					}
					bearer, e = router.authMw.validator.IssueAccessToken(user, s.ID, "Test", "test@example.com")
					if e != nil {
						t.Fatal(e)
					}
					revoke = func() {
						if e := router.sessionsStore.Revoke(t.Context(), s.ID, sessions.ReasonLogout); e != nil {
							t.Fatal(e)
						}
					}
				} else {
					bearer = "crewship_cli_restricted-text-synthetic"
					execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('text-stream-token',?,'test',?,'STANDARD')`, user, hashStandard(bearer))
					revoke = func() {
						req := httptest.NewRequest("DELETE", "/api/v1/auth/cli-tokens/text-stream-token", nil)
						req.Header.Set("Authorization", "Bearer "+bearer)
						rr := httptest.NewRecorder()
						router.ServeHTTP(rr, req)
						if rr.Code != 200 {
							t.Fatalf("SETUP revoke status %d", rr.Code)
						}
					}
				}
				var second error
				router.restrictedText = queuedCredentialText{run: func(ctx context.Context, send func(string, string) error) error {
					if strings.HasSuffix(variant, "before-first") {
						revoke()
						second = send("text", "AFTER_CREDENTIAL_LOSS")
						return second
					}
					if err := send("text", "positive-first-frame"); err != nil {
						return err
					}
					revoke()
					second = send("text", "AFTER_CREDENTIAL_LOSS")
					return second
				}}
				path := "/api/v1/chats/{chatId}/restricted-run"
				if run {
					path = "/api/v1/chats/{chatId}/restricted-cli-run"
				}
				body := `{"content":"bounded fixture"}`
				if strings.HasSuffix(variant, "-files") {
					body = `{"content":"bounded fixture","project_file_versions":["synthetic-version"]}`
				}
				req := httptest.NewRequest("POST", "/api/v1/chats/credential-text-chat/restricted-run", strings.NewReader(body))
				req.SetPathValue("chatId", "credential-text-chat")
				req.Pattern = "POST " + path
				req.Header.Set("Authorization", "Bearer "+bearer)
				req.Header.Set("X-Workspace-ID", workspace)
				rr := httptest.NewRecorder()
				handler := router.restrictedTextRun
				if run {
					handler = router.restrictedCLIRun
				}
				router.authMw.RequireAuth(router.authMw.RequireWorkspace(http.HandlerFunc(handler))).ServeHTTP(rr, req)
				if strings.HasSuffix(variant, "before-first") {
					if second == nil || rr.Body.Len() != 0 {
						t.Fatalf("revoked pre-stream request emitted data/error: status %d", rr.Code)
					}
					return
				}
				if rr.Code != 200 || !strings.Contains(rr.Body.String(), "positive-first-frame") {
					t.Fatalf("SETUP stream positive status %d", rr.Code)
				}
				if second == nil || strings.Contains(rr.Body.String(), "AFTER_CREDENTIAL_LOSS") {
					t.Fatal("restricted text delivered queued frame after credential loss")
				}
				if errors.Is(second, context.DeadlineExceeded) {
					t.Fatal("test stopped for unrelated deadline")
				}
			})
		}
	}
}
