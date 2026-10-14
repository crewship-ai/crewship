package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestPageRequestResultCLI(t *testing.T) {
	stub := pageStub(t)
	stub.On("GET", "/api/v1/page-request-results/artifact/job-1", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return 200, []byte(`{"page_slug":"report","revision":2}`), "application/json"
	})
	out, err := runPageCLI(t, "", "page", "request-result", "artifact", "job-1")
	if err != nil || !strings.Contains(out, "report") {
		t.Fatalf("result=%q error=%v", out, err)
	}
	_, err = runPageCLI(t, "", "page", "request-result", "invalid", "job-1")
	if err == nil || !strings.Contains(err.Error(), "kind must be") {
		t.Fatalf("invalid kind: %v", err)
	}
}

func TestPageArtifactLeaseCLI(t *testing.T) {
	stub := pageStub(t)
	resetPageArtifactCommand(t)
	stub.On("GET", "/api/v1/pages/report/artifact/lease", func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("revision") != "3" {
			t.Fatalf("missing exact revision: %s", r.URL.RawQuery)
		}
		return 200, []byte(`{"page_id":"p","revision":3,"runtime_profile":"static-html-v1"}`), "application/json"
	})
	if _, err := runPageCLI(t, "", "page", "artifact", "lease", "report", "--revision", "3"); err != nil {
		t.Fatal(err)
	}
	resetPageArtifactCommand(t)
	if _, err := runPageCLI(t, "", "page", "artifact", "lease", "report"); err == nil {
		t.Fatal("missing revision accepted")
	}
}
