//go:build linux && restrictedruntime_live

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestLiveNativeHTTPSelectedProjectFilesStayScopedAndReadonly(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_NATIVE_IMAGE")
	if image == "" {
		t.Fatal("explicit restrictedruntime_live acceptance requires an owned pinned native worker image")
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
		version, e := store.PutProjectFile(t.Context(), owner, workspace, project, access.ProjectFileWrite{Name: "input.txt"}, []byte("SOURCE_"+actor))
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
		if _, e = store.Replace(t.Context(), owner, user, workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "native-agent", Operation: "chat"}, {Kind: "project", ID: user + "-project", Operation: "read"}}); e != nil {
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
	manager, err := restrictedruntime.NewNative(filepath.Join(t.TempDir(), "native-runtime"), restrictedruntime.Docker{Image: image}, authority, catalog, restrictedruntime.NativeLimits())
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
				if !strings.Contains(fmt.Sprint(item["output"]), "SOURCE_"+actor) || !strings.Contains(fmt.Sprint(item["output"]), "READONLY_CONFIRMED") || strings.Contains(fmt.Sprint(item["output"]), "SOURCE_"+other) || strings.Contains(fmt.Sprint(item["output"]), "WRITE_ALLOWED") || !strings.Contains(fmt.Sprint(item["output"]), "PRIVATE_"+actor) {
					t.Errorf("scratch output missing or foreign: %+v", item)
				}
			}
		}
		var output []any
		if !followup {
			args, _ := json.Marshal(map[string]any{"cmd": "cat /data/project-inputs/" + selected[actor].ID + "/input.txt; if printf altered > /data/project-inputs/" + selected[actor].ID + "/input.txt 2>/dev/null; then echo WRITE_ALLOWED; else echo READONLY_CONFIRMED; fi; printf PRIVATE_" + actor + " > output.txt; cat output.txt", "shell": "/bin/sh", "login": false, "yield_time_ms": 1000, "max_output_tokens": 100})
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
	router, err := NewRouter(db, secret, newTestLogger(), WithRestrictedTextRunner(runner), WithInternalToken("synthetic-native-http-internal"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
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
	if status, body := request("native-h2", "native-h2-chat", "POST", "/restricted-run", `{"content":"foreign selection","project_file_versions":["`+selected["native-h1"].ID+`"]}`); status != http.StatusForbidden || strings.Contains(body, "SOURCE_") {
		t.Fatalf("foreign selected source %d %q", status, body)
	}
	for _, actor := range []string{"native-h1", "native-h2"} {
		status, body := request(actor, actor+"-chat", "POST", "/restricted-run", `{"content":"ACTOR_`+actor+`","project_file_versions":["`+selected[actor].ID+`"]}`)
		if status != 200 || !strings.Contains(body, "DONE_"+actor) || !strings.Contains(body, `"type":"done"`) {
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
	first := selected["native-h1"]
	if err = store.RetireProjectFile(t.Context(), owner, workspace, first.ProjectID, first.FileID, first.Revision); err != nil {
		t.Fatal(err)
	}
	if files, e := store.FilesForChat(t.Context(), "native-h1", workspace, "native-agent", "native-h1-chat"); e != nil || len(files) != 0 {
		t.Fatalf("revoked source derived file remained readable: %v %v", files, e)
	}
	if files, e := store.FilesForChat(t.Context(), "native-h2", workspace, "native-agent", "native-h2-chat"); e != nil || len(files) != 1 {
		t.Fatalf("unrelated human source was revoked: %v %v", files, e)
	}
}
