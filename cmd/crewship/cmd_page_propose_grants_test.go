package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An agent named without --level gets Propose: the default agent grant is
// Propose, and Edit (`write`) has to be asked for by name.
func TestPageCLI_AgentGrantDefaultsToPropose(t *testing.T) {
	for _, args := range [][]string{
		{"page", "grant", pageGrantSlug, "--agent", "worker"},
		{"page", "grant", pageGrantSlug, "--agent", "worker", "--level", "propose"},
	} {
		stub := pageStub(t)
		stub.OnPut(pageGrantsRoute, func(*http.Request, []byte) (int, []byte, string) { return 200, pageGrantStubBody(), "application/json" })
		if out, err := runPageGrantCLI(t, args...); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		calls := stub.CallsFor("PUT", pageGrantsRoute)
		if len(calls) != 1 {
			t.Fatalf("%v: PUT called %d times", args, len(calls))
		}
		var body map[string]any
		if err := json.Unmarshal(calls[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["subject_type"] != "agent" || body["subject"] != "worker" || body["level"] != "propose" {
			t.Fatalf("%v: body %v", args, body)
		}
	}
}

// People never get Propose, and a person still names the level explicitly.
func TestPageCLI_ProposeIsForAgentsOnly(t *testing.T) {
	stub := pageStub(t)
	for _, args := range [][]string{
		{"page", "grant", pageGrantSlug, "--user", "reader@example.test", "--level", "propose"},
		{"page", "grant", pageGrantSlug, "--crew", "engine", "--level", "propose"},
		{"page", "grant", pageGrantSlug, "--workspace", "--level", "propose"},
		{"page", "grant", pageGrantSlug, "--user", "reader@example.test"},
		{"page", "grant", pageGrantSlug, "--agent", "worker", "--level", "propose", "--panels", "x"},
		{"page", "share", pageGrantSlug, "--agent", "worker", "--level", "propose"},
	} {
		if out, err := runPageGrantCLI(t, args...); err == nil {
			t.Fatalf("accepted %v: %s", args, out)
		}
		if len(stub.Calls()) != 0 {
			t.Fatalf("%v reached the network", args)
		}
	}
}
