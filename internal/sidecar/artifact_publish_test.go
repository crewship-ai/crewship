package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactTransportForwardsActualRunBearer(t *testing.T) {
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
			t.Error("credentials not forwarded")
		}
		if r.URL.Path != "/api/v1/internal/artifacts/report/revisions" {
			t.Errorf("path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"html":"x","base_rev":0,"mutation_id":"one"}` {
			t.Errorf("body %s", b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		w.Write([]byte(`{"revision":1}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "crew-ipc"
	r := httptest.NewRequest("POST", "/artifacts/report/revisions", strings.NewReader(`{"html":"x","base_rev":0,"mutation_id":"one"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	s.handleArtifactPublish(rr, r)
	if rr.Code != 201 || calls != 1 {
		t.Fatalf("status %d calls %d body %s", rr.Code, calls, rr.Body.String())
	}
	for _, bad := range []string{"", internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot"), token + "bad"} {
		r := httptest.NewRequest("POST", "/artifacts/report/revisions", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+bad)
		rr := httptest.NewRecorder()
		s.handleArtifactPublish(rr, r)
		if rr.Code != 403 {
			t.Fatalf("bad token status %d", rr.Code)
		}
	}
	if calls != 1 {
		t.Fatal("invalid credentials reached host")
	}
}

func TestArtifactHostResponseLimitIsExplicit(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	r := httptest.NewRequest("GET", "/artifacts/report", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if _, err := s.boundedHostRequest(r, "GET", "/test", nil, 1024); err == nil {
		t.Fatal("silently truncated oversized response")
	}
	response, err := s.boundedHostRequest(r, "GET", "/test", nil, 4096)
	if err != nil || len(response.body) != 2048 {
		t.Fatalf("bounded response %v", err)
	}
}
