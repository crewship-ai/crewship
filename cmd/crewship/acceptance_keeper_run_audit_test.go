package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAcceptance_KeeperAuditPreservesRunIdentity(t *testing.T) {
	for _, command := range []string{"requests", "history"} {
		t.Run(command, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/admin/keeper/requests", "/api/v1/admin/keeper/requests/fixture-request/events":
					_, _ = w.Write([]byte(`[{"id":"fixture-request","requesting_run_id":"msg_verified_fixture","requesting_agent_id":"fixture-agent","agent_id":"fixture-agent","credential_id":"fixture-credential","state":"ALLOW","decision":"ALLOW","seq":2,"actor_type":"keeper","created_at":"2026-11-18T00:00:00Z","recorded_at":"2026-11-18T00:00:00Z"}]`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			args := []string{"keeper", command}
			if command == "history" {
				args = append(args, "fixture-request")
			}
			args = append(args, "-f", "json")
			out, err := runIssueStopCLI(t, srv.URL, args...)
			if err != nil {
				t.Fatalf("binary: %v %s", err, out)
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0]["requesting_run_id"] != "msg_verified_fixture" {
				t.Fatalf("CLI lost verified calling run: %s (%v)", out, err)
			}
		})
	}
}
