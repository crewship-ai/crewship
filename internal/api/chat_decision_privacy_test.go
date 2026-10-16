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
