package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestBoardTransportForwardsActualRunAndBounds(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "crew-ipc" {
			t.Error("actual credentials not forwarded")
		}
		expected := "/api/v1/internal/boards/report"
		if r.Method == "POST" {
			expected += "/batches"
		}
		if r.URL.Path != expected {
			t.Errorf("unexpected host path %s", r.URL.Path)
		}
		if r.Method == "POST" {
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"mutation_id":"one","base_seq":0}` {
				t.Errorf("body %s", body)
			}
		}
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"seq":1}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "crew-ipc"
	for _, method := range []string{"GET", "POST"} {
		path := "/boards/report"
		body := ""
		if method == "POST" {
			path += "/batches"
			body = `{"mutation_id":"one","base_seq":0}`
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		s.handleBoardPublish(rr, r)
		if rr.Code != 200 || rr.Header().Get("Retry-After") != "5" {
			t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
		}
	}
	for _, bad := range []string{"", token + "bad", internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot")} {
		r := httptest.NewRequest("GET", "/boards/report", nil)
		r.Header.Set("Authorization", "Bearer "+bad)
		rr := httptest.NewRecorder()
		s.handleBoardPublish(rr, r)
		if rr.Code != 403 {
			t.Fatalf("bad credential status %d", rr.Code)
		}
	}
	r := httptest.NewRequest("POST", "/boards/report/batches", strings.NewReader(strings.Repeat("x", 2*1024*1024+1)))
	r.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	s.handleBoardPublish(rr, r)
	if rr.Code != 413 || calls != 2 {
		t.Fatalf("unbounded request forwarded status%d calls%d", rr.Code, calls)
	}
}
