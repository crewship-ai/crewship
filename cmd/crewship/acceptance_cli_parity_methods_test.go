package main

// Acceptance for the CLI-parity commands that wrap an existing API method
// (checkpoint get, workflow get by id, notifychannel update, nuke
// keeper-requests, workspace member capabilities list [user]). Each case
// drives the BUILT BINARY against an httptest stub and asserts the exact
// HTTP method + path reached, the request body, and the printed output.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type parityCall struct {
	Method, Path, Body string
}

type parityStub struct {
	mu    sync.Mutex
	calls []parityCall
	// handler returns status+body for a request; unmatched = 404.
	handler func(method, path string) (int, string)
}

func (p *parityStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.calls = append(p.calls, parityCall{r.Method, r.URL.Path, string(b)})
		p.mu.Unlock()
		status, body := p.handler(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (p *parityStub) got() []parityCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	// The CLI resolves the workspace slug with GET /api/v1/workspaces before
	// the command's own call; that is transport noise, not the contract.
	var out []parityCall
	for _, c := range p.calls {
		if c.Method == http.MethodGet && c.Path == "/api/v1/workspaces" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (p *parityStub) count(method, path string) int {
	n := 0
	for _, c := range p.got() {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

func TestAcceptance_CheckpointGet(t *testing.T) {
	stub := &parityStub{handler: func(m, p string) (int, string) {
		if m == "GET" && p == "/api/v1/checkpoints/chk_1" {
			return 200, `{"id":"chk_1","mission_id":"MIS-42","label":"green build","journal_cursor":"j_7f","created_by":"user_1","created_at":"2026-04-17T10:23:41Z"}`
		}
		return 404, `{"error":"checkpoint not found"}`
	}}
	srv := stub.start(t)

	out, err := runIssueStopCLI(t, srv.URL, "checkpoint", "get", "chk_1")
	if err != nil {
		t.Fatalf("checkpoint get: %v\n%s", err, out)
	}
	if stub.count("GET", "/api/v1/checkpoints/chk_1") != 1 {
		t.Errorf("calls = %+v", stub.got())
	}
	for _, w := range []string{"chk_1", "MIS-42", "green build", "j_7f"} {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}

	out, err = runIssueStopCLI(t, srv.URL, "checkpoint", "get", "chk_missing")
	if err == nil || !strings.Contains(out, "not found") {
		t.Errorf("missing checkpoint: err=%v out=%s", err, out)
	}
}

func TestAcceptance_WorkflowGet_ByIDAndSlug(t *testing.T) {
	const tpl = `{"id":"wft_1","name":"Engineering Standard","template_json":"[{\"name\":\"todo\"}]","is_builtin":false,"created_at":"c","updated_at":"u"}`
	stub := &parityStub{handler: func(m, p string) (int, string) {
		switch {
		case m == "GET" && p == "/api/v1/workflow-templates/wft_1":
			return 200, tpl
		case m == "GET" && p == "/api/v1/workflow-templates":
			return 200, "[" + tpl + "]"
		}
		return 404, `{"title":"Workflow template not found"}`
	}}
	srv := stub.start(t)

	t.Run("id is fetched directly without listing", func(t *testing.T) {
		out, err := runIssueStopCLI(t, srv.URL, "workflow", "get", "wft_1")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out, "Engineering Standard") {
			t.Errorf("output:\n%s", out)
		}
		if stub.count("GET", "/api/v1/workflow-templates") != 0 {
			t.Errorf("id lookup must not list: %+v", stub.got())
		}
	})
	t.Run("slug resolves through list then GET by name match", func(t *testing.T) {
		out, err := runIssueStopCLI(t, srv.URL, "workflow", "get", "engineering-standard")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out, "wft_1") || stub.count("GET", "/api/v1/workflow-templates") != 1 {
			t.Errorf("out=%s calls=%+v", out, stub.got())
		}
	})
	t.Run("unknown ref fails", func(t *testing.T) {
		out, err := runIssueStopCLI(t, srv.URL, "workflow", "get", "nope")
		if err == nil || !strings.Contains(out, "no workflow template matches") {
			t.Errorf("err=%v out=%s", err, out)
		}
	})
}

func TestAcceptance_NotifyChannelUpdate(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantBody map[string]any
		wantFail bool
	}{
		{"disable only", []string{"--enabled=false"}, map[string]any{"enabled": false}, false},
		{"enable only", []string{"--enabled"}, map[string]any{"enabled": true}, false},
		{"priority and categories", []string{"--min-priority", "high", "--categories", "chat.replies,security.alerts"},
			map[string]any{"min_priority": "high", "categories": []any{"chat.replies", "security.alerts"}}, false},
		{"empty categories clears", []string{"--categories", ""}, map[string]any{"categories": []any{}}, false},
		{"events", []string{"--events", "failed,completed"}, map[string]any{"events": []any{"failed", "completed"}}, false},
		{"no flags", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &parityStub{handler: func(m, p string) (int, string) {
				if m == "PATCH" && p == "/api/v1/notification-channels/nch_1" {
					return 200, `{"updated":"nch_1"}`
				}
				return 404, `{}`
			}}
			srv := stub.start(t)
			out, err := runIssueStopCLI(t, srv.URL, append([]string{"notifychannel", "update", "nch_1"}, tc.args...)...)
			if tc.wantFail {
				if err == nil || len(stub.got()) != 0 {
					t.Fatalf("want failure with no HTTP call; err=%v calls=%+v out=%s", err, stub.got(), out)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			calls := stub.got()
			if len(calls) != 1 || calls[0].Method != "PATCH" || calls[0].Path != "/api/v1/notification-channels/nch_1" {
				t.Fatalf("calls = %+v", calls)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(calls[0].Body), &got); err != nil {
				t.Fatalf("body %q: %v", calls[0].Body, err)
			}
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(tc.wantBody)
			if string(gb) != string(wb) {
				t.Errorf("body = %s, want %s", gb, wb)
			}
			if !strings.Contains(out, "Notification channel updated: nch_1") {
				t.Errorf("output:\n%s", out)
			}
		})
	}
}

func TestAcceptance_NukeKeeperRequests(t *testing.T) {
	const path = "/api/v1/admin/keeper/requests"
	t.Run("purges and reports counts", func(t *testing.T) {
		stub := &parityStub{handler: func(m, p string) (int, string) {
			if m == "DELETE" && p == path {
				return 200, `{"deleted_requests":4,"deleted_events":9}`
			}
			return 404, `{}`
		}}
		srv := stub.start(t)
		out, err := runIssueStopCLI(t, srv.URL, "nuke", "keeper-requests", "--yes")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if stub.count("DELETE", path) != 1 || !strings.Contains(out, "4 requests, 9 events") {
			t.Errorf("calls=%+v out=%s", stub.got(), out)
		}
	})
	t.Run("refuses without --yes and sends nothing", func(t *testing.T) {
		stub := &parityStub{handler: func(m, p string) (int, string) { return 200, `{}` }}
		srv := stub.start(t)
		out, err := runIssueStopCLI(t, srv.URL, "nuke", "keeper-requests")
		if err == nil || len(stub.got()) != 0 || !strings.Contains(out, "--yes") {
			t.Errorf("err=%v calls=%+v out=%s", err, stub.got(), out)
		}
	})
	t.Run("old server is reported, not claimed as success", func(t *testing.T) {
		for _, status := range []int{404, 405} {
			stub := &parityStub{handler: func(m, p string) (int, string) { return status, `{}` }}
			srv := stub.start(t)
			out, err := runIssueStopCLI(t, srv.URL, "nuke", "keeper-requests", "--yes")
			if err == nil || !strings.Contains(out, "does not support") || strings.Contains(out, "purged (") {
				t.Errorf("status %d: err=%v out=%s", status, err, out)
			}
		}
	})
	t.Run("server error fails", func(t *testing.T) {
		stub := &parityStub{handler: func(m, p string) (int, string) { return 500, `{}` }}
		srv := stub.start(t)
		out, err := runIssueStopCLI(t, srv.URL, "nuke", "keeper-requests", "--yes")
		if err == nil || !strings.Contains(out, "HTTP 500") {
			t.Errorf("err=%v out=%s", err, out)
		}
	})
}

func TestAcceptance_WorkspaceMemberCapabilitiesList(t *testing.T) {
	stub := &parityStub{handler: func(m, p string) (int, string) {
		switch {
		case m == "GET" && p == "/api/v1/workspaces/ws_test/members/capabilities":
			return 200, `{"members":[{"user_id":"u_alice","role":"ADMIN","capabilities":["chat","routine.create"]},{"user_id":"u_bob","role":"MEMBER","capabilities":["chat"]}]}`
		case m == "GET" && p == "/api/v1/workspaces/ws_test/members/u_bob/capabilities":
			return 200, `{"user_id":"u_bob","role":"MEMBER","capabilities":["chat"]}`
		}
		return 404, `{}`
	}}
	srv := stub.start(t)

	out, err := runIssueStopCLI(t, srv.URL, "workspace", "member", "capabilities", "list")
	if err != nil {
		t.Fatalf("bulk: %v\n%s", err, out)
	}
	for _, w := range []string{"u_alice", "ADMIN", "routine.create", "u_bob"} {
		if !strings.Contains(out, w) {
			t.Errorf("bulk output missing %q:\n%s", w, out)
		}
	}
	if stub.count("GET", "/api/v1/workspaces/ws_test/members/capabilities") != 1 {
		t.Errorf("calls = %+v", stub.got())
	}

	out, err = runIssueStopCLI(t, srv.URL, "workspace", "member", "capabilities", "list", "u_bob")
	if err != nil {
		t.Fatalf("single: %v\n%s", err, out)
	}
	if stub.count("GET", "/api/v1/workspaces/ws_test/members/u_bob/capabilities") != 1 || strings.Contains(out, "u_alice") {
		t.Errorf("single: calls=%+v out=%s", stub.got(), out)
	}
}
