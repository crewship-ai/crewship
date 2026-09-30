//go:build linux && restrictedruntime_live

package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// liveNativeObserver records only fixed operation/error classes. It never
// logs handles, principals, requests, outputs, credentials or container IDs.
type liveNativeObserver struct {
	mu                sync.Mutex
	counts            map[string]int
	durationMaxMillis map[string]int64
}

func (o *liveNativeObserver) observe(stage string, ctx context.Context, err error) {
	class := "ok"
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		class = "context_expired"
	} else if errors.Is(err, access.ErrDenied) || errors.Is(err, restrictedruntime.ErrDenied) {
		class = "denied"
	} else if err != nil {
		class = "storage_or_runtime_error"
	}
	o.mu.Lock()
	o.counts[stage+":"+class]++
	o.mu.Unlock()
}
func (o *liveNativeObserver) duration(stage string, started time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.durationMaxMillis == nil {
		o.durationMaxMillis = map[string]int64{}
	}
	elapsed := time.Since(started).Milliseconds()
	if elapsed > o.durationMaxMillis[stage] {
		o.durationMaxMillis[stage] = elapsed
	}
}
func (o *liveNativeObserver) dump(t *testing.T, dir string) {
	o.mu.Lock()
	counts := make(map[string]int, len(o.counts))
	for k, v := range o.counts {
		counts[k] = v
	}
	timings := make(map[string]int64, len(o.durationMaxMillis))
	for k, v := range o.durationMaxMillis {
		timings[k] = v
	}
	o.mu.Unlock()
	t.Logf("native diagnostics max_operation_millis=%v", timings)
	t.Logf("native diagnostics operations=%v sandbox_ready=%t", counts, restrictedruntime.NativeSandboxReady(context.WithoutCancel(t.Context())) == nil)
	// Query only this synthetic manager's immutable owner label. Counts are
	// diagnostic evidence; uncertainty never becomes successful cleanup.
	if owner, e := os.ReadFile(filepath.Join(dir, "owner")); e == nil {
		decoded, e := hex.DecodeString(string(owner))
		if e == nil && len(decoded) == 8 {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Second)
			raw, e := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=ai.crewship.restricted.owner="+string(owner)).Output()
			cancel()
			if e == nil {
				t.Logf("native diagnostics owned_container_count=%d", len(strings.Fields(string(raw))))
			} else {
				t.Log("native diagnostics owned_container_lookup_unconfirmed")
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Log("native diagnostics records_unavailable")
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		var record restrictedruntime.Record
		if e != nil || json.Unmarshal(raw, &record) != nil {
			t.Log("native diagnostics record_unreadable")
			continue
		}
		// Status and Reason are fixed runtime states, not model-controlled text.
		t.Logf("native diagnostics runtime_status=%q runtime_reason=%q", record.Status, record.Reason)
	}
}

type liveObservedNativeAuthority struct {
	restricteddispatch.Authority
	observer *liveNativeObserver
}

func (a liveObservedNativeAuthority) Resolve(ctx context.Context, h string) (restrictedruntime.Plan, error) {
	started := time.Now()
	p, e := a.Authority.Resolve(ctx, h)
	a.observer.duration("resolve", started)
	a.observer.observe("resolve", ctx, e)
	if e == nil {
		stage := "resolve_without_inputs"
		if p.NativeInputs != nil {
			stage = "resolve_with_inputs"
		}
		a.observer.observe(stage, ctx, nil)
	}
	return p, e
}
func (a liveObservedNativeAuthority) Secrets(ctx context.Context, h string) (map[string]string, error) {
	v, e := a.Authority.Secrets(ctx, h)
	a.observer.observe("secrets", ctx, e)
	return v, e
}
func (a liveObservedNativeAuthority) BrokerSecret(ctx context.Context, h, c string) (restrictedruntime.BoundSecret, error) {
	v, e := a.Authority.BrokerSecret(ctx, h, c)
	a.observer.observe("broker_secret", ctx, e)
	return v, e
}
func (a liveObservedNativeAuthority) BrokerReserve(ctx context.Context, h, c, m string, i, o int64) (string, error) {
	v, e := a.Authority.BrokerReserve(ctx, h, c, m, i, o)
	a.observer.observe("broker_reserve", ctx, e)
	return v, e
}
func (a liveObservedNativeAuthority) BrokerSettle(ctx context.Context, h, r string, u restrictedruntime.BrokerUsage) error {
	e := a.Authority.BrokerSettle(ctx, h, r, u)
	a.observer.observe("broker_settle", ctx, e)
	return e
}
func (a liveObservedNativeAuthority) NativeRequest(ctx context.Context, h, c string, raw []byte) ([]byte, string, error) {
	v, t, e := a.Authority.NativeRequest(ctx, h, c, raw)
	a.observer.observe("native_request", ctx, e)
	return v, t, e
}
func (a liveObservedNativeAuthority) NativeComplete(ctx context.Context, h, t string, issued []json.RawMessage) error {
	e := a.Authority.NativeComplete(ctx, h, t, issued)
	a.observer.observe("native_complete", ctx, e)
	return e
}

type liveObservedNativeRunner struct {
	*restricteddispatch.NativeRunner
	observer *liveNativeObserver
}

func (r liveObservedNativeRunner) Execute(ctx context.Context, u, w, c, input string, emit func(string, string) error) error {
	e := r.NativeRunner.Execute(ctx, u, w, c, input, emit)
	r.observer.observe("execute_chat", ctx, e)
	return e
}
func (r liveObservedNativeRunner) ExecuteWithProjectFiles(ctx context.Context, u, w, c, input string, ids []string, emit func(string, string) error) error {
	e := r.NativeRunner.ExecuteWithProjectFiles(ctx, u, w, c, input, ids, emit)
	r.observer.observe("execute_chat_inputs", ctx, e)
	return e
}

type liveObservedNativeCatalog struct {
	*restrictedruntime.FrozenNativeCatalog
	observer *liveNativeObserver
}

func (c liveObservedNativeCatalog) Volume(ctx context.Context, p restrictedruntime.Plan, m restrictedruntime.Mount) (string, error) {
	v, e := c.FrozenNativeCatalog.Volume(ctx, p, m)
	c.observer.observe("catalog_volume", ctx, e)
	return v, e
}
func (c liveObservedNativeCatalog) FreezeNativeInputs(ctx context.Context, p restrictedruntime.Plan, id string) error {
	e := c.FrozenNativeCatalog.FreezeNativeInputs(ctx, p, id)
	c.observer.observe("catalog_freeze", ctx, e)
	return e
}
func (c liveObservedNativeCatalog) ReleaseNativeInputs(ctx context.Context, p restrictedruntime.Plan) error {
	e := c.FrozenNativeCatalog.ReleaseNativeInputs(ctx, p)
	c.observer.observe("catalog_release", ctx, e)
	return e
}
func (c liveObservedNativeCatalog) ReconcileNativeInputs(ctx context.Context) error {
	e := c.FrozenNativeCatalog.ReconcileNativeInputs(ctx)
	c.observer.observe("catalog_reconcile", ctx, e)
	return e
}

func TestLiveNativeHTTPRetainsOnlyOwnScratchFiles(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_NATIVE_IMAGE")
	if image == "" {
		t.Skip("owned pinned native worker image required")
	}
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('native-crew',?,'Native','native-crew')`, workspace)
	seedAgentRow(t, db, "native-agent", workspace, "native-crew", "Native", "native-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='native_api_key',llm_provider='OPENAI',llm_model='gpt-5-mini',system_prompt_legacy='UNCLASSIFIED_PROMPT_CANARY' WHERE id='native-agent'`)
	cipher, err := encryption.Encrypt("synthetic-native-key")
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('native-key',?,'Key',?,'API_KEY','OPENAI',?)`, workspace, cipher, owner)
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('native-grant','native-agent','native-key','OPENAI_API_KEY')`)
	execOrFatal(t, db, `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('native-http-cap',?,'workspace',?,'month',.4,'hard')`, workspace, workspace)
	store := access.Store{DB: db}
	const secret = "synthetic-native-http-jwt-secret-2026"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	selected := map[string]access.ProjectFileVersion{}
	for _, actor := range []string{"native-h1", "native-h2"} {
		project := actor + "-project"
		execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,?,?,?)`, project, workspace, project, project)
		version, e := store.PutProjectFile(t.Context(), owner, workspace, project, access.ProjectFileWrite{Name: actor + "-brief.txt"}, []byte("SOURCE_"+actor))
		if e != nil {
			t.Fatal(e)
		}
		selected[actor] = version
	}
	for _, user := range []string{"native-h1", "native-h2"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@native.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, user, workspace, user)
		execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,'native-agent',?,'private')`, user+"-chat", workspace, user)
		member, e := store.Membership(t.Context(), user, workspace)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.Replace(t.Context(), owner, user, workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "native-agent", Operation: "chat"}, {Kind: "agent", ID: "native-agent", Operation: "discover"}, {Kind: "project", ID: user + "-project", Operation: "read"}}); e != nil {
			t.Fatal(e)
		}
		if _, e = store.SaveNote(t.Context(), user, workspace, "native-agent", user+"-chat", "MEMORY_"+user); e != nil {
			t.Fatal(e)
		}
		session, e := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@native.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	authority := restricteddispatch.Authority{Store: store}
	catalog, err := restrictedruntime.NewFrozenNativeCatalog(filepath.Join(t.TempDir(), "native-inputs"), restrictedruntime.Docker{Image: image}, restricteddispatch.ProjectInputSource(store))
	if err != nil {
		t.Fatal(err)
	}
	observer := &liveNativeObserver{counts: map[string]int{}}
	runtimeDir := filepath.Join(t.TempDir(), "native-runtime")
	t.Cleanup(func() {
		if t.Failed() {
			observer.dump(t, runtimeDir)
		}
	})
	observedAuthority := liveObservedNativeAuthority{Authority: authority, observer: observer}
	observedCatalog := liveObservedNativeCatalog{FrozenNativeCatalog: catalog, observer: observer}
	manager, err := restrictedruntime.NewNative(runtimeDir, restrictedruntime.Docker{Image: image}, observedAuthority, observedCatalog, restrictedruntime.NativeLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := manager.Close(); e != nil {
			t.Error(e)
		}
	})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid native provider body")
			w.WriteHeader(400)
			return
		}
		raw, _ := json.Marshal(body)
		actor := "native-h1"
		other := "native-h2"
		if strings.Contains(string(raw), "ACTOR_native-h2") {
			actor, other = other, actor
		}
		if !strings.Contains(string(raw), "MEMORY_"+actor) || strings.Contains(string(raw), "MEMORY_"+other) || strings.Contains(string(raw), "UNCLASSIFIED_PROMPT_CANARY") {
			t.Errorf("native HTTP prompt crossed scope: %s", raw)
		}
		followup := false
		for _, value := range body["input"].([]any) {
			item := value.(map[string]any)
			if item["type"] == "function_call_output" {
				followup = true
				if !strings.Contains(fmt.Sprint(item["output"]), "PRIVATE_"+actor) {
					t.Errorf("scratch output missing or foreign: %+v", item)
				}
			}
		}
		var output []any
		if !followup {
			args, _ := json.Marshal(map[string]any{"cmd": "printf PRIVATE_" + actor + " > output.txt; cat output.txt", "shell": "/bin/sh", "login": false, "yield_time_ms": 1000, "max_output_tokens": 100})
			output = []any{map[string]any{"type": "function_call", "id": "fc_" + actor, "call_id": "call_" + actor, "name": "exec_command", "arguments": string(args), "status": "completed"}}
		} else {
			output = []any{map[string]any{"type": "message", "id": "msg_" + actor, "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "DONE_" + actor, "annotations": []any{}}}}}
		}
		response := map[string]any{"id": "resp_" + actor + fmt.Sprint(followup), "model": "gpt-5-mini", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 20, "output_tokens": 20, "total_tokens": 40, "input_tokens_details": map[string]any{"cached_tokens": 0}}}
		payload, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: "+string(payload)+"\n\n")
	}))
	t.Cleanup(upstream.Close)
	if err = restrictedruntime.InstallAcceptanceTLS(manager, upstream); err != nil {
		t.Fatal(err)
	}
	runner := &restricteddispatch.NativeRunner{Authority: authority, Manager: manager, MaxOutputTokens: 128}
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		started := time.Now()
		session, e := manager.Start(ctx, handle)
		observer.duration("start", started)
		observer.observe("start", ctx, e)
		return session, e
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithRestrictedTextRunner(liveObservedNativeRunner{NativeRunner: runner, observer: observer}), WithInternalToken("synthetic-native-http-internal"))
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler = router
	if repo := os.Getenv("CREWSHIP_RESTRICTED_BROWSER_REPO"); repo != "" {
		// Use the production static handler and actual authenticated API. The
		// exported frontend is built independently before this opt-in test.
		if _, e := os.Stat(filepath.Join(repo, "out", "index.html")); e != nil {
			t.Fatal("production browser acceptance requires pnpm build first", e)
		}
		mux := http.NewServeMux()
		mux.Handle("/api/", router)
		mux.Handle("/ws", router)
		mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
		handler = mux
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: time.Minute}
	request := func(actor, chat, method, suffix, content string) (int, string) {
		req, e := http.NewRequestWithContext(t.Context(), method, server.URL+"/api/v1/chats/"+chat+suffix+"?workspace_id="+workspace, strings.NewReader(content))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+tokens[actor])
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		data, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, string(data)
	}
	for _, actor := range []string{"native-h1", "native-h2"} {
		status, body := request(actor, actor+"-chat", "POST", "/restricted-run", `{"content":"ACTOR_`+actor+`"}`)
		if status != 200 || !strings.Contains(body, "DONE_"+actor) || !strings.Contains(body, `"type":"done"`) {
			observer.dump(t, runtimeDir)
			t.Fatalf("native HTTP completion %d %s", status, body)
		}
		files, e := store.FilesForChat(t.Context(), actor, workspace, "native-agent", actor+"-chat")
		if e != nil || len(files) != 1 {
			t.Fatalf("retained native output: %+v %v", files, e)
		}
		status, body = request(actor, actor+"-chat", "GET", "/restricted-files/"+files[0].ID+"/download", "")
		if status != 200 || body != "PRIVATE_"+actor {
			t.Fatalf("own completed file download %d %q", status, body)
		}
		other := "native-h2"
		if actor == other {
			other = "native-h1"
		}
		if status, body = request(other, other+"-chat", "GET", "/restricted-files/"+files[0].ID+"/download", ""); status != 404 || strings.Contains(body, "PRIVATE_") {
			t.Fatalf("foreign file leak %d %q", status, body)
		}
	}
	if repo := os.Getenv("CREWSHIP_RESTRICTED_BROWSER_REPO"); repo != "" {
		fixture, e := json.Marshal(map[string]any{"repo": repo, "server": server.URL, "tokens": tokens, "workspace": workspace, "project_files": selected})
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(t.TempDir(), "browser-fixture.json")
		if e = os.WriteFile(path, fixture, 0600); e != nil {
			t.Fatal(e)
		}
		command := exec.CommandContext(t.Context(), "node", filepath.Join(repo, "scripts/acceptance-restricted-browser.mjs"))
		command.Env = append(os.Environ(), "CREWSHIP_BROWSER_FIXTURE="+path)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if e = command.Run(); e != nil {
			t.Fatalf("actual restricted browser acceptance: %v", e)
		}
		// Closing both browser contexts must not withdraw completed outputs.
		for _, actor := range []string{"native-h1", "native-h2"} {
			files, e := store.FilesForChat(t.Context(), actor, workspace, "native-agent", actor+"-chat")
			if e != nil || len(files) != 2 {
				t.Fatalf("browser completion did not retain both own output versions: %s %+v %v", actor, files, e)
			}
		}
	}
}
