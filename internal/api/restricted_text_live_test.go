//go:build linux && restrictedruntime_live

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

// Bound each fixture response independently of the overall Go test timeout.
// A worker regression that floods output must fail rather than exhaust CI.
type boundedLiveTextScanner struct {
	t             *testing.T
	scanner       *bufio.Scanner
	body          io.ReadCloser
	frames, bytes int
}

func (s *boundedLiveTextScanner) Scan() bool {
	s.t.Helper()
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			s.t.Errorf("production stream read: %v", err)
		}
		return false
	}
	s.frames++
	s.bytes += len(s.scanner.Bytes()) + 1
	if s.frames > 256 || s.bytes > 256<<10 {
		_ = s.body.Close()
		s.t.Fatal("production stream exceeded fixture frame/byte limit")
	}
	return true
}
func (s *boundedLiveTextScanner) Text() string { return s.scanner.Text() }
func (s *boundedLiveTextScanner) Err() error   { return s.scanner.Err() }

func TestLiveRestrictedTextRouterProductionWorker(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_PRODUCTION_IMAGE")
	if image == "" {
		t.Fatal("explicit restrictedruntime_live acceptance requires an owned production worker image")
	}
	testCtx, cancelTest := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancelTest()
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('text-crew',?,'Crew','text-crew')`, workspace)
	seedAgentRow(t, db, "text-agent", workspace, "text-crew", "Text", "text-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text',llm_provider='OPENAI',llm_model='gpt-5-mini',system_prompt_legacy='SHARED_PROMPT_CANARY' WHERE id='text-agent'`)
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
		_, err = store.Replace(t.Context(), owner, user, workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "text-agent", Operation: "discover"}, {Kind: "agent", ID: "text-agent", Operation: "chat"}})
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
	shutdownProvider := make(chan struct{})
	clientCancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	groupRelease := make(chan struct{})
	defer func() {
		select {
		case <-groupRelease:
		default:
			close(groupRelease)
		}
	}()
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

		if strings.Contains(body.Input, "GROUP_") {
			if strings.Contains(body.Input, "CANARY_text-") {
				t.Error("private canary entered group prompt")
			}
			if strings.Contains(body.Input, "GROUP_SECOND") && (!strings.Contains(body.Input, "GROUP_FIRST") || !strings.Contains(body.Input, "answer-h1")) {
				t.Error("shared group history missing")
			}
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
		if strings.Contains(body.Input, "GROUP_EPOCH_STREAM") {
			select {
			case <-groupRelease:
			case <-req.Context().Done():
				return
			case <-shutdownProvider:
				return
			}
		}
		if strings.Contains(body.Input, "REVOKE_DURING_STREAM") {
			select {
			case <-release:
			case <-req.Context().Done():
				return
			case <-shutdownProvider:
				return
			}
		}
		if strings.Contains(body.Input, "CANCEL_DURING_STREAM") {
			select {
			case <-req.Context().Done():
				select {
				case clientCancelled <- struct{}{}:
				default:
				}
			case <-shutdownProvider:
			}
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5-mini\",\"usage\":{\"input_tokens\":20,\"output_tokens\":5,\"total_tokens\":25,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
	}))
	defer upstream.Close()
	authority := restricteddispatch.Authority{Store: store}
	// Preserve the manager's owner/attempt records if termination cannot be
	// confirmed; a shared development host must not lose its recovery handle.
	runtimeDir, err := os.MkdirTemp("", "crewship-text-http-live-")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := restrictedruntime.New(runtimeDir, restrictedruntime.Docker{Image: image}, authority, authority, restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		_ = os.RemoveAll(runtimeDir) // New has not admitted any worker yet.
		t.Fatal(err)
	}
	defer func() {
		clean := true
		if err := manager.Close(); err != nil {
			t.Errorf("owned production runtime cleanup: %v", err)
			clean = false
		}
		owner, err := os.ReadFile(filepath.Join(runtimeDir, "owner"))
		if err != nil {
			t.Error(err)
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ids, err := exec.CommandContext(cleanupCtx, "docker", "ps", "-aq", "--filter", "label=ai.crewship.restricted.owner="+string(owner)).Output()
		if err != nil || strings.TrimSpace(string(ids)) != "" {
			t.Errorf("owned production containers remain or absence unconfirmed: %v", err)
			clean = false
		}
		if clean {
			if err := os.RemoveAll(runtimeDir); err != nil {
				t.Errorf("remove confirmed empty owned runtime state: %v", err)
			}
		} else {
			t.Logf("owned runtime recovery state retained at %s", runtimeDir)
		}
	}()
	if err = manager.Reconcile(testCtx); err != nil {
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
	workflow, err := restrictedworkflow.New(db, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer workflow.Close()
	router.SetRestrictedWorkflow(workflow)
	server := httptest.NewServer(router)
	defer server.Close()
	defer cancelTest() // cancel before server.Close, including failed reads
	defer close(shutdownProvider)
	streamScanner := func(body io.ReadCloser) *boundedLiveTextScanner {
		t.Cleanup(func() { _ = body.Close() })
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		return &boundedLiveTextScanner{t: t, scanner: scanner, body: body}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	request := func(user, chat, content string) *http.Response {
		body, _ := json.Marshal(map[string]string{"content": content})
		req, err := http.NewRequestWithContext(testCtx, http.MethodPost, server.URL+"/api/v1/chats/"+chat+"/restricted-run?workspace_id="+workspace, strings.NewReader(string(body)))
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
		scan := streamScanner(resp.Body)
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

	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('text-group',?,'text-agent','text-h1','group')`, workspace)
	execOrFatal(t, db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('text-group','text-h2','member')`)
	for _, turn := range []struct{ user, content string }{{"text-h1", "GROUP_FIRST"}, {"text-h2", "GROUP_SECOND"}} {
		response := request(turn.user, "text-group", turn.content)
		scanner := streamScanner(response.Body)
		complete := false
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), `"type":"done"`) {
				complete = true
			}
		}
		response.Body.Close()
		if response.StatusCode != 200 || !complete {
			t.Fatalf("group run %s status=%d complete=%v", turn.user, response.StatusCode, complete)
		}
	}
	for _, user := range []string{"text-h1", "text-h2"} {
		entries, err := store.ContextEntriesForChat(t.Context(), user, workspace, "text-agent", "text-group")
		if err != nil || len(entries) != 4 {
			t.Fatalf("group history user=%s count=%d err=%v", user, len(entries), err)
		}
	}
	groupResp := request("text-h1", "text-group", "GROUP_EPOCH_STREAM")
	groupScanner := streamScanner(groupResp.Body)
	if !groupScanner.Scan() || !strings.Contains(groupScanner.Text(), `"type":"text"`) {
		t.Fatal("group text absent")
	}
	execOrFatal(t, db, `DELETE FROM chat_participants WHERE chat_id='text-group' AND user_id='text-h2'`)
	execOrFatal(t, db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('text-group','text-h2','member')`)
	close(groupRelease)
	for groupScanner.Scan() {
		if strings.Contains(groupScanner.Text(), `"type":"done"`) {
			t.Fatal("group completion after audience epoch change")
		}
	}
	groupResp.Body.Close()
	entries, err := store.ContextEntriesForChat(t.Context(), "text-h2", workspace, "text-agent", "text-group")
	if err != nil || len(entries) != 0 {
		t.Fatalf("old epoch group history reused count=%d err=%v", len(entries), err)
	}
	before := calls.Load()
	resp := request("text-h2", "text-h1-chat", "foreign")
	resp.Body.Close()
	if resp.StatusCode != 403 || calls.Load() != before {
		t.Fatal("foreign request reached provider")
	}
	resp = request("text-h1", "text-h1-chat", "REVOKE_DURING_STREAM")
	scan := streamScanner(resp.Body)
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
	scan = streamScanner(resp.Body)
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
	// Client cancellation must reach the actual provider and stop its stream;
	// fixture cleanup confirms owned worker termination separately below.
	cancelCtx, cancelRequest := context.WithCancel(testCtx)
	defer cancelRequest()
	cancelReq, err := http.NewRequestWithContext(cancelCtx, http.MethodPost, server.URL+"/api/v1/chats/text-h2-chat/restricted-run?workspace_id="+workspace, strings.NewReader(`{"content":"CANCEL_DURING_STREAM CANARY_text-h2"}`))
	if err != nil {
		t.Fatal(err)
	}
	cancelReq.Header.Set("Authorization", "Bearer "+tokens["text-h2"])
	cancelResp, err := client.Do(cancelReq)
	if err != nil {
		cancelRequest()
		t.Fatal(err)
	}
	cancelScanner := streamScanner(cancelResp.Body)
	if !cancelScanner.Scan() || !strings.Contains(cancelScanner.Text(), `"type":"text"`) {
		cancelRequest()
		t.Fatal("cancellation fixture never received live text")
	}
	cancelRequest()
	_ = cancelResp.Body.Close()
	select {
	case <-clientCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("client cancellation did not stop the production provider stream")
	}

	// A fresh run-only ordinary CLI context reaches the same production worker,
	// without acquiring conversational permissions or importing the prior chat.
	member, err = store.Membership(t.Context(), "text-h2", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "text-h2", workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "text-agent", Operation: "run"}, {Kind: "agent", ID: "text-agent", Operation: "discover"}}); err != nil {
		t.Fatal(err)
	}
	cliRequest := func(path, body string) *http.Response {
		req, err := http.NewRequestWithContext(testCtx, http.MethodPost, server.URL+path+"?workspace_id="+workspace, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens["text-h2"])
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	created := cliRequest("/api/v1/agents/text-agent/restricted-cli-chats", `{}`)
	var contextRow struct {
		ID string `json:"id"`
	}
	if created.StatusCode != 201 || json.NewDecoder(created.Body).Decode(&contextRow) != nil || contextRow.ID == "" {
		t.Fatal("run-only production CLI context unavailable")
	}
	created.Body.Close()
	runResp := cliRequest("/api/v1/chats/"+contextRow.ID+"/restricted-cli-run", `{"content":"CANARY_text-h2 fresh cli run"}`)
	scan = streamScanner(runResp.Body)
	done = false
	for scan.Scan() {
		if strings.Contains(scan.Text(), `"type":"done"`) {
			done = true
		}
	}
	runResp.Body.Close()
	if runResp.StatusCode != 200 || !done {
		t.Fatalf("run-only production CLI status=%d done=%v", runResp.StatusCode, done)
	}

	// Ordinary manual routine admission, durable private queue and result HTTP
	// projection reach the actual production worker with no shared journal.
	recipe := `{"dsl_version":"1.0","name":"live-private","inputs":[{"name":"task","type":"string","required":true}],"steps":[{"id":"first","type":"agent_run","agent_slug":"text-agent","prompt":"{{ inputs.task }}"},{"id":"second","type":"agent_run","agent_slug":"text-agent","prompt":"Continue {{ steps.first.output }}"}]}`
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('live-private',?,'live-private','Private',?,'live-fixture','text-crew','active')`, workspace, recipe)
	for _, user := range []string{"text-h1", "text-h2"} {
		execOrFatal(t, db, `UPDATE workspace_members SET capabilities='["routine.run"]' WHERE user_id=? AND workspace_id=?`, user, workspace)
		m, err := store.Membership(t.Context(), user, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, user, workspace, "restricted", m, []access.Right{{Kind: "agent", ID: "text-agent", Operation: "run"}}); err != nil {
			t.Fatal(err)
		}
	}
	workflowRequest := func(user, method, path, body string) *http.Response {
		req, err := http.NewRequestWithContext(testCtx, method, server.URL+path+"?workspace_id="+workspace, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	receipts := map[string]string{}
	for _, user := range []string{"text-h1", "text-h2"} {
		response := workflowRequest(user, http.MethodPost, "/api/v1/workspaces/"+workspace+"/pipelines/live-private/run", fmt.Sprintf(`{"inputs":{"task":"CANARY_%s"}}`, user))
		var receipt struct {
			ID string `json:"run_id"`
		}
		if response.StatusCode != 202 || json.NewDecoder(response.Body).Decode(&receipt) != nil || receipt.ID == "" {
			t.Fatalf("ordinary workflow admission %s %d", user, response.StatusCode)
		}
		response.Body.Close()
		receipts[user] = receipt.ID
		if worked, err := workflow.DispatchNext(testCtx); !worked || err != nil {
			t.Fatalf("production workflow %s %v %v", user, worked, err)
		}
		response = workflowRequest(user, http.MethodGet, "/api/v1/workspaces/"+workspace+"/restricted-routine-runs/"+receipt.ID, "")
		var result struct {
			State   string            `json:"status"`
			Outputs map[string]string `json:"step_outputs"`
		}
		if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&result) != nil || result.State != "completed" || len(result.Outputs) != 2 {
			t.Fatalf("private production result %s %d %+v", user, response.StatusCode, result)
		}
		response.Body.Close()
	}
	foreign := workflowRequest("text-h2", http.MethodGet, "/api/v1/workspaces/"+workspace+"/restricted-routine-runs/"+receipts["text-h1"], "")
	if foreign.StatusCode != 404 {
		t.Fatal("foreign production workflow result exposed")
	}
	foreign.Body.Close()
	queued := workflowRequest("text-h2", http.MethodPost, "/api/v1/workspaces/"+workspace+"/pipelines/live-private/run", `{"inputs":{"task":"DO_NOT_BUILD_REVOKED_QUEUE"}}`)
	if queued.StatusCode != 202 {
		t.Fatalf("queue preparation %d", queued.StatusCode)
	}
	queued.Body.Close()
	beforeQueued := calls.Load()
	execOrFatal(t, db, `UPDATE pipelines SET definition_json=? WHERE id='live-private'`, strings.Replace(recipe, "Continue", "Changed", 1))
	if worked, err := workflow.DispatchNext(testCtx); !worked || err == nil || calls.Load() != beforeQueued {
		t.Fatalf("revoked queue reached production worker %v %v calls%d->%d", worked, err, beforeQueued, calls.Load())
	}
	for _, table := range []string{"pipeline_runs", "pending_runs"} {
		var count int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("private production work leaked %s %d %v", table, count, err)
		}
	}

}
