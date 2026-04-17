package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestCheckpointCmdStructure(t *testing.T) {
	t.Parallel()

	if checkpointCmd.Use != "checkpoint" {
		t.Errorf("checkpoint Use: got %q want %q", checkpointCmd.Use, "checkpoint")
	}
	have := map[string]bool{}
	for _, sub := range checkpointCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "create", "restore", "fork", "delete"} {
		if !have[want] {
			t.Errorf("checkpoint missing subcommand %q; have %v", want, have)
		}
	}
}

func TestCheckpointListFlags(t *testing.T) {
	t.Parallel()

	mission := checkpointListCmd.Flags().Lookup("mission")
	if mission == nil {
		t.Fatal("checkpoint list missing --mission flag")
	}
	if mission.DefValue != "" {
		t.Errorf("--mission default: got %q want empty", mission.DefValue)
	}
}

func TestCheckpointCreateFlags(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"mission", "label"} {
		if f := checkpointCreateCmd.Flags().Lookup(name); f == nil {
			t.Errorf("checkpoint create missing --%s flag", name)
		}
	}
}

func TestCheckpointForkFlags(t *testing.T) {
	t.Parallel()

	if checkpointForkCmd.Flags().Lookup("label") == nil {
		t.Error("checkpoint fork missing --label flag")
	}
}

func TestCheckpointDeleteFlags(t *testing.T) {
	t.Parallel()

	if checkpointDeleteCmd.Flags().Lookup("yes") == nil {
		t.Error("checkpoint delete missing --yes flag")
	}
}

func TestCheckpointListRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := checkpointListCmd.RunE(checkpointListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestCheckpointListRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := checkpointListCmd.RunE(checkpointListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestCheckpointListRunE_MissionRequired(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs"}
	if err := checkpointListCmd.Flags().Set("mission", ""); err != nil {
		t.Fatalf("reset --mission: %v", err)
	}

	err := checkpointListCmd.RunE(checkpointListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--mission is required") {
		t.Errorf("expected '--mission is required'; got %v", err)
	}
}

// checkpointMock stubs the mission checkpoints + delete endpoints.
type checkpointMock struct {
	t             *testing.T
	mu            sync.Mutex
	listedMission string
	createMission string
	deleteID      string
	createStatus  int
}

func (m *checkpointMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/missions/") && strings.HasSuffix(r.URL.Path, "/checkpoints"):
			parts := strings.Split(r.URL.Path, "/")
			m.mu.Lock()
			m.listedMission = parts[4]
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"checkpoints": []map[string]any{
					{"id": "chk-1", "mission_id": "mis-1", "label": "green-build", "journal_cursor": "cur-1", "created_by": "u-1", "created_at": "2026-04-17T00:00:00Z"},
				},
				"count":      1,
				"mission_id": "mis-1",
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/missions/") && strings.HasSuffix(r.URL.Path, "/checkpoints"):
			parts := strings.Split(r.URL.Path, "/")
			m.mu.Lock()
			m.createMission = parts[4]
			m.mu.Unlock()
			if m.createStatus != 0 {
				w.WriteHeader(m.createStatus)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "nope"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "chk-new", "mission_id": "mis-1", "label": "created"})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/checkpoints/"):
			parts := strings.Split(r.URL.Path, "/")
			m.mu.Lock()
			m.deleteID = parts[len(parts)-1]
			m.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			m.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestCheckpointListRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &checkpointMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := checkpointListCmd.Flags().Set("mission", "mis-1"); err != nil {
		t.Fatalf("set --mission: %v", err)
	}
	t.Cleanup(func() { _ = checkpointListCmd.Flags().Set("mission", "") })

	if err := checkpointListCmd.RunE(checkpointListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	got := m.listedMission
	m.mu.Unlock()
	if got != "mis-1" {
		t.Errorf("mission not forwarded: got %q want mis-1", got)
	}
}

func TestCheckpointCreateRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &checkpointMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := checkpointCreateCmd.Flags().Set("mission", "mis-1"); err != nil {
		t.Fatalf("set --mission: %v", err)
	}
	if err := checkpointCreateCmd.Flags().Set("label", "green-build"); err != nil {
		t.Fatalf("set --label: %v", err)
	}
	t.Cleanup(func() {
		_ = checkpointCreateCmd.Flags().Set("mission", "")
		_ = checkpointCreateCmd.Flags().Set("label", "")
	})

	if err := checkpointCreateCmd.RunE(checkpointCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	got := m.createMission
	m.mu.Unlock()
	if got != "mis-1" {
		t.Errorf("mission not forwarded: got %q want mis-1", got)
	}
}

func TestCheckpointCreateRunE_MissionRequired(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs"}
	if err := checkpointCreateCmd.Flags().Set("mission", ""); err != nil {
		t.Fatalf("reset --mission: %v", err)
	}

	err := checkpointCreateCmd.RunE(checkpointCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--mission is required") {
		t.Errorf("expected '--mission is required'; got %v", err)
	}
}

func TestCheckpointDeleteRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &checkpointMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := checkpointDeleteCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	t.Cleanup(func() { _ = checkpointDeleteCmd.Flags().Set("yes", "false") })

	if err := checkpointDeleteCmd.RunE(checkpointDeleteCmd, []string{"chk-99"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	got := m.deleteID
	m.mu.Unlock()
	if got != "chk-99" {
		t.Errorf("delete id: got %q want chk-99", got)
	}
}

func TestCheckpointRestoreArgsValidation(t *testing.T) {
	t.Parallel()

	if err := checkpointRestoreCmd.Args(checkpointRestoreCmd, []string{}); err == nil {
		t.Error("restore with no args should error")
	}
	if err := checkpointRestoreCmd.Args(checkpointRestoreCmd, []string{"a"}); err != nil {
		t.Errorf("restore with one arg: %v", err)
	}
}

func TestCheckpointForkArgsValidation(t *testing.T) {
	t.Parallel()

	if err := checkpointForkCmd.Args(checkpointForkCmd, []string{}); err == nil {
		t.Error("fork with no args should error")
	}
	if err := checkpointForkCmd.Args(checkpointForkCmd, []string{"a"}); err != nil {
		t.Errorf("fork with one arg: %v", err)
	}
}
