package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInventoryFixture(t *testing.T, root, name, body string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func inventoryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"internal", "cmd", "scripts", "tools", "web", "packaging", "docs"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeInventoryFixture(t, root, openAPIPath, `{"paths":{"/api/v1/widgets":{"parameters":[],"get":{"operationId":"listWidgets","tags":["widgets"],"responses":{"200":{"content":{"application/json":{"schema":{"type":"array","items":{"type":"string"}}}}}}}}}}`)
	writeInventoryFixture(t, root, "commands.json", `{"commands":[{"path":"widgets","use":"widgets","commands":[{"path":"widgets list","use":"list","flags":[{"name":"limit"}]}]}]}`)
	writeInventoryFixture(t, root, manifestKindSourcePath, "package manifest\nconst (\n KindCrew = \"Crew\"\n)\n")
	writeInventoryFixture(t, root, "cmd/crewship/cmd_widgets.go", "package main\nfunc listWidgets() { client.Get(\"/api/v1/widgets\") }\n")
	writeInventoryFixture(t, root, "internal/api/widgets.go", "package api\nvar route = \"/api/v1/widgets\"\nvar config = \"CREWSHIP_FIXTURE\"\n")
	writeInventoryFixture(t, root, "cmd/crewship/cmd_widgets_test.go", "package main\nvar cases = []string{\"widgets\",\"widgets list\",\"/api/v1/widgets\"}\n")
	writeInventoryFixture(t, root, "internal/api/widgets_test.go", "package api\nvar target = \"/api/v1/widgets\"\n")
	writeInventoryFixture(t, root, "docs/api-reference/widgets.mdx", "Authentication: session\n\n## GET /api/v1/widgets\nResponse: JSON array.\nStatuses: 200, 401.\n")
	writeInventoryFixture(t, root, "docs/cli/widgets.mdx", "# widgets\n\nRun `crewship widgets`.\n\n## widgets list\n\nRun `crewship widgets list --limit 10`. The `--limit` flag bounds the list.\n")
	writeInventoryFixture(t, root, "docs/guides/settings.mdx", "# Settings\n\nCREWSHIP_FIXTURE controls the owned fixture.\n\n```yaml\nkind: Crew\n```\n")
	return root
}

func TestInventoryBuildsDeterministicEvidenceAndPassesCompleteFixture(t *testing.T) {
	root := inventoryFixture(t)
	t.Chdir(root)
	if err := run(openAPIPath, "commands.json", true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(jsonReport)
	if err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.API) != 1 || len(got.CLI) != 2 || len(got.Env) != 1 || len(got.Manifest) != 1 {
		t.Fatalf("inventory omitted surface: %+v", got.Summary)
	}
	api := got.API[0]
	if api.Method != "GET" || api.Path != "/api/v1/widgets" || api.Status != "documented_exact" || api.CLIParity != cliParityCovered || len(api.CLICallers) != 1 || len(api.Contract.Structural.Missing) != 0 || len(api.TestSignals) != 2 || api.SourceFile == "" {
		t.Fatalf("wrong API evidence: %+v", api)
	}
	if got.Env[0].Name != "CREWSHIP_FIXTURE" || got.Env[0].Status != "documented" || got.Manifest[0].Name != "Crew" || got.Manifest[0].Status != "documented" {
		t.Fatalf("non-API surfaces: %+v %+v", got.Env, got.Manifest)
	}
	markdownBefore, err := os.ReadFile(markdownReport)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(markdownBefore, []byte("API operations needing attention")) || !bytes.Contains(markdownBefore, []byte("CLI parity: 1 API paths")) {
		t.Fatalf("missing readable report: %s", markdownBefore)
	}
	if err := run(openAPIPath, "commands.json", true); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(jsonReport)
	if err != nil {
		t.Fatal(err)
	}
	markdownAfter, err := os.ReadFile(markdownReport)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) || !bytes.Equal(markdownBefore, markdownAfter) {
		t.Fatal("identical inputs produced different reports")
	}
}

func TestInventoryPublishesEvidenceBeforeStrictFailure(t *testing.T) {
	root := inventoryFixture(t)
	t.Chdir(root)
	writeInventoryFixture(t, root, openAPIPath, `{"paths":{"/api/v1/undocumented":{"delete":{"operationId":"deleteUnknown","responses":{"204":{}}}},"/api/v1/gadgets":{"get":{"tags":["gadgets"],"responses":{"200":{}}}},"/api/v1/widgets":{"get":{"responses":{"200":{}}},"post":{"requestBody":{"content":{"application/json":{"schema":{"type":"object"}}}},"responses":{"200":{}}}}}}`)
	writeInventoryFixture(t, root, "docs/api-reference/gadgets.mdx", "# Gadgets\nA general description with no exact route mention.\n")
	writeInventoryFixture(t, root, "commands.json", `{"commands":[{"path":"","use":"empty"},{"path":"widgets","commands":[{"path":"widgets extra","flags":[{"name":"missing"}]}]},{"path":"orphan","use":"orphan"},{"path":"naked","use":"naked"}]}`)
	writeInventoryFixture(t, root, "docs/cli/other.mdx", "Run `crewship naked`.\n")
	writeInventoryFixture(t, root, "scripts/install.sh", "CREWSHIP_UNDOCUMENTED=yes\n")
	writeInventoryFixture(t, root, cliParityExemptionsPath, "/api/v1/widgets stale covered route\n/api/v1/gadgets legacy fixture exception\n/api/v1/removed obsolete route\n")
	writeInventoryFixture(t, root, cliFlagSectionBaselinePath, "widgets --old-flag\n")
	if err := run(openAPIPath, "commands.json", false); err != nil {
		t.Fatal(err)
	}
	if err := run(openAPIPath, "commands.json", true); err == nil {
		t.Fatal("strict mode accepted deliberate gaps")
	}
	raw, err := os.ReadFile(jsonReport)
	if err != nil {
		t.Fatal("strict failure lost report", err)
	}
	var got report
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, row := range got.API {
		statuses[row.Path] = row.Status
	}
	if statuses["/api/v1/undocumented"] != "missing_docs" || statuses["/api/v1/gadgets"] != "documented_resource" || statuses["/api/v1/widgets"] != "documented_exact" {
		t.Fatalf("documentation strengths conflated: %v", statuses)
	}
	cli := map[string]string{}
	for _, row := range got.CLI {
		cli[row.Path] = row.Status
	}
	if cli["widgets extra"] != "documented_root" || cli["orphan"] != "missing_root_docs" || cli["naked"] != "documented_exact_no_root" {
		t.Fatalf("CLI evidence conflated: %v", cli)
	}
	md, err := os.ReadFile(markdownReport)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "/api/v1/undocumented") || !strings.Contains(string(md), "missing flags: missing") {
		t.Fatalf("gaps missing from readable report: %s", md)
	}
}
