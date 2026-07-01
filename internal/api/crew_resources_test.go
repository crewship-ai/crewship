package api

import (
	"context"
	"strings"
	"testing"
)

func TestResolveCrewResources_Datastores(t *testing.T) {
	db := setupTestDB(t)
	uid := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, uid)

	services := `[
		{"name":"postgres","image":"postgres:16","ports":["5432/tcp"]},
		{"name":"redis","image":"redis:7-alpine","ports":["6379"]},
		{"name":"queue","image":"rabbitmq:3","ports":["5672"]}
	]`
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, services_json) VALUES (?,?,?,?,?)`,
		"crew-ds", wsID, "DS", "ds", services); err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	res, err := ResolveCrewResources(context.Background(), db, "crew-ds")
	if err != nil {
		t.Fatalf("ResolveCrewResources: %v", err)
	}
	if len(res.Datastores) != 3 {
		t.Fatalf("expected 3 datastores, got %d: %+v", len(res.Datastores), res.Datastores)
	}

	byName := map[string]DatastoreCap{}
	for _, d := range res.Datastores {
		byName[d.Name] = d
	}
	if d := byName["postgres"]; d.Type != "postgres" || d.Host != "postgres" || d.Port != "5432" {
		t.Errorf("postgres datastore wrong: %+v", d)
	}
	if d := byName["redis"]; d.Type != "redis" || d.Host != "redis" || d.Port != "6379" {
		t.Errorf("redis datastore wrong: %+v", d)
	}
	// Unknown image → type "other" but still surfaced with host/port.
	if d := byName["queue"]; d.Type != "other" || d.Host != "queue" || d.Port != "5672" {
		t.Errorf("queue datastore wrong: %+v", d)
	}
}

func TestResolveCrewResources_Tools(t *testing.T) {
	db := setupTestDB(t)
	uid := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, uid)

	devcontainer := `{
		"image":"mcr.microsoft.com/devcontainers/base:bookworm",
		"features":{
			"ghcr.io/devcontainers/features/terraform:1":{},
			"ghcr.io/devcontainers/features/kubectl-helm-minikube:1":{},
			"ghcr.io/devcontainers-extra/features/ansible:2":{},
			"ghcr.io/devcontainers/features/common-utils:2":{}
		}
	}`
	mise := `{"tools":{"node":"22","python":"3.12","golang":"1.22"}}`

	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config, mise_config) VALUES (?,?,?,?,?,?)`,
		"crew-tools", wsID, "Tools", "tools", devcontainer, mise); err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	res, err := ResolveCrewResources(context.Background(), db, "crew-tools")
	if err != nil {
		t.Fatalf("ResolveCrewResources: %v", err)
	}

	got := map[string]bool{}
	for _, tcap := range res.Tools {
		got[tcap.Type] = true
	}
	for _, want := range []string{"terraform", "kubectl", "ansible", "git", "node", "python", "go"} {
		if !got[want] {
			t.Errorf("expected tool %q in %+v", want, res.Tools)
		}
	}
	// "golang" mise alias should fold to "go", not appear as "golang".
	if got["golang"] {
		t.Errorf("golang alias not folded to go: %+v", res.Tools)
	}
}

func TestResolveCrewResources_EmptyAndMalformed(t *testing.T) {
	db := setupTestDB(t)
	uid := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, uid)

	// Crew with all configs NULL → empty, no error.
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?,?,?,?)`,
		"crew-empty", wsID, "Empty", "empty"); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	res, err := ResolveCrewResources(context.Background(), db, "crew-empty")
	if err != nil {
		t.Fatalf("ResolveCrewResources(empty): %v", err)
	}
	if len(res.Datastores) != 0 || len(res.Tools) != 0 {
		t.Errorf("expected empty resources, got %+v", res)
	}

	// Crew with malformed JSON in every column → empty, no error.
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, services_json, devcontainer_config, mise_config) VALUES (?,?,?,?,?,?,?)`,
		"crew-bad", wsID, "Bad", "bad", "{not json", "also[bad", "<<<"); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	res, err = ResolveCrewResources(context.Background(), db, "crew-bad")
	if err != nil {
		t.Fatalf("ResolveCrewResources(bad): %v", err)
	}
	if len(res.Datastores) != 0 || len(res.Tools) != 0 {
		t.Errorf("malformed config should yield empty resources, got %+v", res)
	}

	// Missing crew id → empty, no error.
	res, err = ResolveCrewResources(context.Background(), db, "does-not-exist")
	if err != nil {
		t.Fatalf("ResolveCrewResources(missing): %v", err)
	}
	if len(res.Datastores) != 0 || len(res.Tools) != 0 {
		t.Errorf("missing crew should yield empty resources, got %+v", res)
	}
}

func TestBuildContainerResourcesBlock(t *testing.T) {
	// nil / empty → omitted.
	if got := buildContainerResourcesBlock(nil); got != "" {
		t.Errorf("expected empty block for nil, got %q", got)
	}
	if got := buildContainerResourcesBlock(&CrewResources{}); got != "" {
		t.Errorf("expected empty block for empty resources, got %q", got)
	}

	res := &CrewResources{
		Datastores: []DatastoreCap{
			{Type: "postgres", Name: "postgres", Host: "postgres", Port: "5432"},
			{Type: "redis", Name: "redis", Host: "redis", Port: "6379"},
			{Type: "other", Name: "queue", Host: "queue", Port: ""},
		},
		Tools: []ToolCap{
			{Type: "ansible", Name: "ansible"},
			{Type: "kubectl", Name: "kubectl"},
			{Type: "git", Name: "git"},
		},
	}
	block := buildContainerResourcesBlock(res)
	for _, want := range []string{
		"[CONTAINER RESOURCES]",
		"Datastores:",
		"Postgres — host: postgres, port: 5432",
		"Redis — host: redis, port: 6379",
		"queue — host: queue", // "other" falls back to name, no port suffix
		"Tools (CLIs installed): ansible, kubectl, git",
		"[END CONTAINER RESOURCES]",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q\n---\n%s", want, block)
		}
	}
	// The "other" datastore with no port must not render a trailing ", port:".
	if strings.Contains(block, "queue — host: queue, port:") {
		t.Errorf("portless datastore rendered a port: %s", block)
	}

	// Tools-only crew (no datastores) → block renders but omits the
	// datastore-specific guidance line.
	toolsOnly := buildContainerResourcesBlock(&CrewResources{
		Tools: []ToolCap{{Type: "python", Name: "python"}},
	})
	if !strings.Contains(toolsOnly, "Tools (CLIs installed): python") {
		t.Errorf("tools-only block missing tools line: %s", toolsOnly)
	}
	if strings.Contains(toolsOnly, "Datastores:") {
		t.Errorf("tools-only block should not have a Datastores section: %s", toolsOnly)
	}
	if strings.Contains(toolsOnly, "resources.datastores") {
		t.Errorf("tools-only block should not have datastore guidance: %s", toolsOnly)
	}
}
