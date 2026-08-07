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

	got := queryParamNames(body)
	sort.Strings(got)
	want := []string{"cursor", "include_deleted", "range", "status", "tag"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queryParamNames = %v, want %v", got, want)
	}
}
