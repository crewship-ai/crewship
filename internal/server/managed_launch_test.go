package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
)

func TestManagedLaunchResolverBindsStoredRevisionAndCurrentLock(t *testing.T) {
	db, close := newTestDB(t)
	defer close()
	db.SetMaxOpenConns(1)
	for _, query := range []string{
		`ALTER TABLE crews ADD COLUMN deleted_at TEXT`, `ALTER TABLE crews ADD COLUMN mise_config TEXT`,
		`ALTER TABLE crews ADD COLUMN cached_requirements TEXT`, `ALTER TABLE crews ADD COLUMN config_hash TEXT`,
		`ALTER TABLE crews ADD COLUMN runtime_image TEXT`, `ALTER TABLE crews ADD COLUMN devcontainer_config TEXT`,
		`CREATE TABLE agents (crew_id TEXT,cli_adapter TEXT,deleted_at TEXT)`,
		`INSERT INTO agents VALUES('crew-1','CLAUDE_CODE',NULL)`,
		`CREATE TABLE environment_revisions(id TEXT,workspace_id TEXT,crew_id TEXT,image_id TEXT,build_hash TEXT,definition_hash TEXT,toolchain_json TEXT,created_at TEXT)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	mise := devcontainer.MiseConfig{Tools: map[string]string{"claude": "2.1.288"}, Lock: &devcontainer.MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "fixture native lock v1"}}}
	miseRaw, _ := json.Marshal(mise)
	image := "sha256:" + strings.Repeat("a", 64)
	inventory := devcontainer.ToolchainInventory{SchemaVersion: 1, Status: "recorded", ImageID: image, Tools: []devcontainer.ToolchainTool{{Binary: "claude", Version: "2.1.288", Status: "observed", Path: "/opt/native/claude", LaunchArtifact: &managedlaunch.Artifact{Path: "/opt/native/claude", SHA256: strings.Repeat("b", 64), Format: "static_elf"}}}, Qualification: &devcontainer.ToolchainQualification{Status: "passed", ImageID: image, Tools: []devcontainer.ToolchainProbe{{Binary: "claude", Status: "passed"}}}}
	inventoryRaw, _ := json.Marshal(inventory)
	requirements, _ := json.Marshal(devcontainer.AggregatedRequirements{Toolchain: &inventory})
	if _, err := db.Exec(`UPDATE crews SET mise_config=?,cached_requirements=?,config_hash='build-1'`, string(miseRaw), string(requirements)); err != nil {
		t.Fatal(err)
	}
	definition := api.EnvironmentDefinitionHash(database.EffectiveCrewDevcontainerConfig("", false), string(miseRaw), "", []string{"CLAUDE_CODE"})
	if _, err := db.Exec(`INSERT INTO environment_revisions VALUES('r1','ws-1','crew-1',?,'build-1',?,?, 'now')`, image, definition, string(inventoryRaw)); err != nil {
		t.Fatal(err)
	}
	resolve := managedLaunchResolver(db)
	d, err := resolve(context.Background(), "ws-1", "crew-1", "CLAUDE_CODE")
	if err != nil || d.Version != "2.1.288" || d.RevisionID != "r1" || d.ImageID != image {
		t.Fatalf("stored revision=%+v %v", d, err)
	}
	if _, err := resolve(context.Background(), "other-workspace", "crew-1", "CLAUDE_CODE"); err == nil {
		t.Fatal("cross-tenant authority accepted")
	}
	if _, err := resolve(context.Background(), "ws-1", "crew-1", "GEMINI_CLI"); err == nil {
		t.Fatal("unsupported adapter accepted")
	}
	// Same exact selector, different material: old build must not authorize it.
	mise.Lock.Files["mise.lock"] = "fixture native lock v2"
	changed, _ := json.Marshal(mise)
	if _, err := db.Exec(`UPDATE crews SET mise_config=?`, string(changed)); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(context.Background(), "ws-1", "crew-1", "CLAUDE_CODE"); err == nil {
		t.Fatal("changed lock reused old revision")
	}
}
