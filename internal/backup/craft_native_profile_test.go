package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCraftNativeProfileBackupPreservesExactPinsAndWholeVersions(t *testing.T) {
	for _, app := range []string{"wordcraft", "gridcraft"} {
		t.Run(app, func(t *testing.T) {
			source := openDurableArtifactBackupDB(t)
			ws := seedWorkspace(t, source)
			scope := mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: "native-backup"}
			if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,'Native','u_admin','{}')`, scope.ArtifactID, ws, scope.ArtifactID); err != nil {
				t.Fatal(err)
			}
			principal := mooring.EditLeasePrincipal{ActorID: "u_admin", SessionID: "source-connection"}
			store := func(db *sql.DB) *mooring.NativeStore {
				leases := mooring.NewEditLeaseStore(db, mooring.EditLeaseConfig{Enabled: true, Clock: recoveredOwnerClock{}, TTL: time.Minute, Authority: func(context.Context, *sql.Tx, mooring.EditLeaseAccess) error { return nil }})
				return mooring.NewNativeStore(db, mooring.NativeStoreConfig{Enabled: true, Application: app, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Leases: leases, Authorize: func(context.Context, *sql.Tx, mooring.EditLeaseScope, string) (mooring.EditLeasePrincipal, error) {
					return principal, nil
				}, Validate: func(context.Context, []byte) error { return nil }})
			}
			s := store(source)
			lease, _, err := s.Initialize(t.Context(), scope, principal, []byte(`{"v":1}`))
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"native":"whole original model","media":"AP+AwAA="}`)
			if _, err = s.Save(t.Context(), lease, 1, raw); err != nil {
				t.Fatal(err)
			}
			actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
			created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "native-roundtrip-passphrase", Actor: actor})
			if err != nil {
				t.Fatal(err)
			}
			for _, fork := range []bool{false, true} {
				t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
					target := openDurableArtifactBackupDB(t)
					opts := backup.RestoreOptions{Path: created.Path, Passphrase: "native-roundtrip-passphrase", Actor: actor}
					if fork {
						opts.AsWorkspace = "native-fork"
					}
					if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
						t.Fatal(err)
					}
					var restored mooring.EditLeaseScope
					if err = target.QueryRow(`SELECT workspace_id,page_id FROM craft_native_documents`).Scan(&restored.WorkspaceID, &restored.ArtifactID); err != nil {
						t.Fatal(err)
					}
					if fork && (restored.WorkspaceID == ws || restored.ArtifactID == scope.ArtifactID) {
						t.Fatal("native profile fork not remapped")
					}
					state, err := store(target).Read(t.Context(), restored)
					if err != nil || state.LatestVersion != 2 || !bytes.Equal(state.Manifest.Files["document.json"].Content, raw) {
						t.Fatalf("native profile/history lost: %+v %v", state, err)
					}
					var count int
					if err = target.QueryRow(`SELECT count(*) FROM mooring_edit_leases`).Scan(&count); err != nil || count != 0 {
						t.Fatalf("source capability transported: %d %v", count, err)
					}
				})
			}
		})
	}
}
