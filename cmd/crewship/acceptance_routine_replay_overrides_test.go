package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAcceptanceRoutineReplayOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"inherit", nil, `{}`},
		{"replace", []string{"--inputs", `{"account":"new"}`, "--metadata", `{"source":"operator"}`, "--tags", `["fixed"]`, "--version", "1"}, `{"inputs":{"account":"new"},"metadata":{"source":"operator"},"tags":["fixed"],"pinned_version":1}`},
		{"clear", []string{"--inputs", `{}`, "--metadata", `{}`, "--tags", `[]`}, `{"inputs":{},"metadata":{},"tags":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces" {
					_, _ = w.Write([]byte(`[{"id":"ws_test","slug":"ws_test"}]`))
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/workspaces/ws_test/pipelines/runs/original/replay" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode: %v", err)
				}
				select {
				case received <- body:
				default:
					t.Error("CLI sent replay more than once")
				}
				_, _ = w.Write([]byte(`{"run_id":"replayed","status":"COMPLETED"}`))
			}))
			defer srv.Close()
			out, err := runChatListCLI(t, srv.URL, append([]string{"routine", "replay", "original"}, tc.args...)...)
			if err != nil || !strings.Contains(out, "replayed") {
				t.Fatalf("replay: %v %s", err, out)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-received:
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("request=%#v want=%#v", got, want)
				}
			default:
				t.Fatal("CLI did not send replay")
			}
		})
	}
}
