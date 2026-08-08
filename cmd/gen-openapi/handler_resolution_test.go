package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// specOperations is the shape every assertion in this file reads: the
// parameters the published spec attaches to an operation.
type specOperations map[string]map[string]struct {
	Parameters []struct {
		Name     string `json:"name"`
		In       string `json:"in"`
		Required bool   `json:"required"`
	} `json:"parameters"`
	Responses map[string]json.RawMessage `json:"responses"`
}

func loadSpecOperations(t *testing.T) specOperations {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "openapi.gen.json"))
	if err != nil {
		t.Fatalf("read generated spec: %v", err)
	}
	var spec struct {
		Paths specOperations `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse generated spec: %v", err)
	}
	if len(spec.Paths) == 0 {
		t.Fatal("generated spec has no operations — every assertion below would be vacuous")
	}
	return spec.Paths
}

func specQueryParams(t *testing.T, ops specOperations, method, path string) []string {
	t.Helper()
	op, ok := ops[path][strings.ToLower(method)]
	if !ok {
		t.Fatalf("%s %s missing from the generated spec", method, path)
	}
	var names []string
	for _, p := range op.Parameters {
		if p.In == "query" {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}

// handlersThatReadNoQueryString are the five operations whose registration
// shape the generator could not resolve to a concrete handler:
//
//   - GET /api/health — an inline closure taking `_ *http.Request`
//   - GET /api/v1/auth/google/status — likewise
//   - GET /api/v1/system/version — a closure delegating to SystemHandler.Version
//   - GET /api/v1/admin/keeper/health — NewAdminKeeperHealthHandler(...).Get
//   - POST /api/v1/chats/{chatId}/steer — r.steerHandler.Steer
//
// Each read nothing off the query string, and each documented 95 query
// parameters: the union of every query parameter in internal/api, because an
// unresolved registration fell back to matching every same-named function in
// the package — _test.go helpers included — and merging their reads.
//
// GET /openapi.json is served so a client, usually an agent, can drive the API
// from it. An operation advertising 95 parameters it does not accept is worse
// than an undocumented one: it invites the caller to send nonsense and tells it
// nothing about what is real.
// The `statuses` column keeps the fix falsifiable in both directions: zero
// query parameters alone would also be satisfied by resolving these
// registrations to nothing at all. Each listed status is one only the real
// handler body produces — CrewFileDelete's neighbours cannot supply
// SteerHandler.Steer's 503, and AdminKeeperHealthHandler.Get's 400 comes from
// its own writeProblem, not from authedAdmin.
var handlersThatReadNoQueryString = []struct {
	method, path string
	statuses     []string
}{
	{"GET", "/api/health", []string{"200"}},
	{"GET", "/api/v1/auth/google/status", []string{"200"}},
	{"GET", "/api/v1/system/version", []string{"200", "401"}},
	{"GET", "/api/v1/admin/keeper/health", []string{"200", "400", "401", "403"}},
	{"POST", "/api/v1/chats/{chatId}/steer", []string{"202", "404", "500", "503"}},
}

// unionOnlyStatuses are statuses none of the five handlers above can produce.
// Every one of them was documented on all five before the fix, because the
// merge pulled in every status branch in internal/api along with every query
// parameter.
var unionOnlyStatuses = []string{"201", "204", "409", "429", "501", "502"}

func TestGeneratedSpecDocumentsNoQueryParametersForHandlersThatReadNone(t *testing.T) {
	ops := loadSpecOperations(t)
	for _, want := range handlersThatReadNoQueryString {
		got := specQueryParams(t, ops, want.method, want.path)
		if len(got) > 0 {
			shown := got
			if len(shown) > 12 {
				shown = append(append([]string{}, shown[:12]...), "…")
			}
			t.Errorf("%s %s documents %d query parameters, want 0 — the handler reads none: %s",
				want.method, want.path, len(got), strings.Join(shown, ", "))
		}

		op := ops[want.path][strings.ToLower(want.method)]
		for _, status := range want.statuses {
			if _, ok := op.Responses[status]; !ok {
				t.Errorf("%s %s does not document %s — its registration resolved to no handler at all, "+
					"which is quiet but not correct", want.method, want.path, status)
			}
		}
		for _, status := range unionOnlyStatuses {
			if _, ok := op.Responses[status]; ok {
				t.Errorf("%s %s documents %s, which its handler never answers — the merge is still on",
					want.method, want.path, status)
			}
		}
	}
}

// The companion property, and the guard against "fixing" this by amputation:
// an unresolved registration must stop contributing parameters without taking
// the genuine ones with it. Each entry below is read straight off the inbound
// request by a handler the generator resolves, one per registration shape in
// use. #1819's first attempt would have stripped them.
func TestGeneratedSpecKeepsParametersHandlersActuallyRead(t *testing.T) {
	ops := loadSpecOperations(t)
	mustKeep := []struct {
		method, path string
		params       []string
	}{
		// r.authedAdmin("GET", path, audit.List), plus an // openapi: annotation.
		{"GET", "/api/v1/audit", []string{"action", "date_from", "entity_type", "limit", "page", "search"}},
		// authed(wsCtx(http.HandlerFunc(h.List))) — journal Count reads these
		// off an r.Clone-derived request, so it is the first to disappear if
		// the inference is narrowed too far.
		{"GET", "/api/v1/journal/count", []string{"cursor", "limit"}},
		{"GET", "/api/v1/issues", []string{"assignee_id", "crew_id", "limit", "status"}},
		// r.authedMut("DELETE", path, role, h.CrewFileDelete).
		{"DELETE", "/api/v1/crews/{crewId}/files/delete", []string{"path"}},
		// An // openapi: annotated route keeps its declared parameters.
		{"GET", "/api/v1/skills", []string{"category", "installed", "source"}},
	}
	for _, want := range mustKeep {
		got := specQueryParams(t, ops, want.method, want.path)
		have := map[string]bool{}
		for _, name := range got {
			have[name] = true
		}
		for _, name := range want.params {
			if !have[name] {
				t.Errorf("%s %s lost query parameter %q (has %v)", want.method, want.path, name, got)
			}
		}
	}
}

// An `// openapi:` annotation documents the route directly below it, not the
// next few. The lookup used to scan a 400-byte window, so the annotation on
// /api/v1/audit also applied to the three admin routes registered under it and
// the one on /api/v1/skills to the two under that — handlers that read no
// query string at all published, respectively, the audit log's ten filters and
// the skill catalogue's eight.
func TestGeneratedSpecDoesNotBleedAnnotationsOntoNeighbouringRoutes(t *testing.T) {
	ops := loadSpecOperations(t)
	for _, op := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/stats"},
		{"GET", "/api/v1/admin/users"},
		{"GET", "/api/v1/admin/workspaces"},
		{"GET", "/api/v1/skills/{skillId}"},
		{"POST", "/api/v1/workspaces/{workspaceId}/skills/import"},
	} {
		if got := specQueryParams(t, ops, op.method, op.path); len(got) > 0 {
			t.Errorf("%s %s documents %v — its handler reads no query string; these come from the "+
				"annotation on the route above it", op.method, op.path, got)
		}
	}
}

// requiredQueryParametersInSpec is the exact set the requiredness inference
// fires on, pinned deliberately.
//
// Every entry was read against its handler and answers 4xx when the parameter
// is absent — for example ProxyHandler.CrewFileDelete
// (internal/api/proxy_files.go), whose 400 TestCrewFileDelete_MissingPathParam_400
// already pins. The rule is narrow by construction (requiredQueryParams) and
// the other ~150 query parameters stay optional, some of them wrongly: under-
// claiming leaves a client where it already was, over-claiming tells it a
// request will fail when it will succeed.
//
// Pinning the whole set rather than one example is the point: it means the
// inference cannot quietly start marking parameters required. If this test
// fails after a handler change, check the new entry against its handler before
// updating the list.
// Six entries joined the list in #1844's PR. Four came from widening the read
// pattern to look through an emptiness-preserving wrapper
// (emptinessPreservingWrappers), two from the `!` annotation, which is the
// escape hatch for a handler whose emptiness check lives somewhere the scan
// cannot follow. Each names the test that already pins its 4xx:
//
//	DELETE /api/v1/feedback ?message_id, ?signal   TestFeedbackDelete_Guards/missing_params_400
//	GET /api/v1/integrations/composio/tools ?toolkit  TestComposio_ListTools_RequiresToolkit
//	GET /api/v1/models ?provider                   TestModelsList_BadRequest/missing_provider
//	GET /api/v1/metrics/timeseries ?metric   (`!`) TestMetricsTimeseries_ParamValidation/missing_metric
//	GET /api/v1/auth/pair/poll ?code         (`!`) TestCovCLIPairPoll_AbsentCode400
//
// GET /api/v1/feedback is the control and is deliberately NOT here: it reads
// the same two trimmed parameters as the DELETE fifty lines above it, joined by
// && instead of ||, so either alone satisfies it and neither is required.
var requiredQueryParametersInSpec = []string{
	"DELETE /api/v1/admin/backups ?path",
	"DELETE /api/v1/crews/{crewId}/files/delete ?path",
	"DELETE /api/v1/feedback ?message_id",
	"DELETE /api/v1/feedback ?signal",
	"DELETE /api/v1/notification-templates ?category",
	"GET /api/v1/admin/backups/download ?path",
	"GET /api/v1/admin/backups/inspect ?path",
	"GET /api/v1/admin/backups/verify ?path",
	"GET /api/v1/agents/{agentId}/files/download ?path",
	"GET /api/v1/auth/pair/poll ?code",
	"GET /api/v1/crews/{crewId}/files/download ?path",
	"GET /api/v1/integrations/composio/tools ?toolkit",
	"GET /api/v1/memory/versions ?path",
	"GET /api/v1/memory/versions/{sha} ?path",
	"GET /api/v1/metrics/timeseries ?metric",
	"GET /api/v1/models ?provider",
	"GET /api/v1/oauth/callback ?code",
	"GET /api/v1/oauth/callback ?state",
	"GET /api/v1/skills/proposed ?crew_id",
	"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/diff ?from",
	"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/diff ?to",
	"POST /api/v1/connectors/{connectorId}/install ?workspace_id",
	"PUT /api/v1/agents/{agentId}/files/save ?path",
	"PUT /api/v1/crews/{crewId}/files/save ?path",
}

func TestGeneratedSpecMarksExactlyTheVerifiedParametersRequired(t *testing.T) {
	ops := loadSpecOperations(t)
	var got []string
	for path, item := range ops {
		for method, op := range item {
			for _, p := range op.Parameters {
				if p.In == "query" && p.Required {
					got = append(got, strings.ToUpper(method)+" "+path+" ?"+p.Name)
				}
			}
		}
	}
	sort.Strings(got)
	want := append([]string{}, requiredQueryParametersInSpec...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("required query parameters changed.\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
