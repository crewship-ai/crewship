package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// headerShapedName matches a hyphenated, header-style name (X-Crewship-Signature,
// anthropic-version, PRIVATE-TOKEN). No query parameter this API actually accepts
// is spelled with a hyphen — they are all snake_case — so a hyphen in a query
// parameter name means a header leaked into the spec, whether or not it is a
// standard one. This catches the custom X-* headers that no fixed list would.
var headerShapedName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:-[A-Za-z0-9]+)+$`)

// wellKnownRequestHeaders are HTTP header names that must never appear as an
// `in: query` parameter. GET /openapi.json is served so a client (an agent,
// typically) can drive the API straight from the spec; a header documented as
// a query parameter tells that client to put credentials in the query string,
// where they do not authenticate and do land in access logs.
//
// "Range" is deliberately absent: `range` is a real query parameter here
// (GET /api/v1/paymaster/metrics?range=30d), and the match is case-insensitive,
// so listing it would flag a legitimate parameter.
var wellKnownRequestHeaders = map[string]bool{
	"accept":              true,
	"accept-encoding":     true,
	"accept-language":     true,
	"authorization":       true,
	"cache-control":       true,
	"connection":          true,
	"content-disposition": true,
	"content-length":      true,
	"content-type":        true,
	"cookie":              true,
	"etag":                true,
	"host":                true,
	"idempotency-key":     true,
	"if-match":            true,
	"if-none-match":       true,
	"last-event-id":       true,
	"location":            true,
	"origin":              true,
	"referer":             true,
	"retry-after":         true,
	"upgrade":             true,
	"user-agent":          true,
	"www-authenticate":    true,
	"x-api-key":           true,
	"x-forwarded-for":     true,
	"x-forwarded-proto":   true,
	"x-internal-token":    true,
	"x-real-ip":           true,
	"x-request-id":        true,
}

func TestGeneratedSpecNeverDocumentsHeadersAsQueryParameters(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "openapi.gen.json"))
	if err != nil {
		t.Fatalf("read generated spec: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse generated spec: %v", err)
	}

	operations := 0
	var offenders []string
	for path, item := range spec.Paths {
		for method, op := range item {
			operations++
			var bad []string
			for _, param := range op.Parameters {
				if param.In != "query" {
					continue
				}
				if wellKnownRequestHeaders[strings.ToLower(param.Name)] || headerShapedName.MatchString(param.Name) {
					bad = append(bad, param.Name)
				}
			}
			if len(bad) > 0 {
				sort.Strings(bad)
				offenders = append(offenders, strings.ToUpper(method)+" "+path+": "+strings.Join(bad, ", "))
			}
		}
	}
	if operations == 0 {
		t.Fatal("generated spec has no operations — the assertion below would be vacuous")
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("%d of %d operations document an HTTP header as an in:query parameter:\n  %s",
			len(offenders), operations, strings.Join(offenders, "\n  "))
	}
}

// handlerSignature is the parameter list every handler in internal/api has —
// the text functionSignature hands to queryParamNames, and the only place the
// inbound request is declared.
const handlerSignature = `w http.ResponseWriter, r *http.Request) `

// TestQueryParamNamesIgnoresNonQueryReceivers pins the inference itself: only
// values that came from r.URL.Query() count. An unrecognised receiver must
// fail safe (no parameter), never be guessed into the spec.
func TestQueryParamNamesIgnoresNonQueryReceivers(t *testing.T) {
	body := `{
		cursor := r.URL.Query().Get("cursor")
		if r.URL.Query().Has("include_deleted") { _ = cursor }
		qs := r.URL.Query()
		window := qs.Get("range")
		tags := qs.Values("tag")
		q := r.URL.Query()
		_ = q.Get("status")
		if auth := r.Header.Get("Authorization"); auth == "" { return }
		_ = r.Header.Values("Accept")
		_ = r.PostForm.Get("state")
		_ = r.Form.Get("code")
		_ = r.Trailer.Get("X-Checksum")
		_ = r.Cookie("session")
		out := url.Values{}
		_ = out.Get("not_a_request_param")
		resp, _ := client.Get("https://example.test/webhook")
		_ = resp
		_ = window
		_ = tags
	}`

	got := queryParamNames(handlerSignature, body)
	sort.Strings(got)
	want := []string{"cursor", "include_deleted", "range", "status", "tag"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queryParamNames = %v, want %v", got, want)
	}
}

// TestQueryParamNamesIgnoresOutboundRequests covers the residual of the same
// class: narrowing to `.URL.Query()` is not enough, because an outbound
// *http.Request has that method too. Only the request the server was handed —
// and requests derived from it — carries API query parameters.
func TestQueryParamNamesIgnoresOutboundRequests(t *testing.T) {
	body := `{
		limit := r.URL.Query().Get("limit")
		upstream, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
		upstream.URL.RawQuery = signed.Encode()
		_ = upstream.URL.Query().Get("upstream_token")
		outQ := upstream.URL.Query()
		_ = outQ.Get("upstream_signature")
		probe, _ := http.NewRequest("GET", target, nil)
		_ = probe.URL.Query().Get("probe_nonce")
		_ = limit
	}`

	got := queryParamNames(handlerSignature, body)
	sort.Strings(got)
	want := []string{"limit"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queryParamNames = %v, want %v — an outbound request is not the API's query surface", got, want)
	}
}

// TestQueryParamNamesKeepsRequestsDerivedFromTheInboundOne is the guard against
// over-narrowing. internal/api/journal_handler.go:492 reads its query off
// `stripped := r.Clone(r.Context())`; those are genuine inbound parameters, and
// a rule that accepted only the literal receiver `r` would silently drop them.
func TestQueryParamNamesKeepsRequestsDerivedFromTheInboundOne(t *testing.T) {
	body := `{
		stripped := r.Clone(r.Context())
		if rawQ := stripped.URL.Query(); rawQ.Has("limit") || rawQ.Has("cursor") {
			rawQ.Del("limit")
		}
		scoped := r.WithContext(ctx)
		_ = scoped.URL.Query().Get("workspace_id")
	}`

	got := queryParamNames(handlerSignature, body)
	sort.Strings(got)
	want := []string{"cursor", "limit", "workspace_id"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queryParamNames = %v, want %v", got, want)
	}
}

// TestGeneratedSpecKeepsJournalCountPagination pins the same guard against the
// real spec rather than a fixture: GET /api/v1/journal/count reads `limit` and
// `cursor` only through the r.Clone-derived request, so it is the operation
// that disappears first if the inference is narrowed to the literal `r`.
//
// It asserted the exact set until #1844, when the operation gained the journal
// filter grammar by annotation — those parameters are parsed in
// parseJournalQuery, which no body scan can see. The property this test exists
// for is provenance across r.Clone, and that is what it still checks: exactness
// for this operation now lives in helperParsedQueryParameters, and the guard
// against the inference marking things required lives in
// requiredQueryParametersInSpec.
func TestGeneratedSpecKeepsJournalCountPagination(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "openapi.gen.json"))
	if err != nil {
		t.Fatalf("read generated spec: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse generated spec: %v", err)
	}
	op, ok := spec.Paths["/api/v1/journal/count"]["get"]
	if !ok {
		t.Fatal("GET /api/v1/journal/count missing from the generated spec")
	}
	var query []string
	for _, param := range op.Parameters {
		if param.In == "query" {
			query = append(query, param.Name)
		}
	}
	sort.Strings(query)
	documented := map[string]bool{}
	for _, name := range query {
		documented[name] = true
	}
	for _, name := range []string{"cursor", "limit"} {
		if !documented[name] {
			t.Fatalf("GET /api/v1/journal/count lost %q — the only place it is read is the "+
				"r.Clone-derived request, so provenance across the clone has stopped working (has %v)",
				name, query)
		}
	}
}
