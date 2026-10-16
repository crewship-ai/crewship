package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestVerifiedTextPrivateSourceHiddenAcrossInboxAndJournal(t *testing.T) {
	d := newDecisionTestRig(t)
	if err := d.router.queryHandler.journal.(*journal.Writer).Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	hidden := "unrelated-manager"
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES (?,?)`, hidden, hidden+"@example.test")
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES (?,?,?,'MANAGER','trusted')`, "member-unrelated", d.f.workspace, hidden)
	inboxHandler := NewInboxHandler(d.f.h.db, newTestLogger(), nil)
	journalHandler := NewJournalHandler(d.f.h.db, newTestLogger(), nil)
	chainHandler := NewChainHandler(d.f.h.db, newTestLogger())
	var inboxID, journalID string
	if err := d.f.h.db.QueryRow(`SELECT id FROM inbox_items WHERE kind='escalation' AND source_id=?`, d.id).Scan(&inboxID); err != nil {
		t.Fatal(err)
	}
	if err := d.f.h.db.QueryRow(`SELECT id FROM journal_entries WHERE entry_type='peer.escalation' AND json_extract(refs,'$.escalation_id')=?`, d.id).Scan(&journalID); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path, pathKey, pathValue string
		handler                        http.HandlerFunc
		direct                         bool
	}{
		{"crew list", "/escalations", "crewId", d.f.crew, d.router.queryHandler.ListEscalations, false},
		{"inbox list", "/inbox", "", "", inboxHandler.List, false},
		{"inbox count", "/inbox/unread-count", "", "", inboxHandler.UnreadCount, false},
		{"pending count", "/escalations/pending-count", "", "", d.router.queryHandler.PendingEscalationCount, false},
		{"inbox get", "/inbox/" + inboxID, "id", inboxID, inboxHandler.Get, true},
		{"chain source anchor", "/chains/" + inboxID, "anchor", inboxID, chainHandler.Get, true},
		{"journal list", "/journal?type=peer.escalation", "", "", journalHandler.List, false},
		{"journal count", "/journal/count?type=peer.escalation", "", "", journalHandler.Count, false},
		{"journal get", "/journal/" + journalID, "id", journalID, journalHandler.Get, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := func(user, role string) *httptest.ResponseRecorder {
				req := withWorkspaceUser(httptest.NewRequest("GET", tc.path, nil), user, d.f.workspace, role)
				if tc.pathKey != "" {
					req.SetPathValue(tc.pathKey, tc.pathValue)
				}
				rr := httptest.NewRecorder()
				tc.handler(rr, req)
				return rr
			}
			denied := request(hidden, "MANAGER")
			if tc.direct {
				if denied.Code != 404 {
					t.Fatalf("hidden direct status%d %s", denied.Code, denied.Body.String())
				}
			} else if denied.Code != 200 {
				t.Fatalf("hidden list status%d %s", denied.Code, denied.Body.String())
			}
			if strings.Contains(denied.Body.String(), d.id) || strings.Contains(denied.Body.String(), "When should") {
				t.Fatalf("hidden source leaked: %s", denied.Body.String())
			}
			allowed := request(d.f.user, "OWNER")
			if allowed.Code != 200 {
				t.Fatalf("allowed status%d %s", allowed.Code, allowed.Body.String())
			}
			if !strings.Contains(tc.name, "count") && !strings.Contains(allowed.Body.String(), "When should") {
				t.Fatalf("allowed source missing: %s", allowed.Body.String())
			}
		})
	}
	// Read filters are applied before counts, not after serializing a page.
	q, err := journalHandler.journalQuery(withWorkspaceUser(httptest.NewRequest("GET", "/journal", nil), hidden, d.f.workspace, "MANAGER"), d.f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	q.Types = []journal.EntryType{journal.EntryPeerEscalation}
	count, err := journal.Count(t.Context(), d.f.h.db, q)
	if err != nil || count != 0 {
		t.Fatalf("hidden journal count%d %v", count, err)
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/inbox", nil), hidden, d.f.workspace, "MANAGER")
	rr := httptest.NewRecorder()
	inboxHandler.List(rr, req)
	var body inboxListResponse
	if err = json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || body.UnreadCount != 0 {
		t.Fatalf("hidden inbox counts %+v", body)
	}
}

func TestVerifiedTextSourceSafeRealtimeHint(t *testing.T) {
	d := newDecisionTestRig(t)
	hidden := "hint-hidden-manager"
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES (?,?)`, hidden, hidden+"@example.test")
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES (?,?,?,'MANAGER','trusted')`, "hint-hidden-member", d.f.workspace, hidden)
	hub := startedTestHub(t)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(d.f.h.db))
	d.router.queryHandler.hub = hub
	channel := "chat-hints:" + d.f.chat
	allowed := hub.AddObserver(channel, d.f.user, 16)
	defer hub.RemoveObserver(channel, allowed)
	if allowed, err := hub.CanSubscribeChannel(t.Context(), hidden, channel); err != nil || allowed {
		t.Fatalf("hidden subscription admitted: %v %v", allowed, err)
	}
	workspace := hub.AddObserver("workspace:"+d.f.workspace, hidden, 16)
	defer hub.RemoveObserver("workspace:"+d.f.workspace, workspace)
	req := httptest.NewRequest("POST", "/internal/escalations", jsonBody(map[string]string{"workspace_id": d.f.workspace, "crew_id": d.f.crew, "source_run_id": d.f.run, "actor_agent_id": d.f.agent, "reason": "private notification title"}))
	req.Header.Set("X-Crewship-Run-Token", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(d.router.queryHandler.internalToken, d.f.workspace, d.f.crew), d.f.workspace, d.f.agent, d.f.run))
	rr := httptest.NewRecorder()
	d.router.queryHandler.CreateEscalation(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create%d %s", rr.Code, rr.Body.String())
	}
	check := func() {
		frames := entityHintFramesOnChannel(t, hub, allowed, channel)
		if len(frames) != 1 || frames[0].Type != "chat.entity_refs_changed" {
			t.Fatalf("authorized hints %+v", frames)
		}
		raw, _ := json.Marshal(frames[0].Payload)
		if string(raw) != `{"contract_version":1}` {
			t.Fatalf("hint carried source data: %s", raw)
		}
		workspaceChannel := "workspace:" + d.f.workspace
		hub.Broadcast(workspaceChannel, ws.ServerMessage{Type: "test.sentinel", Channel: workspaceChannel})
		for {
			raw := <-workspace.Frames()
			var frame ws.ServerMessage
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Type == "test.sentinel" {
				break
			}
			t.Fatalf("private source workspace event %s", raw)
		}
	}
	check()
	ref := d.get(t)
	answer := d.answer(ref.Decision.Source.Version, "safe-hint-answer", "Tomorrow")
	if answer.Code != 200 {
		t.Fatalf("answer%d %s", answer.Code, answer.Body.String())
	}
	check()
}

func TestVerifiedTextCountsKeepLegacyAndCredentialSources(t *testing.T) {
	d := newDecisionTestRig(t)
	hidden := "count-manager"
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES (?,?)`, hidden, hidden+"@example.test")
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MANAGER')`, "count-manager-member", d.f.workspace, hidden)
	agentHandler := NewAgentInboxHandler(d.f.h.db, newTestLogger())
	check := func(expected int) {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), hidden, d.f.workspace, "MANAGER")
		rr := httptest.NewRecorder()
		d.router.queryHandler.PendingEscalationCount(rr, req)
		var pending map[string]int
		if err := json.Unmarshal(rr.Body.Bytes(), &pending); err != nil || rr.Code != 200 || pending["count"] != expected {
			t.Fatalf("pending count%d %s %v", rr.Code, rr.Body.String(), err)
		}
		req.SetPathValue("agentId", d.f.agent)
		rr = httptest.NewRecorder()
		agentHandler.Handle(rr, req)
		var agent agentInboxResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &agent); err != nil || rr.Code != 200 || agent.EscalationsOpen != expected {
			t.Fatalf("agent count%d %s %v", rr.Code, rr.Body.String(), err)
		}
	}
	check(0)
	for _, kind := range []string{"TEXT", "CREDENTIAL"} {
		execOrFatal(t, d.f.h.db, `INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,type,reason,status,created_at) VALUES(?,?,?,?,?,?,?,'PENDING','2026-10-16T12:00:00Z')`, "legacy-count-"+kind, d.f.workspace, d.f.crew, d.f.chat, d.f.agent, kind, "legacy source")
	}
	check(2)
}

func TestVerifiedTextPurgeCannotDeclassifyRetainedProjections(t *testing.T) {
	d := newDecisionTestRig(t)
	if err := d.router.queryHandler.journal.(*journal.Writer).Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	hidden := "purge-manager"
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES (?,?)`, hidden, hidden+"@example.test")
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MANAGER')`, "purge-manager-member", d.f.workspace, hidden)
	var inboxID, journalID string
	if err := d.f.h.db.QueryRow(`SELECT id FROM inbox_items WHERE source_id=?`, d.id).Scan(&inboxID); err != nil {
		t.Fatal(err)
	}
	if err := d.f.h.db.QueryRow(`SELECT id FROM journal_entries WHERE json_extract(refs,'$.escalation_id')=?`, d.id).Scan(&journalID); err != nil {
		t.Fatal(err)
	}
	ref := d.get(t)
	if answer := d.answer(ref.Decision.Source.Version, "purge-answer", "Saved private value"); answer.Code != 200 {
		t.Fatalf("answer%d %s", answer.Code, answer.Body.String())
	}
	for _, kind := range []string{"TEXT", "CREDENTIAL"} {
		execOrFatal(t, d.f.h.db, `INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,type,reason,status,created_at) VALUES(?,?,?,?,?,?,?,'PENDING','2026-10-16T12:00:00Z')`, "purge-legacy-"+kind, d.f.workspace, d.f.crew, d.f.chat, d.f.agent, kind, "legacy source")
	}
	purge := func(expected int) {
		req := withWorkspaceUser(httptest.NewRequest("DELETE", "/", nil), d.f.user, d.f.workspace, "OWNER")
		req.SetPathValue("crewId", d.f.crew)
		rr := httptest.NewRecorder()
		d.router.queryHandler.PurgeEscalations(rr, req)
		var body map[string]int
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || rr.Code != 200 || body["deleted"] != expected {
			t.Fatalf("purge%d %s %v", rr.Code, rr.Body.String(), err)
		}
	}
	purge(3)
	purge(0)
	var retained, legacy int
	if err := d.f.h.db.QueryRow(`SELECT COUNT(*) FROM escalations WHERE id=? AND deleted_at IS NOT NULL AND status='CANCELLED' AND reason='' AND context IS NULL AND metadata IS NULL AND resolution IS NULL AND resolved_by_user_id IS NULL AND answer_operation_key IS NULL AND answer_operation_hash IS NULL AND source_run_id=?`, d.id, d.f.run).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("minimal source tombstone %d %v", retained, err)
	}
	if err := d.f.h.db.QueryRow(`SELECT COUNT(*) FROM escalations WHERE id LIKE 'purge-legacy-%'`).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("legacy hard purge changed%d %v", legacy, err)
	}
	inboxHandler := NewInboxHandler(d.f.h.db, newTestLogger(), nil)
	journalHandler := NewJournalHandler(d.f.h.db, newTestLogger(), nil)
	chainHandler := NewChainHandler(d.f.h.db, newTestLogger())
	for _, viewer := range []struct{ id, role string }{{hidden, "MANAGER"}, {d.f.user, "OWNER"}} {
		for _, endpoint := range []struct {
			pathKey, id string
			handler     http.HandlerFunc
		}{{"id", inboxID, inboxHandler.Get}, {"id", journalID, journalHandler.Get}, {"anchor", inboxID, chainHandler.Get}} {
			req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), viewer.id, d.f.workspace, viewer.role)
			req.SetPathValue(endpoint.pathKey, endpoint.id)
			rr := httptest.NewRecorder()
			endpoint.handler(rr, req)
			if rr.Code != 404 {
				t.Fatalf("purged private source resurrected for%s: %d %s", viewer.id, rr.Code, rr.Body.String())
			}
		}
		req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), viewer.id, d.f.workspace, viewer.role)
		req.SetPathValue("chatId", d.f.chat)
		req.SetPathValue("sourceType", "escalation")
		req.SetPathValue("sourceId", d.id)
		rr := httptest.NewRecorder()
		d.router.chatDecisionGet(rr, req)
		if rr.Code != 404 {
			t.Fatalf("purged card%d %s", rr.Code, rr.Body.String())
		}
		for _, handler := range []http.HandlerFunc{inboxHandler.List, journalHandler.List, d.router.queryHandler.PendingEscalationCount} {
			rr = httptest.NewRecorder()
			handler(rr, req)
			if rr.Code != 200 || strings.Contains(rr.Body.String(), d.id) || strings.Contains(rr.Body.String(), "Saved private value") || strings.Contains(rr.Body.String(), "When should") {
				t.Fatalf("purged projection leaked%d %s", rr.Code, rr.Body.String())
			}
		}
	}
	// The append-only journal was not rewritten or deleted to achieve privacy.
	var journalPresent int
	if err := d.f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE id=?`, journalID).Scan(&journalPresent); err != nil || journalPresent != 1 {
		t.Fatalf("immutable journal removed%d %v", journalPresent, err)
	}
}

func TestVerifiedTextOriginalGenerationCannotRebindToRecreatedChat(t *testing.T) {
	d := newDecisionTestRig(t)
	original := d.get(t)
	if answer := d.answer(original.Decision.Source.Version, "generation-key", "Original answer"); answer.Code != 200 {
		t.Fatalf("original answer%d %s", answer.Code, answer.Body.String())
	}
	// Reconstruct the handlers over the persisted source, as after a restart.
	restarted := NewQueryHandler(d.f.h.db, nil, nil, "decision-test", newTestLogger())
	d.router = &Router{db: d.f.h.db, queryHandler: restarted, logger: newTestLogger()}
	if retry := d.answer(original.Decision.Source.Version, "generation-key", "Original answer"); retry.Code != 200 {
		t.Fatalf("ordinary restart retry%d %s", retry.Code, retry.Body.String())
	}
	execOrFatal(t, d.f.h.db, `DELETE FROM chats WHERE id=?`, d.f.chat)
	execOrFatal(t, d.f.h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, d.f.chat, d.f.workspace, d.f.agent, d.f.user)
	// The same actor and operation key cannot turn the new resource generation
	// into access to the old source's completed receipt.
	retry := d.answer(original.Decision.Source.Version, "generation-key", "Original answer")
	if retry.Code != 404 {
		t.Fatalf("generation transferred old receipt%d %s", retry.Code, retry.Body.String())
	}
	rr := httptest.NewRecorder()
	d.router.chatDecisionGet(rr, d.request("GET", "", "", ""))
	if rr.Code != 404 {
		t.Fatalf("old source visible in recreated chat%d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	d.router.chatDecisionList(rr, d.request("GET", "", "", ""))
	var page ChatCardPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil || rr.Code != 200 || len(page.Refs) != 0 || page.HasMore {
		t.Fatalf("generation hidden list%d %s %v", rr.Code, rr.Body.String(), err)
	}
	inboxHandler := NewInboxHandler(d.f.h.db, newTestLogger(), nil)
	rr = httptest.NewRecorder()
	inboxHandler.List(rr, d.request("GET", "", "", ""))
	if rr.Code != 200 || strings.Contains(rr.Body.String(), d.id) || strings.Contains(rr.Body.String(), "Original answer") {
		t.Fatalf("old source leaked via Inbox%d %s", rr.Code, rr.Body.String())
	}
}
