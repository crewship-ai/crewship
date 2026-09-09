package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

func TestPageProjectRetentionPinsLiveDraftAndRunningBuild(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	h.SetBuildWorker(&testPageBuilder{}, &pagebuild.Store{Directory: t.TempDir()})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var page, digest, commit, spec string
	if err := h.db.QueryRow(`SELECT page_id,source_digest,git_commit,spec_json FROM page_project_drafts`).Scan(&page, &digest, &commit, &spec); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	artifact := &pagebuild.Artifact{Format: pagebuild.ArtifactFormat, JavaScript: "export {};", Toolchain: "test"}
	liveArtifact, err := h.pageArtifacts.Put(ctx, ws, artifact)
	if err != nil {
		t.Fatal(err)
	}
	artifact.JavaScript = "// orphan\nexport {};"
	orphanArtifact, err := h.pageArtifacts.Put(ctx, ws, artifact)
	if err != nil {
		t.Fatal(err)
	}
	orphanSource := projectTestSource()
	orphanSource.Files[3].Content = "// failed CAS candidate"
	orphanDigest, err := h.projectStore.Put(ctx, ws, orphanSource)
	if err != nil {
		t.Fatal(err)
	}
	orphanCommit, err := h.projectStore.Checkpoint(ctx, ws, page, commit, spec, "test", 2, orphanSource)
	if err != nil {
		t.Fatal(err)
	}
	foreignDigest, err := h.projectStore.Put(ctx, "foreign", orphanSource)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := h.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Reuse content across 80 revisions: retention is about SQL reachability, not
	// how frequently a particular blob happens to be referenced.
	for i := 2; i <= 80; i++ {
		exec(`INSERT INTO page_project_revisions(page_id,revision,source_digest,git_commit,spec_json,created_at) VALUES(?,?,?,?,?,'2026-09-09')`, page, i, digest, commit, spec)
	}
	exec(`UPDATE page_project_drafts SET revision=80 WHERE page_id=?`, page)
	for i := 1; i <= 80; i++ {
		exec(`INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,artifact_digest,created_at) VALUES(?,?,?,?,'ready',?,?)`, fmt.Sprintf("build-%03d", i), page, i, digest, liveArtifact, fmt.Sprintf("2026-09-09T00:00:%03d", i))
	}
	exec(`UPDATE page_project_builds SET state='running',artifact_digest=NULL WHERE id='build-002'`)
	for i := 1; i <= 40; i++ {
		exec(`INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,created_at) VALUES(?,?,?,?,?,?,?,?,'{}','2026-09-09')`, page, i, fmt.Sprintf("build-%03d", i), i, digest, commit, liveArtifact, spec)
	}
	// An older live receipt survives the recent-publication window.
	exec(`INSERT INTO page_project_live(page_id,version,published) VALUES(?,1,1)`, page)
	exec(`INSERT INTO page_project_withdrawals(page_id,version,created_at) VALUES(?,3,'2026-09-09')`, page)
	if err := h.RetainPageProjects(ctx, ws); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int{1, 2, 9, 80} {
		var count int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_project_revisions WHERE page_id=? AND revision=?`, page, revision).Scan(&count); err != nil || count != 1 {
			t.Fatalf("lost protected revision %d: %v", revision, err)
		}
	}
	var old int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_project_revisions WHERE page_id=? AND revision=3`, page).Scan(&old); err != nil || old != 0 {
		t.Fatalf("old revision retained: %d %v", old, err)
	}
	for _, table := range []string{"page_project_publications", "page_project_withdrawals"} {
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE page_id=? AND version=3`, page).Scan(&old); err != nil || old != 0 {
			t.Fatalf("old receipt retained in %s: %v", table, err)
		}
	}
	if _, err := h.projectStore.Get(ctx, ws, orphanDigest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan source survives: %v", err)
	}
	if _, err := h.pageArtifacts.Get(ctx, ws, orphanArtifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan artifact survives: %v", err)
	}
	if _, _, err := h.projectStore.ReadCheckpoint(ctx, ws, page, orphanCommit); err == nil {
		t.Fatal("orphan Git candidate survives")
	}
	if _, _, err := h.projectStore.ReadCheckpoint(ctx, ws, page, commit); err != nil {
		t.Fatal("live Git lost", err)
	}
	if _, err := h.pageArtifacts.Get(ctx, ws, liveArtifact); err != nil {
		t.Fatal("live artifact lost", err)
	}
	if _, err := h.projectStore.Get(ctx, "foreign", foreignDigest); err != nil {
		t.Fatal("foreign workspace changed", err)
	}
	if err := h.RetainPageProjects(ctx, ws); err != nil {
		t.Fatal("non-idempotent maintenance", err)
	}
}

func TestPageWorkspaceBuildQuotaRecoversAfterRetention(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	h.SetBuildWorker(&testPageBuilder{}, &pagebuild.Store{Directory: t.TempDir()})
	for pageN := 0; pageN < 8; pageN++ {
		slug := fmt.Sprintf("quota-%d", pageN)
		if pageN == 0 {
			slug = "health"
		}
		pagesCreate(t, h, ws, user, slug)
		if w := projectPut(t, h, ws, user, "OWNER", slug, 0, projectTestSource()); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var page, digest string
		if err := h.db.QueryRow(`SELECT d.page_id,d.source_digest FROM page_project_drafts d JOIN pages p ON p.id=d.page_id WHERE p.slug=?`, slug).Scan(&page, &digest); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 64; i++ {
			if _, err := h.db.Exec(`INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,created_at) VALUES(?,?,1,?,'failed','2026-09-09')`, fmt.Sprintf("quota-build-%d-%d", pageN, i), page, digest); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Admission must reclaim history itself, without waiting for hourly maintenance.
	w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{ "expected_revision":1 }`)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_project_builds`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count >= 512 || w.Code != 202 {
		t.Fatalf("count=%d status=%d %s", count, w.Code, w.Body.String())
	}
	t.Logf("after retention: %d failed builds remain, new build HTTP %d: %s", count, w.Code, w.Body.String())
}
