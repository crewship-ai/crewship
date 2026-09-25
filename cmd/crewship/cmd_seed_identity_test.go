package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

func TestSeedIdentityPreservesCustomPersonaAndInstallsSouls(t *testing.T) {
	writes := map[string]string{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			if r.URL.Path == "/api/v1/agents/custom/persona" {
				_, _ = w.Write([]byte(`{"layer":"agent","content":"My own voice"}`))
			} else {
				_, _ = w.Write([]byte(`{"layer":"crew","from_default":true}`))
			}
			return
		}
		if r.Method == "PUT" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			writes[r.URL.Path] = body["content"]
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer s.Close()
	ids := map[string]string{"sam": "custom", "jordan": "fresh"}
	if err := seedDemoIdentity(context.Background(), cli.NewClient(s.URL, "test", covWSCli7), ids, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := writes["/api/v1/agents/custom/persona"]; ok {
		t.Fatal("overwrote custom persona")
	}
	if got := writes["/api/v1/agents/fresh/persona"]; got != seeddata.AgentSoul("jordan") {
		t.Fatal("did not install Jordan's soul")
	}
	seen := map[string]bool{}
	for _, a := range seeddata.Agents {
		soul := seeddata.AgentSoul(a.Slug)
		if len(soul) == 0 || len(soul) > 1500 || seen[soul] {
			t.Fatalf("invalid or duplicate soul: %s", a.Slug)
		}
		seen[soul] = true
	}
}
