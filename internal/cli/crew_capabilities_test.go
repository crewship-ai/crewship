package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCrewCapabilities_FetchesAndDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/crews/crew-1/capabilities") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"crew_id": "crew-1", "crew_slug": "acct",
			"container":    map[string]any{"tools": []map[string]string{{"name": "terraform"}}},
			"integrations": []map[string]any{{"name": "gmail", "tools": []string{"GMAIL_FETCH_EMAIL"}}},
			"agents":       []map[string]any{{"slug": "parse", "name": "Parser"}},
			"runtimes": map[string]any{
				"code":                map[string]any{"wired": []string{"cel", "expr"}, "reserved_unwired": []string{"python"}},
				"script_interpreters": map[string]string{".py": "python3"},
			},
			"schema": map[string]any{"type": "object"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", testWorkspaceCUID)
	caps, err := c.CrewCapabilities(context.Background(), "crew-1")
	if err != nil {
		t.Fatalf("CrewCapabilities: %v", err)
	}
	if caps.CrewSlug != "acct" {
		t.Errorf("slug = %q", caps.CrewSlug)
	}
	if len(caps.Integrations) != 1 || len(caps.Integrations[0].Tools) != 1 || caps.Integrations[0].Tools[0] != "GMAIL_FETCH_EMAIL" {
		t.Errorf("integrations = %+v", caps.Integrations)
	}
	if len(caps.Runtimes.Code.Wired) != 2 || caps.Runtimes.ScriptInterpreters[".py"] != "python3" {
		t.Errorf("runtimes = %+v", caps.Runtimes)
	}
	// Schema survives as raw JSON.
	var sch map[string]any
	if err := json.Unmarshal(caps.Schema, &sch); err != nil || sch["type"] != "object" {
		t.Errorf("schema = %s err=%v", caps.Schema, err)
	}
}

func TestCrewCapabilities_EmptyID(t *testing.T) {
	c := NewClient("http://127.0.0.1:0", "tok", testWorkspaceCUID)
	if _, err := c.CrewCapabilities(context.Background(), "  "); err == nil {
		t.Error("expected error for empty crew id")
	}
}
