package main

// Coverage tests for cmd_notification.go — list / count / read /
// read-all / delete RunE paths.

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestNotificationListRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := notificationListCmd.RunE(notificationListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestNotificationListRunE_QueryParams(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	actorName := "Viktor"
	title := "PR #42 merged"
	readAt := "2026-06-01T10:00:00Z"
	stub.OnGet("/api/v1/notifications", clitest.JSONResponse(200, []map[string]any{
		{
			"id": "cnotif01234567890123456", "actor_type": "AGENT", "actor_id": "agent-1",
			"actor_name": actorName, "action": "merged", "entity_type": "pull_request",
			"entity_title": title, "read_at": readAt, "created_at": "2026-06-01T09:00:00Z",
		},
		{
			"id": "cnotif11234567890123456", "actor_type": "USER", "actor_id": "user-9",
			"action": "commented", "entity_type": "issue", "created_at": "2026-06-01T09:30:00Z",
		},
	}))
	covSetFlagCli8(t, notificationListCmd, "unread", "true")
	covSetFlagCli8(t, notificationListCmd, "limit", "5")

	out := covCaptureStdoutCli8(t, func() {
		if err := notificationListCmd.RunE(notificationListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("GET", "/api/v1/notifications")
	if len(calls) != 1 {
		t.Fatalf("expected 1 GET, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "read=false") || !strings.Contains(calls[0].Query, "limit=5") {
		t.Errorf("query params not propagated: %q", calls[0].Query)
	}
	for _, want := range []string{"merged", "Viktor", "PR #42 merged", "user-9", "yes", "no"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestNotificationListRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/notifications", clitest.ErrorResponse(500, "Internal server error"))

	err := notificationListCmd.RunE(notificationListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected API error; got %v", err)
	}
}

func TestNotificationCountRunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/notifications/count", clitest.JSONResponse(200, map[string]int{"unread": 7}))

	out := covCaptureStdoutCli8(t, func() {
		if err := notificationCountCmd.RunE(notificationCountCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Unread: 7") {
		t.Errorf("count output wrong: %q", out)
	}
}

func TestNotificationCountRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := notificationCountCmd.RunE(notificationCountCmd, nil); err == nil {
		t.Error("expected not-logged-in error")
	}
}

func TestNotificationReadRunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/notifications/n-1/read", clitest.JSONResponse(200, map[string]any{"ok": true}))

	if err := notificationReadCmd.RunE(notificationReadCmd, []string{"n-1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/notifications/n-1/read"); len(calls) != 1 {
		t.Errorf("expected 1 POST read, got %d", len(calls))
	}
}

func TestNotificationReadRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/notifications/ghost/read", clitest.ErrorResponse(404, "Notification not found"))

	err := notificationReadCmd.RunE(notificationReadCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "Notification not found") {
		t.Errorf("expected not-found; got %v", err)
	}
}

func TestNotificationReadAllRunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/notifications/read-all", clitest.JSONResponse(200, map[string]int{"updated": 12}))

	if err := notificationReadAllCmd.RunE(notificationReadAllCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/notifications/read-all"); len(calls) != 1 {
		t.Errorf("expected 1 POST read-all, got %d", len(calls))
	}
}

func TestNotificationDeleteRunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnDelete("/api/v1/notifications/n-9", clitest.EmptyResponse(204))
	covSetFlagCli8(t, notificationDeleteCmd, "yes", "true")

	if err := notificationDeleteCmd.RunE(notificationDeleteCmd, []string{"n-9"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("DELETE", "/api/v1/notifications/n-9"); len(calls) != 1 {
		t.Errorf("expected 1 DELETE, got %d", len(calls))
	}
}

// TestNotificationRunE_ErrorBranches sweeps the remaining auth /
// transport / decode / API-error branches.
func TestNotificationRunE_ErrorBranches(t *testing.T) {
	withYes := func(t *testing.T) { covSetFlagCli8(t, notificationDeleteCmd, "yes", "true") }
	cases := []struct {
		name    string
		cmd     *cobra.Command
		args    []string
		route   func(*clitest.StubServer)
		noAuth  bool
		prepare func(*testing.T)
	}{
		{name: "read no auth", cmd: notificationReadCmd, args: []string{"n"}, noAuth: true},
		{name: "read-all no auth", cmd: notificationReadAllCmd, noAuth: true},
		{name: "delete no auth", cmd: notificationDeleteCmd, args: []string{"n"}, noAuth: true},
		{name: "list transport", cmd: notificationListCmd, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/notifications", covAbort())
		}},
		{name: "list decode", cmd: notificationListCmd, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/notifications", covNotJSON())
		}},
		{name: "count transport", cmd: notificationCountCmd, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/notifications/count", covAbort())
		}},
		{name: "count api error", cmd: notificationCountCmd, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/notifications/count", clitest.ErrorResponse(500, "boom"))
		}},
		{name: "count decode", cmd: notificationCountCmd, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/notifications/count", covNotJSON())
		}},
		{name: "read transport", cmd: notificationReadCmd, args: []string{"n"}, route: func(s *clitest.StubServer) {
			s.OnPost("/api/v1/notifications/n/read", covAbort())
		}},
		{name: "read-all transport", cmd: notificationReadAllCmd, route: func(s *clitest.StubServer) {
			s.OnPost("/api/v1/notifications/read-all", covAbort())
		}},
		{name: "read-all api error", cmd: notificationReadAllCmd, route: func(s *clitest.StubServer) {
			s.OnPost("/api/v1/notifications/read-all", clitest.ErrorResponse(500, "boom"))
		}},
		{name: "read-all decode", cmd: notificationReadAllCmd, route: func(s *clitest.StubServer) {
			s.OnPost("/api/v1/notifications/read-all", covNotJSON())
		}},
		{name: "delete transport", cmd: notificationDeleteCmd, args: []string{"n"}, prepare: withYes,
			route: func(s *clitest.StubServer) { s.OnDelete("/api/v1/notifications/n", covAbort()) }},
		{name: "delete api error", cmd: notificationDeleteCmd, args: []string{"n"}, prepare: withYes,
			route: func(s *clitest.StubServer) {
				s.OnDelete("/api/v1/notifications/n", clitest.ErrorResponse(404, "Notification not found"))
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			if c.noAuth {
				cliCfg = &cli.CLIConfig{Server: stub.URL()}
			}
			if c.prepare != nil {
				c.prepare(t)
			}
			if c.route != nil {
				c.route(stub)
			}
			if err := c.cmd.RunE(c.cmd, c.args); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}

func TestNotificationDeleteRunE_Aborted(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	covWithStdinCli8(t, "n\n", func() {
		err := notificationDeleteCmd.RunE(notificationDeleteCmd, []string{"n-9"})
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Errorf("expected aborted; got %v", err)
		}
	})
	if len(stub.Calls()) != 0 {
		t.Errorf("no API calls expected after abort; got %d", len(stub.Calls()))
	}
}
