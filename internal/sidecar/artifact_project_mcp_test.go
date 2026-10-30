package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactProjectMCPRejectsMissingCASAndSpoofedIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"slug":"demo","entrypoint":"index.html","files":[]}`,
		`{"slug":"demo","base_version":1,"base_version":2,"entrypoint":"index.html","files":[]}`,
		`{"slug":"demo","base_version":1,"entrypoint":"index.html","files":[],"human_user_id":"victim"}`,
		`{"slug":"../demo","base_version":1,"entrypoint":"index.html","files":[]}`,
	} {
		if _, _, err := artifactProjectMCPPacket("artifact_publish", []byte(raw)); err == nil {
			t.Errorf("accepted invalid tool packet %s", raw)
		}
	}
}

func TestArtifactProjectMCPForwardsOriginalRunAndCAS(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/internal/pages/artifact-tool" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Error("lost immutable run transport")
		}
		var body struct {
			Operation string `json:"operation"`
			Slug      string `json:"slug"`
			Payload   struct {
				Base int64 `json:"base_version"`
			} `json:"payload"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Operation != "artifact_publish" || body.Slug != "demo" || body.Payload.Base != 7 {
			t.Error("manifest CAS changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":"conflict; keep draft"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"artifact_publish","arguments":{"slug":"demo","base_version":7,"entrypoint":"index.html","files":[{"path":"index.html","content":"<h1>Hello</h1>","media_type":"text/html"}]}}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	isError, text := documentCreateMCPResult(t, w)
	if !isError || !strings.Contains(text, "keep draft") || calls.Load() != 1 {
		t.Fatalf("conflict retried or hidden: calls=%d error=%v response=%s", calls.Load(), isError, text)
	}
}

func TestArtifactProjectMCPRejectsNullFileStringsBeforeNetwork(t *testing.T) {
	for _, field := range []string{"path", "content", "media_type"} {
		t.Run(field, func(t *testing.T) {
			s, key := newRunIdentityServer(t)
			token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
			var calls atomic.Int32
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer host.Close()
			s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
			file := map[string]any{"path": "index.html", "content": "", "media_type": "text/html"}
			file[field] = nil
			packet, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "artifact_publish", "arguments": map[string]any{"slug": "demo", "base_version": 1, "entrypoint": "index.html", "files": []any{file}}}})
			r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(string(packet)))
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.handleRoutinesMCP(w, r)
			isError, _ := documentCreateMCPResult(t, w)
			if !isError || calls.Load() != 0 {
				t.Fatalf("null %s forwarded: calls=%d", field, calls.Load())
			}
		})
	}
	if _, _, err := artifactProjectMCPPacket("artifact_publish", []byte(`{"slug":"demo","base_version":1,"entrypoint":"index.html","files":[{"path":"index.html","content":"","media_type":"text/html"}]}`)); err != nil {
		t.Fatalf("valid empty content rejected: %v", err)
	}
}

func TestArtifactProjectMCPAuthoringRequiresStableSelectionIDs(t *testing.T) {
	for _, name := range []string{"artifact_create", "artifact_publish"} {
		t.Run(name, func(t *testing.T) {
			var description string
			for _, definition := range artifactProjectMCPCatalog() {
				if definition.Name == name {
					description = definition.Description
				}
			}
			for _, required := range []string{"stable unique", "artifact-", "sections", "cards", "headings", "buttons", "Do not rewrite"} {
				if !strings.Contains(description, required) {
					t.Errorf("%s description lacks authoring contract %q", name, required)
				}
			}
		})
	}
}

// The advertised schema must not invite a version the sidecar or host rejects.
func TestArtifactProjectMCPVersionMinimumMatchesValidation(t *testing.T) {
	minimums := map[string]map[string]float64{}
	for _, definition := range artifactProjectMCPCatalog() {
		var schema struct {
			Properties map[string]struct {
				Minimum *float64 `json:"minimum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			t.Fatalf("%s schema: %v", definition.Name, err)
		}
		minimums[definition.Name] = map[string]float64{}
		for field, property := range schema.Properties {
			if property.Minimum != nil {
				minimums[definition.Name][field] = *property.Minimum
			}
		}
	}
	for _, tc := range []struct {
		tool, field string
		want        float64
		zeroArgs    string
	}{
		{"artifact_data_read", "version", 1, `{"slug":"demo","collection":"sales","version":0}`},
		{"artifact_data_collections", "version", 1, `{"slug":"demo","version":0}`},
		{"artifact_data_batch", "base_version", 1, `{"slug":"demo","collection":"sales","base_version":0,"documents":[{"id":"a","ifVersion":0,"value":{}}]}`},
		{"artifact_slides_read", "version", 1, `{"slug":"@page-id","version":0}`},
		{"artifact_export_pdf", "version", 1, `{"slug":"@page-id","version":0}`},
		// Host-side checks reject 0 for these exact selections.
		{"artifact_restore", "version", 1, ""},
		{"artifact_reply", "version", 1, ""},
		// 0 means "latest" for optional reads and "new" for a stale CAS base.
		{"artifact_versions", "version", 0, ""},
		{"artifact_comments", "version", 0, ""},
		{"artifact_publish", "base_version", 0, ""},
		{"artifact_restore", "base_version", 0, ""},
	} {
		got, ok := minimums[tc.tool][tc.field]
		if !ok || got != tc.want {
			t.Errorf("%s.%s minimum = %v (present %v), want %v", tc.tool, tc.field, got, ok, tc.want)
		}
		if tc.zeroArgs != "" {
			if _, _, err := artifactProjectMCPPacket(tc.tool, []byte(tc.zeroArgs)); err == nil {
				t.Errorf("%s accepted %s 0 although schema minimum is 1", tc.tool, tc.field)
			}
		}
	}
	for _, tool := range []string{"artifact_versions", "artifact_comments"} {
		if _, _, err := artifactProjectMCPPacket(tool, []byte(`{"slug":"demo","version":0}`)); err != nil {
			t.Errorf("%s rejected version 0 (latest): %v", tool, err)
		}
	}
}

// Agents act on these descriptions: conflicts, quota, mandate and the
// human-only surfaces must stay explicit.
func TestArtifactProjectMCPDescriptionsStateRecoveryContract(t *testing.T) {
	descriptions := map[string]string{}
	for _, definition := range artifactProjectMCPCatalog() {
		descriptions[definition.Name] = definition.Description
	}
	for tool, required := range map[string][]string{
		"artifact_create":      {"413", "artifact_slug_taken", "403", "artifact_projects", "One create per run"},
		"artifact_publish":     {"409 artifact_version_conflict", "never retry", "413 artifact_history_quota", "tell the human", "complete manifest"},
		"artifact_restore":     {"409 artifact_version_conflict", "413 artifact_history_quota", "owner"},
		"artifact_data_batch":  {"409 artifact_data_conflict", "artifact_data_read", "413 artifact_data_quota", "never blind retry"},
		"artifact_versions":    {"page_id", "Omit version", "sharing, creating comments", "no agent tool", "403"},
		"artifact_comments":    {"needs_clarification", "no agent tool"},
		"artifact_reply":       {"needs_clarification", "newer than the comment", "404"},
		"artifact_slides_read": {"@", "page_id", "artifact_slides"},
		"artifact_export_pdf":  {"@", "413 artifact_slides_limit"},
		"artifact_data_read":   {"artifact_dashboard_data", "ifVersion"},
	} {
		for _, text := range required {
			if !strings.Contains(descriptions[tool], text) {
				t.Errorf("%s description lacks %q", tool, text)
			}
		}
	}
	for tool, description := range descriptions {
		if strings.Contains(description, "Page mandate") {
			t.Errorf("%s still says Page mandate; artifacts use the artifact mandate", tool)
		}
	}
}
