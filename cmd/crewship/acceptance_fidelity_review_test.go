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

// Drive the binary at the affected response boundaries, including server fields
// this CLI version does not know about and omitted legacy zero values.
func TestAcceptance_FidelityReviewRegressions(t *testing.T) {
	const crew = "cdeletedcrew00000000000001"
	var filtered atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/pipelines/demo"):
			_, _ = w.Write([]byte(`{"id":"routine","slug":"demo","new_field":{"count":9007199254740993},"description":"kept"}`))
		case strings.HasSuffix(r.URL.Path, "/pipelines"):
			_, _ = w.Write([]byte(`[{"id":"routine","slug":"demo","new_field":{"count":9007199254740993}}]`))
		case r.Method == "DELETE":
			_, _ = w.Write([]byte(`not-json`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/crews/"):
			if r.URL.Query().Get("unused") == "1" {
				t.Error("unexpected query")
			}
			// This crew exists for deletion; a different CUID is absent for history.
			if strings.Contains(r.URL.Path, "chistorycrew00000000000001") {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":"not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + crew + `","slug":"gone","name":"Gone"}`))
		case r.URL.Path == "/api/v1/crews":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/inbox":
			filtered.Store(r.URL.Query().Get("crew") == "chistorycrew00000000000001" && r.URL.Query().Get("space") == "history")
			_, _ = w.Write([]byte(`{"rows":[],"total":0}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: cfidws00000000000000001\ntoken: crewship_cli_x\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rig := &fidelityRig{t: t, config: cfg}
	for _, list := range []bool{false, true} {
		args := []string{"routine", "get", "demo", "-f", "json"}
		if list {
			args = []string{"routine", "list", "-f", "json"}
		}
		out := rig.stdout(args...)
		var doc map[string]json.RawMessage
		if list {
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
				t.Fatalf("rows: %s %v", out, err)
			}
			doc = rows[0]
		} else {
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
		}
		for _, key := range []string{"description", "last_invoked_at", "last_invocation_status", "author_user_id", "author_crew_id", "author_agent_id"} {
			if _, ok := doc[key]; !ok {
				t.Errorf("legacy key %s missing from %s", key, out)
			}
		}
		if string(doc["last_invoked_at"]) != "null" {
			t.Errorf("last_invoked_at=%s", doc["last_invoked_at"])
		}
		if !strings.Contains(string(doc["new_field"]), "9007199254740993") {
			t.Errorf("unknown field changed: %s", out)
		}
		if !list && string(doc["description"]) != `"kept"` {
			t.Errorf("existing value changed: %s", out)
		}
	}
	receipt := decodeDoc(t, rig.stdout("crew", "delete", crew, "--yes", "-f", "json"))
	if receipt["deleted"] != true || receipt["crew_id"] != crew {
		t.Fatalf("missing fallback receipt: %v", receipt)
	}
	rig.stdout("inbox", "list", "--space", "history", "--crew", "chistorycrew00000000000001", "-f", "json")
	if !filtered.Load() {
		t.Fatal("deleted crew history filter was not forwarded")
	}
}

func TestAcceptance_FidelityMemoryFileBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (128<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	rig := &fidelityRig{t: t}
	// Validation must happen before client/config setup or any network call.
	out, err := rig.run(nil, "memory", "initialize", "--crew", "backend", "--doc", "CREW.md=@"+path)
	if err == nil || !strings.Contains(out, "128 KiB") {
		t.Fatalf("oversized file not refused locally: %v %s", err, out)
	}
}
