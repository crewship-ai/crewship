package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
)

func e2ProposalFixture(f chatIssueFixture, status string) ChatCardResponse {
	currentRun := "running"
	ref := ChatCardResponse{EntityRefResponse: EntityRefResponse{
		EventID: "proposal:source-1", Kind: "proposal", Verb: "proposed",
		Entity:           EntityRefEntity{ID: "source-1", WorkspaceID: f.workspace},
		Provenance:       EntityRefProvenance{Actor: EntityRefActor{Type: "agent", ID: f.agent}, At: "2026-10-16T08:00:00Z"},
		Anchor:           EntityRefAnchor{RunID: f.run, TriggerMessageID: "trigger-" + f.run, RunStatus: &currentRun},
		RevisionAtAction: EntityRefRevision{Revision: 1, AggregateRevision: 1},
	}, Proposal: &chatentities.ProposalCard{SchemaVersion: 1, Version: 1, Status: status, Title: "Investigate dashboard latency",
		ExpiresAt: "2026-10-16T10:00:00Z", AllowedActions: []string{"view"},
		Items: []chatentities.ProposalItem{
			{ID: "item-1", Title: "Profile API", Description: "Measure first", CrewID: f.crew, Priority: "high", Selected: true},
			{ID: "item-2", Title: "Cache results", Description: "Bound the cache", CrewID: f.crew, Priority: "medium", Selected: true},
			{ID: "item-3", Title: "Add regression", Description: "Cover latency", CrewID: f.crew, Priority: "low", Selected: false},
		},
	}}
	if status == "draft" {
		ref.Proposal.AllowedActions = []string{"view", "edit", "confirm", "discard"}
	}
	if status == "accepted" {
		ref.Proposal.Version = 2
		ref.Proposal.Outcome = &chatentities.ProposalOutcome{ReviewedVersion: 1, ConfirmedBy: f.user, ConfirmedAt: "2026-10-16T08:10:00Z",
			Issues: []chatentities.CreatedIssueRef{{ID: "issue-1", Identifier: "ENG-1"}, {ID: "issue-2", Identifier: "ENG-2"}}}
	}
	return ref
}

func e2DecisionFixture(f chatIssueFixture, source, status string) ChatCardResponse {
	ref := e2ProposalFixture(f, "draft")
	ref.Proposal = nil
	ref.Kind, ref.Verb, ref.EventID = "decision", "asked", "decision:source-1"
	d := &chatentities.DecisionCard{SchemaVersion: 1, Source: chatentities.DecisionSource{Type: source, ID: "source-1", Version: "1"},
		DecisionType: "information", Status: status, Title: "When should this run?", AllowedActions: []string{"view"},
		Fields: []chatentities.DecisionField{
			{ID: "timing", Type: "choice", Label: "Run now?", Required: true, Options: []chatentities.DecisionOption{{Value: "now", Label: "Now"}, {Value: "later", Label: "Later"}}},
			{ID: "note", Type: "textarea", Label: "Note"},
		},
	}
	if source == "waitpoint" {
		d.DecisionType = "workflow"
	}
	if source == "run_approval" {
		d.DecisionType = "whole_run_approval"
		d.Title = "Approve this whole run?"
		d.Fields = []chatentities.DecisionField{}
	}
	if status == "pending" {
		if source == "run_approval" {
			d.AllowedActions = []string{"view", "approve", "reject"}
		} else {
			d.AllowedActions = []string{"view", "answer"}
		}
	} else {
		d.Source.Version = "2"
		if status == "answered" || status == "approved" || status == "rejected" {
			d.Resolution = &chatentities.DecisionResolution{AnsweredBy: f.user, AnsweredAt: "2026-10-16T08:10:00Z", Values: map[string]json.RawMessage{}}
			if source != "run_approval" {
				d.Resolution.Values = map[string]json.RawMessage{"timing": json.RawMessage(`"later"`), "note": json.RawMessage(`"After review"`)}
			}
		}
	}
	ref.Decision = d
	return ref
}

func e2CardRequest(f chatIssueFixture) *http.Request {
	req := httptest.NewRequest("GET", "/api/v1/chats/"+f.chat+"/cards/source-1", nil)
	req.SetPathValue("chatId", f.chat)
	req.SetPathValue("sourceId", "source-1")
	return req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: f.user}), f.workspace, "OWNER"))
}

// UPDATE_CHAT_E2_FIXTURES=1 writes real projection-handler responses. The source
// snapshots are injected; this does not certify durable storage or confirmation.
func TestChatCardProjectionFixtures(t *testing.T) {
	f := newChatIssueFixture(t)
	r := &Router{db: f.h.db, logger: newTestLogger()}
	cards := map[string]ChatCardResponse{}
	for _, status := range []string{"draft", "accepted", "discarded", "expired"} {
		cards["proposal-"+status] = e2ProposalFixture(f, status)
	}
	for _, source := range []string{"escalation", "waitpoint", "run_approval"} {
		for _, status := range []string{"pending", "answered", "expired", "cancelled"} {
			if source == "run_approval" && status == "answered" {
				continue
			}
			cards["decision-"+source+"-"+status] = e2DecisionFixture(f, source, status)
		}
	}
	for _, status := range []string{"approved", "rejected"} {
		cards["decision-run_approval-"+status] = e2DecisionFixture(f, "run_approval", status)
	}
	for name, card := range cards {
		t.Run(name, func(t *testing.T) {
			called := false
			reader := func(_ context.Context, audience chataudience.Receipt, id string) (ChatCardProjection, error) {
				called = true
				if audience.WorkspaceID != f.workspace || audience.ChatID != f.chat || audience.UserID != f.user || id != card.Entity.ID {
					t.Fatal("reader received wrong scope")
				}
				return ChatCardProjection{ChatID: f.chat, Ref: card}, nil
			}
			rr := httptest.NewRecorder()
			r.ChatCardProjectionHandler(reader)(rr, e2CardRequest(f))
			if !called || rr.Code != 200 || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("projection: %d %s", rr.Code, rr.Body)
			}
			var wire ChatCardResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &wire); err != nil {
				t.Fatal(err)
			}
			wire.Entity.WorkspaceID, wire.Provenance.Actor.ID = "test-workspace-id", "entity-agent"
			wire.Anchor.RunID, wire.Anchor.TriggerMessageID = "entity-run", "trigger-message"
			if wire.Proposal != nil {
				for i := range wire.Proposal.Items {
					wire.Proposal.Items[i].CrewID = "test-crew-id"
				}
				if wire.Proposal.Outcome != nil {
					wire.Proposal.Outcome.ConfirmedBy = "viewer"
				}
			}
			if wire.Decision != nil && wire.Decision.Resolution != nil {
				wire.Decision.Resolution.AnsweredBy = "viewer"
			}
			assertE2Fixture(t, name, wire)
		})
	}
	missing := r.ChatCardProjectionHandler(func(context.Context, chataudience.Receipt, string) (ChatCardProjection, error) {
		return ChatCardProjection{}, sql.ErrNoRows
	})
	rr := httptest.NewRecorder()
	missing(rr, e2CardRequest(f))
	if rr.Code != 404 {
		t.Fatal(rr.Code)
	}
	assertE2Fixture(t, "unavailable", json.RawMessage(rr.Body.Bytes()))
}

func assertE2Fixture(t *testing.T, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join("testdata", "chat-e2", name+".json")
	if os.Getenv("UPDATE_CHAT_E2_FIXTURES") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != string(data) {
		t.Fatalf("fixture %s differs: %v; regenerate via UPDATE_CHAT_E2_FIXTURES=1", name, err)
	}
}

func TestChatCardProjectionDoesNotLeakAnotherScope(t *testing.T) {
	f := newChatIssueFixture(t)
	r := &Router{db: f.h.db, logger: newTestLogger()}
	for _, scope := range []string{"chat", "workspace", "source"} {
		t.Run(scope, func(t *testing.T) {
			card := ChatCardProjection{ChatID: f.chat, Ref: e2ProposalFixture(f, "draft")}
			switch scope {
			case "chat":
				card.ChatID = "other"
			case "workspace":
				card.Ref.Entity.WorkspaceID = "other"
			case "source":
				card.Ref.Entity.ID = "other"
			}
			rr := httptest.NewRecorder()
			r.ChatCardProjectionHandler(func(context.Context, chataudience.Receipt, string) (ChatCardProjection, error) { return card, nil })(rr, e2CardRequest(f))
			if rr.Code != 404 || strings.Contains(rr.Body.String(), "dashboard") {
				t.Fatalf("leaked source: %d %s", rr.Code, rr.Body)
			}
		})
	}
	for _, viewer := range []string{"restricted", "suspended", "missing"} {
		t.Run(viewer, func(t *testing.T) {
			g := newChatIssueFixture(t)
			request := e2CardRequest(g)
			switch viewer {
			case "restricted":
				execOrFatal(t, g.h.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id=?`, g.user)
			case "suspended":
				execOrFatal(t, g.h.db, `UPDATE users SET suspended_at='2026-10-16T08:00:00Z' WHERE id=?`, g.user)
			case "missing":
				request = request.WithContext(withWorkspace(context.Background(), g.workspace, "OWNER"))
			}
			rr := httptest.NewRecorder()
			(&Router{db: g.h.db, logger: newTestLogger()}).ChatCardProjectionHandler(func(context.Context, chataudience.Receipt, string) (ChatCardProjection, error) {
				t.Fatal("denied reader called")
				return ChatCardProjection{}, nil
			})(rr, request)
			if rr.Code != 404 {
				t.Fatalf("denied: %d %s", rr.Code, rr.Body)
			}
		})
	}
}
