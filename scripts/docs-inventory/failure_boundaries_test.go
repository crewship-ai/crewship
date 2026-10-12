package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryFailuresNameTheUnavailableEvidenceOrOutput(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"missing-openapi", "read "}, {"invalid-openapi", "decode "}, {"missing-commands", "read command manifest"}, {"invalid-commands", "decode command manifest"},
		{"missing-docs", "read docs"}, {"missing-source-root", "read Go source root web"}, {"unreadable-doc", "read docs"}, {"unreadable-source", "read Go source root internal"},
		{"invalid-cli-source", "parse"}, {"invalid-exemptions", "expected"}, {"invalid-baseline", "expected"}, {"invalid-operation", "decode OpenAPI operation"},
		{"missing-kinds", "no manifest kinds"}, {"report-parent", "create report directory"}, {"json-output", "write " + jsonReport}, {"markdown-output", "write " + markdownReport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := inventoryFixture(t)
			t.Chdir(root)
			remove := func(name string) {
				t.Helper()
				if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.name {
			case "missing-openapi":
				remove(openAPIPath)
			case "invalid-openapi":
				writeInventoryFixture(t, root, openAPIPath, "not JSON")
			case "missing-commands":
				remove("commands.json")
			case "invalid-commands":
				writeInventoryFixture(t, root, "commands.json", "not JSON")
			case "missing-docs":
				remove("docs")
			case "missing-source-root":
				remove("web")
			case "unreadable-doc":
				if err := os.Symlink("absent", filepath.Join(root, "docs/broken.md")); err != nil {
					t.Fatal(err)
				}
			case "unreadable-source":
				if err := os.Symlink("absent", filepath.Join(root, "internal/broken.go")); err != nil {
					t.Fatal(err)
				}
			case "invalid-cli-source":
				writeInventoryFixture(t, root, "cmd/crewship/cmd_widgets.go", "not Go")
			case "invalid-exemptions":
				writeInventoryFixture(t, root, cliParityExemptionsPath, "no explanation\n")
			case "invalid-baseline":
				writeInventoryFixture(t, root, cliFlagSectionBaselinePath, "no flag\n")
			case "invalid-operation":
				writeInventoryFixture(t, root, openAPIPath, `{"paths":{"/api/v1/widgets":{"get":"not an operation"}}}`)
			case "missing-kinds":
				writeInventoryFixture(t, root, manifestKindSourcePath, "package manifest\n")
			case "report-parent":
				writeInventoryFixture(t, root, "docs/prd", "a file blocks the report directory")
			case "json-output":
				if err := os.MkdirAll(filepath.Join(root, jsonReport), 0755); err != nil {
					t.Fatal(err)
				}
			case "markdown-output":
				if err := os.MkdirAll(filepath.Join(root, markdownReport), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := run(openAPIPath, "commands.json", false); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s failure hidden or misattributed: %v", tc.name, err)
			}
		})
	}
}

func TestInventoryCommandFlagsDriveStrictReportGeneration(t *testing.T) {
	root := inventoryFixture(t)
	t.Chdir(root)
	oldArgs, oldFlags := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFlags }()
	os.Args = []string{"docs-inventory", "-commands", "commands.json", "-strict"}
	flag.CommandLine = flag.NewFlagSet("inventory-fixture", flag.ContinueOnError)
	main()
	if _, err := os.Stat(jsonReport); err != nil {
		t.Fatal("command did not generate report", err)
	}
}

func TestInventoryCommandDiscoveryReportsCompilerFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := readCommands(""); err == nil || !strings.Contains(err.Error(), "run CLI command manifest") {
		t.Fatalf("failed command discovery treated as empty inventory: %v", err)
	}
}

func TestEndpointEvidenceCombinesSharedContractsAndTableRows(t *testing.T) {
	docs := []docFile{{Path: "docs/api-reference/widgets.mdx", Text: "Authentication applies to this page.\n\n# Operations\n\n| Method | Path |\n| GET | `/api/v1/widgets?limit=2` |\nResponse: array\nStatuses: 200\n\n## Shared contract\nRequest headers: authorization\n\n## Unrelated\nAn unrelated paragraph.\n"}}
	evidence := inventoryEndpointEvidence(docs)
	rows := evidence["GET /api/v1/widgets"]
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "Authentication applies") || !strings.Contains(rows[0].Text, "Request headers") {
		t.Fatalf("missing shared evidence: %+v", rows)
	}
	if got := contractFor("GET", "/api/v1/widgets", evidence, "source", nil, true); len(got.Structural.Missing) != 0 {
		t.Fatalf("complete table contract considered missing: %+v", got)
	}
	for _, line := range []string{"not a table", "| only one field", "| INVALID | /api/v1/widgets |", "| GET | no path |"} {
		if _, _, ok := endpointTableRow(line); ok {
			t.Fatalf("non-endpoint table row accepted: %q", line)
		}
	}
}
