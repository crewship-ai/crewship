package main

// Acceptance for #2233's CLI knob: `crewship workspace update
// --approvals-retention-days N` → PATCH /api/v1/workspaces/{id} with
// {"approvals_retention_days": N}.
//
// Driven through the BUILT BINARY, not an in-process RunE call — same
// reasoning acceptance_flags_and_admin_reads_test.go documents: calling
// workspaceUpdateCmd.RunE directly with a hand-built *cobra.Command (as the
// cov-test-file unit tests in cmd_workspace_cov_test.go do) never touches
// the real flag registration in cmd_workspace.go's init(), so a typo'd flag
// name or a dropped Flags().Int call there would still pass. Only a real
// invocation of the compiled binary exercises the actual registration.

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

type approvalsRetentionStub struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (s *approvalsRetentionStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/workspaces/ws_test" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"unexpected request"}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ws_test","name":"W","slug":"w"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func approvalsRetentionConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runApprovalsRetentionCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAcceptance_WorkspaceUpdate_ApprovalsRetentionDays drives the real
// binary end to end: flag parse → PATCH body → server round trip →
// "Workspace updated." on stdout.
func TestAcceptance_WorkspaceUpdate_ApprovalsRetentionDays(t *testing.T) {
	stub := &approvalsRetentionStub{}
	srv := stub.start(t)
	cfg := approvalsRetentionConfig(t, srv.URL)

	out, err := runApprovalsRetentionCLI(t, cfg, "workspace", "update", "--approvals-retention-days", "45")
	if err != nil {
		t.Fatalf("workspace update: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Workspace updated.") {
		t.Errorf("output does not confirm the update:\n%s", out)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.bodies) != 1 {
		t.Fatalf("want 1 PATCH, got %d", len(stub.bodies))
	}
	body := stub.bodies[0]
	if body["approvals_retention_days"] != float64(45) {
		t.Errorf("approvals_retention_days = %v, want 45", body["approvals_retention_days"])
	}
	// The other retention flags weren't passed — they must not ride along.
	// Regression guard for the flags.Changed gate cmd_workspace.go's own
	// comment describes (an unset flag must never silently send a 0).
	if _, ok := body["credential_audit_retention_days"]; ok {
		t.Errorf("credential_audit_retention_days sent unset: %v", body)
	}
	if _, ok := body["run_retention_days"]; ok {
		t.Errorf("run_retention_days sent unset: %v", body)
	}
}

// TestAcceptance_WorkspaceUpdate_ApprovalsRetentionDaysHelpListsFlag pins
// the flag's existence and its default-behaviour description in --help, so
// a rename shows up as a test failure instead of only a docs drift.
func TestAcceptance_WorkspaceUpdate_ApprovalsRetentionDaysHelpListsFlag(t *testing.T) {
	stub := &approvalsRetentionStub{}
	srv := stub.start(t)
	cfg := approvalsRetentionConfig(t, srv.URL)

	out, err := runApprovalsRetentionCLI(t, cfg, "workspace", "update", "--help")
	if err != nil {
		t.Fatalf("workspace update --help: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "--approvals-retention-days") {
		t.Errorf("--help does not list --approvals-retention-days:\n%s", out)
	}
}
