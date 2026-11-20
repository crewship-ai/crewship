package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// A bundle sealed with `backup create --recipient` can only be opened with
// the matching age identity. The API has accepted `identity` since recipients
// shipped, but `backup restore` offered only a passphrase, so the CLI could
// not restore what the CLI had created.
func TestBackupRestoreRunE_IdentityFile(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "ops.key")
	if err := os.WriteFile(keyFile, []byte("# created: today\n# public key: "+id.Recipient().String()+"\n"+id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	covSetFlagCli5(t, backupRestoreCmd, "identity", keyFile)
	stub.OnPost("/api/v1/admin/backups/restore", clitest.JSONResponse(200, map[string]any{
		"restored_ws": "acme", "restored_workspace_id": covWSCli5, "crews_count": 1, "rows_inserted": 5,
	}))

	covCaptureAll(t, func() { err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/backups/ws.tar"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups/restore")[0].Body, &body)
	if body["identity"] != id.String() {
		t.Errorf("restore body carries identity %v, want the key from %s", body["identity"], keyFile)
	}
	if p, _ := body["passphrase"].(string); p != "" {
		t.Errorf("an identity restore must not also send a passphrase, got %q", p)
	}
}

func TestBackupRestoreRunE_IdentityRejectsBadInput(t *testing.T) {
	guardCLIState(t)
	dir := t.TempDir()
	notAKey := filepath.Join(dir, "not-a-key")
	if err := os.WriteFile(notAKey, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	passFile := filepath.Join(dir, "pass")
	if err := os.WriteFile(passFile, []byte("secret-pass-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, identity, passFile, want string
	}{
		{"garbage file", notAKey, "", "identity"},
		{"missing file", filepath.Join(dir, "absent"), "", "identity"},
		{"with passphrase file", notAKey, passFile, "only one of --identity or --passphrase-file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := covSetupCli5(t)
			covResetBackupRestoreFlags(t)
			covSetFlagCli5(t, backupRestoreCmd, "identity", tc.identity)
			covSetFlagCli5(t, backupRestoreCmd, "passphrase-file", tc.passFile)
			var err error
			covCaptureAll(t, func() { err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"}) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
			if n := len(stub.CallsFor("POST", "/api/v1/admin/backups/restore")); n != 0 {
				t.Errorf("bad identity input still reached the server (%d calls)", n)
			}
		})
	}
}
