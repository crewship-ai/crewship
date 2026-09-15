package usermodel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// #1693 — a verified fact has to say WHICH turn its quote was found in,
// because that pointer is the whole of what the provenance store beside
// the model file can offer a person asking "when did I say that?". Verify
// already knew (it found the span in one specific subject turn) and threw
// the answer away with the quote.

func TestVerify_CarriesTheMatchingTurnAndAdmittedSource(t *testing.T) {
	p := ProfileStatedTechnical
	turns := []Turn{
		{Role: "user", BySubject: true, MessageID: "m-1", Content: "I run the platform team here, and the deploy pipeline is mine."},
		{Role: "assistant", MessageID: "m-2", Content: "Understood. I'll route pipeline questions to you."},
		{Role: "user", BySubject: true, MessageID: "m-3", Content: "One rule: commits must not carry a co-author trailer. I run the platform team here, remember."},
		{Role: "user", BySubject: false, MessageID: "m-4", Content: "Pavel is basically the only person who understands billing."},
	}

	cases := []struct {
		name          string
		cand          Candidate
		wantMessageID string
	}{
		{
			name:          "quote in the first subject turn",
			cand:          Candidate{Key: "owns", Value: "the deploy pipeline", Quote: "the deploy pipeline is mine", Source: "stated"},
			wantMessageID: "m-1",
		},
		{
			name:          "quote in a later subject turn",
			cand:          Candidate{Key: "constraint", Value: "commits carry no co-author trailer", Quote: "commits must not carry a co-author trailer", Source: "stated"},
			wantMessageID: "m-3",
		},
		{
			name:          "quote said twice points at the first time",
			cand:          Candidate{Key: "role", Value: "runs the platform team", Quote: "I run the platform team here", Source: "stated"},
			wantMessageID: "m-1",
		},
		{
			name:          "source is normalised as the profile admitted it",
			cand:          Candidate{Key: "timezone", Value: "UTC+1", Quote: "the deploy pipeline is mine", Source: "  Stated "},
			wantMessageID: "m-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accepted, refused := Verify(p, turns, []Candidate{tc.cand})
			if len(refused) != 0 || len(accepted) != 1 {
				t.Fatalf("accepted=%d refused=%+v, want one accepted fact", len(accepted), refused)
			}
			f := accepted[0]
			if f.MessageID != tc.wantMessageID {
				t.Errorf("MessageID = %q, want %q", f.MessageID, tc.wantMessageID)
			}
			if f.Source != SourceStated {
				t.Errorf("Source = %q, want %q", f.Source, SourceStated)
			}
			if f.Quote != strings.TrimSpace(tc.cand.Quote) {
				t.Errorf("Quote = %q, want the candidate's span", f.Quote)
			}
		})
	}
}

// A turn built without an id (a transcript from somewhere other than the
// mirror) still verifies; the pointer is simply empty. Provenance is
// additive — it must never turn a stated fact into a refusal.
func TestVerify_TurnWithoutAnIDStillVerifies(t *testing.T) {
	accepted, refused := Verify(ProfileStatedTechnical, transcript(), []Candidate{
		{Key: "role", Value: "runs the platform team", Quote: "I run the platform team here", Source: "stated"},
	})
	if len(refused) != 0 || len(accepted) != 1 {
		t.Fatalf("accepted=%d refused=%+v", len(accepted), refused)
	}
	if accepted[0].MessageID != "" {
		t.Errorf("MessageID = %q, want empty for a turn that had none", accepted[0].MessageID)
	}
}

// Evidence is the part of a Fact that Render throws away, in the shape the
// sweep stores. Order and every field survive the trip.
func TestEvidence_MirrorsTheFacts(t *testing.T) {
	facts := []Fact{
		{Key: "role", Value: "runs the platform team", Quote: "I run the platform team", MessageID: "m-1", Source: SourceStated},
		{Key: "owns", Value: "the deploy pipeline", Quote: "the deploy pipeline is mine", MessageID: "m-1", Source: SourceStated},
	}
	got := Evidence(facts)
	if len(got) != len(facts) {
		t.Fatalf("Evidence returned %d entries for %d facts", len(got), len(facts))
	}
	for i, f := range facts {
		want := consolidate.UserModelEvidence{Key: f.Key, Value: f.Value, Quote: f.Quote, MessageID: f.MessageID, Source: string(f.Source)}
		if got[i] != want {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want)
		}
	}
	if Evidence(nil) != nil {
		t.Errorf("Evidence(nil) should be nil, not an empty slice")
	}
}

// End to end through the real transcript loader: the evidence names the
// conversation_messages.id the quote was found in, and the body is the
// same one Extract would have produced.
func TestExtractWithEvidence_PointsAtTheMirrorRow(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	// seedTranscript ids the turns "ma", "mb", "mc" in order.
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"user", "I run the platform team here, and the deploy pipeline is mine.", "u1"},
		{"assistant", "Understood.", ""},
		{"user", "One rule: commits must not carry a co-author trailer.", "u1"},
	})
	reply, _ := json.Marshal(map[string]any{"facts": []Candidate{
		{Key: "role", Value: "runs the platform team", Quote: "I run the platform team here", Source: "stated"},
		{Key: "constraint", Value: "commits carry no co-author trailer", Quote: "commits must not carry a co-author trailer", Source: "stated"},
		{Key: "prefers", Value: "seems to like it", Quote: "Understood.", Source: "stated"}, // assistant turn: refused
	}})
	prov := &fakeProvider{reply: string(reply)}
	ex := New(db, func() (llm.Provider, string, time.Duration) { return prov, "m", time.Second },
		func(context.Context) Profile { return ProfileStatedTechnical }, quietLogger())

	body, evidence, err := ex.ExtractWithEvidence(context.Background(),
		consolidate.UserModelCandidate{WorkspaceID: "ws1", CrewID: "cr1", UserID: "u1"}, "")
	if err != nil {
		t.Fatalf("ExtractWithEvidence: %v", err)
	}
	if !strings.Contains(body, "- role: runs the platform team") || !strings.Contains(body, "- constraint: commits carry no co-author trailer") {
		t.Fatalf("body missing the stated facts:\n%s", body)
	}
	byKey := map[string]consolidate.UserModelEvidence{}
	for _, ev := range evidence {
		byKey[ev.Key] = ev
	}
	if len(byKey) != 2 {
		t.Fatalf("evidence for %d key(s), want 2 (the refused candidate must not leave a trace): %+v", len(byKey), evidence)
	}
	if ev := byKey["role"]; ev.MessageID != "ma" || ev.Quote != "I run the platform team here" || ev.Source != "stated" {
		t.Errorf("role evidence = %+v, want message ma with its quote", ev)
	}
	if ev := byKey["constraint"]; ev.MessageID != "mc" {
		t.Errorf("constraint evidence points at %q, want mc", ev.MessageID)
	}

	// Extract is the same call with the evidence dropped — the two must
	// not diverge, or a caller that has not been taught about evidence
	// silently reads a different model.
	plain, err := ex.Extract(context.Background(),
		consolidate.UserModelCandidate{WorkspaceID: "ws1", CrewID: "cr1", UserID: "u1"}, "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if plain != body {
		t.Errorf("Extract and ExtractWithEvidence disagree:\n%s\n---\n%s", plain, body)
	}
}
