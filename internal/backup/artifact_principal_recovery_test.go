package backup_test

import (
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

func TestArtifactPrincipalPhysicalRecoveryRequiresFreshAdmission(t *testing.T) {
	for _, tt := range []struct {
		name    string
		present bool
	}{
		{"no dispatch tables", true},
		{"principal table absent", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstanceFixture(t)
			// Recovery must fence admission without depending on later dispatch tables.
			for _, table := range []string{"document_requests", "page_artifact_requests", "page_artifact_run_delegations"} {
				if _, err := f.db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by) VALUES('principal-chat','ws_e2e_1','a_alice','u_admin')`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at)
 VALUES('principal-run','ws_e2e_1','a_alice','c_alpha','u_admin','principal-chat','message','member',1,'generation',1,'2099-01-01T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			if !tt.present {
				if _, err := f.db.Exec("DROP TABLE artifact_run_principals"); err != nil {
					t.Fatal(err)
				}
			}
			created := f.create(t, true)
			dataDir := filepath.Join(t.TempDir(), "recovered")
			report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{
				BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: dataDir, Actor: "u_admin",
			})
			if err != nil || report.Result != backup.RestoreResultOK {
				t.Fatalf("recovery %#v: %v", report, err)
			}
			if !tt.present {
				return
			}
			recovered, err := database.Open("file:" + filepath.Join(dataDir, "crewship.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			var count int
			if err := recovered.QueryRow("SELECT count(*) FROM artifact_run_principals").Scan(&count); err != nil || count != 0 {
				t.Fatalf("restored live principal count %d: %v", count, err)
			}
			if err := f.db.QueryRow("SELECT count(*) FROM artifact_run_principals").Scan(&count); err != nil || count != 1 {
				t.Fatalf("backup changed source admissions: %d %v", count, err)
			}
			dump, err := backup.DumpWorkspace(t.Context(), f.db, "ws_e2e_1")
			if err != nil {
				t.Fatal(err)
			}
			if _, present := dump.Tables["artifact_run_principals"]; present {
				t.Fatal("portable workspace dump carries live principal admissions")
			}
			if !strings.Contains(strings.Join(report.RuntimeReset, ","), "artifact run principal") {
				t.Fatalf("principal reset missing from recovery report: %v", report.RuntimeReset)
			}
		})
	}
}
