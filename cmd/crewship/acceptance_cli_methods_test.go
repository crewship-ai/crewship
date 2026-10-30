package main

// Acceptance for the crew member set-role, agent learning, agent files
// --container and persona suggest commands, driven through the BUILT BINARY
// against an httptest stub. Each case asserts the exact method + path (+ query
// + body) that reached the server and the printed output.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type methodsCall struct{ method, path, query, body string }

type methodsStub struct {
	mu    sync.Mutex
	calls []methodsCall
}

func (s *methodsStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			_, _ = w.Write([]byte(`[{"id":"agent_1","slug":"viktor","crew_id":"crew_1"}]`))
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces":
			// Workspace-slug probe: not part of the contract under test.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such workspace"}`))
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews":
			_, _ = w.Write([]byte(`[{"id":"crew_1","slug":"core","name":"Core"}]`))
			return
		}
		q := r.URL.Query()
		q.Del("workspace_id") // injected by the client on every call
		s.mu.Lock()
		s.calls = append(s.calls, methodsCall{r.Method, r.URL.Path, q.Encode(), string(b)})
		s.mu.Unlock()
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/crews/crew_1/members/m_1":
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents/agent_1/learning":
			_, _ = w.Write([]byte(`{"agent_id":"agent_1","enabled":true,"set_by_user_id":"u_1","set_at":"2026-10-01T00:00:00Z","reason":"trial run"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents/agent_1/container-files":
			_, _ = w.Write([]byte(`[{"name":"app/main.go","size":12}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/agent_1/persona/suggest":
			_, _ = w.Write([]byte(`{"agent_id":"agent_1","decision":"inbox_approve","bytes":5,"applied":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no stub"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_CLIMethods(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		want      methodsCall
		wantOut   []string
		wantFails bool
	}{
		{
			name:    "crew_member_set_role",
			args:    []string{"crew", "member", "set-role", "core", "m_1", "MANAGER"},
			want:    methodsCall{http.MethodPatch, "/api/v1/crews/crew_1/members/m_1", "", `{"role":"MANAGER"}`},
			wantOut: []string{"MANAGER"},
		},
		{
			name:    "crew_member_set_role_clear",
			args:    []string{"crew", "member", "set-role", "core", "m_1", "--clear"},
			want:    methodsCall{http.MethodPatch, "/api/v1/crews/crew_1/members/m_1", "", `{"role":""}`},
			wantOut: []string{"cleared"},
		},
		{
			// The server ranks roles by upper-case name; a typed `manager`
			// must not be rejected as an unknown role.
			name:    "crew_member_set_role_normalises_case",
			args:    []string{"crew", "member", "set-role", "core", "m_1", "manager"},
			want:    methodsCall{http.MethodPatch, "/api/v1/crews/crew_1/members/m_1", "", `{"role":"MANAGER"}`},
			wantOut: []string{"MANAGER"},
		},
		{
			name:      "crew_member_set_role_rejects_dot_segment",
			args:      []string{"crew", "member", "set-role", "core", "..", "ADMIN"},
			wantFails: true,
		},
		{
			name:      "crew_member_set_role_needs_role_or_clear",
			args:      []string{"crew", "member", "set-role", "core", "m_1"},
			wantFails: true,
		},
		{
			name:      "crew_member_set_role_rejects_both",
			args:      []string{"crew", "member", "set-role", "core", "m_1", "ADMIN", "--clear"},
			wantFails: true,
		},
		{
			name:    "agent_learning",
			args:    []string{"agent", "learning", "viktor"},
			want:    methodsCall{http.MethodGet, "/api/v1/agents/agent_1/learning", "", ""},
			wantOut: []string{"true", "trial run", "u_1"},
		},
		{
			name:    "agent_files_container",
			args:    []string{"agent", "files", "viktor", "--container"},
			want:    methodsCall{http.MethodGet, "/api/v1/agents/agent_1/container-files", "", ""},
			wantOut: []string{"app/main.go"},
		},
		{
			name:    "agent_files_container_subdir",
			args:    []string{"agent", "files", "viktor", "--container", "--subdir", "app"},
			want:    methodsCall{http.MethodGet, "/api/v1/agents/agent_1/container-files", "subdir=app", ""},
			wantOut: []string{"app/main.go"},
		},
		{
			name:    "persona_suggest",
			args:    []string{"persona", "suggest", "viktor", "--content", "hello", "--rationale", "why"},
			want:    methodsCall{http.MethodPost, "/api/v1/agents/agent_1/persona/suggest", "", `{"content":"hello","rationale":"why"}`},
			wantOut: []string{"inbox_approve"},
		},
		{
			name:      "persona_suggest_requires_content",
			args:      []string{"persona", "suggest", "viktor"},
			wantFails: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &methodsStub{}
			srv := stub.start(t)
			out, err := runIssueStopCLI(t, srv.URL, tc.args...)
			if tc.wantFails {
				if err == nil {
					t.Fatalf("want failure, got success\noutput: %s", out)
				}
				if len(stub.calls) != 0 {
					t.Errorf("invalid invocation reached the server: %v", stub.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %v\noutput: %s", tc.args, err, out)
			}
			if len(stub.calls) != 1 || stub.calls[0] != tc.want {
				t.Errorf("calls = %+v, want exactly %+v", stub.calls, tc.want)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out, w) {
					t.Errorf("output %q missing %q", out, w)
				}
			}
		})
	}
}
