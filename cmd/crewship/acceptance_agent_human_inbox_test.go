package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptance_AgentHumanInboxReadsThroughCLI(t *testing.T) {
	const agent = "c123456789012345678901234"
	srv := newFleetStub(t, nil, fleetRoute{
		method: http.MethodGet,
		path:   "/api/v1/agents/" + agent,
		body:   `{"id":"` + agent + `"}`,
	}, fleetRoute{
		method: http.MethodGet,
		path:   "/api/v1/agents/" + agent + "/human-inbox",
		body:   `{"items":[{"id":"fixture","kind":"channel_message","read_at":null}]}`,
	})
	out, err := runFleetCLI(t, fleetStubConfig(t, srv.URL), "--format", "json", "agent", "inbox", "human", agent)
	if err != nil || !strings.Contains(out, "fixture") {
		t.Fatalf("human inbox CLI: %v\n%s", err, out)
	}
}

func TestAcceptance_AgentInboxReadUsesAuthenticatedSidecar(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "synthetic-live-run-proof")
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			r.URL.Path != "/mcp/routines" || r.Method != http.MethodPost ||
			r.Header.Get("Authorization") != "Bearer synthetic-live-run-proof" ||
			req.Method != "tools/call" || req.Params.Name != "inbox_read" || len(req.Params.Arguments) != 0 {
			http.Error(w, "unexpected sidecar request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"id\":\"fixture\",\"state\":\"read\"}"}]}}`))
	}))
	defer sidecar.Close()
	out, err := runFleetCLI(t, fleetStubConfig(t, sidecar.URL), "--format", "json", "agent", "inbox", "read", "--sidecar", sidecar.URL)
	if err != nil || !strings.Contains(out, "fixture") {
		t.Fatalf("run inbox CLI: %v\n%s", err, out)
	}
}
