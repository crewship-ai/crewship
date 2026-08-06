package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestResponseSchemaQuality(t *testing.T) {
	response := func(schema string) openAPIResponse {
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
			t.Fatal(err)
		}
		return openAPIResponse{Content: map[string]openAPIMediaType{
			"application/json": {Schema: parsed},
		}}
	}

	tests := []struct {
		name     string
		response openAPIResponse
		concrete bool
	}{
		{name: "generic object", response: response(`{"type":"object"}`), concrete: false},
		{name: "array", response: response(`{"type":"array","items":{"type":"string"}}`), concrete: true},
		{name: "object properties", response: response(`{"type":"object","properties":{"id":{"type":"string"}}}`), concrete: true},
		{name: "component reference", response: response(`{"$ref":"#/components/schemas/Workspace"}`), concrete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responses := map[string]openAPIResponse{"200": tt.response}
			if got := hasConcreteSuccessSchema(responses); got != tt.concrete {
				t.Fatalf("hasConcreteSuccessSchema() = %v, want %v", got, tt.concrete)
			}
			if got := hasSuccessSchema(responses); !got {
				t.Fatal("hasSuccessSchema() = false, want true")
			}
		})
	}
}

func TestResponseSchemaQualityIgnoresErrors(t *testing.T) {
	responses := map[string]openAPIResponse{
		"400": {Content: map[string]openAPIMediaType{"application/json": {Schema: map[string]json.RawMessage{"type": json.RawMessage(`"object"`)}}}},
	}
	if hasSuccessSchema(responses) || hasConcreteSuccessSchema(responses) {
		t.Fatal("error responses must not count as success response schemas")
	}
}

func TestEndpointEvidenceStaysWithinOperationSection(t *testing.T) {
	lines := []string{
		"## Resource",
		"All routes require authentication.",
		"### List",
		"GET /api/v1/items",
		"**Response:** `200 OK`",
		"### Create",
		"POST /api/v1/items",
		"**Request:** JSON body.",
		"**Response:** `201 Created`",
	}
	list := endpointSection(lines, 3)
	if !strings.Contains(list, "200 OK") || strings.Contains(list, "201 Created") {
		t.Fatalf("list section escaped operation boundary: %q", list)
	}
	if !statusMarkerPresent(list) {
		t.Fatal("HTTP response status should count as status evidence")
	}
}

// A flag counts as documented only when the page mentions that flag, not when
// it mentions a LONGER flag that happens to start with the same characters.
//
// The pair below is real: `--server` and `--server-allow-mismatch` are both
// global flags on every crewship command, so a substring match reported the
// whole CLI as fully flag-documented while `--server` itself was absent from
// the page. A coverage number that cannot distinguish those two is not a
// coverage number.
func TestFlagEvidenceRejectsPrefixCollision(t *testing.T) {
	node := commandNode{Flags: []flagManifest{{Name: "server"}, {Name: "profile"}}}
	docs := []docFile{{
		Path: "docs/cli/token.mdx",
		Text: "Use `--server-allow-mismatch` when the host differs.\nPass `--profile` to pick a target.",
	}}

	documented, missing := cliFlagEvidence(node, []string{"docs/cli/token.mdx"}, nil, docs)

	if !slices.Contains(missing, "server") {
		t.Errorf("--server documented only via --server-allow-mismatch; got documented=%v missing=%v", documented, missing)
	}
	if !slices.Contains(documented, "profile") {
		t.Errorf("--profile is mentioned verbatim and must count; got documented=%v missing=%v", documented, missing)
	}
}

// The boundary must not be so strict that ordinary prose stops counting.
func TestFlagEvidenceAcceptsRealMentions(t *testing.T) {
	node := commandNode{Flags: []flagManifest{
		{Name: "server"}, {Name: "quiet"}, {Name: "output-file"}, {Name: "format"},
	}}
	docs := []docFile{{
		Path: "docs/cli/token.mdx",
		Text: "`--server=<url>` sets the host.\nEnd of line: --quiet\n" +
			"| `--output-file` | writes the token |\nUse --format json for scripting.",
	}}

	documented, missing := cliFlagEvidence(node, []string{"docs/cli/token.mdx"}, nil, docs)

	if len(missing) != 0 {
		t.Errorf("all four flags are mentioned verbatim; got documented=%v missing=%v", documented, missing)
	}
}

// -strict is the whole point of the audit: without it the published
// "531/531, 0 missing" is a snapshot that the next merge can invalidate
// silently. Each gate must fail, and must name the offending row.
func TestStrictGatesFailAndNameTheOffender(t *testing.T) {
	cases := []struct {
		name string
		r    report
		want string
	}{
		{
			name: "undocumented API operation",
			r:    report{API: []apiRecord{{Method: "GET", Path: "/api/v1/orphan", Status: "missing_docs"}}},
			want: "GET /api/v1/orphan",
		},
		{
			name: "missing contract evidence",
			r: report{API: []apiRecord{{Method: "POST", Path: "/api/v1/half", Status: "documented_exact",
				Contract: contractChecks{Structural: structuralChecks{Missing: []string{"auth", "statuses"}}}}}},
			want: "POST /api/v1/half",
		},
		{
			name: "generic response schema",
			r: report{API: []apiRecord{{Method: "GET", Path: "/api/v1/vague", Status: "documented_exact",
				GenericResponseSchema: true}}},
			want: "GET /api/v1/vague",
		},
		{
			name: "undocumented CLI flag",
			r: report{CLI: []cliRecord{{Path: "crewship token create", Status: "documented_exact",
				Flags: []string{"quiet", "output-file"}, DocumentedFlags: []string{"quiet"},
				MissingFlags: []string{"output-file"}}}},
			want: "--output-file",
		},
		{
			name: "undocumented environment variable",
			r:    report{Env: []surfaceRecord{{Name: "CREWSHIP_NEW_SETTING", Status: "missing_docs"}}},
			want: "CREWSHIP_NEW_SETTING",
		},
		{
			name: "undocumented manifest kind",
			r:    report{Manifest: []surfaceRecord{{Name: "NewKind", Status: "missing_docs"}}},
			want: "NewKind",
		},
		{
			name: "docs reference missing command",
			r:    report{Reverse: reverseChecks{MissingCommands: 1, Missing: []reverseRow{{Kind: "command", Symbol: "crewship vanished", Doc: "docs/cli/vanished.mdx", Line: 7}}}},
			want: "docs/cli/vanished.mdx:7: crewship vanished",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.r.Summary = summarize(tc.r)
			err := enforce(tc.r)
			if err == nil {
				t.Fatal("enforce() = nil; a documentation gap must fail the gate")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("enforce() error does not name the offender %q:\n%s", tc.want, err)
			}
		})
	}
}

func TestTokenMentionedRejectsSubstringCollision(t *testing.T) {
	if tokenMentioned("CREWSHIP_SERVER_NAME", "CREWSHIP_SERVER") {
		t.Fatal("a longer environment variable must not satisfy the shorter name")
	}
	if !tokenMentioned("set `CREWSHIP_SERVER` before starting", "CREWSHIP_SERVER") {
		t.Fatal("an exact token must count")
	}
}

func TestStrictGatesPassWhenClean(t *testing.T) {
	r := report{
		API: []apiRecord{{Method: "GET", Path: "/api/v1/items", Status: "documented_exact",
			ConcreteResponseSchema: true,
			Contract:               contractChecks{Structural: structuralChecks{CanonicalMethodPath: true, Auth: true, Request: true, Response: true, Statuses: true}}}},
		CLI: []cliRecord{{Path: "crewship items list", Status: "documented_exact", ExactDocs: []string{"docs/cli/items.mdx"}}},
	}
	r.Summary = summarize(r)
	if err := enforce(r); err != nil {
		t.Fatalf("enforce() = %v; a fully documented report must pass", err)
	}
}

func TestContractDoesNotRequireBodyForBodylessOperation(t *testing.T) {
	evidence := map[string][]endpointEvidence{
		"GET /api/v1/items": {{Text: "**Response:** `200 OK`"}},
	}
	checks := contractFor("GET", "/api/v1/items", evidence, "router.go", nil, false)
	for _, missing := range checks.Structural.Missing {
		if missing == "request" {
			t.Fatal("bodyless operation must not require a request-body marker")
		}
	}
}

func TestDocsToCodeNamesUnknownCommand(t *testing.T) {
	checks := inventoryDocsToCode(
		openAPIDocument{Paths: map[string]map[string]json.RawMessage{"/api/v1/items": {}}},
		commandManifest{Commands: []commandNode{{Path: "items", Flags: []flagManifest{{Name: "format"}}}}},
		[]docFile{{Path: "docs/guides/items.mdx", Text: "`crewship totally-made-up-commails --format json`\n"}},
		nil, nil,
	)
	if checks.MissingCommands != 1 || len(checks.Missing) != 1 {
		t.Fatalf("unknown command was not surfaced: %+v", checks)
	}
	if got := checks.missingRows("command")[0]; !strings.Contains(got, "docs/guides/items.mdx:1: crewship totally-made-up-commails") {
		t.Fatalf("missing-command row lacks page and line: %q", got)
	}
}

func TestDocsToCodeAcceptsCommandAliasesAndVariableAPIPaths(t *testing.T) {
	checks := inventoryDocsToCode(
		openAPIDocument{Paths: map[string]map[string]json.RawMessage{"/api/v1/items/{itemId}": {}}},
		commandManifest{Commands: []commandNode{{Path: "routine", Aliases: []string{"pipeline"}, Commands: []commandNode{{Path: "routine list"}}}}},
		[]docFile{{Path: "docs/guides/items.mdx", Text: "`crewship pipeline list`\nGET /api/v1/items/demo\n"}},
		nil, nil,
	)
	if checks.MissingCommands != 0 || checks.MissingAPIPaths != 0 {
		t.Fatalf("valid alias or variable API path was rejected: %+v", checks)
	}
}

func TestDocsToCodeExplicitIgnoreConvention(t *testing.T) {
	checks := inventoryDocsToCode(
		openAPIDocument{Paths: map[string]map[string]json.RawMessage{}},
		commandManifest{},
		[]docFile{{Path: "docs/architecture.mdx", Text: "<!-- docs-inventory: ignore --> `crewship illustrative-command` /api/v1/retired\n"}},
		nil, nil,
	)
	if len(checks.Missing) != 0 {
		t.Fatalf("explicit docs-inventory ignore marker did not suppress illustrative symbols: %+v", checks.Missing)
	}
}
