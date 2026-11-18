package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// A single running sidecar must not treat its boot peer snapshot as the
// membership authority: it excludes boot itself and cannot see hires/revokes.
// The host already authorizes current actors and targets within bound crews.
func TestDelegationUsesHostMembershipWithoutSidecarRestart(t *testing.T) {
	for _, query := range []bool{false, true} {
		t.Run(map[bool]string{false: "assignment", true: "query"}[query], func(t *testing.T) {
			s, key := newRunIdentityServer(t)
			var calls atomic.Int32
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", 500)
					return
				}
				if body["actor_agent_id"] != "agent-peer" || body["source_run_id"] != "peer-current-run" || body["crew_id"] != "crew-1" || body["workspace_id"] != "ws-1" {
					t.Errorf("lost bound actor/run/scope: %+v", body)
				}
				if r.Header.Get("X-Internal-Token") != s.ipc.Token {
					t.Error("lost internal binding")
				}
				if body["target_slug"] == "peer" {
					w.WriteHeader(http.StatusNotFound)
					w.Write([]byte(`{"error":"target agent not found"}`))
					return
				}
				w.Write([]byte(`{"id":"accepted"}`))
			}))
			defer host.Close()
			s.ipc.BaseURL = host.URL
			token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-peer", "peer-current-run")
			for _, tc := range []struct {
				target string
				code   int
			}{{"boot", 200}, {"new-peer", 200}, {"peer", 404}} {
				raw := `{"target":"` + tc.target + `","task":"work"}`
				if query {
					raw = `{"target":"` + tc.target + `","question":"question"}`
				}
				req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+token)
				rr := httptest.NewRecorder()
				if query {
					s.handleQuery(rr, req)
				} else {
					s.handleAssign(rr, req)
				}
				if rr.Code != tc.code {
					t.Fatalf("%s status=%d want=%d: %s", tc.target, rr.Code, tc.code, rr.Body.String())
				}
			}
			if calls.Load() != 3 {
				t.Fatalf("host checked %d requests, want 3", calls.Load())
			}
			// New membership resolution must never downgrade unrecognized actor auth.
			req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(`{"target":"boot","task":"work","question":"q"}`))
			req.Header.Set("Authorization", "Bearer forged")
			rr := httptest.NewRecorder()
			if query {
				s.handleQuery(rr, req)
			} else {
				s.handleAssign(rr, req)
			}
			if rr.Code != http.StatusForbidden || calls.Load() != 3 {
				t.Fatalf("forged actor reached host: code=%d calls=%d", rr.Code, calls.Load())
			}
		})
	}
}
