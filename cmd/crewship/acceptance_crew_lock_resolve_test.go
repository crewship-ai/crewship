package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAcceptanceRemoteCrewLockResolveCLI(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mise.json")
	if err := os.WriteFile(file, []byte(`{"tools":{"node":"22"},"env":{"PRIVATE":"must-not-send"},"tasks":{"evil":"must-not-send"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			w.Write([]byte(`[{"id":"ws_test","slug":"ws_test"}]`))
		case "/api/v1/crews":
			w.Write([]byte(`[{"id":"crew-test","slug":"fixture"}]`))
		case "/api/v1/crews/crew-test/provision/lock-resolve":
			calls.Add(1)
			var body map[string]json.RawMessage
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid request")
				w.WriteHeader(400)
				return
			}
			raw, _ := json.Marshal(body)
			if strings.Contains(string(raw), "must-not-send") || strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "image") {
				t.Error("unsafe resolver inputs", string(raw))
				w.WriteHeader(400)
				return
			}
			if string(body["bump"]) != "true" || !strings.Contains(string(body["config"]), `"node":"22"`) {
				t.Error("missing selectors/bump", string(raw))
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"resolver_image_id":"sha256:server-owned","lock":{"schema_version":1,"files":{"mise.lock":"native"}}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	out, err := runGuestCLI(t, srv.URL, "", "crew", "provision", "lock-resolve", file, "--crew", "fixture", "--bump")
	if err != nil || !strings.Contains(out, "sha256:server-owned") || calls.Load() != 1 {
		t.Fatalf("remote resolve: %s %v calls=%d", out, err, calls.Load())
	}
	for _, flags := range [][]string{{"--crew", "fixture", "--image", "sha256:caller"}, {}} {
		args := append([]string{"crew", "provision", "lock-resolve", file}, flags...)
		if out, err := runGuestCLI(t, srv.URL, "", args...); err == nil {
			t.Fatalf("invalid selection accepted: %s", out)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid selection reached server")
	}
}
