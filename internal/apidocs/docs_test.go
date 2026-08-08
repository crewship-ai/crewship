package apidocs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// realSpec loads the generated spec the server actually embeds. Reading the
// file directly (rather than importing internal/api for its embed) keeps this
// package's test binary small enough to compile in seconds — internal/api
// takes minutes — while still exercising the real 536-operation document
// rather than a toy fixture that would hide every scaling problem.
func realSpec(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "api", "openapi.gen.json"))
	if err != nil {
		t.Fatalf("read generated spec: %v", err)
	}
	return b
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestIndexRendersOperationsNotJSON is the core of #1846: /openapi answers a
// human-readable rendering of the same document /openapi.json serves as
// machine JSON.
func TestIndexRendersOperationsNotJSON(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	rec := get(t, h, "/openapi")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), "<!doctype html") {
		t.Errorf("body does not start with an HTML doctype: %.80q", body)
	}
	// A route a reader would look for, and its method, must both be present.
	for _, want := range []string{"/api/v1/crews", "GET", "POST", "Crewship API"} {
		if !strings.Contains(body, want) {
			t.Errorf("index is missing %q", want)
		}
	}
	// It must not simply dump the JSON document into a <pre>.
	if strings.Contains(body, `"openapi": "3.0`) {
		t.Error("index dumps raw spec JSON instead of rendering it")
	}
}

// TestIndexLinksEveryOperation pins that the rendering is complete: an
// operation that exists in the spec but is missing from the page is exactly
// the drift that makes a rendered spec worse than no rendering at all.
func TestIndexLinksEveryOperation(t *testing.T) {
	t.Parallel()
	raw := realSpec(t)
	h := NewHandler(raw)

	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}

	body := get(t, h, "/openapi").Body.String()
	var missing []string
	total := 0
	for _, item := range doc.Paths {
		for _, op := range item {
			total++
			if !strings.Contains(body, "/openapi/op/"+op.OperationID+`"`) {
				missing = append(missing, op.OperationID)
			}
		}
	}
	if total < 100 {
		t.Fatalf("only %d operations parsed from the spec — fixture looks wrong", total)
	}
	if len(missing) > 0 {
		show := missing
		if len(show) > 10 {
			show = show[:10]
		}
		t.Errorf("%d/%d operations are not linked from the index, e.g. %v", len(missing), total, show)
	}
}

// TestOperationPageShowsTheContract checks the fields a human is actually
// checking the agent's view against: method, path, parameters with their
// location, and whether the parameter is binding.
func TestOperationPageShowsTheContract(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	rec := get(t, h, "/openapi/op/get_api_v1_admin_backups_download")
	if rec.Code != http.StatusOK {
		t.Fatalf("operation page = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"GET",
		"/api/v1/admin/backups/download",
		"path",     // the query parameter's name
		"query",    // its location
		"required", // its binding-ness
		"401",      // an enumerated response status
	} {
		if !strings.Contains(body, want) {
			t.Errorf("operation page is missing %q", want)
		}
	}
}

// TestOperationPageUnknownIDIs404 — a docs surface that answers 200 for a
// name it does not have is the same lie /openapi.json used to tell when the
// SPA catch-all owned that path.
func TestOperationPageUnknownIDIs404(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	for _, p := range []string{
		"/openapi/op/no_such_operation",
		"/openapi/schema/NoSuchSchema",
		"/openapi/nonsense",
		"/openapi/op/",
	} {
		rec := get(t, h, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
		// The 404 does not echo the path back. It would be escaped, but a
		// page that reflects an arbitrary segment is a page a scanner has to
		// reason about for no reader benefit.
		if seg := strings.TrimPrefix(p, "/openapi/"); seg != "" && strings.Contains(rec.Body.String(), seg) {
			t.Errorf("GET %s reflects the requested path back into the page", p)
		}
	}
}

// TestUnauthenticatedOperationsAreMarked — 13 operations in the generated
// spec carry `security: []`. Which routes answer without credentials is the
// single most consequential thing a reader can get wrong, so the rendering
// says it in words rather than leaving an empty list to be inferred.
func TestUnauthenticatedOperationsAreMarked(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	pub := get(t, h, "/openapi/op/post_api_v1_auth_signup").Body.String()
	if !strings.Contains(pub, "No authentication") {
		t.Error("public operation is not marked as unauthenticated")
	}

	authed := get(t, h, "/openapi/op/post_api_v1_crews").Body.String()
	if strings.Contains(authed, "No authentication") {
		t.Error("authenticated operation is marked as unauthenticated")
	}
	if !strings.Contains(authed, "bearerAuth") {
		t.Error("authenticated operation does not name its security schemes")
	}
}

// TestSchemaPageResolvesRefs — 523 of the spec's schema references point into
// components; a rendering that shows "$ref: #/components/schemas/X" and stops
// is not a rendering.
func TestSchemaPageResolvesRefs(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	// The request body of POST /api/v1/crews is a $ref. The operation page
	// must name the target and link to it, not print the raw pointer.
	op := get(t, h, "/openapi/op/post_api_v1_crews").Body.String()
	if strings.Contains(op, "#/components/schemas/") {
		t.Error("operation page prints raw $ref pointers")
	}
	if !strings.Contains(op, "CoreCrewCreateRequestV2") {
		t.Error("operation page does not name the request body schema")
	}

	// Issue has a property that is itself a $ref (created_by → IssueCreator);
	// the tree must follow it.
	sc := get(t, h, "/openapi/schema/Issue")
	if sc.Code != http.StatusOK {
		t.Fatalf("schema page = %d, want 200", sc.Code)
	}
	for _, want := range []string{"Issue", "created_by", "IssueCreator"} {
		if !strings.Contains(sc.Body.String(), want) {
			t.Errorf("schema page is missing %q", want)
		}
	}
}

// TestSchemaIndexFlagsUnreferencedSchemas — 87 of the 461 component schemas
// are not reachable from any operation. Rendering them silently alongside the
// live ones would present dead weight as contract.
func TestSchemaIndexFlagsUnreferencedSchemas(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	rec := get(t, h, "/openapi/schemas")
	if rec.Code != http.StatusOK {
		t.Fatalf("schema index = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unreferenced") {
		t.Error("schema index does not distinguish unreferenced schemas")
	}
}

var (
	urlAttrRe = regexp.MustCompile(`(?i)\b(?:src|href|action|formaction|poster|data)\s*=\s*"([^"]*)"`)
	cssURLRe  = regexp.MustCompile(`(?i)url\(\s*['"]?([^'")]*)`)
	importRe  = regexp.MustCompile(`(?i)@import`)
	// An inline script is a <script> whose first non-space character is not
	// the start of its own closing tag; `<script src=… ></script>` is the
	// external form and is allowed under script-src 'self'.
	inlineJSRe  = regexp.MustCompile(`(?is)<script(?:\s[^>]*)?>\s*[^<\s]`)
	styleAttrRe = regexp.MustCompile(`(?i)\bstyle\s*=\s*"`)
)

// TestServedBytesReferenceNothingRemote is the offline/air-gapped proof at the
// artifact level: every URL in every byte this surface serves resolves back to
// the same instance. A CDN script tag, a Google font, a remote logo — any of
// them would render a broken page on an air-gapped install, and the binary
// embeds its whole frontend precisely so that cannot happen.
func TestServedBytesReferenceNothingRemote(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	pages := []string{
		"/openapi",
		"/openapi/op/post_api_v1_crews",
		"/openapi/schemas",
		"/openapi/schema/Issue",
		"/openapi/ui.css",
		"/openapi/ui.js",
	}
	for _, p := range pages {
		rec := get(t, h, p)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, m := range urlAttrRe.FindAllStringSubmatch(body, -1) {
			assertLocalURL(t, p, m[1])
		}
		for _, m := range cssURLRe.FindAllStringSubmatch(body, -1) {
			assertLocalURL(t, p, m[1])
		}
		if importRe.MatchString(body) {
			t.Errorf("%s uses @import — a remote stylesheet risk", p)
		}
	}
}

func assertLocalURL(t *testing.T, page, u string) {
	t.Helper()
	switch {
	case u == "", strings.HasPrefix(u, "#"):
		return
	case strings.HasPrefix(u, "//"):
		t.Errorf("%s references protocol-relative URL %q — not served by this instance", page, u)
	case strings.HasPrefix(u, "/"):
		return
	case strings.HasPrefix(u, "data:"):
		return
	default:
		t.Errorf("%s references non-local URL %q", page, u)
	}
}

// TestServedHTMLNeedsNoInlineScriptOrStyle keeps the surface compatible with a
// strict Content-Security-Policy (script-src 'self'; style-src 'self'). Adding
// an inline <script> or a style="" attribute would still render in a browser
// with CSP relaxed, so the browser cannot be the thing that catches it — this
// test is.
func TestServedHTMLNeedsNoInlineScriptOrStyle(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	for _, p := range []string{"/openapi", "/openapi/op/post_api_v1_crews", "/openapi/schema/Issue"} {
		body := get(t, h, p).Body.String()
		if inlineJSRe.MatchString(body) {
			t.Errorf("%s contains an inline <script> body — blocked by script-src 'self'", p)
		}
		if styleAttrRe.MatchString(body) {
			t.Errorf("%s contains a style=\"\" attribute — blocked by style-src 'self'", p)
		}
	}
}

// TestSpecContentIsEscaped — the spec is generated from the codebase, so its
// strings are not attacker-controlled today. They are, however, machine-
// generated from route registrations and schema names, and this page is served
// unauthenticated. Escaping is a property of the renderer, not of the current
// input.
func TestSpecContentIsEscaped(t *testing.T) {
	t.Parallel()
	evil := `<script>alert(1)</script>`
	spec := `{
	  "openapi": "3.0.3",
	  "info": {"title": "` + evil + `", "version": "1"},
	  "paths": {
	    "/x/` + evil + `": {
	      "get": {
	        "operationId": "get_x",
	        "tags": ["` + evil + `"],
	        "security": [],
	        "parameters": [{"in": "query", "name": "` + evil + `", "required": true, "schema": {"type": "string"}}],
	        "responses": {"200": {"description": "` + evil + `"}}
	      }
	    }
	  },
	  "components": {"schemas": {"Evil": {"type": "object", "description": "` + evil + `"}}}
	}`
	h := NewHandler([]byte(spec))

	for _, p := range []string{"/openapi", "/openapi/op/get_x", "/openapi/schema/Evil"} {
		body := get(t, h, p).Body.String()
		if strings.Contains(body, evil) {
			t.Errorf("%s emits spec content unescaped", p)
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("%s does not contain the escaped form — did it drop the content entirely?", p)
		}
	}
}

// TestUnparseableSpecFailsLoudly — a 200 that renders an empty shell is the
// exact failure mode #1325 fixed for /openapi.json. Do not reintroduce it.
func TestUnparseableSpecFailsLoudly(t *testing.T) {
	t.Parallel()
	h := NewHandler([]byte("not json"))

	rec := get(t, h, "/openapi")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("unparseable spec = %d, want 500", rec.Code)
	}
}

// TestAssetsCarryTheirOwnContentType — served with the wrong type the CSS is
// ignored and the JS refuses to execute under X-Content-Type-Options: nosniff,
// which the server stamps on every response.
func TestAssetsCarryTheirOwnContentType(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	for path, wantType := range map[string]string{
		"/openapi/ui.css": "text/css",
		"/openapi/ui.js":  "javascript",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, wantType) {
			t.Errorf("GET %s Content-Type = %q, want to contain %q", path, ct, wantType)
		}
	}
}

// TestOnlyGETIsAnswered — the docs are a read surface; anything else is a
// misrouted request and should say so rather than render a page.
func TestOnlyGETIsAnswered(t *testing.T) {
	t.Parallel()
	h := NewHandler(realSpec(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/openapi", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /openapi = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow == "" {
		t.Error("405 without an Allow header")
	}
}
