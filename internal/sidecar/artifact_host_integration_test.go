package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Real human session -> run admission -> delegation -> sidecar -> crew-bound
// host API. No live server, agent process or Docker is involved.
func TestArtifactHostHTTPDelegatedLifecycle(t *testing.T) {
	h := newR6Host(t)
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('artifact-human','artifact-human@test','Human')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('artifact-member','ws-r6','artifact-human','OWNER')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('artifact-chat','ws-r6','agent-r6-alpha','artifact-human','private')`,
		`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('artifact-message','artifact-chat','agent-r6-alpha','user','report','2026-10-13T00:00:00Z','artifact-human')`,
	} {
		if _, err := h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(h.db, runTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	router, err := api.NewRouter(h.db, r6JWT, runTestLogger(), api.WithInternalToken(r6Master), api.WithStoragePath(h.storage), api.WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(router)
	defer upstream.Close()
	session, err := sessions.NewDBStore(h.db).Create(context.Background(), "artifact-human", "test", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	validator, _ := auth.NewJWTValidator(r6JWT)
	human, err := validator.IssueAccessToken("artifact-human", session.ID, "Human", "artifact-human@test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, ipc, bearer string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		r, _ := http.NewRequest(method, upstream.URL+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if ipc != "" {
			r.Header.Set("X-Internal-Token", ipc)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s status%d want%d: %s", method, path, res.StatusCode, want, b)
		}
		return b
	}
	call("POST", "/api/v1/pages?workspace_id="+r6WS, "", human, map[string]any{"slug": "report", "name": "Report", "panels": []any{}, "allow_empty": true}, 201)
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='report'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	_, err = h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'agent',?,'write','artifact-human',?)`, page, r6AgentID, tsformat.Format(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := chataudience.CaptureTrusted(context.Background(), h.db, "artifact-chat", "artifact-human", nil)
	if err != nil || receipt == nil {
		t.Fatalf("receipt %v", err)
	}
	admission := map[string]any{"id": r6RunA, "workspace_id": r6WS, "agent_id": r6AgentID, "chat_id": "artifact-chat", "trigger_type": "USER", "artifact_human_receipt": receipt, "artifact_human_message_id": "artifact-message"}
	crew := internaltoken.DeriveCrewToken(r6Master, r6WS, r6Crew)
	// Even a genuine receipt cannot turn crew transport into human admission.
	call("POST", "/api/v1/internal/runs", crew, "", admission, 403)
	call("POST", "/api/v1/internal/runs", r6Master, "", admission, 201)
	call("POST", "/api/v1/pages/report/artifact/delegations?workspace_id="+r6WS, "", human, map[string]any{"run_id": r6RunA}, 201)
	call("POST", "/api/v1/internal/artifacts/report/revisions", r6Master, r6RunToken(r6RunA), map[string]any{"html": "x", "base_rev": 0, "mutation_id": "master-denied"}, 403)
	s, _ := newR6Sidecar(t, upstream.URL)
	side := httptest.NewServer(s.buildHandler(NewProxy(ProxyConfig{})))
	defer side.Close()
	agentCall := func(method, path, body string, want int) {
		t.Helper()
		r, _ := http.NewRequest(method, side.URL+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+r6RunToken(r6RunA))
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("sidecar%s status%d want%d %s", path, res.StatusCode, want, b)
		}
	}
	agentCall("POST", "/artifacts/report/revisions", `{"html":"x","base_rev":0,"mutation_id":"first","agent_id":"forged"}`, 400)
	agentCall("POST", "/artifacts/report/revisions", `{"html":"<h1>Report</h1>","base_rev":0,"mutation_id":"first"}`, 201)
	agentCall("GET", "/artifacts/report?revision=1", "", 200)
	call("DELETE", "/api/v1/pages/report/artifact/delegations/"+r6RunA+"?workspace_id="+r6WS, "", human, nil, 204)
	agentCall("GET", "/artifacts/report", "", 403)
	agentCall("POST", "/artifacts/report/revisions", `{"html":"blocked","base_rev":1,"mutation_id":"second"}`, 403)
}
