package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCraftNativeRoutesTwoHumansSeeNamedHolderAndHandoff(t *testing.T) {
	requireCraftNativeProductionClock(t)
	f, _, _ := craftNativeHTTPFixture(t)
	mustExec(t, f.db, `UPDATE users SET full_name='Pavel' WHERE id=?`, f.userID)
	mustExec(t, f.db, `UPDATE users SET full_name='Jana' WHERE id=?`, f.adminID)
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='native-page'`)
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,'write',?)`, f.adminID, f.userID)
	owner := mintIngressScopedToken(t, f.db, f.userID, "native-route-owner", "pages:*")
	other := mintIngressScopedToken(t, f.db, f.adminID, "native-route-other", "pages:*")
	router, err := NewRouter(f.db, "native-route-fixture-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	server := httptest.NewServer(router)
	defer server.Close()
	call := func(method, path, bearer, admission, body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+"/api/v1/pages/@native-page/craft/"+path+"?workspace_id="+f.wsID, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		if admission != "" {
			req.Header.Set("X-Craft-Session", admission)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		var out map[string]any
		if err = json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("non JSON HTTP%d: %s", response.StatusCode, raw)
		}
		return response.StatusCode, out
	}
	if status, v := call("GET", "availability", owner, "", ""); status != 200 || v["enabled"] != true {
		t.Fatalf("availability %d %+v", status, v)
	}
	status, a := call("POST", "sessions", owner, "", "{}")
	if status != 201 {
		t.Fatalf("owner admission %d %+v", status, a)
	}
	status, b := call("POST", "sessions", other, "", "{}")
	if status != 201 {
		t.Fatalf("other admission %d %+v", status, b)
	}
	at := a["session_token"].(string)
	bt := b["session_token"].(string)
	status, first := call("POST", "lease", owner, at, "{}")
	if status != 200 {
		t.Fatalf("acquire %d %+v", status, first)
	}
	status, view := call("GET", "lease", other, bt, "")
	if status != 200 || view["mine"] != false || view["can_write"] != false {
		t.Fatalf("observer %d %+v", status, view)
	}
	holder, ok := view["holder"].(map[string]any)
	if !ok || holder["display_name"] != "Pavel" || holder["kind"] != "human" {
		t.Fatalf("named holder %+v", view)
	}
	if status, _ = call("POST", "lease", other, bt, "{}"); status != 409 {
		t.Fatalf("second human acquired %d", status)
	}
	status, request := call("POST", "lease/request-handoff", other, bt, "{}")
	if status != 201 {
		t.Fatalf("request %d %+v", status, request)
	}
	status, ownerView := call("GET", "lease", owner, at, "")
	if status != 200 {
		t.Fatal(status)
	}
	requests, ok := ownerView["handoff_requested_by"].([]any)
	if !ok || len(requests) != 1 || requests[0].(map[string]any)["display_name"] != "Jana" {
		t.Fatalf("handoff requests %+v", ownerView)
	}
	body, _ := json.Marshal(map[string]any{"generation": first["generation"], "request_id": request["request_id"]})
	if status, v := call("POST", "lease/handoff", owner, at, string(body)); status != 200 {
		t.Fatalf("handoff %d %+v", status, v)
	}
	if status, v := call("GET", "lease", other, bt, ""); status != 200 || v["mine"] != true || v["can_write"] != true || v["generation"] == first["generation"] {
		t.Fatalf("new holder %d %+v", status, v)
	}
	body, _ = json.Marshal(map[string]any{"generation": first["generation"]})
	if status, _ = call("POST", "lease/renew", owner, at, string(body)); status != 409 {
		t.Fatalf("old generation renewed %d", status)
	}
	// The production Router middleware must preserve ResponseController deadline
	// and flush support, not merely let direct-handler tests pass.
	streamCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, "GET", server.URL+"/api/v1/pages/@native-page/craft/events?workspace_id="+f.wsID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+other)
	req.Header.Set("X-Craft-Session", bt)
	stream, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 || !strings.HasPrefix(stream.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("router SSE status %d", stream.StatusCode)
	}
	scanner := bufio.NewScanner(stream.Body)
	if !scanner.Scan() || scanner.Text() != "event: lease" || !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "data: ") {
		t.Fatalf("missing bounded lease SSE: %v", scanner.Err())
	}
	var streamed map[string]any
	if err = json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &streamed); err != nil || streamed["mine"] != true || streamed["can_write"] != true {
		t.Fatalf("SSE holder mismatch %v %+v", err, streamed)
	}
}

func TestCraftNativeAvailabilityPageAndDefaultOff(t *testing.T) {
	f, m, ctx := craftNativeHTTPFixture(t)
	f.seedPage(t, "native-legacy", "native-legacy", "Legacy", f.userID, "")
	for _, tc := range []struct {
		slug    string
		enabled bool
	}{{"native-page", true}, {"native-legacy", false}, {"absent", false}} {
		req := httptest.NewRequest("GET", "/availability", nil).WithContext(ctx)
		req.SetPathValue("slug", tc.slug)
		w := httptest.NewRecorder()
		m.availability(w, req)
		var v map[string]bool
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != 200 || v["enabled"] != tc.enabled {
			t.Fatalf("availability %s %d %s", tc.slug, w.Code, w.Body)
		}
	}
	m.enabled = false
	req := httptest.NewRequest("GET", "/availability", nil).WithContext(ctx)
	req.SetPathValue("slug", "native-page")
	w := httptest.NewRecorder()
	m.availability(w, req)
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal("default-off availability enabled")
	}
}

// Real-clock transport tests exercise Linux CLOCK_BOOTTIME. The remaining
// authority tests use their deterministic clock and run on every platform.
func requireCraftNativeProductionClock(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("native production owner leases require Linux CLOCK_BOOTTIME")
	}
}

func TestCraftNativeUnsupportedPlatformFailsClosed(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("unsupported-platform startup check runs on non-Linux")
	}
	f, _, _ := craftNativeHTTPFixture(t)
	router, err := NewRouter(f.db, "native-route-fixture-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true}))
	if router != nil {
		router.Shutdown()
	}
	if err == nil || !strings.Contains(err.Error(), "Linux CLOCK_BOOTTIME") {
		t.Fatalf("unsupported native owner clock was not refused: %v", err)
	}
}
