package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestRestoreDumpImageSelectionOwnershipAndExclusion(t *testing.T) {
	for _, state := range []string{"same-installation", "new-installation"} {
		t.Run(state, func(t *testing.T) {
			source := openMigratedDBCov(t)
			ws, crew := seedCovWorkspace(t, source, "image-selection")
			if _, err := source.Exec(`UPDATE crews SET cached_image='synthetic-image',previous_image='synthetic-previous' WHERE id=?`, crew); err != nil {
				t.Fatal(err)
			}
			dump, err := DumpWorkspace(t.Context(), source, ws)
			if err != nil {
				t.Fatal(err)
			}
			target := openMigratedDBCov(t)
			gate := resourcelifecycle.Images(target)

			checked := false
			_, err = RestoreDumpTxHooks(t.Context(), target, dump, &RestoreDumpHooks{PreCommit: func(context.Context) error {
				if release, ok := gate.TryCollect(); ok {
					release()
					t.Fatal("collector admitted during restore")
				}
				checked = true
				return nil
			}})

			if err != nil || !checked {
				t.Fatalf("restore failed: %v checked=%t", err, checked)
			}
			var current, previous sql.NullString
			if err := target.QueryRow(`SELECT cached_image,previous_image FROM crews WHERE id=?`, crew).Scan(&current, &previous); err != nil {
				t.Fatal(err)
			}
			if current.String != "synthetic-image" || previous.String != "synthetic-previous" {
				t.Fatal("local selections lost")
			}
		})
	}
}

type importedEnvironmentOps struct {
	*fakeEnvOps
	importedRef string
}

func (o *importedEnvironmentOps) LoadImage(ctx context.Context, r io.Reader) (string, error) {
	result, err := o.fakeEnvOps.LoadImage(ctx, r)
	if err == nil {
		o.images[o.importedRef] = true
	}
	return result, err
}

func TestCompleteEnvironmentRestoreSelectsCapturedSnapshotAcrossIdentities(t *testing.T) {
	source := openMigratedDBCov(t)
	ws, crew := seedCovWorkspace(t, source, "complete-selection")
	var seq int
	var dbName, sourceLocation string
	if err := source.QueryRow(`PRAGMA database_list`).Scan(&seq, &dbName, &sourceLocation); err != nil {
		t.Fatal(err)
	}
	sourceIdentity, err := resourcelifecycle.LoadIdentity(t.Context(), t.TempDir(), source, sourceLocation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sourceIdentity.Close() })
	sourceImage := "sha256:" + strings.Repeat("a", 64)
	if _, err := source.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, sourceImage, crew); err != nil {
		t.Fatal(err)
	}
	snapshotOps := newFakeEnvOps()
	snapshotOps.snap = crewSnapshot()
	snapshotOps.snap.Image = sourceImage
	snapshotOps.snap.ImageID = sourceImage
	snapshotOps.snap.Labels["crewship.image.instance-id"] = sourceIdentity.ID
	snapshotOps.snap.Labels["crewship.image.kind"] = "crew"
	store := &EnvironmentStore{Dir: t.TempDir()}
	env, _ := captureWithFake(t, snapshotOps, store, nil, "")
	env.CrewID = crew
	env.Crew = "crew-complete-selection"
	dump, err := DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDBCov(t)
	var targetLocation string
	if err := target.QueryRow(`PRAGMA database_list`).Scan(&seq, &dbName, &targetLocation); err != nil {
		t.Fatal(err)
	}
	targetIdentity, err := resourcelifecycle.LoadIdentity(t.Context(), t.TempDir(), target, targetLocation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = targetIdentity.Close() })
	if targetIdentity.ID == sourceIdentity.ID {
		t.Fatal("fixture did not change installation identity")
	}
	imported := &importedEnvironmentOps{fakeEnvOps: newFakeEnvOps(), importedRef: env.ImageRef}
	open, has := BlobSources(store)
	var commitTx *sql.Tx
	_, err = RestoreDumpTxHooks(t.Context(), target, dump, &RestoreDumpHooks{PostInsert: func(_ context.Context, tx *sql.Tx) error { commitTx = tx; return nil }, PreCommit: func(ctx context.Context) error {
		out := RestoreEnvironment(ctx, imported, env, EnvironmentRestoreOptions{Open: open, Has: has, Keys: store.ServerKeys()})
		if out.Result != EnvRestored {
			return fmt.Errorf("snapshot restore: %+v", out)
		}
		return selectRestoredEnvironments(ctx, commitTx, &ExtractedPayload{DBDump: dump, EnvironmentBySlug: map[string]*Environment{env.Crew: env}}, []EnvironmentOutcome{out}, map[string]bool{crew: true})
	}})
	if err != nil {
		t.Fatal(err)
	}

	var selected string
	if err := target.QueryRow(`SELECT cached_image FROM crews WHERE id=?`, crew).Scan(&selected); err != nil {
		t.Fatal(err)
	}
	if selected != env.ImageRef || len(imported.loaded) != 1 || len(imported.committed) != 0 {
		t.Fatalf("snapshot replaced by build/source: selected=%s loads=%d commits=%v", selected, len(imported.loaded), imported.committed)
	}
	if env.Config.Labels["crewship.image.instance-id"] != sourceIdentity.ID {
		t.Fatal("restore rewrote provenance")
	}
}

func TestCapturedEnvironmentPublicationRejectsForeignCrewAndWrongDescriptor(t *testing.T) {
	for _, mode := range []string{"foreign-id", "wrong-image", "unknown-descriptor", "existing-row", "legacy-no-crew-id"} {
		t.Run(mode, func(t *testing.T) {
			db := openMigratedDBCov(t)
			ws, crew := seedCovWorkspace(t, db, "publish-environment")
			_, foreign := seedCovWorkspace(t, db, "foreign-environment")
			env := &Environment{Crew: "crew-publish-environment", CrewID: crew, Managed: true, ImageRef: "crewship-env/test:snapshot"}
			ex := &ExtractedPayload{DBDump: &DBDump{Tables: map[string][]map[string]any{"crews": {{"id": crew, "workspace_id": ws, "slug": env.Crew}}}}, EnvironmentBySlug: map[string]*Environment{env.Crew: env}}
			outcome := EnvironmentOutcome{Crew: env.Crew, Result: EnvRestored, Image: env.ImageRef}
			eligible := map[string]bool{crew: true}
			switch mode {
			case "foreign-id":
				env.CrewID = foreign
			case "wrong-image":
				outcome.Image = "unrelated:artifact"
			case "unknown-descriptor":
				outcome.Crew = "foreign-archive-entry"
			case "existing-row":
				eligible = map[string]bool{}
			case "legacy-no-crew-id":
				env.CrewID = ""
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			err = selectRestoredEnvironments(t.Context(), tx, ex, []EnvironmentOutcome{outcome}, eligible)
			shouldPass := mode == "existing-row" || mode == "legacy-no-crew-id"
			if (err == nil) != shouldPass {
				tx.Rollback()
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if err != nil {
				tx.Rollback()
			} else if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var foreignImage, local sql.NullString
			if err := db.QueryRow(`SELECT cached_image FROM crews WHERE id=?`, foreign).Scan(&foreignImage); err != nil {
				t.Fatal(err)
			}
			if foreignImage.Valid {
				t.Fatal("foreign crew selection was modified")
			}
			if err := db.QueryRow(`SELECT cached_image FROM crews WHERE id=?`, crew).Scan(&local); err != nil {
				t.Fatal(err)
			}
			if mode == "legacy-no-crew-id" && local.String != env.ImageRef {
				t.Fatal("legacy descriptor could not map to restored crew")
			}
			if mode != "legacy-no-crew-id" && local.Valid {
				t.Fatal("unbound or existing row was modified")
			}
		})
	}
}
