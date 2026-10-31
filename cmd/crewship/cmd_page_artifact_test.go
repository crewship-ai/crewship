package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPageArtifactPublishPreservesManifestCAS(t *testing.T) {
	stub := pageStub(t)
	route := "/api/v1/pages/demo/artifact/versions"
	stub.OnPost(route, func(r *http.Request, raw []byte) (int, []byte, string) {
		if r.Header.Get("Authorization") != "Bearer page-acceptance-token" || r.URL.Query().Get("workspace_id") != covWSCli3 {
			t.Error("publish lost the editor's authenticated workspace transport")
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body["base_version"] != float64(7) || body["entrypoint"] != "index.html" {
			t.Errorf("lost manifest CAS: %s", raw)
		}
		return 409, []byte(`{"error":"artifact changed; keep your draft","latest_version":8}`), "application/json"
	})
	// A 409 may be the echo of a first attempt whose reply was lost, so the CLI
	// reads version 8 once. Someone else's version 8 keeps the conflict as is.
	stub.OnGet("/api/v1/pages/demo/artifact", func(r *http.Request, _ []byte) (int, []byte, string) {
		return 200, []byte(`{"manifest":{"version":8,"entrypoint":"index.html","files":[{"path":"index.html","content":"<h1>Theirs</h1>","media_type":"text/html"}]}}`), "application/json"
	})
	file := pageBundleFile(t, `{"entrypoint":"index.html","files":[{"path":"index.html","content":"<h1>Draft</h1>","media_type":"text/html"}]}`)
	cmd := newPageArtifactCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"publish", "demo", "--base-version", "7", "--file", file})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "artifact changed") {
		t.Fatalf("conflict not preserved: %v", err)
	}
	if len(stub.CallsFor("POST", route)) != 1 {
		t.Fatal("publish retried or not sent")
	}
}

func TestPageArtifactRestoreRequiresExplicitBase(t *testing.T) {
	stub := pageStub(t)
	cmd := newPageArtifactCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "demo", "--version", "1"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("restore accepted no base_version")
	}
	if len(stub.CallsFor("POST", "/api/v1/pages/demo/artifact/restore")) != 0 {
		t.Fatal("invalid restore reached server")
	}
}

func TestPageArtifactManifestRejectsAmbiguousJSONBeforeNetwork(t *testing.T) {
	for _, raw := range []string{
		`{"entrypoint":"index.html","entrypoint":"other.html","files":[]}`,
		`{"entrypoint":"index.html","files":[{"path":"index.html","content":"first","content":"last","media_type":"text/html"}]}`,
		"{\"entrypoint\":\"index.html\",\"files\":[{\"path\":\"index.html\",\"content\":\"" + string([]byte{255}) + "\",\"media_type\":\"text/html\"}]}",
	} {
		t.Run(raw, func(t *testing.T) {
			stub := pageStub(t)
			cmd := newPageArtifactCommand()
			cmd.PersistentPreRun = rootCmd.PersistentPreRun
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{"publish", "demo", "--base-version", "1", "--file", "-"})
			cmd.SetIn(strings.NewReader(raw))
			if err := cmd.Execute(); err == nil {
				t.Fatal("accepted ambiguous manifest")
			}
			if len(stub.CallsFor("POST", "/api/v1/pages/demo/artifact/versions")) != 0 {
				t.Fatal("invalid JSON reached server")
			}
		})
	}
}

func TestPageArtifactAvailabilityUsesAuthenticatedWorkspaceTransport(t *testing.T) {
	stub := pageStub(t)
	route := "/api/v1/pages/artifacts/availability"
	stub.OnGet(route, func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Header.Get("Authorization") != "Bearer page-acceptance-token" || r.URL.Query().Get("workspace_id") != covWSCli3 {
			t.Errorf("lost authenticated workspace transport")
		}
		return 200, []byte(`{"enabled":false}`), "application/json"
	})
	cmd := newPageArtifactCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"availability"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(stub.CallsFor("GET", route)) != 1 {
		t.Fatal("availability did not use the authenticated Page client")
	}
}

func TestPageArtifactAvailabilityAgentFlagScopesTheQuery(t *testing.T) {
	stub := pageStub(t)
	route := "/api/v1/pages/artifacts/availability"
	stub.OnGet(route, func(r *http.Request, _ []byte) (int, []byte, string) {
		if got := r.URL.Query().Get("agent_id"); got != "agent 1" {
			t.Errorf("agent_id = %q", got)
		}
		return 200, []byte(`{"enabled":true,"can_create":false,"code":"artifact_crew_not_allowed"}`), "application/json"
	})
	cmd := newPageArtifactCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"availability", "--agent", "agent 1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(stub.CallsFor("GET", route)) != 1 {
		t.Fatal("scoped availability was not requested")
	}
}

func TestPageArtifactPublishRequiresPositiveBaseBeforeNetwork(t *testing.T) {
	stub := pageStub(t)
	file := pageBundleFile(t, `{"entrypoint":"index.html","files":[{"path":"index.html","content":"<h1>Draft</h1>","media_type":"text/html"}]}`)
	cmd := newPageArtifactCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"publish", "demo", "--base-version", "0", "--file", file})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("wrong base refusal: %v", err)
	}
	if len(stub.CallsFor("POST", "/api/v1/pages/demo/artifact/versions")) != 0 {
		t.Fatal("zero base reached server")
	}
}
