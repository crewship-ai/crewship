package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Rotation adds a new active key to the app-owned keyring with the instance
// reseal authority, keeps every earlier key, records one audit entry signed by
// the running snapshot, and puts the keyring back when that entry fails.
func TestAuditSigningKeyRotate(t *testing.T) {
	for _, tc := range []struct {
		name, caller string
		external     bool
		auditFails   bool
		want         int
	}{
		{name: "instance_owner", caller: "boss", want: 200},
		{name: "workspace_admin", caller: "wsadmin", want: 403},
		{name: "external_keyring", caller: "boss", external: true, want: 409},
		{name: "audit_failure_restores", caller: "boss", auditFails: true, want: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
			t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
			root := t.TempDir()
			t.Setenv("CREWSHIP_HOME", root)
			t.Setenv("CREWSHIP_DATA_DIR", "")
			keys, path, err := journal.EnsureSigningKeys(root, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(journal.SigningKeysPathEnv, path)
			if tc.external {
				external := filepath.Join(t.TempDir(), "operator-keyring.json")
				raw, _ := os.ReadFile(path)
				if err := os.WriteFile(external, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				path = external
				t.Setenv(journal.SigningKeysPathEnv, external)
			}
			f := newInstanceFixture(t)
			defer f.r.Shutdown()
			t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
			if tc.auditFails {
				mustExec(t, f.db, `CREATE TRIGGER fail_key_rotation_audit BEFORE INSERT ON instance_audit_logs WHEN NEW.action='audit.signing_key_rotated' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			}
			before, _ := os.ReadFile(path)
			token := f.boss
			if tc.caller == "wsadmin" {
				token = f.wsAdmin
			}
			body, _ := json.Marshal(map[string]any{"expected_active_key_id": keys.ActiveID(), "reason": "Scheduled audit key rotation"})
			response := f.do(token, "POST", "/api/v1/admin/audit/signing-keys/rotate", string(body))
			if response.Code != tc.want {
				t.Fatalf("HTTP=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
			}
			if tc.want != 200 {
				after, _ := os.ReadFile(path)
				rotated, err := journal.LoadSigningKeys(path)
				if err != nil || rotated.ActiveID() != keys.ActiveID() {
					t.Fatalf("refused rotation changed the active key: %v", err)
				}
				if !bytes.Equal(before, after) {
					t.Error("refused rotation rewrote the keyring")
				}
				if staged, _ := filepath.Glob(path + ".pending-*"); len(staged) != 0 {
					t.Errorf("refused rotation left a staged keyring: %v", staged)
				}
				return
			}
			var result struct {
				Previous  string `json:"previous_active_key_id"`
				Active    string `json:"active_key_id"`
				Effective string `json:"effective"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Previous != keys.ActiveID() || result.Active == result.Previous || result.Effective != "next_start" {
				t.Fatalf("rotation reply: %s", response.Body.String())
			}
			rotated, err := journal.LoadSigningKeys(path)
			if err != nil || rotated.ActiveID() != result.Active {
				t.Fatalf("keyring active key: %v", err)
			}
			var keyID string
			if err := f.db.QueryRow(`SELECT chain_key_id FROM instance_audit_logs WHERE action='audit.signing_key_rotated' AND entity_id=? AND user_id='boss'`, result.Active).Scan(&keyID); err != nil {
				t.Fatal(err)
			}
			if keyID != keys.ActiveID() {
				t.Errorf("rotation entry signed by %s, want the running snapshot %s", keyID, keys.ActiveID())
			}
			verified, err := journal.VerifyInstanceAuditWithKeys(t.Context(), f.db, rotated)
			if err != nil || !verified.OK {
				t.Fatalf("instance chain does not verify with the rotated keyring: %+v %v", verified, err)
			}
		})
	}
}
