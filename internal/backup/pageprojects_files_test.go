package backup

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

func TestPageProjectsEncryptedBackupCleanAndForkRestore(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	ws, crew := seedCovWorkspace(t, db, "page-files")
	store := &pages.ProjectStore{Directory: t.TempDir()}
	source := pageprofile.Source()
	digest, err := store.Put(ctx, ws, source)
	if err != nil {
		t.Fatal(err)
	}
	const page = "page-files"
	spec := `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"name":"Health","slug":"health"},"spec":{"panels":[]}}`
	commit, err := store.Checkpoint(ctx, ws, page, "", spec, "agent/original", 1, source)
	if err != nil {
		t.Fatal(err)
	}
	parent := commit
	commit, err = store.Checkpoint(ctx, ws, page, parent, spec, "agent/original", 2, source)
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.Lease(ctx, ws, true)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Prune(ctx, ws, map[string]bool{digest: true}, map[string][]string{page: {commit}})
	release()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReadCheckpoint(ctx, ws, page, parent); err == nil {
		t.Fatal("unretained parent survived compaction")
	}
	artifact := &pagebuild.Artifact{Format: pagebuild.ArtifactFormat, JavaScript: "document.body.textContent='health';", CSS: "body{color:green}", Toolchain: "test-profile"}
	artifacts := &pagebuild.Store{Directory: filepath.Join(store.Directory, "artifacts")}
	artifactDigest, err := artifacts.Put(ctx, ws, artifact)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES(?,?,'health','Health',?,?)`, page, ws, crew, spec)
	exec(`INSERT INTO page_project_drafts(page_id,source_digest,revision,spec_json,updated_at,git_commit) VALUES(?,?,1,?,'2026-09-09',?)`, page, digest, spec, commit)
	exec(`INSERT INTO page_project_revisions(page_id,revision,source_digest,created_at,git_commit,spec_json,actor_json) VALUES(?,1,?,'2026-09-09',?,?,'{"agent_id":"original-agent"}')`, page, digest, commit, spec)
	exec(`INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,artifact_digest,created_at) VALUES('build-files',?,1,?,'ready',?,'2026-09-09')`, page, digest, artifactDigest)
	exec(`INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,created_at) VALUES(?,1,'build-files',1,?,?,?,?,'{}','2026-09-09')`, page, digest, commit, artifactDigest, spec)
	exec(`INSERT INTO page_project_live(page_id,version,published) VALUES(?,1,0)`, page)
	exec(`INSERT INTO page_project_withdrawals(page_id,version,created_at) VALUES(?,1,'2026-09-09')`, page)
	actor := Actor{UserID: "u_cov_page-files", Email: "page-files@cov.test", Role: "ADMIN"}
	created, err := CreateBackup(ctx, db, CreateOptions{Scope: ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Actor: actor, Passphrase: "pages-fixture-passphrase", PageProjectsPath: store.Directory})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		name := "clean"
		if fork {
			name = "fork"
		}
		t.Run(name, func(t *testing.T) {
			target := openMigratedDBCov(t)
			directory := t.TempDir()
			asWorkspace := ""
			if fork {
				target = db
				directory = store.Directory
				asWorkspace = "forked-pages"
			}
			result, err := RestoreBackup(ctx, target, RestoreOptions{Path: created.Path, Passphrase: "pages-fixture-passphrase", Actor: actor, PageProjectsPath: directory, AsWorkspace: asWorkspace})
			if err != nil {
				t.Fatal(err)
			}
			restoredWS := ws
			if fork {
				restoredWS = result.RestoredWorkspaceID
				if restoredWS == "" || restoredWS == ws {
					t.Fatal("workspace was not forked")
				}
			}
			var restoredPage string
			if err := target.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='health'`, restoredWS).Scan(&restoredPage); err != nil {
				t.Fatal(err)
			}
			if fork && restoredPage == page {
				t.Fatal("Page identity was not remapped")
			}
			restored := &pages.ProjectStore{Directory: directory}
			boundaries, err := restored.CheckpointBoundaries(ctx, restoredWS, restoredPage)
			if err != nil || len(boundaries) != 1 || boundaries[0] != commit {
				t.Fatalf("shallow archive boundary lost: %v %v", boundaries, err)
			}
			if _, _, err := restored.ReadCheckpoint(ctx, restoredWS, restoredPage, parent); err == nil {
				t.Fatal("backup resurrected discarded ancestry")
			}
			got, archived, err := restored.ReadCheckpoint(ctx, restoredWS, restoredPage, commit)
			if err != nil || archived != spec {
				t.Fatalf("Git provenance lost: %v", err)
			}
			actual, _ := got.Digest()
			if actual != digest {
				t.Fatal("source bytes changed")
			}
			fetched, err := (&pagebuild.Store{Directory: filepath.Join(directory, "artifacts")}).Get(ctx, restoredWS, artifactDigest)
			if err != nil || fetched.JavaScript != artifact.JavaScript {
				t.Fatalf("artifact lost: %v", err)
			}
			var published bool
			var author string
			if err := target.QueryRow(`SELECT published FROM page_project_live WHERE page_id=?`, restoredPage).Scan(&published); err != nil || published {
				t.Fatalf("withdrawal state lost: %v", err)
			}
			if err := target.QueryRow(`SELECT actor_json FROM page_project_revisions WHERE page_id=?`, restoredPage).Scan(&author); err != nil {
				t.Fatal(err)
			}
			var identity map[string]string
			json.Unmarshal([]byte(author), &identity)
			if identity["agent_id"] != "original-agent" {
				t.Fatal("historical provenance rewritten")
			}
		})
	}
	// A metadata-only backup must not be presented as a complete Page backup.
	if _, err := CreateBackup(ctx, db, CreateOptions{Scope: ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Actor: actor, Passphrase: "pages-fixture-passphrase"}); err == nil {
		t.Fatal("missing source directory silently omitted files")
	}
}
