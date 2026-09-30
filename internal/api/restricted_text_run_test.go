package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
)

type fixtureTextSession struct {
	output string
	done   chan struct{}
}

func (s *fixtureTextSession) Output(context.Context) (string, error) { return s.output, nil }
func (s *fixtureTextSession) Done() <-chan struct{}                  { return s.done }
func (s *fixtureTextSession) Stop(string)                            {}

func TestRestrictedTextRouterIsolatesTwoHumansAndHistory(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('text-crew',?,'Crew','text-crew')`, workspace)
	seedAgentRow(t, db, "text-agent", workspace, "text-crew", "Text", "text-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text',llm_provider='OPENAI',llm_model='fixture-model',system_prompt_legacy='SHARED_PROMPT_CANARY' WHERE id='text-agent'`)
	cipher, err := encryption.Encrypt("synthetic-text-key")
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('text-key',?,'Key',?,'API_KEY','OPENAI',?)`, workspace, cipher, owner)
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('text-grant','text-agent','text-key','OPENAI_API_KEY')`)
	store := access.Store{DB: db}
	tokens := map[string]string{}
	const secret = "restricted-text-router-jwt-secret"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"text-h1", "text-h2"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@text.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, user, workspace, user)
		execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,'text-agent',?,'private')`, user+"-chat", workspace, user)
		member, err := store.Membership(t.Context(), user, workspace)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Replace(t.Context(), owner, user, workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "text-agent", Operation: "run"}, {Kind: "agent", ID: "text-agent", Operation: "discover"}, {Kind: "agent", ID: "text-agent", Operation: "chat"}})
		if err != nil {
			t.Fatal(err)
		}
		session, err := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[user], err = validator.IssueAccessToken(user, session.ID, user, user+"@text.test")
		if err != nil {
			t.Fatal(err)
		}
	}
	seedAgentRow(t, db, "foreign-text-agent", workspace, "text-crew", "FOREIGN_AGENT_CANARY", "foreign-text-agent", "AGENT")
	prompts := map[string][]string{}
	starts := 0
	runner := &restricteddispatch.TextRunner{Authority: restricteddispatch.Authority{Store: store}, MaxOutputTokens: 128}
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		attempt, err := store.Resolve(ctx, handle)
		if err != nil {
			return nil, err
		}
		starts++
		var command string
		if err = db.QueryRowContext(ctx, `SELECT command_json FROM restricted_launches WHERE attempt_id=?`, attempt.ID).Scan(&command); err != nil {
			return nil, err
		}
		prompts[attempt.Principal] = append(prompts[attempt.Principal], command)
		done := make(chan struct{})
		close(done)
		return &fixtureTextSession{`{"type":"text","text":"answer-for-` + attempt.Principal + `"}` + "\n" + `{"type":"done"}` + "\n", done}, nil
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithRestrictedTextRunner(runner), WithInternalToken("synthetic-router-test-token"))
	if err != nil {
		t.Fatal(err)
	}
	directory := func(path string) *httptest.ResponseRecorder {
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		req := httptest.NewRequest(http.MethodGet, path+separator+"workspace_id="+workspace, nil)
		req.Header.Set("Authorization", "Bearer "+tokens["text-h2"])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/api/v1/agents", "/api/v1/agents/text-agent"} {
		rec := directory(path)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "text-agent") || strings.Contains(rec.Body.String(), "FOREIGN_AGENT_CANARY") || strings.Contains(rec.Body.String(), "SHARED_PROMPT_CANARY") {
			t.Fatalf("directory %s %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := directory("/api/v1/agents?search=FOREIGN_AGENT_CANARY"); rec.Code != 200 || rec.Body.String() != "[]\n" || rec.Header().Get("X-Total-Count") != "0" {
		t.Fatalf("foreign search %d %s", rec.Code, rec.Body.String())
	}
	if rec := directory("/api/v1/agents/foreign-text-agent"); rec.Code != 404 {
		t.Fatalf("guessed foreign agent %d", rec.Code)
	}
	request := func(user, chat, content string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"content": content})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/chats/"+chat+"/restricted-run?workspace_id="+workspace, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, user := range []string{"text-h1", "text-h2", "text-h1"} {
		rec := request(user, user+"-chat", "CANARY_"+user)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"done"`) {
			t.Fatalf("run %s: %d %s", user, rec.Code, rec.Body.String())
		}
	}
	if rec := directory("/api/v1/chats/text-h2-chat/messages"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "CANARY_text-h2") || strings.Contains(rec.Body.String(), "CANARY_text-h1") || strings.Contains(rec.Body.String(), `"ts":""`) {
		t.Fatalf("scoped history %d %s", rec.Code, rec.Body.String())
	}
	if starts != 3 {
		t.Fatalf("starts %d", starts)
	}
	for user, commands := range prompts {
		for _, command := range commands {
			other := "text-h1"
			if user == other {
				other = "text-h2"
			}
			if strings.Contains(command, "CANARY_"+other) || strings.Contains(command, "SHARED_PROMPT_CANARY") {
				t.Fatalf("foreign/shared context for %s", user)
			}
		}
	}
	if !strings.Contains(prompts["text-h1"][1], "answer-for-text-h1") {
		t.Fatal("completed prior assistant history missing")
	}
	rec := request("text-h2", "text-h1-chat", "foreign")
	if rec.Code != 403 || starts != 3 {
		t.Fatalf("foreign run %d starts %d", rec.Code, starts)
	}
	member, err := store.Membership(t.Context(), "text-h1", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "text-h1", workspace, "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	rec = request("text-h1", "text-h1-chat", "revoked")
	if rec.Code != 403 || starts != 3 {
		t.Fatalf("revoked run %d starts %d", rec.Code, starts)
	}
	rec = request("text-h2", "text-h2-chat", "still allowed")
	if rec.Code != 200 || starts != 4 {
		t.Fatalf("other human impacted %d starts %d", rec.Code, starts)
	}
	// Chat-only is a complete conversational permission; direct CLI remains run.
	replace := func(operations ...string) {
		t.Helper()
		member, err := store.Membership(t.Context(), "text-h1", workspace)
		if err != nil {
			t.Fatal(err)
		}
		var rights []access.Right
		for _, op := range operations {
			rights = append(rights, access.Right{Kind: "agent", ID: "text-agent", Operation: op})
		}
		if _, err = store.Replace(t.Context(), owner, "text-h1", workspace, "restricted", member, rights); err != nil {
			t.Fatal(err)
		}
	}
	cliRun := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/chats/text-h1-chat/restricted-cli-run?workspace_id="+workspace, strings.NewReader(`{"content":"direct run"}`))
		req.Header.Set("Authorization", "Bearer "+tokens["text-h1"])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	contextRequest := func(method, agent, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/agents/"+agent+path+"?workspace_id="+workspace, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens["text-h1"])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	replace("chat")
	if rec := contextRequest(http.MethodPost, "text-agent", "/restricted-cli-chats", `{}`); rec.Code != 404 {
		t.Fatalf("chat-only created run context %d", rec.Code)
	}
	if rec := contextRequest(http.MethodGet, "text-agent", "/run-profile", ""); rec.Code != 404 {
		t.Fatalf("chat-only run profile %d", rec.Code)
	}
	if rec := request("text-h1", "text-h1-chat", "chat-only"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"done"`) {
		t.Fatalf("chat-only failed %d %s", rec.Code, rec.Body.String())
	}
	count := starts
	if rec := cliRun(); rec.Code != 403 || starts != count {
		t.Fatalf("chat-only acquired run %d starts %d", rec.Code, starts)
	}
	replace("run")
	for _, body := range []string{`{"created_by":"text-h2"}`, `{"visibility":"group"}`, `{"agent_id":"foreign-text-agent"}`, `null`, `{} {}`} {
		if rec := contextRequest(http.MethodPost, "text-agent", "/restricted-cli-chats", body); rec.Code != 400 {
			t.Fatalf("caller selected context body=%s status=%d", body, rec.Code)
		}
	}
	if rec := contextRequest(http.MethodPost, "foreign-text-agent", "/restricted-cli-chats", `{}`); rec.Code != 404 {
		t.Fatalf("foreign run context status=%d", rec.Code)
	}
	if rec := contextRequest(http.MethodPost, "text-agent", "/chats", `{"origin":"CLI"}`); rec.Code != 404 {
		t.Fatalf("origin spelling granted chat create %d", rec.Code)
	}
	if rec := contextRequest(http.MethodPost, "text-agent", "/restricted-cli-chats", `{}`); rec.Code != 201 {
		t.Fatalf("run-only create status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := cliRun(); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"done"`) {
		t.Fatalf("run-only failed %d %s", rec.Code, rec.Body.String())
	}
	count = starts
	if rec := request("text-h1", "text-h1-chat", "chat denied"); rec.Code != 403 || starts != count {
		t.Fatalf("run-only acquired chat %d starts %d", rec.Code, starts)
	}
	// No implicit conversion of the native CLI adapter to text execution.
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='disabled',cli_adapter='CODEX_CLI' WHERE id='text-agent'`)
	if rec := cliRun(); rec.Code != 403 || starts != count {
		t.Fatalf("native adapter silently converted %d starts %d", rec.Code, starts)
	}

	// Only the normal agent editor may deliberately select the text profile.
	profilePut := func(token, profile string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/agents/text-agent/restricted-execution?workspace_id="+workspace, strings.NewReader(`{"profile":"`+profile+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := profilePut(tokens["text-h1"], "responses_text"); rec.Code != 404 {
		t.Fatalf("restricted human edited execution profile %d", rec.Code)
	}
	ownerSession, err := sessions.NewDBStore(db).Create(t.Context(), owner, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := validator.IssueAccessToken(owner, ownerSession.ID, "Owner", "owner@text.test")
	if err != nil {
		t.Fatal(err)
	}
	if rec := profilePut(ownerToken, "native_api_key"); rec.Code != 200 {
		t.Fatalf("admin could not configure native profile %d", rec.Code)
	}
	if rec := request("text-h1", "text-h1-chat", "native must not fall back to text"); rec.Code != 403 {
		t.Fatalf("missing native runner silently fell back %d", rec.Code)
	}
	if rec := profilePut(ownerToken, "responses_text"); rec.Code != 200 {
		t.Fatalf("admin could not configure text profile %d %s", rec.Code, rec.Body.String())
	}
	getAgent := httptest.NewRequest(http.MethodGet, "/api/v1/agents/text-agent?workspace_id="+workspace, nil)
	getAgent.Header.Set("Authorization", "Bearer "+ownerToken)
	agentRec := httptest.NewRecorder()
	router.ServeHTTP(agentRec, getAgent)
	if agentRec.Code != 200 || !strings.Contains(agentRec.Body.String(), `"restricted_execution_profile":"responses_text"`) {
		t.Fatalf("agent settings lost configured profile %d %s", agentRec.Code, agentRec.Body.String())
	}

	authority := restricteddispatch.Authority{Store: store}
	bound, _, err := authority.PrepareResponses(t.Context(), "text-h1", workspace, "text-agent", "text-h1-chat", "", nil, 128, func(context.Context, access.Attempt) ([]string, error) { return []string{"/bin/true"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if rec := profilePut(ownerToken, "disabled"); rec.Code != 200 {
		t.Fatalf("admin could not disable profile %d", rec.Code)
	}
	if _, err = authority.Resolve(t.Context(), bound); err == nil {
		t.Fatal("profile change retained old provider authority")
	}
	if rec := profilePut(ownerToken, "responses_text"); rec.Code != 200 {
		t.Fatalf("admin could not restore profile %d", rec.Code)
	}
	if _, err = authority.Resolve(t.Context(), bound); err == nil {
		t.Fatal("profile re-enable revived old attempt")
	}

}
