package backup

import (
	"context"
	"strings"
	"testing"

	"filippo.io/age"
)

// Reuse the existing no-Docker snapshot transport and count target detaches.
// The bundle predates both consent and the target service declaration: replacing
// that target really exercises fenceExistingTargets even with no import images.
type privateArtifactFenceProbe struct {
	snapshotProbe
	detaches int
}

func (p *privateArtifactFenceProbe) DetachQuotaService(context.Context, string, string) error {
	p.detaches++
	return nil
}

func TestPrivateArtifactTargetRefusalPrecedesServiceFence(t *testing.T) {
	for _, protected := range []bool{true, false} {
		for _, dry := range []bool{false, true} {
			name := "ordinary"
			if protected {
				name = "protected"
			}
			if dry {
				name += " dry-run"
			}
			t.Run(name, func(t *testing.T) {
				db := openMigratedDBCov(t)
				const suffix = "private-service-fence"
				workspace, crew := seedCovWorkspace(t, db, suffix)
				actor := Actor{UserID: "u_cov_" + suffix, Role: "OWNER"}
				identity, err := age.GenerateX25519Identity()
				if err != nil {
					t.Fatal(err)
				}
				bundle, err := CreateBackup(t.Context(), db, CreateOptions{Scope: ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Actor: actor, Recipients: []age.Recipient{identity.Recipient()}})
				if err != nil {
					t.Fatal(err)
				}
				exec := func(q string, args ...any) {
					t.Helper()
					if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
						t.Fatal(err)
					}
				}
				const service = `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`
				exec(`UPDATE crews SET services_json=? WHERE id=?`, service, crew)
				exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES('private-fence-intent',?,'database','running',7,'2026-10-19T00:00:00Z')`, crew)
				if protected {
					exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,mode,origin) VALUES('private-fence-chat',?,?,?,'private','CHAT','UI')`, workspace, "a_cov_"+suffix, actor.UserID)
					exec(`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('private-fence-message',?,?,'private-fence-chat','private-fence-submission','2026-10-19T00:00:00Z')`, workspace, actor.UserID)
					var author string
					if err := db.QueryRow(`SELECT private_artifact_author_user_id FROM chats WHERE id='private-fence-chat'`).Scan(&author); err != nil || author != actor.UserID {
						t.Fatalf("protected target setup: author matches=%v err=%v", author == actor.UserID, err)
					}
				}
				probe := &privateArtifactFenceProbe{}
				_, restoreErr := RestoreBackup(t.Context(), db, RestoreOptions{Path: bundle.Path, Identities: []age.Identity{identity}, Actor: actor, Replace: true, DryRun: dry, ServiceSnapshots: probe})
				if protected {
					if restoreErr == nil || !strings.Contains(restoreErr.Error(), "private artifact") {
						t.Errorf("protected replacement must refuse, including dry-run: %v", restoreErr)
					}
				} else if restoreErr != nil {
					t.Fatalf("ordinary replacement: %v", restoreErr)
				}
				var fences int
				if err := db.QueryRow(`SELECT count(*) FROM service_backup_fences WHERE crew_id=?`, crew).Scan(&fences); err != nil {
					t.Fatal(err)
				}
				if protected || dry {
					if probe.stop != 0 || probe.detaches != 0 || fences != 0 {
						t.Errorf("refusal/dry-run touched service: stops=%d detaches=%d fences=%d", probe.stop, probe.detaches, fences)
					}
					var body, desired string
					var version int
					if err := db.QueryRow(`SELECT c.services_json,i.desired_state,i.version FROM crews c JOIN service_runtime_intents i ON i.crew_id=c.id WHERE c.id=?`, crew).Scan(&body, &desired, &version); err != nil {
						t.Fatal(err)
					}
					if body != service || desired != "running" || version != 7 {
						t.Error("refusal/dry-run mutated target service declaration or intent")
					}
				} else {
					if probe.stop != 1 || probe.detaches != 1 {
						t.Errorf("ordinary replacement did not exercise real service fence path: stops=%d detaches=%d", probe.stop, probe.detaches)
					}
					var oldServices int
					if err := db.QueryRow(`SELECT count(*) FROM crews WHERE id=? AND services_json=?`, crew, service).Scan(&oldServices); err != nil || oldServices != 0 {
						t.Fatalf("ordinary earlier declaration not restored: retained=%d err=%v", oldServices, err)
					}
				}
			})
		}
	}
}
