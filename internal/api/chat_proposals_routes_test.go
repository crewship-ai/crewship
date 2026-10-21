package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

func proposalRouterRequest(t *testing.T, f chatIssueFixture, r *Router, token, id, verb, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	method := http.MethodPost
	path := "/api/v1/chats/" + f.chat + "/proposals/" + id
	if verb == "edit" {
		method = http.MethodPatch
	} else {
		path += "/" + verb
	}
	req := httptest.NewRequest(method, path, jsonBody(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", f.workspace)
	req.Header.Set("Idempotency-Key", key)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func proposalRouteCount(t *testing.T, f chatIssueFixture, table string, want int) {
	t.Helper()
	switch table {
	case "missions", "mission_activity", "issue_mutation_outbox", "agent_operations", "chat_entity_refs", "chat_proposal_operations":
	default:
		t.Fatal("unsupported proposal effect table")
	}
	var got int
	if err := f.h.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil || got != want {
		t.Fatalf("%s count=%d want=%d: %v", table, got, want, err)
	}
}

func TestChatProposalRouterScopedTokenCannotMutate(t *testing.T) {
	for _, scopes := range []string{`["agents:write"]`, `["workspace:admin"]`} {
		t.Run(scopes, func(t *testing.T) {
			f, source, c := proposalFixture(t)
			token := mintTokenFor(t, source.db, f.user, strings.Repeat("a", 64))
			execOrFatal(t, source.db, `UPDATE cli_tokens SET scopes=? WHERE user_id=?`, scopes, f.user)
			r, err := NewRouter(source.db, "proposal-route-scope-test-secret-long", newTestLogger(), WithInternalToken("proposal-route-internal-test-token"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(r.Shutdown)
			for _, suffix := range []string{"", "/" + c.Entity.ID} {
				req := httptest.NewRequest("GET", "/api/v1/chats/"+f.chat+"/proposals"+suffix, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("X-Workspace-ID", f.workspace)
				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, req)
				if rr.Code != 200 {
					t.Fatalf("scoped authorized read: %d %s", rr.Code, rr.Body)
				}
				var card ChatCardResponse
				if suffix == "" {
					var page ChatCardPage
					if err = json.Unmarshal(rr.Body.Bytes(), &page); err != nil || len(page.Refs) != 1 {
						t.Fatalf("scoped list: %s %v", rr.Body, err)
					}
					card = page.Refs[0]
				} else if err = json.Unmarshal(rr.Body.Bytes(), &card); err != nil {
					t.Fatal(err)
				}
				if card.Proposal == nil || len(card.Proposal.AllowedActions) != 1 || card.Proposal.AllowedActions[0] != "view" {
					t.Fatalf("insufficient token advertises mutation: %s", rr.Body)
				}
			}
			for _, tc := range []struct {
				verb string
				body any
			}{
				{"edit", chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Denied", Items: c.Proposal.Items}},
				{"confirm", chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1}},
				{"discard", chatentities.ProposalDiscard{ExpectedVersion: 1}},
			} {
				rr := proposalRouterRequest(t, f, r, token, c.Entity.ID, tc.verb, "denied-"+tc.verb, tc.body)
				if rr.Code != 403 {
					t.Fatalf("scoped %s: %d %s", tc.verb, rr.Code, rr.Body)
				}
			}
			var version int
			var status string
			if err = source.db.QueryRow(`SELECT version,status FROM chat_issue_proposals WHERE id=?`, c.Entity.ID).Scan(&version, &status); err != nil || version != 1 || status != "draft" {
				t.Fatalf("denied token changed source: %d %s %v", version, status, err)
			}
			proposalRouteCount(t, f, "chat_proposal_operations", 1)
			for _, table := range []string{"missions", "mission_activity", "issue_mutation_outbox", "agent_operations", "chat_entity_refs"} {
				proposalRouteCount(t, f, table, 0)
			}

		})
	}
}
func TestChatProposalRouterAllowedTokenAndCurrentReadReplay(t *testing.T) {
	for _, scopes := range []string{`["crews:write"]`, `[]`} {
		t.Run(scopes, func(t *testing.T) {
			f, source, c := proposalFixture(t)
			token := mintTokenFor(t, source.db, f.user, strings.Repeat("b", 64))
			execOrFatal(t, source.db, `UPDATE cli_tokens SET scopes=? WHERE user_id=?`, scopes, f.user)
			r, err := NewRouter(source.db, "proposal-route-allowed-test-secret-long", newTestLogger(), WithInternalToken("proposal-route-internal-test-token"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(r.Shutdown)
			edit := proposalRouterRequest(t, f, r, token, c.Entity.ID, "edit", "router-edit", chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Scoped edit", Items: c.Proposal.Items})
			if edit.Code != 200 {
				t.Fatalf("allowed edit: %d %s", edit.Code, edit.Body)
			}
			body := chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 2}
			first := proposalRouterRequest(t, f, r, token, c.Entity.ID, "confirm", "router-confirm", body)
			if first.Code != 200 {
				t.Fatalf("allowed confirm: %d %s", first.Code, first.Body)
			}
			execOrFatal(t, source.db, `UPDATE workspace_members SET role='VIEWER',capabilities=NULL WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
			retry := proposalRouterRequest(t, f, r, token, c.Entity.ID, "confirm", "router-confirm", body)
			if retry.Code != 200 || retry.Body.String() != first.Body.String() {
				t.Fatalf("VIEWER authorized read replay: %d %s", retry.Code, retry.Body)
			}
			proposalRouteCount(t, f, "missions", 2)
			proposalRouteCount(t, f, "chat_proposal_operations", 3)
			proposalRouteCount(t, f, "issue_mutation_outbox", 2)
			execOrFatal(t, source.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
			revoked := proposalRouterRequest(t, f, r, token, c.Entity.ID, "confirm", "router-confirm", body)
			if revoked.Code == 200 || strings.Contains(revoked.Body.String(), "ENG-1") {
				t.Fatalf("revoked replay exposed outcome: %d %s", revoked.Code, revoked.Body)
			}
		})
	}
}
