package main

import (
	"net/http"
	"testing"
)

func TestPageBoardRequestsCLIExactEndpointsNoImplicitRetry(t *testing.T) {
	for _, tc := range []struct {
		verb, method, path, input string
		args                      []string
	}{
		{"list", "GET", "/api/v1/pages/plan/board/requests", "", nil},
		{"show", "GET", "/api/v1/pages/plan/board/requests/job", "", []string{"job"}},
		{"send", "POST", "/api/v1/pages/plan/board/requests", `{"submission_id":"same","submission_id":"same","base_seq":0}`, []string{"--file", "-"}},
		{"cancel", "POST", "/api/v1/pages/plan/board/requests/job/cancel", "", []string{"job"}},
		{"insert", "POST", "/api/v1/pages/plan/board/requests/job/insert", `{"mutation_id":"same","base_seq":0}`, []string{"job", "--file", "-"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			stub := pageStub(t)
			resetPageBoardCommand(t)
			handler := func(_ *http.Request, body []byte) (int, []byte, string) {
				if tc.input != "" && string(body) != tc.input {
					t.Errorf("request rewritten %s", body)
				}
				return 409, []byte(`{"error":"review current state"}`), "application/json"
			}
			if tc.method == "GET" {
				stub.OnGet(tc.path, handler)
			} else {
				stub.OnPost(tc.path, handler)
			}
			args := append([]string{"page", "board", "requests", tc.verb, "plan"}, tc.args...)
			if _, err := runPageCLI(t, tc.input, args...); err == nil {
				t.Fatal("conflict lost")
			}
			if len(stub.CallsFor(tc.method, tc.path)) != 1 {
				t.Fatal("implicit retry")
			}
		})
	}
}
