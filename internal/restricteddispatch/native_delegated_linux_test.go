//go:build linux

package restricteddispatch

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestNativeWorkflowProofIsExactAndRevocable(t *testing.T) {
	a := nativeFixture(t)
	setRights(t, a, "h1", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}})
	r := &NativeRunner{Authority: a, MaxOutputTokens: 128}
	starts := 0
	r.StartSession = func(context.Context, string) (TextSession, error) {
		starts++
		done := make(chan struct{})
		close(done)
		return &textFixtureSession{output: "{\"type\":\"text\",\"text\":\"native workflow answer\"}\n{\"type\":\"done\"}\n", done: done}, nil
	}
	if !r.SupportsWorkflowProfile("native_api_key") || r.SupportsWorkflowProfile("responses_text") {
		t.Fatal("incorrect native capability")
	}
	req := DelegatedRunRequest{User: "h1", Workspace: "w", Agent: "a", Chat: "c1", Input: "classified task"}
	proof, err := r.ExecuteWorkflowRun(t.Context(), req, func(string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	entries, err := proof.Read(t.Context(), a.Store, "h1", "w", "a", "c1")
	if err != nil || len(entries) != 1 || entries[0].Content != "native workflow answer" {
		t.Fatalf("exact output proof: %#v %v", entries, err)
	}
	if _, err = proof.Read(t.Context(), a.Store, "h2", "w", "a", "c1"); err == nil {
		t.Fatal("foreign principal read proof")
	}
	if err = a.Store.RevokeAttempt(t.Context(), proof.handle); err != nil {
		t.Fatal(err)
	}
	if err = proof.Check(t.Context(), a.Store, "h1", "w", "a", "c1"); err == nil {
		t.Fatal("revoked proof remained valid")
	}
	req.Agent = "foreign"
	if _, err = r.ExecuteWorkflowRun(t.Context(), req, func(string, string) error { return nil }); err == nil || starts != 1 {
		t.Fatal("foreign root launched")
	}
	req.Agent = "a"
	req.SourceEntryIDs = []string{"foreign"}
	if _, err = r.ExecuteWorkflowRun(t.Context(), req, func(string, string) error { return nil }); err == nil || starts != 1 {
		t.Fatal("unbound source launched")
	}
}

func TestNativeWorkflowCapabilityFailsClosed(t *testing.T) {
	var absent *NativeRunner
	if absent.SupportsWorkflowProfile("native_api_key") {
		t.Fatal("nil native adapter advertised")
	}
	a := nativeFixture(t)
	r := &NativeRunner{Authority: a, MaxOutputTokens: 128}
	if r.SupportsWorkflowProfile("native_api_key") {
		t.Fatal("uninstalled native adapter advertised")
	}
	r.StartSession = func(context.Context, string) (TextSession, error) {
		t.Fatal("invalid configuration launched")
		return nil, access.ErrDenied
	}
	r.MaxOutputTokens = 0
	if r.SupportsWorkflowProfile("native_api_key") {
		t.Fatal("unbounded native adapter advertised")
	}
}
