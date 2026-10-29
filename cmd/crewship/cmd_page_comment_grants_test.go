package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPageCLI_ShareCommentUsesHumanGrantNotVersionShare(t *testing.T) {
	stub := pageStub(t)
	stub.OnPut(pageGrantsRoute, func(*http.Request, []byte) (int, []byte, string) { return 200, pageGrantStubBody(), "application/json" })
	if out, err := runPageGrantCLI(t, "page", "share", pageGrantSlug, "--user", "reader@example.test", "--level", "comment"); err != nil {
		t.Fatalf("share comment %v %s", err, out)
	}
	calls := stub.CallsFor("PUT", pageGrantsRoute)
	if len(calls) != 1 || len(stub.Calls()) != 1 {
		t.Fatalf("wrong share endpoint/call count: %v", stub.Calls())
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["subject_type"] != "user" || body["subject"] != "reader@example.test" || body["level"] != "comment" {
		t.Fatalf("wrong feedback grant %v", body)
	}
}

func TestPageCLI_ShareCommentRejectsAgentAndProducer(t *testing.T) {
	stub := pageStub(t)
	for _, args := range [][]string{
		{"page", "share", pageGrantSlug, "--agent", "worker", "--level", "comment"},
		{"page", "share", pageGrantSlug, "--user", "reader@example.test", "--level", "produce"},
		{"page", "grant", pageGrantSlug, "--agent", "worker", "--level", "comment"},
		{"page", "share", pageGrantSlug, "--user", "reader@example.test", "--level", "comment", "--panels", "x"},
	} {
		if out, err := runPageGrantCLI(t, args...); err == nil {
			t.Fatalf("accepted invalid sharing %v: %s", args, out)
		}
		if len(stub.Calls()) != 0 {
			t.Fatal("invalid sharing reached network")
		}
	}
}
