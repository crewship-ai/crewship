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
