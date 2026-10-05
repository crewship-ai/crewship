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
		`INSERT INTO agents VALUES('crew-1','CODEX_CLI',NULL)`,
		`CREATE TABLE environment_revisions(id TEXT,workspace_id TEXT,crew_id TEXT,image_id TEXT,build_hash TEXT,definition_hash TEXT,toolchain_json TEXT,created_at TEXT)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	mise := devcontainer.MiseConfig{Tools: map[string]string{"codex": "0.160.0"}, Lock: &devcontainer.MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "lockfile_version = 3\n[[tools.codex]]\nversion = \"0.160.0\"\n"}}}
	miseRaw, _ := json.Marshal(mise)
	image := "sha256:" + strings.Repeat("a", 64)
	inventory := devcontainer.ToolchainInventory{SchemaVersion: 1, Status: "recorded", ImageID: image, Tools: []devcontainer.ToolchainTool{{Binary: "codex", Version: "0.160.0", Status: "observed", Path: "/opt/native/codex", ManagedPath: "/opt/native/codex", ManagedVersion: "0.160.0", LaunchArtifact: &managedlaunch.Artifact{Path: "/opt/native/codex", SHA256: strings.Repeat("b", 64), Format: "static_elf"}}}, Qualification: &devcontainer.ToolchainQualification{Status: "passed", ImageID: image, Tools: []devcontainer.ToolchainProbe{{Binary: "codex", Status: "passed"}}}}
	inventoryRaw, _ := json.Marshal(inventory)
	requirements, _ := json.Marshal(devcontainer.AggregatedRequirements{Toolchain: &inventory})
	if _, err := db.Exec(`UPDATE crews SET mise_config=?,cached_requirements=?,config_hash='build-1'`, string(miseRaw), string(requirements)); err != nil {
		t.Fatal(err)
	}
	definition := api.EnvironmentDefinitionHash(database.EffectiveCrewDevcontainerConfig("", false), string(miseRaw), "", []string{"CODEX_CLI"})
	if _, err := db.Exec(`INSERT INTO environment_revisions VALUES('r1','ws-1','crew-1',?,'build-1',?,?, 'now')`, image, definition, string(inventoryRaw)); err != nil {
		t.Fatal(err)
	}
	resolve := managedLaunchResolver(db)
	d, err := resolve(context.Background(), "ws-1", "crew-1", "CODEX_CLI")
	if err != nil || d.Version != "0.160.0" || d.RevisionID != "r1" || d.ImageID != image {
		t.Fatalf("stored revision=%+v %v", d, err)
	}
	if _, err := resolve(context.Background(), "other-workspace", "crew-1", "CODEX_CLI"); err == nil {
		t.Fatal("cross-tenant authority accepted")
	}
	if _, err := resolve(context.Background(), "ws-1", "crew-1", "GEMINI_CLI"); err == nil {
		t.Fatal("unsupported adapter accepted")
	}

	for _, version := range []string{"", "0.159.0"} {
		inventory.Tools[0].ManagedVersion = version
		bad, _ := json.Marshal(inventory)
		if _, err := db.Exec(`UPDATE environment_revisions SET toolchain_json=?`, string(bad)); err != nil {
			t.Fatal(err)
		}
		if _, err := resolve(context.Background(), "ws-1", "crew-1", "CODEX_CLI"); err == nil {
			t.Fatal("missing/mismatched independent native version accepted")
		}
	}
	if _, err := db.Exec(`UPDATE environment_revisions SET toolchain_json=?`, string(inventoryRaw)); err != nil {
		t.Fatal(err)
	}
	// Same exact selector, different material: old build must not authorize it.
	mise.Lock.Files["mise.lock"] = "lockfile_version = 3\n[[tools.codex]]\nversion = \"0.159.0\"\n"
	changed, _ := json.Marshal(mise)
	if _, err := db.Exec(`UPDATE crews SET mise_config=?`, string(changed)); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(context.Background(), "ws-1", "crew-1", "CODEX_CLI"); err == nil {
		t.Fatal("changed lock reused old revision")
	}
	// Bind the revision to the new definition as well: a lock digest and exact
	// selector alone do not establish what version the native lock selected.
	for _, nativeLock := range []string{
		"lockfile_version = 3\n[[tools.codex]]\nversion = \"0.159.0\"\n",
		"opaque native lock",
		"lockfile_version = 3\n[[tools.node]]\nversion = \"22.0.0\"\n",
	} {
		mise.Lock.Files["mise.lock"] = nativeLock
		current, _ := json.Marshal(mise)
		definition = api.EnvironmentDefinitionHash(database.EffectiveCrewDevcontainerConfig("", false), string(current), "", []string{"CODEX_CLI"})
		if _, err := db.Exec(`UPDATE crews SET mise_config=?`, string(current)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE environment_revisions SET definition_hash=?`, definition); err != nil {
			t.Fatal(err)
		}
		if d, err := resolve(context.Background(), "ws-1", "crew-1", "CODEX_CLI"); err == nil {
			t.Fatalf("matching revision admitted pin/lock disagreement: descriptor=%+v lock=%q", d, nativeLock)
		}
	}

}
