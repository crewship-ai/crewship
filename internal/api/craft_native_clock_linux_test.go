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

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// These transport tests exercise the actual Linux CLOCK_BOOTTIME owner clock.
func TestCraftNativeHTTPHeartbeatRenewsServerSideAndStopsOnReadLoss(t *testing.T) {
	f, m, ctx := craftNativeHTTPFixture(t)
	clock, err := mooring.NewBootOwnerClock()
	if err != nil {
		t.Fatal(err)
	}
	m = newCraftNativeHTTP(m.pages, true, clock)
	token, _ := craftNativeIssued(t, m, ctx)
	if w := craftNativeLeaseCall(m, ctx, token, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound := context.WithValue(context.WithValue(context.WithValue(r.Context(), ctxUser, UserFromContext(ctx)), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID)
		bound = auth.WithOriginalCredential(bound, *auth.OriginalCredentialFromContext(ctx))
		r = r.WithContext(bound)
		r.SetPathValue("slug", "native-page")
		m.events(w, r)
	}))
	defer server.Close()
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Craft-Session", token)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	next := func() map[string]any {
		t.Helper()
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				var result map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
		}
		t.Fatalf("stream ended: %v", scanner.Err())
		return nil
	}
	if first := next(); first["can_write"] != true {
		t.Fatal(first)
	}
	var before, after int64
	if err = f.db.QueryRow(`SELECT expires_ns FROM mooring_edit_leases WHERE page_id='native-page'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if second := next(); second["can_write"] != true {
		t.Fatal(second)
	}
	if err = f.db.QueryRow(`SELECT expires_ns FROM mooring_edit_leases WHERE page_id='native-page'`).Scan(&after); err != nil || after <= before {
		t.Fatal("server heartbeat failed to extend lease", before, after, err)
	}
	mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2026-10-27T00:00:00Z' WHERE id='native-source'`)
	if retired := next(); retired["delivery"] != "stopped" || retired["draft"] != "purge" {
		t.Fatal("read loss did not stop delivery and request purge", retired)
	}
}

func TestCraftNativeHTTPFreshCredentialExtendsEstablishedStream(t *testing.T) {
	f, m, ctx := craftNativeHTTPFixture(t)
	clock, err := mooring.NewBootOwnerClock()
	if err != nil {
		t.Fatal(err)
	}
	m = newCraftNativeHTTP(m.pages, true, clock)
	old := craftNativeFreshContext(t, f, ctx, 4*time.Second)
	token, _ := craftNativeIssued(t, m, old)
	if w := craftNativeLeaseCall(m, old, token, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound := context.WithValue(context.WithValue(context.WithValue(r.Context(), ctxUser, UserFromContext(old)), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID)
		r = r.WithContext(auth.WithOriginalCredential(bound, *auth.OriginalCredentialFromContext(old)))
		r.SetPathValue("slug", "native-page")
		m.events(w, r)
	}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Craft-Session", token)
	response, err := (&http.Client{Timeout: 12 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	next := func() map[string]any {
		t.Helper()
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				var data map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
					t.Fatal(err)
				}
				return data
			}
		}
		t.Fatalf("stream ended: %v", scanner.Err())
		return nil
	}
	if first := next(); first["can_write"] != true {
		t.Fatal(first)
	}
	fresh := craftNativeFreshContext(t, f, ctx, 59*time.Minute)
	if w := craftNativeReauthorize(m, fresh, token); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	// The next 5s server heartbeat is after the original authenticated 4s source
	// horizon; only the separately authenticated renewal may keep it alive.
	if afterOldExpiry := next(); afterOldExpiry["can_write"] != true {
		t.Fatal("live stream ignored fresh authenticated source", afterOldExpiry)
	}
}

func TestCraftNativeRoutesTwoHumansSeeNamedHolderAndHandoff(t *testing.T) {
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
