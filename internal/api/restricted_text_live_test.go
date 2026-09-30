//go:build linux && restrictedruntime_live

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestLiveRestrictedTextRouterProductionWorker(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_PRODUCTION_IMAGE")
	if image == "" {
		t.Skip("owned production worker image required")
	}
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('text-crew',?,'Crew','text-crew')`, workspace)
	seedAgentRow(t, db, "text-agent", workspace, "text-crew", "Text", "text-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET llm_provider='OPENAI',llm_model='gpt-5-mini',system_prompt_legacy='SHARED_PROMPT_CANARY' WHERE id='text-agent'`)
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

	execOrFatal(t, db, `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('text-live-cap',?,'workspace',?,'month',100,'hard')`, workspace, workspace)
	var calls atomic.Int64
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Header.Get("Authorization") != "Bearer synthetic-text-key" {
			t.Error("wrong host credential")
			w.WriteHeader(403)
			return
		}
		var body struct {
			Input        string `json:"input"`
			Model        string `json:"model"`
			Instructions string `json:"instructions"`
		}
		if json.NewDecoder(req.Body).Decode(&body) != nil || body.Model != "gpt-5-mini" || strings.Contains(body.Instructions, "SHARED_PROMPT_CANARY") {
			t.Error("invalid provider request")
			w.WriteHeader(403)
			return
		}
		var pending int
		if err := db.QueryRowContext(req.Context(), `SELECT count(*) FROM restricted_cost_reservations WHERE state='pending'`).Scan(&pending); err != nil || pending < 1 {
			t.Errorf("no pre-provider reservation %d %v", pending, err)
		}
		text := "answer-h1"
		if strings.Contains(body.Input, "CANARY_text-h2") {
			text = "answer-h2"
			if strings.Contains(body.Input, "CANARY_text-h1") {
				t.Error("foreign provider prompt")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", text)
		w.(http.Flusher).Flush()
		if strings.Contains(body.Input, "REVOKE_DURING_STREAM") {
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5-mini\",\"usage\":{\"input_tokens\":20,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
	}))
	defer upstream.Close()
	authority := restricteddispatch.Authority{Store: store}
	manager, err := restrictedruntime.New(filepath.Join(t.TempDir(), "runtime"), restrictedruntime.Docker{Image: image}, authority, authority, restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err = manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = restrictedruntime.InstallAcceptanceTLS(manager, upstream); err != nil {
		t.Fatal(err)
	}
	runner := &restricteddispatch.TextRunner{Authority: authority, Manager: manager, MaxOutputTokens: 128}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-live-host-token"), WithRestrictedTextRunner(runner))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	client := &http.Client{Timeout: 30 * time.Second}
	request := func(user, chat, content string) *http.Response {
		body, _ := json.Marshal(map[string]string{"content": content})
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/api/v1/chats/"+chat+"/restricted-run?workspace_id="+workspace, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for _, user := range []string{"text-h1", "text-h2", "text-h1"} {
		resp := request(user, user+"-chat", "CANARY_"+user)
		scan := bufio.NewScanner(resp.Body)
		done := false
		for scan.Scan() {
			if strings.Contains(scan.Text(), `"type":"done"`) {
				done = true
			}
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || !done {
			t.Fatalf("production run %s status=%d done=%v err=%v", user, resp.StatusCode, done, scan.Err())
		}
	}
	before := calls.Load()
	resp := request("text-h2", "text-h1-chat", "foreign")
	resp.Body.Close()
	if resp.StatusCode != 403 || calls.Load() != before {
		t.Fatal("foreign request reached provider")
	}
	resp = request("text-h1", "text-h1-chat", "REVOKE_DURING_STREAM")
	scan := bufio.NewScanner(resp.Body)
	if !scan.Scan() || !strings.Contains(scan.Text(), `"type":"text"`) {
		t.Fatal("missing positive pre-revocation text")
	}
	member, err := store.Membership(t.Context(), "text-h1", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "text-h1", workspace, "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	for scan.Scan() {
		if strings.Contains(scan.Text(), `"type":"done"`) {
			t.Fatal("completion delivered after revocation")
		}
	}
	resp.Body.Close()
	resp = request("text-h2", "text-h2-chat", "CANARY_text-h2")
	scan = bufio.NewScanner(resp.Body)
	done := false
	for scan.Scan() {
		if strings.Contains(scan.Text(), `"type":"done"`) {
			done = true
		}
	}
	resp.Body.Close()
	if !done {
		t.Fatal("H2 affected by H1 revocation")
	}
}
