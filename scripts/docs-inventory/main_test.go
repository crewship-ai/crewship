package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// The manifest kind roster used to be read out of the parser's ERROR MESSAGE:
// two hand-typed copies of the same twenty names lived in parse.go, and this
// tool scraped one of them. #1935 replaced both with a single const built from
// the Kind* declarations — a strictly better change that silently blinded this
// gate, because the literal it was scraping stopped existing. The roster came
// back empty, and 225 correct documentation references were reported as
// "missing from the source inventory".
//
// Two things are pinned here. The roster is read from the DECLARATION, which
// cannot be reworded; and an empty roster is a TOOLING failure that says so,
// rather than a documentation verdict that blames the docs.
func TestManifestKindsComeFromTheDeclarationNotAnErrorMessage(t *testing.T) {
	sources := []docFile{{
		Path: manifestKindSourcePath,
		Text: "const (\n\tKindCrew  = \"Crew\"\n\tKindAgent = \"Agent\"\n\tKindPage  = \"Page\"\n)\n",
	}}
	docs := []docFile{{Path: "docs/guides/x.mdx", Text: "kind: Crew\nkind: Agent\nkind: Page\n"}}

	got, err := inventoryManifestKinds(sources, docs)
	if err != nil {
		t.Fatalf("inventoryManifestKinds: %v", err)
	}
	var names []string
	for _, rec := range got {
		names = append(names, rec.Name)
	}
	want := []string{"Agent", "Crew", "Page"}
	if len(names) != len(want) {
		t.Fatalf("kinds: got %v want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("kinds: got %v want %v", names, want)
		}
	}
}

func TestManifestKindsRefuseToReportAnEmptyRosterAsADocsGap(t *testing.T) {
	// The declaration file present but unrecognisable — a rename, a refactor,
	// a move. The honest answer is "I could not find the roster", not "none of
	// your kinds are documented".
	sources := []docFile{{Path: manifestKindSourcePath, Text: "package manifest\n"}}

	_, err := inventoryManifestKinds(sources, nil)
	if err == nil {
		t.Fatal("an empty roster must be an error, not a silent zero")
	}
	if !strings.Contains(err.Error(), manifestKindSourcePath) {
		t.Errorf("the error must name where the roster was looked for, got: %v", err)
	}
}

// openapiReferencePage is the one published page that quotes the spec's
// schema-quality figures in prose.
const openapiReferencePage = "docs/api-reference/openapi.mdx"

// openapiStatsSentence locates the wording that page carries. Keeping the shape
// in one regexp is what makes the numbers checkable at all: prose is where they
// went stale, because nothing read prose.
//
// No capture groups: the comparison is against the whole rendered sentence, not
// number by number, and eight unused groups would advertise an extraction that
// does not happen. `\s+` rather than literal spaces so that hard-wrapping the
// paragraph — which this tree does elsewhere — reflows the prose without
// reddening the gate.
var openapiStatsSentence = regexp.MustCompile(
	`\d+\s+of\s+\d+\s+operations\s+return\s+a\s+named\s+2xx\s+schema,\s+\d+\s+fall\s+back\s+to\s+an\s+unconstrained\s+` + "`object`" +
		`,\s+and\s+\d+\s+have\s+no\s+success\s+body\s+at\s+all\.\s+\d+\s+operations\s+carry\s+a\s+request\s+body:\s+\d+\s+with\s+a\s+named\s+JSON\s+schema,\s+\d+\s+with\s+a\s+non-JSON\s+media\s+type,\s+and\s+\d+\s+with\s+a\s+generic\s+JSON\s+fallback\.`)

// whitespaceRun collapses the page's line breaks and indentation before the
// comparison, so the assertion is about the words and numbers rather than about
// where the paragraph happens to wrap.
var whitespaceRun = regexp.MustCompile(`\s+`)

// The figures in docs/api-reference/openapi.mdx were 52 operations out of date
// — "513 of 536 … 184 request bodies" against a spec that had reached 588 —
// and the generated report sitting in the same tree carried the right ones. Two
// numbers for one fact, and the wrong one was the published one, because the
// published one was typed by a human and nothing re-read it (#2086).
//
// This is that missing reader. When the spec moves, it fails with the sentence
// to paste, so the fix is mechanical rather than another hand count.
func TestOpenAPIReferenceQuotesTheCurrentSpec(t *testing.T) {
	doc, err := readOpenAPI(filepath.Join("..", "..", filepath.FromSlash(openAPIPath)))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := schemaStats(doc)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Operations == 0 {
		t.Fatalf("%s parsed to zero operations; the spec was not read", openAPIPath)
	}

	want := fmt.Sprintf(
		"%d of %d operations return a named 2xx schema, %d fall back to an unconstrained `object`, and %d have no success body at all. "+
			"%d operations carry a request body: %d with a named JSON schema, %d with a non-JSON media type, and %d with a generic JSON fallback.",
		stats.NamedResponses, stats.Operations, stats.GenericResponses, stats.NoSuccessBody,
		stats.RequestBodies, stats.ConcreteJSONRequests, stats.NonJSONRequestBodies, stats.GenericJSONRequests)

	body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(openapiReferencePage)))
	if err != nil {
		t.Fatal(err)
	}
	// Every occurrence, not the first. FindString would let a page carrying a
	// correct sentence followed by a stale duplicate pass on the strength of the
	// correct one — the published figure would still be wrong on screen.
	found := openapiStatsSentence.FindAllString(whitespaceRun.ReplaceAllString(string(body), " "), -1)
	if len(found) == 0 {
		t.Fatalf("%s no longer carries the schema-quality sentence in the shape this test reads.\nEither restore it or update openapiStatsSentence. It should read:\n\n  %s", openapiReferencePage, want)
	}
	if len(found) > 1 {
		t.Errorf("%s states the schema-quality figures %d times; one page should answer this once.\n  %s", openapiReferencePage, len(found), strings.Join(found, "\n  "))
	}
	for _, got := range found {
		if got != want {
			t.Errorf("%s quotes figures the generated spec does not support.\n  page: %s\n  spec: %s\nReplace the sentence in the page with the second line.", openapiReferencePage, got, want)
		}
	}
}
