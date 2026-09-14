package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

func TestPageProjectCLIStarterAndBuild(t *testing.T) {
	stub := pageStub(t)
	out, err := runPageCLI(t, "", "page", "project", "init")
	if err != nil {
		t.Fatal(err)
	}
	source, err := pages.ParseSourceProject(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range source.Files {
		if f.Path == "src/main.tsx" && strings.Contains(f.Content, "@crewship/pages") {
			found = true
		}
	}
	if !found {
		t.Fatal("starter does not use the read SDK")
	}
	route := "/api/v1/pages/health/project/build"
	stub.OnPost(route, func(*http.Request, []byte) (int, []byte, string) {
		return 202, []byte(`{"id":"build1","source_revision":4,"state":"running"}`), "application/json"
	})
	out, err = runPageCLI(t, "", "page", "project", "build", "health", "--revision", "4")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatal(out)
	}
	calls := stub.CallsFor("POST", route)
	if len(calls) != 1 {
		t.Fatal("missing build request")
	}
	var body map[string]int64
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["expected_revision"] != 4 {
		t.Fatal("revision lost")
	}
	project := findSubcommand(pageCmd.Commands(), "project")
	build := findSubcommand(project.Commands(), "build")
	_ = build.Flags().Set("revision", "0")
}
