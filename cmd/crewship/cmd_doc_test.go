package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const emptyDocPage = `apiVersion: crewship/v1
kind: Page
metadata:
  name: Human document
  slug: human-document
spec:
  panels: []
`

func TestHumanDocCLIStatus(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/documents/status", clitest.JSONResponse(http.StatusOK, map[string]any{"available": true}))
	if findSubcommand(rootCmd.Commands(), "doc") == nil {
		t.Fatal("human document status is unavailable: doc command missing")
	}
	if out, err := runPageCLI(t, "", "doc", "status"); err != nil {
		t.Fatalf("status: %v: %s", err, out)
	}
	if len(stub.CallsFor("GET", "/api/v1/documents/status")) != 1 {
		t.Fatal("status must make one request")
	}
}

func TestHumanDocCLIEmptyPageCreation(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost("/api/v1/pages", func(_ *http.Request, raw []byte) (int, []byte, string) {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body["allow_empty"] != true || len(body["panels"].([]any)) != 0 {
			t.Fatalf("empty Page body: %s", raw)
		}
		if _, ok := body["owner"]; ok {
			t.Fatalf("human owner must be inferred: %s", raw)
		}
		return http.StatusCreated, []byte(`{"id":"cpage","slug":"human-document","name":"Human document","panels":0}`), "application/json"
	})
	if out, err := runPageCLI(t, emptyDocPage, "page", "create", "--file", "-", "--allow-empty"); err != nil {
		t.Fatalf("empty human Page creation: %v: %s", err, out)
	}
	if len(stub.CallsFor("POST", "/api/v1/pages")) != 1 {
		t.Fatal("expected one Page create")
	}
}

func resetHumanDocCommand(t *testing.T) {
	t.Helper()
	old := findSubcommand(rootCmd.Commands(), "doc")
	fresh := newDocCommand()
	rootCmd.RemoveCommand(old)
	rootCmd.AddCommand(fresh)
	t.Cleanup(func() { rootCmd.RemoveCommand(fresh); rootCmd.AddCommand(old) })
}

func TestHumanDocCLIReadRoutes(t *testing.T) {
	guardCLIState(t)
	for _, operation := range []string{"read", "outline"} {
		t.Run(operation, func(t *testing.T) {
			stub := pageStub(t)
			resetHumanDocCommand(t)
			stub.OnGet("/api/v1/pages/report/document", clitest.JSONResponse(200, map[string]any{"rev": 3, "outline": []any{map[string]any{"id": "block", "text": "private text"}}}))
			out, err := runPageCLI(t, "", "doc", operation, "report")
			if err != nil {
				t.Fatalf("%s: %v %s", operation, err, out)
			}
			if !strings.Contains(out, "private text") || len(stub.CallsFor("GET", "/api/v1/pages/report/document")) != 1 {
				t.Fatalf("%s lost outline or route: %s", operation, out)
			}
			if operation == "outline" && strings.Contains(out, `"rev"`) {
				t.Fatalf("outline printed whole state: %s", out)
			}
		})
	}
}

func TestHumanDocCLIPreservesWriteFences(t *testing.T) {
	guardCLIState(t)
	for _, operation := range []string{"initialize", "patch"} {
		for _, status := range []int{200, 409} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				stub := pageStub(t)
				resetHumanDocCommand(t)
				payload := `{"id":"exact-retry","schema":2,"epoch":"opaque-epoch","ifRev":3,"target":"block","ifHash":"digest","text":"changed"}`
				path := "/api/v1/pages/report/document/" + operation
				stub.OnPost(path, func(_ *http.Request, body []byte) (int, []byte, string) {
					var got, want map[string]any
					if json.Unmarshal(body, &got) != nil || json.Unmarshal([]byte(payload), &want) != nil {
						t.Fatal("invalid JSON")
					}
					// The shared client adds workspace_id, but cannot change operation fences.
					delete(got, "workspace_id")
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("changed request: %s", body)
					}
					if status == 409 {
						return status, []byte(`{"error":"semantic precondition conflict"}`), "application/json"
					}
					return status, []byte(`{"id":"exact-retry","rev":4,"epoch":"opaque-epoch"}`), "application/json"
				})
				out, err := runPageCLI(t, payload, "doc", operation, "report", "--file", "-")
				if status == 409 {
					if err == nil || !strings.Contains(err.Error(), "semantic precondition conflict") {
						t.Fatalf("hidden conflict: %v %s", err, out)
					}
				} else if err != nil {
					t.Fatalf("write: %v %s", err, out)
				}
				if len(stub.CallsFor("POST", path)) != 1 {
					t.Fatal("CLI retried or rebased request")
				}
			})
		}
	}
}

func TestHumanDocCLIRejectsInvalidRequestBeforeHTTP(t *testing.T) {
	guardCLIState(t)
	for _, tc := range []struct {
		name, payload string
		noFile        bool
	}{
		{name: "missing file", noFile: true},
		{name: "null", payload: "null"},
		{name: "array", payload: "[]"},
		{name: "two objects", payload: "{} {}"},
		{name: "malformed tail", payload: "{} x"},
		{name: "oversize", payload: strings.Repeat(" ", 2<<20) + "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pageStub(t)
			resetHumanDocCommand(t)
			args := []string{"doc", "patch", "report"}
			if !tc.noFile {
				args = append(args, "--file", "-")
			}
			if out, err := runPageCLI(t, tc.payload, args...); err == nil {
				t.Fatalf("accepted invalid request: %s", out)
			}
		})
	}
}

func TestHumanDocCLIEmptyPageGuards(t *testing.T) {
	guardCLIState(t)
	for _, tc := range []struct {
		name, input string
		args        []string
	}{
		{"default remains strict", emptyDocPage, []string{"page", "create", "--file", "-"}},
		{"crew owner", emptyDocPage, []string{"page", "create", "--file", "-", "--allow-empty", "--owner", "crew/lookout"}},
		{"panels", pageAcceptSpecYAML, []string{"page", "create", "--file", "-", "--allow-empty"}},
		{"update", emptyDocPage, []string{"page", "update", "human-document", "--file", "-", "--allow-empty"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pageStub(t)
			// Each subtest represents a fresh CLI process.
			resetCLIState()
			if out, err := runPageCLI(t, tc.input, tc.args...); err == nil {
				t.Fatalf("accepted invalid empty Page request: %s", out)
			}
		})
	}
}
