package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestPageRequestResultCLI(t *testing.T) {
	stub := pageStub(t)
	stub.On("GET", "/api/v1/page-request-results/board/job-1", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return 200, []byte(`{"page_slug":"report","revision":2}`), "application/json"
	})
	out, err := runPageCLI(t, "", "page", "request-result", "board", "job-1")
	if err != nil || !strings.Contains(out, "report") {
		t.Fatalf("result=%q error=%v", out, err)
	}
	_, err = runPageCLI(t, "", "page", "request-result", "invalid", "job-1")
	if err == nil || !strings.Contains(err.Error(), "kind must be") {
		t.Fatalf("invalid kind: %v", err)
	}
}
