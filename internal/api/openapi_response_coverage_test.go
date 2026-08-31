package api

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// The pair table in openapi_response_shape_test.go is a good mechanism with one
// architectural weakness: it is OPT-IN. Nothing makes a new endpoint bring a
// pair with it, so the guarantee holds exactly where somebody remembered to
// ask for it — and a convention that has to be remembered is what produced the
// defect this all came out of.
//
// This gate turns it opt-out. Every named component a path uses as its 200
// response must either be graded by a pair, or carry a written reason why it
// cannot be. The count of ungraded components is a debt that may fall and may
// not rise: adding a route with a new, ungraded response component now fails
// here rather than being noticed a release later, or never.
//
// The reasons are as valuable as the pairs. "No backing struct" names the work
// that has to happen first (a DTO); "shape disagrees with the handler" names a
// documentation bug that a required list would only make louder.
var responseShapeExclusions = map[string]string{
	"RunRecordList": "shared by /runs and /run-records, which return different shapes — " +
		"marking RunRecord's fields required would fail /runs, a correct response reported as a violation. " +
		"/runs needs its own schema first.",
	"RunRecord": "same as RunRecordList — the component behind it.",
	"PipelineRun": "GetRun builds its body from a map[string]any literal (internal/api/pipeline_runs.go:135). " +
		"Needs a DTO before anything can be derived from it.",
	"PipelineRunList": "ListWorkspaceRuns builds rows and envelope from map[string]any literals " +
		"(internal/api/pipeline_runs.go:517). Needs a DTO first.",
	"WorkspacePipelineResponseV1": "declares 9 properties; the handler serializes pipelineResponse, " +
		"27 fields with different names. A schema rewrite, not a required list.",
	"WorkspacePipelinesResponseV1": "same drift as WorkspacePipelineResponseV1.",
	"Integration": "one component, three backing structs across four routes " +
		"(workspaceMCPServerResponse, crewMCPServerResponse, crewIntegrationOverview) that do not agree on " +
		"their field sets. No single derivation is correct for all of them; split the schema per route first.",
	"IntegrationList": "same as Integration.",
	"CLIToken": "POST /cli-tokens and GET /cli-tokens share this component and disagree: create always " +
		"returns the plaintext token, list never does. Both bodies are map[string]any literals with keys " +
		"inserted conditionally in Go, so there are no tags to derive from either.",
	"CLITokenList": "same as CLIToken.",
	"KeeperRequestList": "the schema declares an object with `requests`/`count`; KeeperLogHandler.List " +
		"writes a bare []keeperLogEntry. Wrong container, not a missing required list — no real response " +
		"will ever have those keys. Replace the schema with an array first.",
	"Connector": "the schema types verify.http and verify.sql as nullable strings; connectors.Manifest " +
		"has them as nested objects ({method,url,headers,...} / {driver,dsn}). A real response with a " +
		"verify block sends an object where the schema promises a string — fix the property types first.",
	"StatusResponse": "five DELETE routes share it and one disagrees: DELETE /credentials/{id} writes " +
		"{\"success\": true} where the schema says {\"status\": string} — wrong key and wrong type. " +
		"Every writer is a map literal, so there are no tags to derive from either.",
	"SkillDetail": "orphaned — no path references it, while GET /skills/{skillId} wrongly $refs Skill. " +
		"Fix the $ref before grading either.",
}

func TestOpenAPIResponseComponents_AreGradedOrExcused(t *testing.T) {
	// Measured, not chosen. Lower it in the same commit that adds pairs; a rise
	// means a route shipped a response shape nothing can check.
	const budget = 203

	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}

	graded := map[string]bool{}
	for _, c := range responseShapeContracts {
		if name, ok := componentOf(c.pointer); ok {
			graded[name] = true
		}
	}

	var ungraded []string
	for name := range responseRootComponents(doc) {
		if graded[name] || responseShapeExclusions[name] != "" {
			continue
		}
		ungraded = append(ungraded, name)
	}
	sort.Strings(ungraded)

	if len(ungraded) > budget {
		shown := ungraded
		if len(shown) > 10 {
			shown = shown[:10]
		}
		t.Errorf("%d response components are neither graded by a pair nor excused (budget %d).\n"+
			"A component with no pair accepts a body that shares no field name with what the handler sends.\n"+
			"Add a pair to responseShapeContracts, or an entry to responseShapeExclusions saying why it cannot have one.\n"+
			"First few: %s", len(ungraded), budget, strings.Join(shown, ", "))
	}
}

// componentOf pulls "Agent" out of "/components/schemas/Agent" and out of
// "/components/schemas/FinalInboxList/properties/rows/items" alike — a pair on
// a nested schema still grades the component it lives in.
func componentOf(pointer string) (string, bool) {
	const prefix = "/components/schemas/"
	if !strings.HasPrefix(pointer, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(pointer, prefix)
	name, _, _ := strings.Cut(rest, "/")
	return name, name != ""
}

// responseRootComponents collects every component a path uses as the root of a
// 200 response, directly or as the items of a top-level array.
func responseRootComponents(doc map[string]any) map[string]bool {
	out := map[string]bool{}
	paths, _ := doc["paths"].(map[string]any)
	for _, item := range paths {
		methods, _ := item.(map[string]any)
		for _, op := range methods {
			operation, ok := op.(map[string]any)
			if !ok {
				continue
			}
			responses, _ := operation["responses"].(map[string]any)
			ok200, _ := responses["200"].(map[string]any)
			content, _ := ok200["content"].(map[string]any)
			appJSON, _ := content["application/json"].(map[string]any)
			schema, _ := appJSON["schema"].(map[string]any)
			if schema == nil {
				continue
			}
			if name, ok := refName(schema); ok {
				out[name] = true
			}
			if items, ok := schema["items"].(map[string]any); ok {
				if name, ok := refName(items); ok {
					out[name] = true
				}
			}
		}
	}
	return out
}

func refName(schema map[string]any) (string, bool) {
	ref, _ := schema["$ref"].(string)
	if ref == "" {
		return "", false
	}
	return componentOf(strings.TrimPrefix(ref, "#"))
}

var _ = fmt.Sprintf
