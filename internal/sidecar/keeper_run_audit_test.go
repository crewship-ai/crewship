package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestKeeperBridgeAttributesVerifiedCallingRun(t *testing.T) {
	for _, endpoint := range []string{"request", "execute"} {
		for _, legacy := range []bool{false, true} {
			t.Run(endpoint+map[bool]string{false: "/v2", true: "/legacy"}[legacy], func(t *testing.T) {
				s, key := newRunIdentityServer(t)
				// A shared sidecar's current IPC boot metadata is not authority
				// for the individual caller's signed run.
				s.ipc.RunID = "stale-sidecar-boot"
				s.ipc.ContainerID = "fixture-container"
				var got map[string]string
				host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					writeJSONResponse(w, http.StatusOK, map[string]any{"decision": "ALLOW", "exit_code": 0})
				}))
				defer host.Close()
				s.ipc.BaseURL = host.URL
				s.ipc.Token = "synthetic-ipc"
				token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
				if legacy {
					token = internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot")
				}
				r := httptest.NewRequest(http.MethodPost, "/keeper/"+endpoint, strings.NewReader(`{"credential_id":"credential-fixture","intent":"fixture access","command":"true","requesting_run_id":"spoofed-run","agent_slug":"peer"}`))
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				if endpoint == "request" {
					s.handleKeeperRequest(w, r)
				} else {
					s.handleKeeperExecute(w, r)
				}
				if w.Code != http.StatusOK {
					t.Fatalf("bridge returned %d: %s", w.Code, w.Body.String())
				}
				if got["requesting_agent_id"] != "agent-boot" {
					t.Fatal("bridge lost verified agent")
				}
				want := "run-boot"
				if legacy {
					want = ""
				}
				if got["requesting_run_id"] != want {
					t.Fatalf("calling run=%q, want %q (neither body nor boot fallback)", got["requesting_run_id"], want)
				}
			})
		}
	}
}
