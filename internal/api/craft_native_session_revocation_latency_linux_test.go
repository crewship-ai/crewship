//go:build linux

package api

import (
	"bufio"
	"bytes"
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

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// The real Router must end an idle native metadata stream on an in-process
// revocation without waiting for the five-second Page-authority poll. This
// measures a synthetic local TCP fixture, not a universal network SLA.
func TestCraftNativeRouterSessionRevokeClosesIdleStreamWithinOneSecond(t *testing.T) {
	f, router, _, _, _, _ := craftNativeDataTCPFixture(t, true)
	server := httptest.NewServer(router)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	// No client timeout or cancel may manufacture the EOF being measured.
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := router.sessionsStore.Create(t.Context(), f.userID, "native latency fixture", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	access, err := router.authMw.validator.IssueAccessToken(f.userID, session.ID, "Alice", "alice@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(origin, []*http.Cookie{{Name: "authjs.session-token", Value: access, Path: "/"}})
	base := server.URL + "/api/v1/pages/@native-data-page/craft/"
	query := "?workspace_id=" + url.QueryEscape(f.wsID)
	admit, err := http.NewRequest(http.MethodPost, base+"sessions"+query, bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}
	admit.Header.Set("Content-Type", "application/json")
	admit.Header.Set("Origin", server.URL)
	response, err := client.Do(admit)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("admit: %d %s", response.StatusCode, raw)
	}
	var admitted struct {
		Token string `json:"session_token"`
	}
	if err := json.Unmarshal(raw, &admitted); err != nil || admitted.Token == "" {
		t.Fatalf("admission token: %v", err)
	}
	request, err := http.NewRequest(http.MethodGet, base+"events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Craft-Session", admitted.Token)
	stream, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", stream.StatusCode)
	}
	scanner := bufio.NewScanner(stream.Body)
	ready := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var lease map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &lease); err != nil {
				t.Fatal(err)
			}
			if _, ok := lease["can_write"]; !ok {
				t.Fatalf("initial metadata is not a lease: %s", line)
			}
			ready = true
		}
		if ready && line == "" {
			break
		}
	}
	if !ready || scanner.Err() != nil {
		t.Fatalf("stream not ready: %v", scanner.Err())
	}
	// Consume independently, but do not close/cancel the body before the result.
	// The bounded failure cleanup closes it only after the assertion has failed.
	ended := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				var value map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &value); err != nil {
					ended <- err
					return
				}
				if value["delivery"] != "stopped" {
					ended <- fmt.Errorf("live metadata after ready/revoke: %s", line)
					return
				}
			}
		}
		ended <- scanner.Err()
	}()
	// A clean EOF already queued before Revoke must not count as causation.
	// Observe a short open interval without canceling or timing out the client.
	openTimer := time.NewTimer(50 * time.Millisecond)
	select {
	case err := <-ended:
		openTimer.Stop()
		t.Fatalf("stream ended before session revocation: %v", err)
	case <-openTimer.C:
	}
	select {
	case err := <-ended:
		t.Fatalf("stream already ended at the revocation boundary: %v", err)
	default:
	}
	started := time.Now()
	if err := router.sessionsStore.Revoke(t.Context(), session.ID, sessions.ReasonLogout); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(started.Add(time.Second)))
	defer timer.Stop()
	select {
	case err := <-ended:
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("stream did not end with clean EOF: %v", err)
		}
		if elapsed >= time.Second {
			t.Fatalf("session revoke to EOF: %s, want <1s", elapsed)
		}
		t.Logf("in-process committed session revoke to real TCP EOF: %s", elapsed)
	case <-timer.C:
		stream.Body.Close()
		<-ended
		t.Fatal("idle stream did not reach EOF within 1s of session revocation")
	}
}
