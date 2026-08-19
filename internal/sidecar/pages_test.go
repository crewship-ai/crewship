package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The producer door for a process inside a crew container (#1946). What these
// tests are really pinning is one sentence from docs/prd/pages.md §4 rule 5:
// the identity on a push comes from the token, never from the body. The
// sidecar is the only place that sentence can be made true for an agent,
// because the agent is the one writing the body.

func newPagePushServer(t *testing.T, backend string) *Server {
	t.Helper()
	return newQueryServer(t, &IPCConfig{
		BaseURL:     backend,
		Token:       "internal-secret",
		AgentID:     "agent-nela",
		AgentSlug:   "nela",
		AgentToken:  "tok-nela",
		CrewID:      "crew-1",
		WorkspaceID: "ws-1",
	}, []CrewMember{
		{ID: "agent-nela", Slug: "nela", AuthToken: "tok-nela"},
		{ID: "agent-riley", Slug: "riley", AuthToken: "tok-riley"},
	})
}

// capturingUpstream stands in for crewshipd's internal page route. It records
// the envelope and the headers it was handed, and answers with whatever the
// test asked for.
type capturedPush struct {
	path    string
	method  string
	token   string
	body    map[string]any
	rawBody []byte
}

func upstream(t *testing.T, status int, reply string, into *capturedPush) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		into.path = r.URL.Path
		into.method = r.Method
		into.token = r.Header.Get("X-Internal-Token")
		into.rawBody = raw
		_ = json.Unmarshal(raw, &into.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
}

func pushReq(path, body, token string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestPagePush_ForwardsWithIdentityFromTheTokenNotTheBody(t *testing.T) {
	t.Parallel()
	var got capturedPush
	be := upstream(t, http.StatusOK, `{"seq":4}`, &got)
	defer be.Close()
	srv := newPagePushServer(t, be.URL)

	// The payload names an agent, a crew and a workspace. Every one of them is
	// a lie the sidecar must not carry: riley's token is on the request, so
	// riley is who pushed, whatever the body says.
	payload := `{"value":42,"unit":"ms","agent_id":"agent-nela","crew_id":"crew-9","workspace_id":"ws-9"}`
	w := httptest.NewRecorder()
	srv.handlePagePush(w, pushReq("/pages/sit/latence", payload, "tok-riley"))

	if w.Code != http.StatusOK {
		t.Fatalf("code: got %d want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got.method != http.MethodPut {
		t.Errorf("upstream method: got %s want PUT", got.method)
	}
	if got.path != "/api/v1/internal/pages/sit/data" {
		t.Errorf("upstream path: got %q", got.path)
	}
	if got.token != "internal-secret" {
		t.Errorf("X-Internal-Token: got %q", got.token)
	}
	if got.body["agent_id"] != "agent-riley" {
		t.Errorf("agent_id: got %v want agent-riley — the acting token decides, not the body", got.body["agent_id"])
	}
	if got.body["crew_id"] != "crew-1" {
		t.Errorf("crew_id: got %v want crew-1 (from IPC)", got.body["crew_id"])
	}
	if got.body["workspace_id"] != "ws-1" {
		t.Errorf("workspace_id: got %v want ws-1 (from IPC)", got.body["workspace_id"])
	}
	if got.body["panel"] != "latence" {
		t.Errorf("panel: got %v want latence", got.body["panel"])
	}

	// The payload rides in `data` untouched — including the fields that tried
	// to claim an identity. They are inert there: the server reads identity
	// from the envelope, and the schema decides what the payload may contain.
	data, _ := json.Marshal(got.body["data"])
	var round map[string]any
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("data is not the payload: %v", err)
	}
	if round["value"] != float64(42) {
		t.Errorf("payload mangled in transit: %s", data)
	}
}

func TestPagePush_ClaimsNoRun(t *testing.T) {
	t.Parallel()
	var got capturedPush
	be := upstream(t, http.StatusOK, `{}`, &got)
	defer be.Close()
	srv := newPagePushServer(t, be.URL)

	w := httptest.NewRecorder()
	srv.handlePagePush(w, pushReq("/pages/sit/latence", `{"value":1,"unit":"ms"}`, "tok-nela"))

	// An agent in a container is not a routine run. Sending an author_run_id
	// here would be the sidecar inventing provenance; the column is nullable
	// for exactly this caller (pages_internal.go resolveProducerRun).
	if _, present := got.body["author_run_id"]; present {
		t.Errorf("author_run_id must be absent, got %v", got.body["author_run_id"])
	}
}

func TestPagePush_StateRidesTheQueryString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		url  string
		want any
	}{
		{"absent", "/pages/sit/latence", nil},
		{"failed", "/pages/sit/latence?state=failed", "failed"},
		{"ok", "/pages/sit/latence?state=ok", "ok"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got capturedPush
			be := upstream(t, http.StatusOK, `{}`, &got)
			defer be.Close()
			srv := newPagePushServer(t, be.URL)

			w := httptest.NewRecorder()
			srv.handlePagePush(w, pushReq(tc.url, `{"value":1,"unit":"ms"}`, "tok-nela"))

			if got.body["state"] != tc.want {
				t.Errorf("state: got %v want %v", got.body["state"], tc.want)
			}
		})
	}
}

func TestPagePush_PassesTheServersRefusalThrough(t *testing.T) {
	t.Parallel()
	// §11b.6: the 422 an agent sees must be the SAME 422 a shell script sees,
	// from the same function. The sidecar re-shaping it would give the feature
	// two vocabularies for one rule.
	body := `{"error":"payload does not satisfy metric.v1: '/delta' expected number, but got null"}`
	var got capturedPush
	be := upstream(t, http.StatusBadRequest, body, &got)
	defer be.Close()
	srv := newPagePushServer(t, be.URL)

	w := httptest.NewRecorder()
	srv.handlePagePush(w, pushReq("/pages/sit/latence", `{"value":1,"delta":null}`, "tok-nela"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("code: got %d want 400", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != body {
		t.Errorf("body was re-shaped:\n got %s\nwant %s", w.Body.String(), body)
	}
}

func TestPagePush_RefusesAForgedToken(t *testing.T) {
	t.Parallel()
	var got capturedPush
	be := upstream(t, http.StatusOK, `{}`, &got)
	defer be.Close()
	srv := newPagePushServer(t, be.URL)

	w := httptest.NewRecorder()
	srv.handlePagePush(w, pushReq("/pages/sit/latence", `{"value":1}`, "tok-nobody"))

	if w.Code != http.StatusForbidden {
		t.Fatalf("code: got %d want 403", w.Code)
	}
	if got.path != "" {
		t.Errorf("a forged token reached the upstream: %s", got.path)
	}
}

func TestPagePush_RefusesATokenlessDowngrade(t *testing.T) {
	t.Parallel()
	// Tokens ARE provisioned on this crew, so omitting one is not "legacy",
	// it is a sibling trying to push as the boot agent.
	var got capturedPush
	be := upstream(t, http.StatusOK, `{}`, &got)
	defer be.Close()
	srv := newPagePushServer(t, be.URL)

	w := httptest.NewRecorder()
	srv.handlePagePush(w, pushReq("/pages/sit/latence", `{"value":1}`, ""))

	if w.Code != http.StatusForbidden {
		t.Fatalf("code: got %d want 403", w.Code)
	}
	if got.path != "" {
		t.Errorf("a token-less push reached the upstream: %s", got.path)
	}
}

func TestPagePush_PathMustNameOnePageAndOnePanel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		path string
	}{
		{"no_panel", "/pages/sit"},
		{"trailing_slash", "/pages/sit/"},
		{"no_page", "/pages//latence"},
		// Not a cosmetic rule: read as page="sit", panel="latence/../../x"
		// this is a path the sidecar would paste into an upstream URL.
		{"panel_with_slash", "/pages/sit/latence/extra"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got capturedPush
			be := upstream(t, http.StatusOK, `{}`, &got)
			defer be.Close()
			srv := newPagePushServer(t, be.URL)

			w := httptest.NewRecorder()
			srv.handlePagePush(w, pushReq(tc.path, `{"value":1}`, "tok-nela"))

			if w.Code != http.StatusNotFound {
				t.Errorf("code: got %d want 404 (body=%s)", w.Code, w.Body.String())
			}
			if got.path != "" {
				t.Errorf("reached the upstream: %s", got.path)
			}
		})
	}
}

func TestPagePush_BodyMustBeJSON(t *testing.T) {
	t.Parallel()
	var got capturedPush
	be := upstream(t, http.StatusOK, `{}`, &got)
	defer be.Close()
	srv := newPagePushServer(t, be.URL)

	w := httptest.NewRecorder()
	srv.handlePagePush(w, pushReq("/pages/sit/latence", `value=1`, "tok-nela"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code: got %d want 400", w.Code)
	}
	// A syntax check, not a schema check. Pasting a non-JSON body into the
	// envelope would break the ENVELOPE, and the agent would be told its
	// request was malformed rather than its payload.
	if got.path != "" {
		t.Errorf("a non-JSON body reached the upstream: %s", got.path)
	}
}

func TestPagePush_NoIPC(t *testing.T) {
	t.Parallel()
	srv := newQueryServer(t, nil, nil)

	w := httptest.NewRecorder()
	srv.handlePagePush(w, pushReq("/pages/sit/latence", `{"value":1}`, ""))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("code: got %d want 503", w.Code)
	}
}

func TestPagePush_RoutedFromBuildHandler(t *testing.T) {
	t.Parallel()
	// The handler being right is worth nothing if nothing dispatches to it —
	// which is the exact shape of #1946 (the server half existed the whole
	// time; only the door was missing).
	var got capturedPush
	be := upstream(t, http.StatusOK, `{"seq":1}`, &got)
	defer be.Close()
	srv := newPagePushServer(t, be.URL)

	w := httptest.NewRecorder()
	r := pushReq("/pages/sit/latence", `{"value":1,"unit":"ms"}`, "tok-nela")
	// The control-plane switch only fires for a loopback Host on a loopback
	// connection (the Patch-E gate above the switch in server.go); the default
	// httptest request looks like proxy traffic and is refused before routing.
	r.Host = "localhost:9119"
	r.RemoteAddr = "127.0.0.1:54321"
	srv.buildHandler(srv.proxy).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code: got %d want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got.path != "/api/v1/internal/pages/sit/data" {
		t.Errorf("not routed to the page push: upstream saw %q", got.path)
	}
}
