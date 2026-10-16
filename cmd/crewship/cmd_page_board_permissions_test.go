package main

import (
	"net/http"
	"testing"
)

func TestPageBoardPermissionsCLIExactEndpointsAndBody(t *testing.T) {
	for _, verb := range []string{"list", "grant", "revoke"} {
		t.Run(verb, func(t *testing.T) {
			stub := pageStub(t)
			resetPageBoardCommand(t)
			path := "/api/v1/pages/plan/board/element-permissions"
			method := "GET"
			input := ""
			args := []string{"page", "board", "permissions", verb, "plan"}
			if verb == "grant" {
				method = "POST"
				input = `{"mutation_id":"same","mutation_id":"same","run_id":"run"}`
				args = append(args, "--file", "-")
			}
			if verb == "revoke" {
				method = "DELETE"
				path += "/permission"
				args = append(args, "permission")
			}
			handler := func(_ *http.Request, raw []byte) (int, []byte, string) {
				if input != "" && string(raw) != input {
					t.Error("rewrote exact body")
				}
				return 409, []byte(`{"error":"changed"}`), "application/json"
			}
			switch method {
			case "GET":
				stub.OnGet(path, handler)
			case "POST":
				stub.OnPost(path, handler)
			case "DELETE":
				stub.OnDelete(path, handler)
			}
			if _, err := runPageCLI(t, input, args...); err == nil {
				t.Fatal("error lost")
			}
			if len(stub.CallsFor(method, path)) != 1 {
				t.Fatal("implicit retry")
			}
		})
	}
}
