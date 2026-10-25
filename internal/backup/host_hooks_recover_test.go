package backup

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// Recovery can run before migrations and without the source journal keys.
func TestRecoveredHostHooksQuarantineReceipt(t *testing.T) {
	for _, approvalColumn := range []bool{false, true} {
		for _, fault := range []string{"", "ABORT", "IGNORE", "UPDATE_IGNORE"} {
			t.Run(fmt.Sprintf("approval=%t/%s", approvalColumn, fault), func(t *testing.T) {
				db, err := database.Open(filepath.Join(t.TempDir(), "recovered.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				schema := `CREATE TABLE hooks_config(id TEXT PRIMARY KEY,workspace_id TEXT,handler_kind TEXT,enabled INTEGER`
				if approvalColumn {
					schema += `,host_authorization TEXT DEFAULT ''`
				}
				schema += `); CREATE TABLE audit_logs(id TEXT,workspace_id TEXT,user_id TEXT,action TEXT,entity_type TEXT,entity_id TEXT,metadata TEXT,created_at TEXT); INSERT INTO hooks_config(id,workspace_id,handler_kind,enabled) VALUES('shell','ws','shell',1),('http','ws','http',1)`
				if _, err := db.Exec(schema); err != nil {
					t.Fatal(err)
				}
				if approvalColumn {
					if _, err := db.Exec(`UPDATE hooks_config SET host_authorization='source-approval' WHERE id='shell'`); err != nil {
						t.Fatal(err)
					}
				}
				if fault != "" {
					stmt := `CREATE TRIGGER reject_receipt BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(IGNORE); END`
					if fault == "UPDATE_IGNORE" {
						stmt = `CREATE TRIGGER reject_quarantine BEFORE UPDATE OF enabled ON hooks_config BEGIN SELECT RAISE(IGNORE); END`
					}
					if fault == "ABORT" {
						stmt = `CREATE TRIGGER reject_receipt BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`
					}
					if _, err := db.Exec(stmt); err != nil {
						t.Fatal(err)
					}
				}
				count, err := resetRecoveredHostHooks(t.Context(), db.DB)
				if fault != "" && err == nil {
					t.Fatal("quarantine succeeded without receipt")
				}
				if fault == "" && (err != nil || count != 1) {
					t.Fatalf("quarantine: %d %v", count, err)
				}
				var shell, http, audits int
				if err := db.QueryRow(`SELECT enabled FROM hooks_config WHERE id='shell'`).Scan(&shell); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT enabled FROM hooks_config WHERE id='http'`).Scan(&http); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM audit_logs`).Scan(&audits); err != nil {
					t.Fatal(err)
				}
				wantShell, wantAudits := 0, 1
				if fault != "" {
					wantShell, wantAudits = 1, 0
				}
				if shell != wantShell || http != 1 || audits != wantAudits {
					t.Fatalf("partial quarantine: shell=%d http=%d receipts=%d", shell, http, audits)
				}
				if approvalColumn {
					var approval string
					if err := db.QueryRow(`SELECT host_authorization FROM hooks_config WHERE id='shell'`).Scan(&approval); err != nil {
						t.Fatal(err)
					}
					want := ""
					if fault != "" {
						want = "source-approval"
					}
					if approval != want {
						t.Fatalf("approval=%q want=%q", approval, want)
					}
				}
			})
		}
	}
}
