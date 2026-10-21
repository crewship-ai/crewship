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

func TestAcceptanceGuestRoutineViewCLI(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				if r.Method != "GET" || r.URL.Path != "/api/v1/workspaces/ws_test/restricted-routine-library" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				calls.Add(1)
				_, _ = io.WriteString(w, `[{"id":"r1","slug":"shared","name":"Client recipe","status":"active","can_view":true,"can_run":false,"runtime_ready":false}]`)
			}))
			defer server.Close()
			output, err := runGuestCLI(t, server.URL, "", "routine", "shared", "--format", format)
			if err != nil || calls.Load() != 1 {
				t.Fatalf("calls=%d err=%v output=%s", calls.Load(), err, output)
			}
			if format == "table" {
				for _, text := range []string{"ROUTINE", "NAME", "VIEW", "RUN", "RUNTIME READY", "Client recipe", "true", "false"} {
					if !strings.Contains(output, text) {
						t.Fatalf("missing %q: %s", text, output)
					}
				}
			} else {
				var items []map[string]any
				if format == "json" {
					err = json.Unmarshal([]byte(output), &items)
				} else {
					err = yaml.Unmarshal([]byte(output), &items)
				}
				if err != nil || len(items) != 1 || items[0]["can_view"] != true || items[0]["can_run"] != false || items[0]["runtime_ready"] != false {
					t.Fatalf("projection %+v err=%v", items, err)
				}
			}
		})
	}
	for _, format := range []string{"json", "yaml"} {
		t.Run("definition-"+format, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				if r.Method != "GET" || r.URL.Path != "/api/v1/workspaces/ws_test/restricted-routine-library/shared" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				_, _ = io.WriteString(w, `{"id":"r1","slug":"shared","name":"Client recipe","status":"active","can_view":true,"can_run":false,"runtime_ready":false,"definition":{"steps":[],"name":"Shared definition"},"definition_hash":"hash","version":2}`)
			}))
			defer server.Close()
			output, err := runGuestCLI(t, server.URL, "", "routine", "shared", "shared", "--format", format)
			if err != nil {
				t.Fatalf("err=%v output=%s", err, output)
			}
			var item map[string]any
			if format == "json" {
				err = json.Unmarshal([]byte(output), &item)
			} else {
				err = yaml.Unmarshal([]byte(output), &item)
			}
			if err != nil || item["definition"].(map[string]any)["name"] != "Shared definition" || item["definition_hash"] != "hash" {
				t.Fatalf("definition %+v err=%v", item, err)
			}
		})
	}
}
