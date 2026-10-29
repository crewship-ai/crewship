package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestPageCLICatalogFiltersAndExplicitTestCreate(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages", func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("test_items") != "only" || r.URL.Query().Get("archived") != "include" {
			t.Errorf("filters%s", r.URL.RawQuery)
		}
		return 200, []byte(`[]`), "application/json"
	})
	if _, err := runPageCLI(t, "", "page", "list", "--test-items", "only", "--archived", "include", "--format", "json"); err != nil {
		t.Fatal(err)
	}
	stub.OnPost("/api/v1/pages", func(_ *http.Request, b []byte) (int, []byte, string) {
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		if body["created_by_test"] != true {
			t.Errorf("missing explicit marker %s", b)
		}
		return 201, []byte(`{"slug":"fleet-201"}`), "application/json"
	})
	if _, err := runPageCLI(t, pageAcceptSpecYAML, "page", "create", "--file", "-", "--test-item"); err != nil {
		t.Fatal(err)
	}
}

func TestPageArtifactCLICreateExplicitTestMarker(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost("/api/v1/pages/artifacts", func(_ *http.Request, b []byte) (int, []byte, string) {
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		if body["created_by_test"] != true {
			t.Errorf("missing explicit marker%s", b)
		}
		return 201, []byte(`{"page_id":"new-test-item"}`), "application/json"
	})
	file := pageBundleFile(t, `{"entrypoint":"index.html","files":[{"path":"index.html","content":"<h1>Test</h1>","media_type":"text/html"}]}`)
	cmd := newPageArtifactCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"create", "test-item", "--name", "Test item", "--file", file, "--test-item"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}
