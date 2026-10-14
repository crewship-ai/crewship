package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Drive the built binary, including Cobra parsing, config, transport and
// machine rendering. The server rejects any route outside this projection.
func TestAcceptanceGuestAccessCLI(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Path != "/api/v1/workspaces" {
			requests = append(requests, r.Method+" "+r.URL.Path)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test","name":"Test"}]`)
		case "/api/v1/workspaces/ws_test/restricted-issues":
			if r.URL.Query().Get("project_id") != "project-1" {
				t.Error("project flag missing")
			}
			_, _ = io.WriteString(w, `[{"id":"issue-1","identifier":"CLIENT-1","title":"Shared","status":"TODO","project_id":"project-1","project_name":"Client","can_read":true}]`)
		case "/api/v1/workspaces/ws_test/restricted-issues/CLIENT-1":
			_, _ = io.WriteString(w, `{"id":"issue-1","identifier":"CLIENT-1","title":"Shared","status":"TODO","project_id":"project-1","project_name":"Client","can_read":true,"description":"Shared brief"}`)
		case "/api/v1/workspaces/ws_test/members":
			_, _ = io.WriteString(w, `[{"id":"member-1","user_id":"user-1"}]`)
		case "/api/v1/workspaces/ws_test/members/member-1/access/effective":
			_, _ = io.WriteString(w, `{"membership_id":"member-1","mode":"restricted","revision":3,"account_restricted":true,"restriction_scope":"account","decisions":[{"kind":"routine","id":"routine-1","operation":"run","state":"conditional","reason":"runtime checked at invocation"}]}`)
		case "/api/v1/workspaces/ws_test/invitations", "/api/v1/workspaces/ws_test/members/provision":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["access_mode"] != "restricted" {
				t.Error("Guest access mode missing")
			}
			rights, ok := body["rights"].([]any)
			if !ok || len(rights) != 1 {
				t.Errorf("rights missing: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"invitation-1","email":"client@example.test","role":"VIEWER","created_user":true,"setup_url":""}`)
		case "/api/v1/auth/invitation/accept":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["token"] != "test-token" {
				t.Error("stdin proof missing")
			}
			if body["confirm_account_restriction"] != true {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":"Guest confirmation required","code":"guest_confirmation_required"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":"unexpected route"}`)
		}
	}))
	defer server.Close()
	cases := [][]string{
		{"--format", "json", "issue", "shared", "--project", "project-1", "--limit", "20", "--offset", "0"},
		{"--format", "json", "issue", "shared", "CLIENT-1"},
		{"--format", "json", "workspace", "member", "access", "explain", "user-1"},
	}
	for _, args := range cases {
		out, err := runIssueRunsCLI(t, server.URL, args...)
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		if !strings.Contains(out, "issue-1") && !strings.Contains(out, "routine-1") {
			t.Fatalf("machine output lost resource: %s", out)
		}
	}
	for _, command := range [][]string{
		{"workspace", "invite", "create", "client@example.test"},
		{"workspace", "invite", "client@example.test"},
		{"workspace", "member", "invite", "client@example.test"},
	} {
		args := append([]string{"--format", "json"}, command...)
		args = append(args, "--role", "VIEWER", "--access-mode", "restricted", "--rights-file", "-")
		out, err := runGuestCLI(t, server.URL, `[{"kind":"routine","id":"routine-1","operation":"run"}]`, args...)
		if err != nil {
			t.Fatalf("Guest creation: %v %s", err, out)
		}
	}
	out, err := runGuestCLI(t, server.URL, "test-token", "--format", "json", "workspace", "invite", "accept", "--token-stdin")
	if err == nil || !strings.Contains(out, "Guest confirmation required") {
		t.Fatalf("unconfirmed acceptance: %v %s", err, out)
	}
	out, err = runGuestCLI(t, server.URL, "test-token", "--format", "json", "workspace", "invite", "accept", "--token-stdin", "--confirm-account-restriction")
	if err != nil || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("confirmed acceptance: %v %s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 9 {
		t.Fatalf("unexpected requests: %v", requests)
	}
}

func runGuestCLI(t *testing.T, serverURL, input string, args ...string) (string, error) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(config, []byte("server: "+serverURL+"\nworkspace: ws_test\ntoken: fake-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(buildCrewshipBinary(t), args...)
	command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestAcceptanceMemberAccessReissueCLI(t *testing.T) {
	policy := `{"membership_id":"member-1","revision":3,"mode":"restricted","rights":[{"kind":"routine","id":"routine-1","operation":"run"}],"grant_details":[{"kind":"routine","id":"routine-1","operation":"run","issuer_id":"old-admin","created_at":"2026-10-14T00:00:00Z","live":false}]}`
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			io.WriteString(w, `[{"id":"ws_test","slug":"ws_test","name":"Test"}]`)
		case "/api/v1/workspaces/ws_test/members":
			io.WriteString(w, `[{"id":"member-1","user_id":"user-1"}]`)
		case "/api/v1/workspaces/ws_test/members/member-1/access":
			if r.Method == "PUT" {
				var body struct {
					Reissue      []struct{ Kind, ID, Operation string }
					GrantDetails []any `json:"grant_details"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Reissue) != 1 || body.Reissue[0].Kind != "routine" || body.Reissue[0].ID != "routine-1" || body.Reissue[0].Operation != "run" {
					t.Errorf("lost reissue instruction: %+v", body)
				}
				puts++
			}
			io.WriteString(w, policy)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	out, err := runGuestCLI(t, server.URL, "", "workspace", "member", "access", "get", "user-1")
	if err != nil {
		t.Fatalf("get: %v %s", err, out)
	}
	for _, column := range []string{"ISSUER", "STATE", "old-admin", "inactive", "routine-1", "run"} {
		if !strings.Contains(out, column) {
			t.Fatalf("missing %s: %s", column, out)
		}
	}
	out, err = runGuestCLI(t, server.URL, policy, "workspace", "member", "access", "set", "user-1", "--file", "-", "--reissue", "routine:routine-1:run")
	if err != nil || puts != 1 {
		t.Fatalf("reissue: %v puts=%d %s", err, puts, out)
	}
	out, err = runGuestCLI(t, server.URL, policy, "workspace", "member", "access", "set", "user-1", "--file", "-", "--reissue", "routine:unshared:run")
	if err == nil || puts != 1 {
		t.Fatalf("invalid reissue reached API: %v puts=%d %s", err, puts, out)
	}
}
