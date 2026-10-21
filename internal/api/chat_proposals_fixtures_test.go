package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

// Fixtures are actual functional handler responses; only nondeterministic
// identities/timestamps are normalized. Frozen A1 fixtures remain untouched.
func normalizeProposalFixture(c *ChatCardResponse) {
	c.EventID = "proposal-event"
	c.OperationID = "proposal-operation"
	c.Entity.ID = "proposal-source"
	c.Entity.WorkspaceID = "workspace"
	c.Provenance.Actor.ID = "agent"
	c.Provenance.At = "2026-10-16T10:00:00Z"
	c.Anchor.RunID = "run"
	c.Anchor.TriggerMessageID = "trigger"
	if p := c.Proposal; p != nil {
		p.ExpiresAt = "2026-10-17T10:00:00Z"
		for i := range p.Items {
			p.Items[i].CrewID = "crew"
		}
		if p.Outcome != nil {
			p.Outcome.ConfirmedBy = "reviewer"
			p.Outcome.ConfirmedAt = "2026-10-16T11:00:00Z"
			for i := range p.Outcome.Issues {
				p.Outcome.Issues[i].ID = fmt.Sprintf("issue-%d", i+1)
				p.Outcome.Issues[i].Identifier = fmt.Sprintf("ENG-%d", i+1)
			}
		}
	}
}
func assertProposalHandlerFixture(t *testing.T, name string, value any) {
	t.Helper()
	var cards []ChatCardResponse
	switch v := value.(type) {
	case ChatCardResponse:
		cards = []ChatCardResponse{v}
	case ChatCardPage:
		cards = v.Refs
	case ChatCardVersionConflict:
		cards = []ChatCardResponse{v.Current}
	}
	for _, card := range cards {
		if err := validateE2Card(card); err != nil {
			t.Fatalf("functional fixture violates frozen A1 card contract: %v", err)
		}
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := filepath.Join("testdata", "chat-proposals", name+".json")
	if os.Getenv("UPDATE_CHAT_PROPOSAL_FIXTURES") == "1" {
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(raw) {
		t.Fatalf("fixture %s changed; regenerate functional proposal fixtures explicitly", name)
	}
}
func TestChatProposalHandlerFixtures(t *testing.T) {
	f, r, c := proposalFixture(t)
	req := proposalRequest(f, c.Entity.ID, "list", "", nil)
	req.Method = "GET"
	rr := httptest.NewRecorder()
	r.chatProposals(rr, req)
	if rr.Code != 200 {
		t.Fatalf("list %d %s", rr.Code, rr.Body)
	}
	var page ChatCardPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	page.NextCursor = "opaque-purpose-bound-cursor"
	page.SnapshotID = "replacement-snapshot"
	for i := range page.Refs {
		normalizeProposalFixture(&page.Refs[i])
	}
	assertProposalHandlerFixture(t, "list", page)
	for _, action := range []string{"edit", "conflict", "confirm", "retry"} {
		var body any
		var verb, key string
		want := 200
		switch action {
		case "edit":
			verb, key = "edit", "fixture-edit"
			body = chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Reviewed plan", Items: c.Proposal.Items}
		case "conflict":
			verb, key = "confirm", "fixture-stale"
			body = chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1}
			want = 409
		case "confirm", "retry":
			verb, key = "confirm", "fixture-confirm"
			body = chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 2}
		}
		rr = proposalMutate(t, f, r, c.Entity.ID, verb, key, body, want)
		if want == 409 {
			var conflict ChatCardVersionConflict
			if err := json.Unmarshal(rr.Body.Bytes(), &conflict); err != nil {
				t.Fatal(err)
			}
			normalizeProposalFixture(&conflict.Current)
			assertProposalHandlerFixture(t, action, conflict)
		} else {
			var card ChatCardResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &card); err != nil {
				t.Fatal(err)
			}
			normalizeProposalFixture(&card)
			assertProposalHandlerFixture(t, action, card)
		}
	}
}
