package bundled_test

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/skills"
	"github.com/crewship-ai/crewship/internal/skills/bundled"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestArtifactPackageSkillRelease(t *testing.T) {
	body, err := fs.ReadFile(bundled.FS(), "crewship/crewship-artifacts/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := skills.ParseSKILLMD(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Meta.Version != "1.1.0" || parsed.Meta.Category != "CODING" {
		t.Fatalf("actual parsed metadata: %+v", parsed.Meta)
	}
	for _, instruction := range []string{"## Project", "## Dashboard", "## Slides", "artifact_data_batch", "ifVersion", "data-row", "artifact_slides_read", "artifact_export_pdf", "Go Regular", "base_version"} {
		if !strings.Contains(parsed.Content, instruction) {
			t.Errorf("missing agent instruction %q", instruction)
		}
	}
}

func TestArtifactPackageUpgradePreservesAssignmentAndContent(t *testing.T) {
	// VersionStore checks real FULL durability; the generic migrated fixture
	// intentionally uses NORMAL, so open its migrated file with production defaults.
	fixture := testutil.MigratedDB(t)
	durable, err := database.Open(fixture.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = durable.Close() })
	db := durable.DB
	ctx := t.Context()
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	if err := bundled.Install(ctx, db, logger); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM skills WHERE vendor='crewship' AND slug='crewship-artifacts'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('package-owner','package@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('package-ws','Package','package-ws')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('package-crew','package-ws','Package','package-crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('package-agent','package-ws','package-crew','Package','package-agent')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('package-page','package-ws','package-page','Content','package-owner','{}')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO agent_skills(id,agent_id,skill_id,enabled) VALUES('package-assignment','package-agent',?,1)`, id); err != nil {
		t.Fatal(err)
	}
	authorize := func(context.Context, *sql.Tx) (string, error) { return "package-owner", nil }
	store := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true})
	key := artifacts.VersionKey{WorkspaceID: "package-ws", PageID: "package-page"}
	const content = `<h1 id="authored-title">Original instance content</h1>`
	if _, err := store.Apply(ctx, key, artifacts.VersionRequest{EntryPoint: "index.html", Files: map[string]artifacts.VersionFile{"index.html": {Content: []byte(content), MediaType: "text/html"}}}, authorize); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE skills SET version='1.0.0',content='Old project-only instructions' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := bundled.Install(ctx, db, logger); err != nil {
		t.Fatal(err)
	}
	var actualID, version, category, body string
	if err := db.QueryRow(`SELECT s.id,s.version,s.category,s.content FROM agent_skills a JOIN skills s ON s.id=a.skill_id WHERE a.id='package-assignment' AND a.enabled=1`).Scan(&actualID, &version, &category, &body); err != nil {
		t.Fatal(err)
	}
	if actualID != id || version != "1.1.0" || category != "CODING" || !strings.Contains(body, "## Dashboard") {
		t.Fatalf("upgrade lost identity or new instructions: %s %s %s", actualID, version, category)
	}
	state, err := store.Read(ctx, key, authorize)
	if err != nil || state.LatestVersion != 1 || string(state.Manifest.Files["index.html"].Content) != content {
		t.Fatalf("package upgrade changed instance content: %+v %v", state, err)
	}
	_, err = skills.NewImporter(db, logger).Import(ctx, "package-ws", "package-owner", skills.ImportRequest{Content: "---\nname: crewship-artifacts\n---\nReplace the official package"})
	if err == nil || !strings.Contains(err.Error(), "BUNDLED") {
		t.Fatalf("instance could overwrite package instructions: %v", err)
	}
}
