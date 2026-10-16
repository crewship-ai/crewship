package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func proposalFixture(t *testing.T) (chatIssueFixture, *Router, ChatCardResponse) {
	t.Helper()
	f := newChatIssueFixture(t)
	r := &Router{db: f.h.db, logger: newTestLogger(), entityCursorKey: []byte("01234567890123456789012345678901")}
	rr := httptest.NewRecorder()
	r.internalProposalCreate(rr, httptest.NewRequest("POST", "/", jsonBody(map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "run_id": f.run, "operation_key": "propose-1", "title": "Plan", "items": []chatentities.ProposalItem{{ID: "a", Title: "One", CrewID: f.crew, Priority: "none", Selected: true}, {ID: "b", Title: "Two", CrewID: f.crew, Priority: "high", Selected: true}}})))
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var card ChatCardResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	return f, r, card
}
func proposalRequest(f chatIssueFixture, id, verb, key string, body any) *http.Request {
	method := "POST"
	if verb == "edit" {
		method = "PATCH"
	}
	req := httptest.NewRequest(method, "/api/v1/chats/"+f.chat+"/proposals/"+id+"/"+verb, jsonBody(body))
	req.SetPathValue("chatId", f.chat)
	req.SetPathValue("sourceId", id)
	req.SetPathValue("action", verb)
	req.Header.Set("Idempotency-Key", key)
	return req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: f.user}), f.workspace, "OWNER"))
}
func proposalMutate(t *testing.T, f chatIssueFixture, r *Router, id, verb, key string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	r.chatProposalMutate(rr, proposalRequest(f, id, verb, key, body))
	if rr.Code != want {
		a, _ := r.entityRefAudience(proposalRequest(f, id, verb, key, body), f.chat)
		if a != nil {
			version := int64(1)
			switch b := body.(type) {
			case chatentities.ProposalConfirm:
				version = b.ReviewedVersion
			case chatentities.ProposalEdit:
				version = b.ExpectedVersion
			case chatentities.ProposalDiscard:
				version = b.ExpectedVersion
			}
			_, e := r.mutateProposal(proposalRequest(f, id, verb, key, body), *a, id, key, verb, version, body)
			t.Logf("mutation error: %v", e)
		}
		t.Fatalf("%s: %d want %d %s", verb, rr.Code, want, rr.Body)
	}
	return rr
}
func TestChatProposalConfirmRetryAndConflict(t *testing.T) {
	f, r, c := proposalFixture(t)
	body := chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1}
	// Human review survives the proposing run ending.
	execOrFatal(t, r.db, `INSERT INTO journal_entries(id,workspace_id,entry_type,trace_id,ts,severity,actor_type,summary) VALUES('proposal-ended',?,'run.completed',?,'2026-10-16T11:00:00Z','info','orchestrator','done')`, f.workspace, f.run)
	first := proposalMutate(t, f, r, c.Entity.ID, "confirm", "confirm-1", body, 200)
	// A new router represents a lost process/response; durable replay is identical.
	r = &Router{db: f.h.db, logger: newTestLogger()}
	replay := proposalMutate(t, f, r, c.Entity.ID, "confirm", "confirm-1", body, 200)
	if first.Body.String() != replay.Body.String() {
		t.Fatal("retry changed result")
	}
	chatIssueCount(t, f, "missions", 2)
	chatIssueCount(t, f, "chat_entity_refs", 2)
	body.ReviewedVersion = 2
	proposalMutate(t, f, r, c.Entity.ID, "confirm", "confirm-1", body, 409)
	body.ReviewedVersion = 1
	proposalMutate(t, f, r, c.Entity.ID, "confirm", "other-reviewer", body, 409)
}
func TestChatProposalAtomicOutboxFailure(t *testing.T) {
	f, r, c := proposalFixture(t)
	execOrFatal(t, r.db, `CREATE TRIGGER fail_second_proposal_issue BEFORE INSERT ON issue_mutation_outbox WHEN (SELECT COUNT(*) FROM missions)=2 BEGIN SELECT RAISE(ABORT,'synthetic outbox failure'); END`)
	body := chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1}
	proposalMutate(t, f, r, c.Entity.ID, "confirm", "atomic", body, 500)
	chatIssueCount(t, f, "missions", 0)
	chatIssueCount(t, f, "agent_operations", 0)
	chatIssueCount(t, f, "chat_entity_refs", 0)
	execOrFatal(t, r.db, `DROP TRIGGER fail_second_proposal_issue`)
	proposalMutate(t, f, r, c.Entity.ID, "confirm", "atomic", body, 200)
}
func TestChatProposalStaleEditAndRevokedRead(t *testing.T) {
	f, r, c := proposalFixture(t)
	edit := chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Revised", Items: c.Proposal.Items}
	proposalMutate(t, f, r, c.Entity.ID, "edit", "edit-1", edit, 200)
	rr := proposalMutate(t, f, r, c.Entity.ID, "edit", "edit-stale", edit, 409)
	var conflict ChatCardVersionConflict
	if err := json.Unmarshal(rr.Body.Bytes(), &conflict); err != nil || conflict.Current.Proposal.Version != 2 {
		t.Fatalf("conflict %s", rr.Body)
	}
	execOrFatal(t, r.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	proposalMutate(t, f, r, c.Entity.ID, "confirm", "revoked", chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 2}, 404)
	chatIssueCount(t, f, "missions", 0)
}
func TestChatProposalConcurrentConfirmers(t *testing.T) {
	f, r, c := proposalFixture(t)
	execOrFatal(t, r.db, `INSERT INTO users(id,email,full_name) VALUES('second-confirmer','second@example.invalid','Second')`)
	execOrFatal(t, r.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('second-membership',?,'second-confirmer','ADMIN')`, f.workspace)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, key := range []string{"one", "two"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			req := proposalRequest(f, c.Entity.ID, "confirm", key, chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1})
			if key == "two" {
				req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "second-confirmer"}), f.workspace, "ADMIN"))
			}
			r.chatProposalMutate(rr, req)
			codes <- rr.Code
		}(key)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent results: %v", counts)
	}
	chatIssueCount(t, f, "missions", 2)
}
func TestChatProposalAssigneeResolution(t *testing.T) {
	for _, actor := range []string{"user", "agent", "ambiguous"} {
		t.Run(actor, func(t *testing.T) {
			f, r, c := proposalFixture(t)
			id := f.user
			if actor == "agent" {
				id = f.agent
			}
			if actor == "ambiguous" {
				execOrFatal(t, r.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES(?,?,?,'Ambiguous','ambiguous','MEMBER')`, f.user, f.workspace, f.crew)
			}
			items := c.Proposal.Items
			items[0].AssigneeID = &id
			want := 200
			if actor == "ambiguous" {
				want = 404
			}
			proposalMutate(t, f, r, c.Entity.ID, "edit", "assign", chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Assigned", Items: items}, want)
			if want == 200 {
				proposalMutate(t, f, r, c.Entity.ID, "confirm", "confirm", chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 2}, 200)
				var got string
				if err := r.db.QueryRow(`SELECT assignee_type FROM missions WHERE assignee_id=?`, id).Scan(&got); err != nil || got != actor {
					t.Fatalf("assignee: %s %v", got, err)
				}
			} else {
				chatIssueCount(t, f, "missions", 0)
			}
		})
	}
}

func TestChatProposalExpiryAndPermissions(t *testing.T) {
	for _, scenario := range []string{"expired", "member-allowed", "member-denied", "restricted", "guest"} {
		t.Run(scenario, func(t *testing.T) {
			f, r, c := proposalFixture(t)
			want := 200
			switch scenario {
			case "expired":
				execOrFatal(t, r.db, `UPDATE chat_issue_proposals SET expires_at='2000-01-01T00:00:00Z' WHERE id=?`, c.Entity.ID)
				want = 409
			case "member-allowed":
				execOrFatal(t, r.db, `UPDATE workspace_members SET role='MEMBER',capabilities='["issue.create"]' WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
			case "member-denied":
				execOrFatal(t, r.db, `UPDATE workspace_members SET role='MEMBER',capabilities='[]' WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
				want = 403
			case "restricted":
				execOrFatal(t, r.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
				want = 404
			case "guest":
				execOrFatal(t, r.db, `UPDATE workspace_members SET role='VIEWER',access_mode='restricted' WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
				want = 404
			}
			proposalMutate(t, f, r, c.Entity.ID, "confirm", "permissions", chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1}, want)
			n := 0
			if want == 200 {
				n = 2
			}
			chatIssueCount(t, f, "missions", n)
		})
	}
}
func TestChatProposalCursorPurposeAndHiddenPagination(t *testing.T) {
	f, r, c := proposalFixture(t)
	// Historical proposals can become hidden when their project, crew or
	// assignee disappears; none of them may create another page/hint.
	for i := 0; i < 102; i++ {
		execOrFatal(t, r.db, `INSERT INTO chat_issue_proposals(id,workspace_id,chat_id,agent_id,run_id,trigger_message_id,trigger_type,title,items,expires_at,operation_id,event_id,created_at) VALUES(?,?,?,?,?,'trigger','USER','Hidden','[{"id":"hidden","title":"SECRET","description":"","crew_id":"missing-crew","project_id":null,"assignee_id":null,"priority":"none","selected":true}]','2099-01-01T00:00:00Z','historical','event','2026-10-16T00:00:00Z')`, fmt.Sprintf("hidden-%d", i), f.workspace, f.chat, f.agent, f.run)
	}
	req := proposalRequest(f, c.Entity.ID, "list", "", nil)
	req.Method = "GET"
	rr := httptest.NewRecorder()
	r.chatProposals(rr, req)
	if rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	var page ChatCardPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Refs) != 1 || page.HasMore || !page.SnapshotComplete || strings.Contains(rr.Body.String(), "SECRET") {
		t.Fatalf("hidden list hint: %s", rr.Body)
	}
	if _, err := r.decodeProposalCursor(page.NextCursor); err != nil {
		t.Fatalf("own proposal cursor rejected: %v", err)
	}
	if _, err := r.decodeEntityCursor(page.NextCursor); err == nil {
		t.Fatal("proposal cursor accepted in E1 domain")
	}
	a, err := r.entityRefAudience(req, f.chat)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := r.encodeEntityCursor(entityCursor{Audience: *a, Snapshot: "e1", Expires: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	req.URL.RawQuery = "cursor=" + url.QueryEscape(wrong)
	rr = httptest.NewRecorder()
	r.chatProposals(rr, req)
	if rr.Code != 400 {
		t.Fatalf("E1 cursor accepted: %d", rr.Code)
	}
}
func TestChatProposalConfirmCapturesHumanExecutionOrigin(t *testing.T) {
	f, r, c := proposalFixture(t)
	execOrFatal(t, r.db, `INSERT INTO users(id,email,full_name) VALUES('origin-confirmer','origin-confirmer@example.invalid','Confirmer')`)
	execOrFatal(t, r.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('origin-confirmer-member',?,'origin-confirmer','ADMIN')`, f.workspace)
	execOrFatal(t, r.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('proposal-origin-ended',?,?,'run.completed','orchestrator','done',?)`, f.workspace, f.agent, f.run)
	req := proposalRequest(f, c.Entity.ID, "confirm", "origin", chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1})
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "origin-confirmer"}), f.workspace, "ADMIN"))
	rr := httptest.NewRecorder()
	r.chatProposalMutate(rr, req)
	if rr.Code != 200 {
		t.Fatalf("other human confirm: %d %s", rr.Code, rr.Body)
	}
	rows, err := r.db.Query(`SELECT id,chat_origin_id FROM missions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	type item struct{ id, origin string }
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.origin); err != nil {
			t.Fatal(err)
		}
		items = append(items, i)
	}
	rows.Close()
	for _, i := range items {
		a, e := runauthority.ValidateOrigin(t.Context(), r.db, i.origin, f.agent, f.workspace)
		if e != nil || a.HumanAuthority == nil || a.HumanAuthority.UserID != "origin-confirmer" || a.HumanAuthority.ResourceID != i.id || a.HumanAuthority.Kind != "web_issue" {
			t.Fatalf("confirm execution origin: %+v %v", a, e)
		}
	}
}

func TestChatProposalLateEditRetryCannotReviveDraft(t *testing.T) {
	f, r, c := proposalFixture(t)
	edit := chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Revised", Items: c.Proposal.Items}
	proposalMutate(t, f, r, c.Entity.ID, "edit", "lost-edit", edit, 200)
	proposalMutate(t, f, r, c.Entity.ID, "confirm", "confirmed", chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 2}, 200)
	rr := proposalMutate(t, f, r, c.Entity.ID, "edit", "lost-edit", edit, 200)
	var result ChatCardResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Proposal.Status != "accepted" || len(result.Proposal.AllowedActions) != 1 {
		t.Fatalf("revived terminal source: %s", rr.Body)
	}
	chatIssueCount(t, f, "missions", 2)
}
func TestChatProposalRestoreRetryDoesNotDuplicateEffects(t *testing.T) {
	f, r, c := proposalFixture(t)
	body := chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1}
	first := proposalMutate(t, f, r, c.Entity.ID, "confirm", "restore-retry", body, 200)
	path := filepath.Join(t.TempDir(), "restored.db")
	if _, err := r.db.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	r = &Router{db: restored, logger: newTestLogger()}
	retry := proposalMutate(t, f, r, c.Entity.ID, "confirm", "restore-retry", body, 200)
	if first.Body.String() != retry.Body.String() {
		t.Fatal("restored confirm changed committed outcome")
	}
	for _, table := range []string{"missions", "mission_activity", "issue_mutation_outbox", "agent_operations", "chat_entity_refs"} {
		var n int
		if err := restored.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 2 {
			t.Fatalf("restored %s effects=%d %v", table, n, err)
		}
	}
}
func TestChatProposalAgentRevisionConflictsWithHumanDraft(t *testing.T) {
	f, r, c := proposalFixture(t)
	edit := chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Agent revised plan", Items: c.Proposal.Items}
	req := httptest.NewRequest("PATCH", "/", jsonBody(map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "run_id": f.run, "operation_key": "agent-edit", "expected_version": 1, "title": edit.Title, "items": edit.Items}))
	req.SetPathValue("sourceId", c.Entity.ID)
	rr := httptest.NewRecorder()
	r.internalProposalEdit(rr, req)
	if rr.Code != 200 {
		t.Fatalf("agent revision %d %s", rr.Code, rr.Body)
	}
	proposalMutate(t, f, r, c.Entity.ID, "edit", "human-stale", chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Human unsaved draft", Items: c.Proposal.Items}, 409)
	proposalMutate(t, f, r, c.Entity.ID, "discard", "discard", chatentities.ProposalDiscard{ExpectedVersion: 2}, 200)
	req = httptest.NewRequest("PATCH", "/", jsonBody(map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "run_id": f.run, "operation_key": "agent-terminal", "expected_version": 3, "title": "Revive?", "items": edit.Items}))
	req.SetPathValue("sourceId", c.Entity.ID)
	rr = httptest.NewRecorder()
	r.internalProposalEdit(rr, req)
	if rr.Code != 409 {
		t.Fatalf("terminal agent edit %d %s", rr.Code, rr.Body)
	}
}

func TestChatProposalAgentCanProposeWithoutHumanCreateRights(t *testing.T) {
	h, workspace, crew, agent, user := newInternalIssueHandler(t)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER',capabilities='[]' WHERE workspace_id=? AND user_id=?`, workspace, user)
	seedChatIssueRun(t, h, workspace, agent, user, "inert-chat", "inert-run")
	r := &Router{db: h.db, logger: newTestLogger()}
	rr := httptest.NewRecorder()
	r.internalProposalCreate(rr, httptest.NewRequest("POST", "/", jsonBody(map[string]any{"workspace_id": workspace, "agent_id": agent, "run_id": "inert-run", "operation_key": "inert-proposal", "title": "Read-only agent plan", "items": []chatentities.ProposalItem{{ID: "one", Title: "One", CrewID: crew, Priority: "none", Selected: true}}})))
	if rr.Code != 201 {
		t.Fatalf("inert proposal acquired create gate: %d %s", rr.Code, rr.Body)
	}
	var c ChatCardResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	f := chatIssueFixture{h: h, workspace: workspace, crew: crew, agent: agent, user: user, chat: "inert-chat", run: "inert-run"}
	proposalMutate(t, f, r, c.Entity.ID, "confirm", "inert-confirm", chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 1}, 403)
	chatIssueCount(t, f, "missions", 0)
}

func TestChatProposalImmutableOriginAndChatDeletion(t *testing.T) {
	f, r, c := proposalFixture(t)
	if _, err := r.db.Exec(`UPDATE chat_issue_proposals SET run_id='forged' WHERE id=?`, c.Entity.ID); err == nil {
		t.Fatal("immutable proposal provenance changed")
	}
	execOrFatal(t, r.db, `DELETE FROM chats WHERE id=?`, f.chat)
	var proposals, receipts int
	if err := r.db.QueryRow(`SELECT (SELECT COUNT(*) FROM chat_issue_proposals),(SELECT COUNT(*) FROM chat_proposal_operations)`).Scan(&proposals, &receipts); err != nil || proposals != 0 || receipts != 0 {
		t.Fatalf("chat deletion proposals=%d receipts=%d %v", proposals, receipts, err)
	}
}

func TestChatProposalInternalTokenScopeCannotNameForeignOrigin(t *testing.T) {
	f, r, _ := proposalFixture(t)
	for _, scope := range []struct{ workspace, crew string }{{"foreign-workspace", f.crew}, {f.workspace, "foreign-crew"}} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/", jsonBody(map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "run_id": f.run, "operation_key": "foreign-token", "title": "Forged", "items": []chatentities.ProposalItem{{ID: "one", Title: "One", CrewID: f.crew, Priority: "none", Selected: true}}}))
		req = withInternalCrewToken(req, scope.workspace, scope.crew)
		r.internalProposalCreate(rr, req)
		if rr.Code != 403 {
			t.Fatalf("foreign token admitted: %d %s", rr.Code, rr.Body)
		}
	}
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM chat_issue_proposals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("foreign token effects=%d %v", n, err)
	}
}

func TestChatProposalSelectionAndEditedContent(t *testing.T) {
	f, r, c := proposalFixture(t)
	items := c.Proposal.Items
	items[0].Title = "Human reviewed title"
	items[1].Selected = false
	proposalMutate(t, f, r, c.Entity.ID, "edit", "select", chatentities.ProposalEdit{ExpectedVersion: 1, Title: "Reviewed", Items: items}, 200)
	proposalMutate(t, f, r, c.Entity.ID, "confirm", "select-confirm", chatentities.ProposalConfirm{ProposalID: c.Entity.ID, ReviewedVersion: 2}, 200)
	chatIssueCount(t, f, "missions", 1)
	var title, human, agent, run string
	if err := r.db.QueryRow(`SELECT title,created_by_user_id,author_agent_id,author_run_id FROM missions`).Scan(&title, &human, &agent, &run); err != nil || title != items[0].Title || human != f.user || agent != f.agent || run != f.run {
		t.Fatalf("reviewed issue content/provenance: %q %q %q %q %v", title, human, agent, run, err)
	}
}

func seedProposalLaterTurn(t *testing.T, f chatIssueFixture, run string) {
	t.Helper()
	store := conversation.NewStore(t.TempDir(), newTestLogger())
	t.Cleanup(store.Close)
	trigger := "trigger-" + run
	if err := store.Append(t.Context(), f.chat, conversation.Message{ID: trigger, Role: conversation.RoleUser, AuthorUserID: f.user, Content: "Revise the proposal"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncSession(t.Context(), f.chat); err != nil {
		t.Fatal(err)
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), f.h.db, f.chat, f.user, nil)
	if err != nil || receipt == nil {
		t.Fatalf("later turn receipt: %+v %v", receipt, err)
	}
	host := NewInternalHandler(f.h.db, "host", newTestLogger())
	wireTestJournalForHandler(t, f.h.db, host)
	rr := httptest.NewRecorder()
	host.CreateRun(rr, httptest.NewRequest("POST", "/", jsonBody(map[string]any{"id": run, "workspace_id": f.workspace, "agent_id": f.agent, "chat_id": f.chat, "human_authority": receipt, "trigger_message_id": trigger, "trigger_type": "USER"})))
	if rr.Code != 201 {
		t.Fatalf("later turn run: %d %s", rr.Code, rr.Body)
	}
}
func TestChatProposalLaterAgentTurnPreservesOriginAndFencesRequester(t *testing.T) {
	f, r, c := proposalFixture(t)
	execOrFatal(t, r.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('proposal-later-original-ended',?,?,'run.completed','orchestrator','done',?)`, f.workspace, f.agent, f.run)
	later := "proposal-later-turn"
	seedProposalLaterTurn(t, f, later)
	call := func(run, agent, key string, version int64) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("PATCH", "/", jsonBody(map[string]any{"workspace_id": f.workspace, "agent_id": agent, "run_id": run, "operation_key": key, "expected_version": version, "title": "Later version", "items": c.Proposal.Items}))
		req.SetPathValue("sourceId", c.Entity.ID)
		r.internalProposalEdit(rr, req)
		return rr
	}
	rr := call(later, f.agent, "later-revision", 1)
	if rr.Code != 200 {
		t.Fatalf("later run revision: %d %s", rr.Code, rr.Body)
	}
	var run, trigger, revisionRun string
	if err := r.db.QueryRow(`SELECT run_id,trigger_message_id FROM chat_issue_proposals WHERE id=?`, c.Entity.ID).Scan(&run, &trigger); err != nil || run != f.run || trigger != "trigger-"+f.run {
		t.Fatalf("origin changed: %s %s %v", run, trigger, err)
	}
	if err := r.db.QueryRow(`SELECT run_id FROM chat_proposal_operations WHERE operation_key='later-revision'`).Scan(&revisionRun); err != nil || revisionRun != later {
		t.Fatalf("revision provenance: %s %v", revisionRun, err)
	}
	if rr = call(later, "foreign-agent", "foreign-agent", 2); rr.Code != 403 {
		t.Fatalf("foreign agent revision: %d %s", rr.Code, rr.Body)
	}
	seedChatIssueRun(t, f.h, f.workspace, f.agent, f.user, "foreign-proposal-chat", "foreign-proposal-run")
	if rr = call("foreign-proposal-run", f.agent, "foreign-chat", 2); rr.Code != 404 {
		t.Fatalf("foreign chat revision: %d %s", rr.Code, rr.Body)
	}
	proposalMutate(t, f, r, c.Entity.ID, "discard", "later-discard", chatentities.ProposalDiscard{ExpectedVersion: 2}, 200)
	if rr = call(later, f.agent, "terminal-later", 3); rr.Code != 409 {
		t.Fatalf("later run revived terminal: %d %s", rr.Code, rr.Body)
	}
	execOrFatal(t, r.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	if rr = call(later, f.agent, "revoked-requester", 2); rr.Code != 403 {
		t.Fatalf("revoked requester revision: %d %s", rr.Code, rr.Body)
	}
}
