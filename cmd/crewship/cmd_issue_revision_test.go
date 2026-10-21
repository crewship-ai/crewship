package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestIssueRevisionCLIUsesBothRegisteredRoutes(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "workspace"}[explicit], func(t *testing.T) {
			stub := covStub(t)
			command := newIssueRevisionCommand()
			path := "/api/v1/issues/ENG-1/revision"
			if explicit {
				command.Flags().String("workspace", "", "Workspace selection")
				_ = command.Flags().Set("workspace", cliCfg.Workspace)
				path = "/api/v1/workspaces/" + cliCfg.Workspace + "/issues/ENG-1/revision"
			}
			stub.OnGet(path, clitest.JSONResponse(200, map[string]any{"revision": 3, "aggregate_revision": 8}))
			out := covCaptureStdoutCli3(t, func() {
				if err := command.RunE(command, []string{"ENG-1"}); err != nil {
					t.Error(err)
				}
			})
			if !strings.Contains(out, "aggregate_revision") || len(stub.CallsFor("GET", path)) != 1 {
				t.Fatalf("route/output mismatch %s %+v", out, stub.CallsFor("GET", path))
			}
		})
	}
}
func TestAcceptance_IssueRevisionCanonicalAndWorkspaceAlias(t *testing.T) {
	const workspace = "cworkspacetest000000001"
	calls := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			_, _ = w.Write([]byte(`[{"id":"` + workspace + `","slug":"ws_test","name":"Synthetic"}]`))
			return
		}
		calls <- r.URL.Path
		_, _ = w.Write([]byte(`{"revision":3,"aggregate_revision":8}`))
	}))
	defer server.Close()
	out, err := runChatListCLI(t, server.URL, "issue", "revision", "ENG-1")
	if err != nil {
		t.Fatalf("canonical %v %s", err, out)
	}
	if path := <-calls; path != "/api/v1/issues/ENG-1/revision" {
		t.Fatal(path)
	}
	out, err = runChatListCLI(t, server.URL, "issue", "revision", "ENG-1", "--workspace", "ws_test", "--format", "yaml")
	if err != nil {
		t.Fatalf("workspace alias %v %s", err, out)
	}
	if path := <-calls; path != "/api/v1/workspaces/"+workspace+"/issues/ENG-1/revision" {
		t.Fatal(path)
	}
	if !strings.Contains(out, "aggregate_revision: 8") {
		t.Fatal(out)
	}
}

func TestIssueItemPreservesBackendRevisionAndActionMetadata(t *testing.T) {
	var issue issueItem
	if err := json.Unmarshal([]byte(`{"id":"synthetic-issue","revision":7,"aggregate_revision":12,"allowed_actions":["view","comment"]}`), &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Revision != 7 || issue.AggregateRevision != 12 || len(issue.AllowedActions) != 2 || issue.AllowedActions[1] != "comment" {
		t.Fatalf("metadata dropped: %+v", issue)
	}
	body, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err = json.Unmarshal(body, &output); err != nil {
		t.Fatal(err)
	}
	if output["revision"] != float64(7) || output["aggregate_revision"] != float64(12) {
		t.Fatalf("machine metadata lost: %s", body)
	}
}

func TestAcceptance_IssueGetPreservesRevisionAndActionMetadata(t *testing.T) {
	server := startIssueGetStub(t, `{"id":"synthetic-issue","crew_id":"crew_1","identifier":"BE-42","revision":7,"aggregate_revision":12,"allowed_actions":["view","comment"]}`)
	out, err := runIssueGetCLI(t, server.URL, "issue", "get", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue get %v %s", err, out)
	}
	var payload map[string]any
	if err = json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	actions, ok := payload["allowed_actions"].([]any)
	if payload["revision"] != float64(7) || payload["aggregate_revision"] != float64(12) || !ok || len(actions) != 2 || actions[1] != "comment" {
		t.Fatalf("machine metadata dropped: %s", out)
	}
}
