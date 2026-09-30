package restricteddispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestWorkflowProofBindsExactCompletedOutput(t *testing.T) {
	a := providerFixture(t)
	if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE agents SET restricted_execution_profile='responses_text' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	setRights(t, a, "h1", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}})
	runner := TextRunner{Authority: a, MaxOutputTokens: 128}
	runner.StartSession = func(context.Context, string) (TextSession, error) {
		done := make(chan struct{})
		close(done)
		return &textFixtureSession{"{\"type\":\"text\",\"text\":\"EXACT_COMPLETED_OUTPUT\"}\n{\"type\":\"done\"}\n", done}, nil
	}
	proof, err := runner.ExecuteWorkflowRun(t.Context(), DelegatedRunRequest{User: "h1", Workspace: "w", Agent: "a", Chat: "c1", Input: "private"}, func(string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.ContextIDs()) != 1 {
		t.Fatal("missing exact completed provenance")
	}
	entries, err := proof.Read(t.Context(), a.Store, "h1", "w", "a", "c1")
	if err != nil || len(entries) != 1 || entries[0].Content != "EXACT_COMPLETED_OUTPUT" {
		t.Fatalf("proof read %+v %v", entries, err)
	}
	if _, err = a.Store.Resolve(t.Context(), proof.handle); err == nil {
		t.Fatal("completed proof still executable")
	}
	raw, err := json.Marshal(proof)
	if err == nil || strings.Contains(string(raw), proof.handle) {
		t.Fatal("opaque proof serialized its handle")
	}
	cipher, err := proof.Seal()
	if err != nil || strings.Contains(cipher, proof.handle) {
		t.Fatal("proof not encrypted")
	}
	restored, err := OpenRunProof(cipher)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.Check(t.Context(), a.Store, "h2", "w", "a", "c1"); err == nil {
		t.Fatal("foreign actor accepted proof")
	}
	if err = restored.Check(t.Context(), a.Store, "h1", "w", "b", "c1"); err == nil {
		t.Fatal("foreign leaf accepted proof")
	}
	if _, err = OpenRunProof(cipher + "tamper"); err == nil {
		t.Fatal("modified cipher accepted")
	}
	// A later same-chat output must not replace the earlier proof's exact entry.
	later, _, err := a.Store.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.AppendContext(t.Context(), later, access.ContextAssistant, "LATER_UNRELATED_OUTPUT"); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.CompleteAttempt(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	entries, err = restored.Read(t.Context(), a.Store, "h1", "w", "a", "c1")
	if err != nil || entries[0].Content != "EXACT_COMPLETED_OUTPUT" {
		t.Fatal("latest-by-chat replaced proof")
	}
	if err = a.Store.RevokeAttempt(t.Context(), proof.handle); err != nil {
		t.Fatal(err)
	}
	if err = restored.Check(t.Context(), a.Store, "h1", "w", "a", "c1"); err == nil {
		t.Fatal("revoked completed output accepted")
	}
}
