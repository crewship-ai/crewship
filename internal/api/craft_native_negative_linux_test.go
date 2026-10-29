//go:build linux

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Exercise ordinary authentication and route middleware over real TCP. Public
// request IDs and generations must never substitute for the bound admission.
func TestCraftNativeRoutesNegativeAuthorityAndStreamCustody(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	f.seedPage(t, "foreign-native", "foreign-native", "Foreign", f.userID, "")
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) SELECT 'foreign-native',workspace_id,application_id,engine_revision,engine_sha256,editor_bundle FROM craft_native_documents WHERE page_id='native-page'`)
	writer := mintIngressScopedToken(t, f.db, f.userID, "native-negative-write", "pages:*")
	reader := mintIngressScopedToken(t, f.db, f.userID, "native-negative-read", "pages:read")
	router, err := NewRouter(f.db, "native-negative-fixture-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	server := httptest.NewServer(router)
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}
	call := func(page, path, bearer, admission, body string) (int, map[string]any, http.Header) {
		t.Helper()
		req, err := http.NewRequest("POST", server.URL+"/api/v1/pages/@"+page+"/craft/"+path+"?workspace_id="+f.wsID, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		if admission != "" {
			req.Header.Set("X-Craft-Session", admission)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err = json.Unmarshal(raw, &out); err != nil {
			t.Fatal("non-JSON response", res.StatusCode)
		}
		return res.StatusCode, out, res.Header
	}
	issue := func(page, bearer string) string {
		t.Helper()
		status, out, _ := call(page, "sessions", bearer, "", "{}")
		if status != 201 {
			t.Fatal("admission", status)
		}
		return out["session_token"].(string)
	}
	a, b, r := issue("native-page", writer), issue("native-page", writer), issue("native-page", reader)
	foreignA, foreignB := issue("foreign-native", writer), issue("foreign-native", writer)
	status, held, _ := call("native-page", "lease/acquire", writer, a, "{}")
	if status != 200 {
		t.Fatal("acquire", status)
	}
	generation := held["generation"].(string)
	before := func() string {
		t.Helper()
		var state string
		if err := f.db.QueryRow(`SELECT json_array(actor_id,session_id,generation,expires_ns) FROM mooring_edit_leases WHERE page_id='native-page'`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	unchanged := func(want string) {
		t.Helper()
		if got := before(); got != want {
			t.Fatal("rejected mutation changed current lease")
		}
	}
	for _, operation := range []string{"lease/acquire", "lease/renew"} {
		baseline := before()
		status, _, _ := call("native-page", operation, reader, r, `{"generation":"`+generation+`"}`)
		if status != 403 {
			t.Fatalf("reader %s: HTTP%d", operation, status)
		}
		unchanged(baseline)
	}
	status, _, _ = call("foreign-native", "lease/acquire", writer, foreignA, "{}")
	if status != 200 {
		t.Fatal(status)
	}
	status, foreignRequest, _ := call("foreign-native", "lease/request-handoff", writer, foreignB, "{}")
	if status != 201 {
		t.Fatal(status)
	}
	for _, request := range []string{"forged-request", foreignRequest["request_id"].(string)} {
		baseline := before()
		status, _, _ := call("native-page", "lease/handoff", writer, a, `{"generation":"`+generation+`","request_id":"`+request+`"}`)
		if status != 403 {
			t.Fatalf("unbound request: HTTP%d", status)
		}
		unchanged(baseline)
	}
	status, request, headers := call("native-page", "lease/request-handoff", writer, b, "{}")
	if status != 201 {
		t.Fatal(status)
	}
	// The request identifier is private host metadata, just like admission data.
	if headers.Get("Cache-Control") != "no-store" {
		t.Error("request-handoff201 lacks Cache-Control:no-store")
	}
	handoff := `{"generation":"` + generation + `","request_id":"` + request["request_id"].(string) + `"}`
	baseline := before()
	status, _, _ = call("native-page", "lease/handoff", writer, b, handoff)
	if status != 409 {
		t.Fatalf("non-holder handoff: HTTP%d", status)
	}
	unchanged(baseline)
	status, _, _ = call("native-page", "lease/handoff", writer, a, handoff)
	if status != 200 {
		t.Fatal("authorized handoff", status)
	}
	for _, operation := range []string{"lease/renew", "lease/release"} {
		baseline := before()
		status, _, _ := call("native-page", operation, writer, a, `{"generation":"`+generation+`"}`)
		if status != 409 {
			t.Fatalf("stale %s: HTTP%d", operation, status)
		}
		unchanged(baseline)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	streamReq := func() *http.Request {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/pages/@native-page/craft/events?workspace_id="+f.wsID, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+writer)
		req.Header.Set("X-Craft-Session", b)
		return req
	}
	first, err := client.Do(streamReq())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	if first.StatusCode != 200 {
		t.Fatal("first stream", first.StatusCode)
	}
	scanner := bufio.NewScanner(first.Body)
	if !scanner.Scan() || scanner.Text() != "event: lease" {
		t.Fatal("first stream not live")
	}
	second, err := client.Do(streamReq())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != 409 {
		t.Fatal("duplicate stream", second.StatusCode)
	}
}
