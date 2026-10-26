package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptanceGuestCrewReadCLI(t *testing.T) {
	const item = `{"id":"crew-shared","name":"Shared crew","slug":"shared","restricted":true,"agent_count":1,"agents":[{"id":"a1","name":"Agent","slug":"agent","agent_role":"AGENT","status":"IDLE"}]}`
	const decision = `{"crew_id":"crew-shared","actions":{"view":{"state":"allowed","reason":"explicit_crew_share"},"use":{"state":"conditional","reason":"agent_authority_and_runtime_preflight_required"},"manage":{"state":"denied","reason":"guest_access_ceiling"}}}`
	for _, format := range []string{"table", "json", "yaml"} {
		for _, command := range []string{"list", "get", "access"} {
			t.Run(command+"-"+format, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method != "GET" {
						t.Errorf("unexpected mutation %s", r.Method)
						w.WriteHeader(405)
						return
					}
					switch r.URL.Path {
					case "/api/v1/workspaces":
						_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					case "/api/v1/crews":
						w.Header().Set("X-Crew-Access-Mode", "restricted")
						w.Header().Set("X-Total-Count", "1")
						_, _ = io.WriteString(w, "["+item+"]")
					case "/api/v1/crews/crew-shared":
						w.Header().Set("X-Crew-Access-Mode", "restricted")
						_, _ = io.WriteString(w, item)
					case "/api/v1/crews/crew-shared/access/me":
						_, _ = io.WriteString(w, decision)
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer server.Close()
				args := []string{"crew", command}
				if command != "list" {
					args = append(args, "shared")
				}
				args = append(args, "--format", format)
				out, err := runGuestCLI(t, server.URL, "", args...)
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				for _, hidden := range []string{"NETWORK", "MEMORY", "CPUS", "free", "container_memory_mb", "mcp_config", "runtime_image", "allow_private_endpoints"} {
					if strings.Contains(out, hidden) {
						t.Fatalf("fabricated private configuration %s: %s", hidden, out)
					}
				}
				want := "Shared crew"
				if command == "access" {
					want = "guest_access_ceiling"
				}
				if !strings.Contains(out, want) {
					t.Fatalf("missing %s: %s", want, out)
				}
				if format == "json" {
					var document any
					if err = json.Unmarshal([]byte(out), &document); err != nil {
						t.Fatal(err, out)
					}
				}
			})
		}
	}
}
