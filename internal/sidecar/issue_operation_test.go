package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestIssueOperationLegacyTokenlessCompatibility(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["author_agent_id"] != "legacy-agent" {
			t.Errorf("actor %+v", body)
		}
		if _, exists := body["author_run_id"]; exists {
			t.Error("legacy origin fabricated run")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"legacy-issue"}`))
	}))
	defer upstream.Close()
	server := newQueryServer(t, &IPCConfig{BaseURL: upstream.URL, Token: "host-token", WorkspaceID: "ws", CrewID: "crew", AgentID: "legacy-agent"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/issue/create", strings.NewReader(`{"title":"Legacy issue"}`))
	rec := httptest.NewRecorder()
	server.handleIssueCreate(rec, req)
	if rec.Code != 200 || calls != 1 {
		t.Fatalf("legacy create %d %s calls %d", rec.Code, rec.Body.String(), calls)
	}
}

func TestIssueOperationVerifiedRunForwardsBoundIdentityAndKey(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		handle                   func(*Server, http.ResponseWriter, *http.Request)
	}{
		{"create", "POST", "/issue/create", `{"title":"Synthetic","operation_key":"stable-op","author_run_id":"forged-run","author_chat_id":"forged-chat","author_agent_id":"forged-agent"}`, (*Server).handleIssueCreate},
		{"update", "PATCH", "/issue/ENG-1", `{"status":"IN_PROGRESS","operation_key":"stable-op","author_run_id":"forged-run","agent_id":"forged-agent"}`, (*Server).handleIssueUpdate},
		{"comment", "POST", "/issue/ENG-1/comment", `{"body":"Synthetic","operation_key":"stable-op","author_run_id":"forged-run","agent_id":"forged-agent"}`, (*Server).handleIssueComment},
	} {
		t.Run(test.name, func(t *testing.T) {
			var received map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Internal-Token") != "host-token" {
					t.Error("wrong host token")
				}
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"synthetic-issue"}`))
			}))
			defer upstream.Close()
			server, key := newRunIdentityServer(t)
			server.ipc.BaseURL = upstream.URL
			server.ipc.Token = "host-token"
			token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "verified-run")
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			test.handle(server, rec, req)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if received["author_run_id"] != "verified-run" || received["operation_key"] != "stable-op" || received["workspace_id"] != "ws-1" {
				t.Fatalf("bound origin %+v", received)
			}
			for _, field := range []string{"agent_id", "author_agent_id"} {
				if value, ok := received[field]; ok && value != "agent-boot" {
					t.Fatalf("forged actor %+v", received)
				}
			}
			if _, ok := received["author_chat_id"]; ok {
				t.Fatal("v2 forwarded caller or boot chat")
			}
		})
	}
}

func TestIssueOperationRejectsKeyErrorsBeforeForward(t *testing.T) {
	server, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "verified-run")
	for _, test := range []struct {
		name, body, header string
		status             int
		code               string
	}{
		{"conflict", `{"title":"Synthetic","operation_key":"body"}`, "header", 409, "operation_key_conflict"},
		{"too long", `{"title":"Synthetic"}`, strings.Repeat("a", 201), 400, "operation_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/issue/create", strings.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Idempotency-Key", test.header)
			rec := httptest.NewRecorder()
			server.handleIssueCreate(rec, req)
			if rec.Code != test.status || !strings.Contains(rec.Body.String(), test.code) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIssueOperationV1DowngradeRefusedOnEveryIssueMutation(t *testing.T) {
	for _, test := range []struct {
		path, method, body string
		handle             func(*Server, http.ResponseWriter, *http.Request)
	}{
		{"/issue/create", "POST", `{"title":"Synthetic"}`, (*Server).handleIssueCreate},
		{"/issue/ENG-1", "PATCH", `{"status":"DONE"}`, (*Server).handleIssueUpdate},
		{"/issue/ENG-1/comment", "POST", `{"body":"Synthetic"}`, (*Server).handleIssueComment},
		{"/issue/ENG-1/link", "POST", `{"target_identifier":"ENG-2","relation_type":"relates_to"}`, (*Server).handleIssueLink},
		{"/issue/ENG-1/work", "POST", `{"action":"handoff_human","target_id":"human"}`, (*Server).handleIssueWork},
		{"/issue/ENG-1/attachments", "POST", `{"filename":"report.txt","content_base64":"YQ=="}`, (*Server).handleIssueAttach},
	} {
		for _, withToken := range []bool{false, true} {
			t.Run(test.path+test.method+map[bool]string{false: "missing", true: "v1"}[withToken], func(t *testing.T) {
				server, _ := newRunIdentityServer(t)
				req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
				if withToken {
					req.Header.Set("Authorization", "Bearer "+server.ipc.AgentToken)
				}
				rec := httptest.NewRecorder()
				test.handle(server, rec, req)
				if rec.Code != 403 || !strings.Contains(rec.Body.String(), "legacy_token_mutation_refused") {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestIssueOperationUnsupportedVerbsUseHostRunClassification(t *testing.T) {
	for _, mode := range []string{"human", "autonomous", "unknown", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			classificationCalls, mutationCalls := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/internal/issues/authority" {
					classificationCalls++
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["workspace_id"] != "ws-1" || body["agent_id"] != "agent-boot" || body["author_run_id"] != "verified-run" || r.Header.Get("X-Internal-Token") != "host-token" {
						t.Errorf("untrusted classification %+v", body)
					}
					if mode == "unavailable" {
						w.WriteHeader(503)
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"mode": mode})
					return
				}
				mutationCalls++
				_, _ = w.Write([]byte(`{"id":"synthetic-link"}`))
			}))
			defer upstream.Close()
			server, key := newRunIdentityServer(t)
			server.ipc.BaseURL = upstream.URL
			server.ipc.Token = "host-token"
			token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "verified-run")
			req := httptest.NewRequest("POST", "/issue/ENG-1/link", strings.NewReader(`{"target_identifier":"ENG-2","relation_type":"relates_to"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.handleIssueLink(rec, req)
			expected := 503
			if mode == "human" {
				expected = 403
			}
			if mode == "autonomous" {
				expected = 200
			}
			if rec.Code != expected || classificationCalls != 1 {
				t.Fatalf("%d %s classification %d", rec.Code, rec.Body.String(), classificationCalls)
			}
			if mode == "autonomous" && mutationCalls != 1 {
				t.Fatal("autonomous compatibility broken")
			}
			if mode != "autonomous" && mutationCalls != 0 {
				t.Fatal("human or unknown authority reached old mutation")
			}
		})
	}
}

func TestIssueOperationKeylessRunDefersHumanRequirementToHost(t *testing.T) {
	server, key := newRunIdentityServer(t)
	req := httptest.NewRequest("POST", "/issue/create", nil)
	req.Header.Set("Authorization", "Bearer "+internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "autonomous-run"))
	rec := httptest.NewRecorder()
	run, operationKey, ok := server.issueOperationOrigin(rec, req, "")
	if !ok || run != "autonomous-run" || operationKey != "" {
		t.Fatalf("autonomous origin refused: %s %s %v %s", run, operationKey, ok, rec.Body.String())
	}
}
