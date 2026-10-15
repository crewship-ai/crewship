package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAcceptanceGuestRoutineResultsCLI(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, format := range []string{"table", "json", "yaml"} {
			t.Run(format+map[bool]string{false: "-list", true: "-detail"}[detail], func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/workspaces" {
						_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
						return
					}
					path := "/api/v1/workspaces/ws_test/restricted-routine-library/žlutá & %/runs"
					if detail {
						path += "/run-1"
					}
					if r.Method != "GET" || r.URL.Path != path {
						t.Errorf("unexpected %s %s", r.Method, r.URL)
						w.WriteHeader(404)
						return
					}
					if !detail && (r.URL.Query().Get("limit") != "7" || r.URL.Query().Get("offset") != "14") {
						t.Errorf("pagination %s", r.URL)
					}
					calls.Add(1)
					body := `{"run_id":"run-1","routine_id":"routine-1","status":"completed","mode":"run","started_at":"2026-10-15T00:00:00Z","ended_at":null`
					if detail {
						body += `,"output":"FINAL_RESULT_CANARY","step_outputs":{"answer":"STEP_RESULT_CANARY"}}`
					} else {
						body = "[" + body + "}]"
					}
					_, _ = io.WriteString(w, body)
				}))
				defer server.Close()
				args := []string{"routine", "shared-runs", "žlutá & %", "--format", format}
				if detail {
					args = append(args, "run-1")
				} else {
					args = append(args, "--limit", "7", "--offset", "14")
				}
				output, err := runGuestCLI(t, server.URL, "", args...)
				if err != nil || calls.Load() != 1 {
					t.Fatalf("calls%d err%v output%s", calls.Load(), err, output)
				}
				if format == "table" {
					for _, text := range []string{"RUN", "STATUS", "MODE", "STARTED", "run-1", "completed"} {
						if !strings.Contains(output, text) {
							t.Fatalf("missing %s: %s", text, output)
						}
					}
					if detail {
						for _, text := range []string{"OUTPUT", "STEP OUTPUTS", "FINAL_RESULT_CANARY", "STEP_RESULT_CANARY"} {
							if !strings.Contains(output, text) {
								t.Fatalf("missing %s: %s", text, output)
							}
						}
					}
				} else {
					var body any
					if format == "json" {
						err = json.Unmarshal([]byte(output), &body)
					} else {
						err = yaml.Unmarshal([]byte(output), &body)
					}
					if err != nil {
						t.Fatal(err, output)
					}
					if detail {
						item, ok := body.(map[string]any)
						if !ok || item["run_id"] != "run-1" || item["output"] != "FINAL_RESULT_CANARY" {
							t.Fatal("detail", body)
						}
					} else {
						items, ok := body.([]any)
						if !ok || len(items) != 1 {
							t.Fatal("list", body)
						}
					}
				}
			})
		}
	}
}

func TestAcceptanceGuestRoutineResultsRejectInvalidOptions(t *testing.T) {
	for _, args := range [][]string{{".."}, {"routine", ".."}, {"routine", "--limit", "0"}, {"routine", "--offset", "-1"}, {"routine", "run-1", "--offset", "0"}} {
		var calls atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
		output, err := runGuestCLI(t, server.URL, "", append([]string{"routine", "shared-runs"}, args...)...)
		server.Close()
		if err == nil || calls.Load() != 0 {
			t.Fatalf("%v err%v calls%d output%s", args, err, calls.Load(), output)
		}
	}
}
