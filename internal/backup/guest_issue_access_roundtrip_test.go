package backup_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestGuestIssueRoutingAndHistoricalAudiencesSurviveBackup(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	for _, query := range []string{
		`INSERT INTO projects(id,workspace_id,name,slug) VALUES('guest-original','ws_e2e_1','Original client','guest-original'),('guest-current','ws_e2e_1','Current client','guest-current')`,
		`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,mission_type,project_id) VALUES('guest-issue','ws_e2e_1','c_alpha','a_alice','guest-issue-trace','Moved request','TODO','issue','guest-original')`,
		`INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,guest_visible,guest_project_id) VALUES('guest-old-audience','guest-issue','user','u_admin','Original client thread',1,'guest-original')`,
		`INSERT INTO mission_comments(id,mission_id,author_type,author_id,body) VALUES('guest-private-note','guest-issue','user','u_admin','Private staff note')`,
		`UPDATE missions SET project_id='guest-current' WHERE id='guest-issue'`,
		`INSERT INTO project_guest_issue_intake(project_id,workspace_id,crew_id,revision,updated_by,updated_at) VALUES('guest-original','ws_e2e_1','c_alpha',7,'u_admin','2026-10-14T00:00:00Z')`,
		// An inactive configuration remains history, never creation authority.
		`UPDATE crews SET deleted_at='2026-10-14T00:00:00Z' WHERE id='c_alpha'`,
	} {
		if _, err := source.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	const passphrase = "guest-issue-synthetic-backup-passphrase"
	created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor}
			if fork {
				options.AsWorkspace = "guest-issue-fork"
			}
			result, err := backup.RestoreBackup(t.Context(), target, options)
			if err != nil {
				t.Fatal(err)
			}
			ws := workspace
			if fork {
				ws = result.RestoredWorkspaceID
			}
			var count int
			err = target.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM project_guest_issue_intake i JOIN projects p ON p.id=i.project_id JOIN crews c ON c.id=i.crew_id WHERE i.workspace_id=? AND p.workspace_id=i.workspace_id AND c.workspace_id=i.workspace_id AND i.revision=7 AND c.deleted_at IS NOT NULL`, ws).Scan(&count)
			if err != nil || count != 1 {
				t.Fatalf("routing metadata lost or cross-scoped: %d %v", count, err)
			}
			err = target.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM mission_comments mc JOIN missions m ON m.id=mc.mission_id JOIN projects audience ON audience.id=mc.guest_project_id WHERE m.workspace_id=? AND audience.workspace_id=m.workspace_id AND mc.body='Original client thread' AND mc.guest_visible=1 AND mc.guest_project_id<>m.project_id`, ws).Scan(&count)
			if err != nil || count != 1 {
				t.Fatalf("original audience lost or revived: %d %v", count, err)
			}
			err = target.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM mission_comments mc JOIN missions m ON m.id=mc.mission_id WHERE m.workspace_id=? AND mc.body='Private staff note' AND mc.guest_visible=0 AND mc.guest_project_id IS NULL`, ws).Scan(&count)
			if err != nil || count != 1 {
				t.Fatalf("private comment publication changed: %d %v", count, err)
			}
			if fork {
				if err = target.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM project_guest_issue_intake WHERE workspace_id=? AND (project_id='guest-original' OR crew_id='c_alpha')`, ws).Scan(&count); err != nil || count != 0 {
					t.Fatalf("fork retained original routing identities: %d %v", count, err)
				}
			}
			if err = target.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("foreign keys: %d %v", count, err)
			}
		})
	}
}
