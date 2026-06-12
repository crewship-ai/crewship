package main

// Coverage tests for cmd_crew_relations.go — connect / disconnect /
// connections / standup / peer-conversations.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covCrewsList registers the crews list used by slug→id resolution.
func covCrewsList(s *clitest.StubServer) {
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrewa1234567890123456789", "slug": "alpha"},
		{"id": "ccrewb1234567890123456789", "slug": "bravo"},
	}))
}

func TestCrewRelations_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"connect", crewConnectCmd, []string{"a", "b"}},
		{"disconnect", crewDisconnectCmd, []string{"conn-1"}},
		{"connections", crewConnectionsCmd, nil},
		{"standup", crewStandupCmd, []string{"a"}},
		{"peer-conversations", crewPeerConvsCmd, []string{"a"}},
	}
	for _, tc := range cases {
		if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", tc.name, err)
		}
	}
}

func TestCrewConnectRunE(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewsList(s)
	s.OnPost("/api/v1/crew-connections", clitest.JSONResponse(201, map[string]string{"id": "conn-42"}))

	if err := crewConnectCmd.Flags().Set("direction", "bidirectional"); err != nil {
		t.Fatal(err)
	}
	if err := crewConnectCmd.RunE(crewConnectCmd, []string{"alpha", "bravo"}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	posts := s.CallsFor("POST", "/api/v1/crew-connections")
	if len(posts) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(posts))
	}
	var body map[string]string
	if err := json.Unmarshal(posts[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["from_crew_id"] != "ccrewa1234567890123456789" ||
		body["to_crew_id"] != "ccrewb1234567890123456789" ||
		body["direction"] != "bidirectional" {
		t.Errorf("connect body = %v", body)
	}
}

func TestCrewConnectRunE_UnknownSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewsList(s)

	err := crewConnectCmd.RunE(crewConnectCmd, []string{"ghost", "bravo"})
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Fatalf("got %v", err)
	}
}

func TestCrewConnectRunE_APIError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewsList(s)
	s.OnPost("/api/v1/crew-connections", clitest.ErrorResponse(409, "already connected"))

	err := crewConnectCmd.RunE(crewConnectCmd, []string{"alpha", "bravo"})
	if err == nil || !strings.Contains(err.Error(), "already connected") {
		t.Fatalf("got %v", err)
	}
}

func TestCrewDisconnectRunE(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	s.OnDelete("/api/v1/crew-connections/conn-42", clitest.EmptyResponse(204))

	if err := crewDisconnectCmd.RunE(crewDisconnectCmd, []string{"conn-42"}); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/crew-connections/conn-42")); n != 1 {
		t.Errorf("expected 1 DELETE, got %d", n)
	}

	// failure path
	s.OnDelete("/api/v1/crew-connections/missing", clitest.ErrorResponse(404, "no such connection"))
	err := crewDisconnectCmd.RunE(crewDisconnectCmd, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "no such connection") {
		t.Fatalf("got %v", err)
	}
}

func TestCrewConnectionsRunE(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	s.OnGet("/api/v1/crew-connections", clitest.JSONResponse(200, []map[string]any{
		{
			"id": "conn-1", "from_crew_slug": "alpha", "to_crew_slug": "bravo",
			"direction": "bidirectional", "status": "active", "created_at": "2026-06-01",
		},
	}))

	out, err := covCaptureStdoutCli7(t, func() error {
		return crewConnectionsCmd.RunE(crewConnectionsCmd, nil)
	})
	if err != nil {
		t.Fatalf("connections: %v", err)
	}
	for _, want := range []string{"conn-1", "alpha", "bravo", "bidirectional", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestCrewStandupRunE(t *testing.T) {
	t.Run("text summary with --since", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewsList(s)
		s.OnGet("/api/v1/crews/ccrewa1234567890123456789/standup", clitest.JSONResponse(200, map[string]string{
			"standup": "Yesterday we fixed the bilge pump.",
		}))
		if err := crewStandupCmd.Flags().Set("since", "2026-06-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = crewStandupCmd.Flags().Set("since", "") })

		out, err := covCaptureStdoutCli7(t, func() error {
			return crewStandupCmd.RunE(crewStandupCmd, []string{"alpha"})
		})
		if err != nil {
			t.Fatalf("standup: %v", err)
		}
		if !strings.Contains(out, "Yesterday we fixed the bilge pump.") {
			t.Errorf("standup output = %q", out)
		}
		gets := s.CallsFor("GET", "/api/v1/crews/ccrewa1234567890123456789/standup")
		if len(gets) != 1 || !strings.Contains(gets[0].Query, "since=2026-06-01T00%3A00%3A00Z") {
			t.Errorf("since param not propagated: %+v", gets)
		}
	})

	t.Run("non-text payload falls back to JSON dump", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewsList(s)
		s.OnGet("/api/v1/crews/ccrewa1234567890123456789/standup", clitest.JSONResponse(200, map[string]any{
			"items": []string{"a", "b"},
		}))
		out, err := covCaptureStdoutCli7(t, func() error {
			return crewStandupCmd.RunE(crewStandupCmd, []string{"alpha"})
		})
		if err != nil {
			t.Fatalf("standup: %v", err)
		}
		if !strings.Contains(out, `"items"`) {
			t.Errorf("expected JSON fallback, got %q", out)
		}
	})
}

func TestCrewPeerConvsRunE(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewsList(s)

	longQ := strings.Repeat("why? ", 20) // > 60 chars → truncated with ellipsis
	s.OnGet("/api/v1/crews/ccrewa1234567890123456789/peer-conversations", clitest.JSONResponse(200, []map[string]any{
		{
			"id": "conv12345678abcdef", "from_name": "Viktor", "to_name": "Eva",
			"question": longQ, "status": "answered", "escalated": true,
			"created_at": "2026-06-02",
		},
		{
			"id": "conv87654321abcdef", "from_name": "Eva", "to_name": "Viktor",
			"question": "short?", "status": "pending", "escalated": false,
			"created_at": "2026-06-03",
		},
	}))
	if err := crewPeerConvsCmd.Flags().Set("limit", "7"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = crewPeerConvsCmd.Flags().Set("limit", "50") })

	out, err := covCaptureStdoutCli7(t, func() error {
		return crewPeerConvsCmd.RunE(crewPeerConvsCmd, []string{"alpha"})
	})
	if err != nil {
		t.Fatalf("peer-conversations: %v", err)
	}
	// IDs are trimmed to 8 chars in the table.
	if !strings.Contains(out, "conv1234") || strings.Contains(out, "conv12345678abcdef") {
		t.Errorf("id should be truncated to 8 chars:\n%s", out)
	}
	if !strings.Contains(out, "...") {
		t.Errorf("long question should be ellipsised:\n%s", out)
	}
	if !strings.Contains(out, "YES") {
		t.Errorf("escalated row should show YES:\n%s", out)
	}
	gets := s.CallsFor("GET", "/api/v1/crews/ccrewa1234567890123456789/peer-conversations")
	if len(gets) != 1 || !strings.Contains(gets[0].Query, "limit=7") {
		t.Errorf("limit flag not propagated: %+v", gets)
	}
}

// ─── additional error paths ──────────────────────────────────────────────

func TestCrewRelations_NoWorkspace(t *testing.T) {
	covNoWorkspaceCLI(t)

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"connect", crewConnectCmd, []string{"a", "b"}},
		{"disconnect", crewDisconnectCmd, []string{"conn-1"}},
		{"connections", crewConnectionsCmd, nil},
		{"standup", crewStandupCmd, []string{"a"}},
		{"peer-conversations", crewPeerConvsCmd, []string{"a"}},
	}
	for _, tc := range cases {
		if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error, got %v", tc.name, err)
		}
	}
}

func TestCrewRelations_TransportErrors(t *testing.T) {
	// CUID args skip slug resolution, so the first request that hits the
	// dead server is the command's own verb.
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWSCli7, Server: covDeadURL(t)}
	cuid := "ccrewa1234567890123456789"

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"connect", crewConnectCmd, []string{cuid, cuid}},
		{"disconnect", crewDisconnectCmd, []string{"conn-1"}},
		{"connections", crewConnectionsCmd, nil},
		{"standup", crewStandupCmd, []string{cuid}},
		{"peer-conversations", crewPeerConvsCmd, []string{cuid}},
	}
	for _, tc := range cases {
		if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "request failed") {
			t.Errorf("%s: expected transport error, got %v", tc.name, err)
		}
	}
}

func TestCrewConnectRunE_SecondSlugUnknown(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewsList(s)
	err := crewConnectCmd.RunE(crewConnectCmd, []string{"alpha", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Fatalf("got %v", err)
	}
}

func TestCrewConnectRunE_UndecodableCreate(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewsList(s)
	s.OnPost("/api/v1/crew-connections", clitest.TextResponse(200, "not json"))
	if err := crewConnectCmd.RunE(crewConnectCmd, []string{"alpha", "bravo"}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestCrewConnectionsRunE_Errors(t *testing.T) {
	t.Run("API error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		s.OnGet("/api/v1/crew-connections", clitest.ErrorResponse(500, "db down"))
		err := crewConnectionsCmd.RunE(crewConnectionsCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "db down") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("undecodable body", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		s.OnGet("/api/v1/crew-connections", clitest.TextResponse(200, "x"))
		if err := crewConnectionsCmd.RunE(crewConnectionsCmd, nil); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestCrewStandupRunE_Errors(t *testing.T) {
	standupPath := "/api/v1/crews/ccrewa1234567890123456789/standup"

	t.Run("API error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewsList(s)
		s.OnGet(standupPath, clitest.ErrorResponse(404, "crew has no activity"))
		err := crewStandupCmd.RunE(crewStandupCmd, []string{"alpha"})
		if err == nil || !strings.Contains(err.Error(), "crew has no activity") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("undecodable body", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewsList(s)
		s.OnGet(standupPath, clitest.TextResponse(200, "x"))
		if err := crewStandupCmd.RunE(crewStandupCmd, []string{"alpha"}); err == nil {
			t.Fatal("expected decode error")
		}
	})

	t.Run("json format returns raw result", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewsList(s)
		s.OnGet(standupPath, clitest.JSONResponse(200, map[string]string{"standup": "all good"}))
		origFormat := flagFormat
		flagFormat = "json"
		t.Cleanup(func() { flagFormat = origFormat })

		out, err := covCaptureStdoutCli7(t, func() error {
			return crewStandupCmd.RunE(crewStandupCmd, []string{"alpha"})
		})
		if err != nil {
			t.Fatalf("standup json: %v", err)
		}
		if !strings.Contains(out, `"standup"`) {
			t.Errorf("json output = %q", out)
		}
	})
}

func TestCrewPeerConvsRunE_Errors(t *testing.T) {
	convPath := "/api/v1/crews/ccrewa1234567890123456789/peer-conversations"

	t.Run("unknown crew", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewsList(s)
		err := crewPeerConvsCmd.RunE(crewPeerConvsCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("API error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewsList(s)
		s.OnGet(convPath, clitest.ErrorResponse(500, "boom"))
		err := crewPeerConvsCmd.RunE(crewPeerConvsCmd, []string{"alpha"})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("undecodable body", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewsList(s)
		s.OnGet(convPath, clitest.TextResponse(200, "x"))
		if err := crewPeerConvsCmd.RunE(crewPeerConvsCmd, []string{"alpha"}); err == nil {
			t.Fatal("expected decode error")
		}
	})
}
