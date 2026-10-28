//go:build linux

package api

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Real cookies, refresh route and Router middleware: the original SSE context
// must expire even after the application admission accepts a fresh credential.
// A new authenticated GET keeps the same admission/generation and saved bytes.
func TestCraftNativeRouterCookieRotationKeepsCustodyBeyondOriginalJWTHorizon(t *testing.T) {
	f, router, _, _, _, cfg := craftNativeDataTCPFixture(t, true)
	server := httptest.NewServer(router)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	client.Timeout = 12 * time.Second
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := router.sessionsStore.Create(t.Context(), f.userID, "native rotation fixture", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	validator := router.authMw.validator
	validator.SetClock(func() time.Time { return time.Now().Add(-auth.AccessTokenTTL - 2*time.Second) })
	oldAccess, err := validator.IssueAccessToken(f.userID, session.ID, "Pavel", "pavel@example.invalid")
	validator.SetClock(nil)
	if err != nil {
		t.Fatal(err)
	}
	oldClaims, err := validator.ValidateAccess(oldAccess)
	if err != nil {
		t.Fatal(err)
	}
	oldHorizon := time.Unix(oldClaims.Exp+6, 0)
	refresh, err := validator.IssueRefreshToken(f.userID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(origin, []*http.Cookie{{Name: "authjs.session-token", Value: oldAccess, Path: "/"}, {Name: "authjs.refresh-token", Value: refresh, Path: "/api/auth"}})
	base := server.URL + "/api/v1/pages/@native-data-page/craft/"
	query := "?workspace_id=" + url.QueryEscape(f.wsID)
	call := func(method, path, admission string, body any) (int, json.RawMessage) {
		t.Helper()
		var raw []byte
		if body != nil {
			var e error
			raw, e = json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
		}
		target := base + path + query
		if strings.HasPrefix(path, "/api/auth/") {
			target = server.URL + path
		}
		req, e := http.NewRequest(method, target, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", server.URL)
		req.Header.Set("X-Craft-Session", admission)
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		data, e := io.ReadAll(io.LimitReader(response.Body, 46<<20))
		if e != nil {
			t.Fatal(e)
		}
		return response.StatusCode, data
	}
	status, raw := call("POST", "sessions", "", struct{}{})
	if status != 201 {
		t.Fatalf("admit: %d", status)
	}
	var admitted struct {
		Token string `json:"session_token"`
		ID    string `json:"session_id"`
	}
	if err = json.Unmarshal(raw, &admitted); err != nil {
		t.Fatal(err)
	}
	status, raw = call("POST", "lease/acquire", admitted.Token, struct{}{})
	if status != 200 {
		t.Fatalf("acquire: %d", status)
	}
	var lease struct {
		Generation string `json:"generation"`
		Mine       bool   `json:"mine"`
	}
	if err = json.Unmarshal(raw, &lease); err != nil || !lease.Mine {
		t.Fatal("initial custody unconfirmed")
	}
	saved := []byte(`{"v":2,"text":"saved before credential rotation"}`)
	body := craftNativeDataTCPBody(f, cfg, admitted.ID, lease.Generation, "1", "before-auth-rotation", saved)
	status, raw = call("POST", "document/save", admitted.Token, body)
	if status != 200 {
		t.Fatalf("save: %d", status)
	}
	var receipt struct {
		Version string `json:"version"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil || receipt.Version != "2" {
		t.Fatal("save not committed")
	}
	openStream := func() (*http.Response, *bufio.Scanner) {
		t.Helper()
		for attempt := 0; attempt < 8; attempt++ {
			req, e := http.NewRequest(http.MethodGet, base+"events"+query, nil)
			if e != nil {
				t.Fatal(e)
			}
			req.Header.Set("X-Craft-Session", admitted.Token)
			response, e := client.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			if response.StatusCode == 409 {
				response.Body.Close()
				time.Sleep(25 * time.Millisecond)
				continue
			}
			if response.StatusCode != 200 {
				response.Body.Close()
				t.Fatalf("stream: %d", response.StatusCode)
			}
			return response, bufio.NewScanner(response.Body)
		}
		t.Fatal("previous metadata connection did not close")
		return nil, nil
	}
	next := func(scanner *bufio.Scanner) map[string]any {
		t.Helper()
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				var value map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &value); err != nil {
					t.Fatal(err)
				}
				return value
			}
		}
		t.Fatalf("metadata ended: %v", scanner.Err())
		return nil
	}
	oldStream, oldScanner := openStream()
	defer oldStream.Body.Close()
	if view := next(oldScanner); view["mine"] != true || view["generation"] != lease.Generation {
		t.Fatal("initial stream custody mismatch")
	}
	status, _ = call("POST", "/api/auth/token/refresh", "", nil)
	if status != 200 {
		t.Fatalf("ordinary cookie refresh: %d", status)
	}
	status, raw = call("POST", "sessions/reauthorize", admitted.Token, struct{}{})
	if status != 200 {
		t.Fatalf("same-admission renewal: %d", status)
	}
	var renewal struct {
		Renewed bool `json:"renewed"`
	}
	if err = json.Unmarshal(raw, &renewal); err != nil || !renewal.Renewed {
		t.Fatal("fresh original credential did not renew admission")
	}
	// No middleware fence is weakened: this already-authenticated connection
	// still ends at its ORIGINAL JWT horizon rather than inheriting the new one.
	for oldScanner.Scan() {
		if strings.HasPrefix(oldScanner.Text(), "data: ") {
			t.Fatal("old authenticated stream delivered beyond its JWT horizon")
		}
	}
	if time.Now().Before(oldHorizon) {
		t.Fatal("old stream ended before the expiry calibration")
	}
	oldStream.Body.Close()
	freshStream, freshScanner := openStream()
	defer freshStream.Body.Close()
	if view := next(freshScanner); view["mine"] != true || view["can_write"] != true || view["generation"] != lease.Generation {
		t.Fatal("new authenticated GET changed or lost custody")
	}
	status, raw = call("GET", "document", admitted.Token, nil)
	if status != 200 {
		t.Fatalf("fresh read beyond original JWT: %d", status)
	}
	var snapshot struct {
		Bytes   []byte `json:"bytes"`
		Version string `json:"version"`
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != "2" || !bytes.Equal(snapshot.Bytes, saved) {
		t.Fatalf("rotation changed saved version/hash: version %q, hash %x", snapshot.Version, sha256.Sum256(snapshot.Bytes))
	}
	later := []byte(`{"v":3,"text":"saved after credential rotation"}`)
	status, _ = call("POST", "document/save", admitted.Token, craftNativeDataTCPBody(f, cfg, admitted.ID, lease.Generation, "2", "after-auth-rotation", later))
	if status != 200 {
		t.Fatalf("same-generation post-rotation save: %d", status)
	}
	if err = router.sessionsStore.Revoke(t.Context(), session.ID, sessions.ReasonLogout); err != nil {
		t.Fatal(err)
	}
	for freshScanner.Scan() {
		if line := freshScanner.Text(); strings.HasPrefix(line, "data: ") {
			var value map[string]any
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &value); err != nil {
				t.Fatal(err)
			}
			if value["delivery"] != "stopped" {
				t.Fatal("credential revocation delivered another live lease")
			}
		}
	}
	status, _ = call("GET", "document", admitted.Token, nil)
	if status != 401 {
		t.Fatalf("revoked credential read: %d", status)
	}
	t.Logf("original horizon expired; same admission/generation retained native version2; post-rotation save committed; revocation stopped stream and read (%s)", fmt.Sprint(oldHorizon.Unix()))
}
