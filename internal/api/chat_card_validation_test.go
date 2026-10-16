package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
)

func TestChatCardRejectsInvalidStateWithoutReturningPayload(t *testing.T) {
	f := newChatIssueFixture(t)
	for name, mutate := range map[string]func(*ChatCardResponse){
		"unsafe numeric version":   func(c *ChatCardResponse) { c.Proposal.Version = maxCardInteger + 1 },
		"unsafe numeric revision":  func(c *ChatCardResponse) { c.RevisionAtAction.Revision = maxCardInteger + 1 },
		"unknown version":          func(c *ChatCardResponse) { c.Proposal.SchemaVersion = 2 },
		"duplicate item":           func(c *ChatCardResponse) { c.Proposal.Items[1].ID = c.Proposal.Items[0].ID },
		"accepted without receipt": func(c *ChatCardResponse) { c.Proposal.Status = "accepted" },
		"terminal write action":    func(c *ChatCardResponse) { c.Proposal.Status = "expired" },
		"foreign actor type":       func(c *ChatCardResponse) { c.Provenance.Actor.Type = "human" },
		"oversized Unicode":        func(c *ChatCardResponse) { c.Proposal.Items[0].Title = strings.Repeat("🙂", 241) },
		"unknown kind":             func(c *ChatCardResponse) { c.Kind = "react" },
	} {
		t.Run(name, func(t *testing.T) {
			card := e2ProposalFixture(f, "draft")
			mutate(&card)
			rr := httptest.NewRecorder()
			(&Router{db: f.h.db, logger: newTestLogger()}).ChatCardProjectionHandler(func(context.Context, chataudience.Receipt, string) (ChatCardProjection, error) {
				return ChatCardProjection{ChatID: f.chat, Ref: card}, nil
			})(rr, e2CardRequest(f))
			if rr.Code != 500 || strings.Contains(rr.Body.String(), "dashboard") {
				t.Fatalf("invalid card disclosed: %d %s", rr.Code, rr.Body)
			}
		})
	}
}

func TestChatDecisionClosedFormsAndApprovalScope(t *testing.T) {
	f := chatIssueFixture{workspace: "w", crew: "c", agent: "a", user: "u", run: "r"}
	base := e2DecisionFixture(f, "waitpoint", "answered")
	base.Decision.Fields = []chatentities.DecisionField{{ID: "ready", Label: "Ready", Type: "boolean", Required: true}, {ID: "count", Label: "Count", Type: "number", Required: true}}
	base.Decision.Resolution.Values = map[string]json.RawMessage{"ready": json.RawMessage(`false`), "count": json.RawMessage(`0`)}
	if err := validateE2Card(base); err != nil {
		t.Fatalf("valid false/zero answers: %v", err)
	}
	for name, mutate := range map[string]func(*ChatCardResponse){
		"unknown action":                 func(c *ChatCardResponse) { c.Decision.Resolution.ActionID = "invented" },
		"mismatched action":              func(c *ChatCardResponse) { c.Decision.Resolution.ActionID = "stop" },
		"unknown field":                  func(c *ChatCardResponse) { c.Decision.Resolution.Values["code"] = json.RawMessage(`"eval()"`) },
		"nested value":                   func(c *ChatCardResponse) { c.Decision.Resolution.Values["ready"] = json.RawMessage(`{"value":true}`) },
		"missing required":               func(c *ChatCardResponse) { delete(c.Decision.Resolution.Values, "ready") },
		"unknown widget":                 func(c *ChatCardResponse) { c.Decision.Fields[0].Type = "ReactComponent" },
		"information cannot approve run": func(c *ChatCardResponse) { c.Decision.DecisionType = "whole_run_approval" },
		"information cannot be approved": func(c *ChatCardResponse) { c.Decision.DecisionType = "information"; c.Decision.Status = "approved" },
		"source mismatch":                func(c *ChatCardResponse) { c.Decision.Source.ID = "foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			wire, _ := json.Marshal(base)
			var card ChatCardResponse
			if err := json.Unmarshal(wire, &card); err != nil {
				t.Fatal(err)
			}
			mutate(&card)
			if validateE2Card(card) == nil {
				t.Fatal("invalid form accepted")
			}
		})
	}
}

func TestChatCardAudienceRecheckedAfterSourceRead(t *testing.T) {
	f := newChatIssueFixture(t)
	rr := httptest.NewRecorder()
	(&Router{db: f.h.db, logger: newTestLogger()}).ChatCardProjectionHandler(func(context.Context, chataudience.Receipt, string) (ChatCardProjection, error) {
		execOrFatal(t, f.h.db, `UPDATE users SET suspended_at='2026-10-16T08:00:00Z' WHERE id=?`, f.user)
		return ChatCardProjection{ChatID: f.chat, Ref: e2ProposalFixture(f, "draft")}, nil
	})(rr, e2CardRequest(f))
	if rr.Code != 404 || strings.Contains(rr.Body.String(), "dashboard") {
		t.Fatalf("revocation race: %d %s", rr.Code, rr.Body)
	}
}
