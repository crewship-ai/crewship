package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// #814 — revoking a file-based credential must remove its /secrets file(s)
// from running crew containers, exec'd as UID 1001.

// recordingCtr captures every ExecConfig passed to Exec (reusing
// mockContainerExec from keeper_execute_test.go for the rest of the interface).
type recordingCtr struct {
	*mockContainerExec
	calls *[]provider.ExecConfig
}

func (r *recordingCtr) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	*r.calls = append(*r.calls, cfg)
	return r.mockContainerExec.Exec(ctx, cfg)
}

func newRecordingCtr(calls *[]provider.ExecConfig, execErr error) *recordingCtr {
	return &recordingCtr{mockContainerExec: &mockContainerExec{execErr: execErr}, calls: calls}
}

func TestCredSecretPaths(t *testing.T) {
	cases := []struct {
		credType string
		want     []string
	}{
		{"SECRET", []string{"/secrets/writer/GH_TOKEN"}},
		{"CLI_TOKEN", []string{"/secrets/writer/GH_TOKEN"}},
		{"GENERIC_SECRET", []string{"/secrets/writer/GH_TOKEN"}},
		{"USERPASS", []string{"/secrets/writer/GH_TOKEN_USERNAME", "/secrets/writer/GH_TOKEN_PASSWORD"}},
		{"SSH_KEY", []string{"/secrets/writer/ssh/GH_TOKEN"}},
		{"CERTIFICATE", []string{"/secrets/writer/certs/GH_TOKEN.pem"}},
		{"API_KEY", nil},      // sidecar-injected, never on disk
		{"AI_CLI_TOKEN", nil}, // ditto
	}
	for _, c := range cases {
		got := credSecretPaths("writer", "GH_TOKEN", c.credType, nil)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: paths = %v, want %v", c.credType, got, c.want)
		}
	}
}

// TestCredSecretPaths_IncludesMultiPartFields is the revoke half of P4 delivery.
// A multi-part credential writes one file per part (exec_sidecar.go), so a
// revoke that removed only the primary would leave the secret access key on
// disk in a live container while the vault reported the credential gone — the
// operator's revoke would be a lie until the container next restarted.
//
// The names are derived with the SAME function delivery used; a second spelling
// here would remove the wrong paths and, being a best-effort `rm -f`, would say
// nothing about it.
func TestCredSecretPaths_IncludesMultiPartFields(t *testing.T) {
	got := credSecretPaths("writer", "AWS", "GENERIC_SECRET", []string{"region", "secret_access_key"})
	want := []string{
		"/secrets/writer/AWS",
		"/secrets/writer/AWS_REGION",
		"/secrets/writer/AWS_SECRET_ACCESS_KEY",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("paths = %v, want %v", got, want)
	}

	// A type with no on-disk form has no on-disk parts either: buildCredFileScript
	// skips the whole credential before it ever looks at the fields.
	if got := credSecretPaths("writer", "ANTHROPIC", "API_KEY", []string{"region"}); got != nil {
		t.Errorf("API_KEY paths = %v, want none — the credential itself never touches disk", got)
	}

	// An unsafe derived name is dropped rather than interpolated into the `rm`.
	// Delivery would have refused to write it, so there is nothing to remove,
	// and the one thing that must not happen is it reaching a shell.
	if got := credSecretPaths("writer", "AWS", "SECRET", []string{"a;rm -rf /"}); strings.Join(got, "|") != "/secrets/writer/AWS" {
		t.Errorf("paths = %v, want only the primary — an unsafe part name must not reach the shell", got)
	}
}

func TestBuildCredRemoveScript(t *testing.T) {
	if s := buildCredRemoveScript("writer", "GH_TOKEN", "SECRET", nil); s != "rm -f '/secrets/writer/GH_TOKEN'" {
		t.Errorf("SECRET script = %q", s)
	}
	if s := buildCredRemoveScript("writer", "DB", "USERPASS", nil); s != "rm -f '/secrets/writer/DB_USERNAME' '/secrets/writer/DB_PASSWORD'" {
		t.Errorf("USERPASS script = %q", s)
	}
	if s := buildCredRemoveScript("writer", "KEY", "SSH_KEY", nil); s != "rm -f '/secrets/writer/ssh/KEY'" {
		t.Errorf("SSH_KEY script = %q", s)
	}
	if s := buildCredRemoveScript("writer", "X", "API_KEY", nil); s != "" {
		t.Errorf("API_KEY (no disk form) script = %q, want empty", s)
	}
}

// seedFileMountCred wires ws → crew → agent → credential (of credType) →
// agent_credentials, returning the credential id. Whether the reconciler acts
// is decided by credType (file-materialized vs sidecar-injected), NOT by the
// vestigial agent_credentials.mount_type (always 'env' — nothing sets 'file').
func seedFileMountCred(t *testing.T, db *sql.DB, credType string) (wsID, credID string) {
	t.Helper()
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	seedCrew(t, db, "crew-rec", wsID, "Rec", "recrew")
	seedAgent(t, db, "agent-rec", wsID, "crew-rec", "Writer", "writer")
	credID = "cred-rec"
	// Insert directly (no encryption dep — reconcile reads type/slug/env only).
	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		 VALUES (?, ?, 'gh', 'x', ?, 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		credID, wsID, credType, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	// mount_type left at its 'env' default on purpose — it must not matter.
	if _, err := db.Exec(
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES ('ac-rec', 'agent-rec', ?, 'GH_TOKEN', 0)`, credID); err != nil {
		t.Fatalf("seed agent_credentials: %v", err)
	}
	return wsID, credID
}

func TestReconcileRevokedCredential_FileMount_ExecsRemoveAsUID1001(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "SECRET")

	var calls []provider.ExecConfig
	h := NewCredentialHandler(db, newTestLogger())
	h.SetContainer(newRecordingCtr(&calls, nil))

	h.reconcileRevokedCredential(context.Background(), credID, wsID)

	if len(calls) != 1 {
		t.Fatalf("exec called %d times, want 1", len(calls))
	}
	c := calls[0]
	if c.ContainerID != "crewship-team-recrew" {
		t.Errorf("ContainerID = %q, want crewship-team-recrew (crew slug)", c.ContainerID)
	}
	if c.User != "1001:1001" {
		t.Errorf("User = %q, want 1001:1001 (agent UID)", c.User)
	}
	if len(c.Cmd) != 3 || c.Cmd[0] != "sh" || c.Cmd[1] != "-c" {
		t.Fatalf("Cmd = %v, want [sh -c <script>]", c.Cmd)
	}
	if !strings.Contains(c.Cmd[2], "rm -f '/secrets/writer/GH_TOKEN'") {
		t.Errorf("script = %q, want an rm of the secret file", c.Cmd[2])
	}
}

// A sidecar-injected type (API_KEY) never touches disk, so revoking it must
// not exec anything — the distinction is the TYPE, not the (unused) mount_type.
func TestReconcileRevokedCredential_NonFileType_NoExec(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "API_KEY")

	var calls []provider.ExecConfig
	h := NewCredentialHandler(db, newTestLogger())
	h.SetContainer(newRecordingCtr(&calls, nil))

	h.reconcileRevokedCredential(context.Background(), credID, wsID)
	if len(calls) != 0 {
		t.Fatalf("non-file type (API_KEY) must not exec (nothing on disk); got %d calls", len(calls))
	}
}

func TestReconcileRevokedCredential_NilContainer_NoOp(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "SECRET")
	h := NewCredentialHandler(db, newTestLogger()) // no SetContainer → container nil
	// Must not panic.
	h.reconcileRevokedCredential(context.Background(), credID, wsID)
}

func TestReconcileRevokedCredential_ExecError_Tolerated(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "SECRET")

	var calls []provider.ExecConfig
	h := NewCredentialHandler(db, newTestLogger())
	// A stopped container makes Exec error — reconcile must swallow it.
	h.SetContainer(newRecordingCtr(&calls, context.DeadlineExceeded))

	h.reconcileRevokedCredential(context.Background(), credID, wsID)
	if len(calls) != 1 {
		t.Fatalf("exec attempted %d times, want 1 (error tolerated, not retried)", len(calls))
	}
}
