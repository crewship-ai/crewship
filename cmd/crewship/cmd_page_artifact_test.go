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
	stub.OnPost(route, func(_ *http.Request, raw []byte) (int, []byte, string) {
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body["base_version"] != float64(7) || body["entrypoint"] != "index.html" {
			t.Errorf("lost manifest CAS: %s", raw)
		}
		return 409, []byte(`{"error":"artifact changed; keep your draft","latest_version":8}`), "application/json"
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

func TestPageArtifactAvailabilityAndHumanConsent(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
	}{
		{[]string{"availability"}, "GET", "/api/v1/pages/artifacts/availability"},
		{[]string{"consent", "run-one", "--page-id", "page-one"}, "POST", "/api/v1/pages/artifacts/consent"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			stub := pageStub(t)
			if tc.method == "GET" {
				stub.OnGet(tc.path, func(*http.Request, []byte) (int, []byte, string) {
					return 200, []byte(`{"enabled":false}`), "application/json"
				})
			} else {
				stub.OnPost(tc.path, func(_ *http.Request, raw []byte) (int, []byte, string) {
					var body map[string]any
					if json.Unmarshal(raw, &body) != nil || len(body) != 2 || body["run_id"] != "run-one" || body["page_id"] != "page-one" {
						t.Errorf("consent changed exact human-selected scope: %s", raw)
					}
					return 200, []byte(`{"authorized":true,"run_id":"run-one","page_id":"page-one","expires_at":"2026-10-27T13:00:00Z"}`), "application/json"
				})
			}
			cmd := newPageArtifactCommand()
			cmd.PersistentPreRun = rootCmd.PersistentPreRun
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if len(stub.CallsFor(tc.method, tc.path)) != 1 {
				t.Fatal("missing exact availability/consent request")
			}
		})
	}
}
