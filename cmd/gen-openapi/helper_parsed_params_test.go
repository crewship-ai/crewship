package main

import (
	"sort"
	"strings"
	"testing"
)

// #1844. Resolving the handler (#1832) stopped five operations from publishing
// the union of every query parameter in internal/api. The side effect was that
// 23 parameter names left the document entirely — and some of them are real,
// accepted by a live public route, now documented on nothing rather than on
// everything.
//
// They are invisible to the handler-body scan for one reason: the handler does
// not read the query string itself. It calls a package-level helper that does —
// parseWindow, parseJournalQuery, parseTimeseriesParams, parseRegistryFilters,
// resolveIOStep. The scan reads one function body and stops, by design: chasing
// call graphs is how the generator published `?Authorization=` (#1819) and 95
// phantom parameters (#1832) in the first place.
//
// The `// openapi:` annotation is the declared escape hatch for exactly this,
// and it became safe to use in #1830, which replaced the 400-byte lookback with
// a comment-block walk. Before that, an annotation here would have bled onto
// whichever route was registered below it.
//
// Each entry below was read against the helper that parses it. The helper is
// named so the next reader can re-derive the claim rather than trust this list.
var helperParsedQueryParameters = []struct {
	method, path string
	helper       string
	params       []string
}{
	// parseWindow (paymaster_handler.go) accepts ?since=&until= RFC3339 or
	// ?range=1h|24h|7d|30d. Every caller passes the inbound request straight in.
	{"GET", "/api/v1/paymaster/spend/by-crew", "parseWindow", []string{"range", "since", "until"}},
	{"GET", "/api/v1/paymaster/spend/by-agent/{crewId}", "parseWindow", []string{"range", "since", "until"}},
	{"GET", "/api/v1/paymaster/subscriptions", "parseWindow", []string{"range", "since", "until"}},
	// TopSpenders calls parseWindow too, but discards the `until` half
	// (`since, _ := parseWindow(r)`). ?until= is parsed and then thrown away, so
	// it is accepted without having any effect — documenting it would advertise
	// a filter that does not filter. `limit` is read inline and already inferred.
	{"GET", "/api/v1/paymaster/top-spenders", "parseWindow", []string{"range", "since"}},

	// parseJournalQuery (journal_handler.go) is the whole filter grammar of the
	// journal. GET /api/v1/journal documented none of it.
	{"GET", "/api/v1/journal", "parseJournalQuery", []string{
		"actor_type", "agent_id", "agent_ids", "crew_id", "crew_ids", "cursor",
		"entry_type", "exclude_entry_type", "limit", "mission_id", "priority",
		"q", "severity", "since", "trace_id", "until",
	}},
	{"GET", "/api/v1/journal/stream", "parseJournalQuery", []string{
		"actor_type", "agent_id", "agent_ids", "crew_id", "crew_ids", "cursor",
		"entry_type", "exclude_entry_type", "limit", "mission_id", "priority",
		"q", "severity", "since", "trace_id", "until",
	}},
	// Count parses the same grammar, but off a clone with ?limit and ?cursor
	// deleted first — a count has no use for either, and parseJournalQuery
	// would 400 on a malformed one. They stay documented because the handler
	// body does read them (rawQ.Has) to strip them; they are accepted and
	// ignored, which is what the operation does.
	{"GET", "/api/v1/journal/count", "parseJournalQuery", []string{
		"actor_type", "agent_id", "agent_ids", "crew_id", "crew_ids",
		"entry_type", "exclude_entry_type", "mission_id", "priority",
		"q", "severity", "since", "trace_id", "until",
	}},

	// parseTimeseriesParams (metrics_handler.go). ?metric is required — see
	// requiredQueryParametersInSpec.
	{"GET", "/api/v1/metrics/timeseries", "parseTimeseriesParams", []string{
		"bucket", "group_by", "metric", "window",
	}},

	// parseRegistryFilters (mcp_registry.go), shared by List and Search, plus
	// parsePagination (helpers.go) for limit/offset.
	{"GET", "/api/v1/mcp-registry", "parseRegistryFilters", []string{"featured", "limit", "offset", "trust_tier"}},
	{"GET", "/api/v1/mcp-registry/search", "parseRegistryFilters", []string{"featured", "limit", "offset", "q", "trust_tier"}},

	// resolveIOStep (pipeline_runs.go) — the #863 sub-span I/O gate.
	{"GET", "/api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}", "resolveIOStep", []string{"include_io", "io_step"}},
}

func TestGeneratedSpecDocumentsParametersParsedByHelpers(t *testing.T) {
	ops := loadSpecOperations(t)
	for _, want := range helperParsedQueryParameters {
		got := specQueryParams(t, ops, want.method, want.path)
		have := map[string]bool{}
		for _, name := range got {
			have[name] = true
		}
		for _, name := range want.params {
			if !have[name] {
				t.Errorf("%s %s does not document %q, which %s reads off the query string (has %v)",
					want.method, want.path, name, want.helper, got)
			}
		}
	}
}

// namesBelongingToNoPublicOperation is the other half of #1844, and the more
// important half. Nine of the 23 names that left the document belong on no
// public operation at all, and putting them back would re-commit #1832's error
// with the sign flipped: a parameter documented on an operation that does not
// accept it.
//
// Each is annotated with why it cannot be attached to anything. If one of these
// ever legitimately appears — a new public route really does accept ?direction=
// — read the handler, then move the name out of this list rather than deleting
// the list.
var namesBelongingToNoPublicOperation = map[string]string{
	// Read by CrewMessagingHandler.ListMessages / ReadFile, registered only
	// under /api/v1/internal/ — the sidecar-only surface addRoute excludes from
	// a spec served unauthenticated at GET /openapi.json.
	"direction":         "internal-only route (GET /api/v1/internal/crew-messages)",
	"peer_crew_id":      "internal-only route (GET /api/v1/internal/crew-messages)",
	"requester_crew_id": "internal-only route (GET /api/v1/internal/crew-files/{crewId})",
	"include_values":    "internal-only route (GET /api/v1/internal/credentials)",

	// NextAuthHandler.SignIn serves GET /api/auth/signin, which addRoute
	// excludes with the rest of the /api/auth/ compatibility surface.
	"callbackUrl": "excluded /api/auth/ surface (GET /api/auth/signin)",

	// GoogleAuthHandler.Redirect reads ?redirect, but its registration was
	// deliberately removed when Google OAuth was switched off (router_auth.go).
	// The handler is unreachable; documenting its parameter would advertise a
	// route that answers 404.
	"redirect": "handler registered nowhere — Google OAuth is switched off",

	// Read by httptest servers standing in for the Composio API inside
	// _test.go files. They are query parameters of a fake upstream, never of
	// this API. #1832 stopped scanning _test.go for exactly this reason.
	"name":         "read only by a test double of an upstream API",
	"toolkit_slug": "read only by a test double of an upstream API",
}

func TestGeneratedSpecOmitsNamesThatBelongToNoPublicOperation(t *testing.T) {
	ops := loadSpecOperations(t)
	sightings := map[string][]string{}
	for path, item := range ops {
		for method, op := range item {
			for _, p := range op.Parameters {
				if p.In != "query" {
					continue
				}
				if _, listed := namesBelongingToNoPublicOperation[p.Name]; listed {
					sightings[p.Name] = append(sightings[p.Name], strings.ToUpper(method)+" "+path)
				}
			}
		}
	}
	for name, where := range sightings {
		sort.Strings(where)
		t.Errorf("%q is documented on %v, but %s — annotating it back onto an operation that does not "+
			"accept it is the same lie #1832 removed, in the opposite direction",
			name, where, namesBelongingToNoPublicOperation[name])
	}
}
