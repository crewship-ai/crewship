package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/journal"
)

type decisionTestRig struct {
	f      chatIssueFixture
	router *Router
	id     string
}

func newDecisionTestRig(t *testing.T) decisionTestRig {
	t.Helper()
	f := newChatIssueFixture(t)
	h := NewQueryHandler(f.h.db, nil, nil, "decision-test", newTestLogger())
	writer := journal.NewWriter(f.h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h.SetJournal(writer)
	req := httptest.NewRequest("POST", "/api/v1/internal/escalations", jsonBody(map[string]string{"workspace_id": f.workspace, "crew_id": f.crew, "chat_id": f.chat, "actor_agent_id": f.agent, "source_run_id": f.run, "reason": "When should this run?"}))
	req.Header.Set("X-Crewship-Run-Token", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.internalToken, f.workspace, f.crew), f.workspace, f.agent, f.run))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != 201 {
		t.Fatalf("source create %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return decisionTestRig{f: f, router: &Router{db: f.h.db, queryHandler: h, logger: newTestLogger()}, id: created["escalation_id"].(string)}
}
func (d decisionTestRig) request(method, version, key, text string) *http.Request {
	var body any
	if method == "POST" {
		body = map[string]any{"source_id": d.id, "expected_version": version, "action": "answer", "values": map[string]any{"answer": text}}
	}
	req := httptest.NewRequest(method, "/api/v1/chats/"+d.f.chat+"/decisions/escalation/"+d.id, jsonBody(body))
	req = withWorkspaceUser(req, d.f.user, d.f.workspace, "OWNER")
	req.SetPathValue("chatId", d.f.chat)
	req.SetPathValue("sourceType", "escalation")
	req.SetPathValue("sourceId", d.id)
	req.Header.Set("Idempotency-Key", key)
	return req
}
func (d decisionTestRig) get(t *testing.T) ChatCardResponse {
	t.Helper()
	rr := httptest.NewRecorder()
	d.router.chatDecisionGet(rr, d.request("GET", "", "", ""))
	if rr.Code != 200 {
		t.Fatalf("get %d %s", rr.Code, rr.Body.String())
	}
	var ref ChatCardResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}
func (d decisionTestRig) answer(version, key, text string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	d.router.chatDecisionAnswer(rr, d.request("POST", version, key, text))
	return rr
}
func TestChatDecisionEscalationReplayAndActor(t *testing.T) {
	d := newDecisionTestRig(t)
	pending := d.get(t)
	version := pending.Decision.Source.Version
	first := d.answer(version, "answer-key", "Tomorrow")
	if first.Code != 200 {
		t.Fatalf("answer %d %s", first.Code, first.Body.String())
	}
	var discriminator string
	if err := d.f.h.db.QueryRow(`SELECT resolved_by FROM escalations WHERE id=?`, d.id).Scan(&discriminator); err != nil || discriminator != "user" {
		t.Fatalf("legacy discriminator=%q err=%v", discriminator, err)
	}
	after := d.get(t)
	if after.Decision.Status != "answered" || after.Decision.Resolution.AnsweredBy != d.f.user {
		t.Fatalf("receipt %+v", after.Decision)
	}
	retry := d.answer(version, "answer-key", "Tomorrow")
	if retry.Code != 200 {
		t.Fatalf("replay %d %s", retry.Code, retry.Body.String())
	}
	execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='VIEWER' WHERE user_id=? AND workspace_id=?`, d.f.user, d.f.workspace)
	readReplay := d.answer(version, "answer-key", "Tomorrow")
	if readReplay.Code != 200 {
		t.Fatalf("read-authorized replay after write revocation: %d %s", readReplay.Code, readReplay.Body.String())
	}
	execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='OWNER' WHERE user_id=? AND workspace_id=?`, d.f.user, d.f.workspace)
	collision := d.answer(version, "answer-key", "Different")
	if collision.Code != 409 || json.Valid(collision.Body.Bytes()) == false {
		t.Fatalf("collision %d %s", collision.Code, collision.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(collision.Body.Bytes(), &body)
	if body["current"] != nil {
		t.Fatal("key collision disclosed unrelated receipt")
	}
	if err := d.router.queryHandler.journal.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var deliveries int
	if err := d.f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='peer.escalation' AND json_extract(refs,'$.escalation_id')=? AND json_extract(payload,'$.state')='resolved'`, d.id).Scan(&deliveries); err != nil || deliveries != 1 {
		t.Fatalf("retry deliveries=%d err=%v", deliveries, err)
	}
	stale := d.answer(version, "another-key", "Again")
	if stale.Code != 409 {
		t.Fatalf("stale %d %s", stale.Code, stale.Body.String())
	}
}
func TestChatDecisionEscalationInboxAndChatSingleWinner(t *testing.T) {
	d := newDecisionTestRig(t)
	version := d.get(t).Decision.Source.Version
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES('inbox-other','inbox-other@example.test')`)
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('inbox-other-member',?,'inbox-other','MANAGER')`, d.f.workspace)
	execOrFatal(t, d.f.h.db, `UPDATE chats SET visibility='group' WHERE id=?`, d.f.chat)
	execOrFatal(t, d.f.h.db, `INSERT INTO chat_participants(chat_id,user_id) VALUES(?,'inbox-other')`, d.f.chat)
	d.f.h.db.SetMaxOpenConns(2)
	d.f.h.db.SetMaxIdleConns(2)
	// Pin two actual SQLite connections, then hold the writer reservation until
	// one request waits for it and the other waits for the second connection.
	writer, err := d.f.h.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.f.h.db.Conn(t.Context())
	if err != nil {
		writer.Close()
		t.Fatal(err)
	}
	second.Close()
	if _, err = writer.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	waits := d.f.h.db.Stats().WaitCount
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; results <- d.answer(version, "chat-key", "Chat answer").Code }()
	go func() {
		defer wg.Done()
		<-start
		rr := covEscResolve(d.router.queryHandler, "inbox-other", d.f.workspace, d.id, map[string]string{"action": "approve", "resolution": "Inbox answer"})
		results <- rr.Code
	}()
	close(start)
	deadline := time.Now().Add(3 * time.Second)
	for d.f.h.db.Stats().WaitCount == waits && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	contended := d.f.h.db.Stats().WaitCount > waits
	_, _ = writer.ExecContext(t.Context(), "COMMIT")
	writer.Close()
	wg.Wait()
	if !contended {
		t.Fatal("test did not establish two-connection contention")
	}
	close(results)
	wins, conflicts := 0, 0
	for status := range results {
		if status == 200 {
			wins++
		} else if status == 409 {
			conflicts++
		} else {
			t.Fatalf("unexpected race status %d", status)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	after := d.get(t)
	if after.Decision.Resolution == nil || after.Decision.Status != "answered" {
		t.Fatal("terminal source did not survive reload")
	}
	if err := d.router.queryHandler.journal.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='peer.escalation' AND json_extract(refs,'$.escalation_id')=? AND json_extract(payload,'$.state')='resolved'`, d.id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("resolution deliveries=%d", count)
	}
}
func TestChatDecisionEscalationRevokedRoleAndExpiry(t *testing.T) {
	t.Run("demoted role despite stale HTTP role", func(t *testing.T) {
		d := newDecisionTestRig(t)
		version := d.get(t).Decision.Source.Version
		execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, d.f.user, d.f.workspace)
		rr := d.answer(version, "revoked-key", "Answer")
		if rr.Code != 403 {
			t.Fatalf("demoted %d %s", rr.Code, rr.Body.String())
		}
		var status string
		_ = d.f.h.db.QueryRow(`SELECT status FROM escalations WHERE id=?`, d.id).Scan(&status)
		if status != "PENDING" {
			t.Fatal("demotion mutated source")
		}
	})
	t.Run("deadline before canonical resolve", func(t *testing.T) {
		d := newDecisionTestRig(t)
		version := d.get(t).Decision.Source.Version
		execOrFatal(t, d.f.h.db, `UPDATE escalations SET answer_deadline_at=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), d.id)
		canonical := covEscResolve(d.router.queryHandler, d.f.user, d.f.workspace, d.id, map[string]string{"action": "approve", "resolution": "Late Inbox answer"})
		if canonical.Code != 409 {
			t.Fatalf("expired canonical source accepted: %d %s", canonical.Code, canonical.Body.String())
		}
		rr := d.answer(version, "expired-key", "Too late")
		if rr.Code != 409 {
			t.Fatalf("expired %d %s", rr.Code, rr.Body.String())
		}
		after := d.get(t)
		if after.Decision.Status != "expired" {
			t.Fatalf("expiry=%s", after.Decision.Status)
		}
	})
	t.Run("read revoked retry", func(t *testing.T) {
		d := newDecisionTestRig(t)
		version := d.get(t).Decision.Source.Version
		if rr := d.answer(version, "read-key", "Answer"); rr.Code != 200 {
			t.Fatal(rr.Body.String())
		}
		execOrFatal(t, d.f.h.db, `DELETE FROM workspace_members WHERE user_id=? AND workspace_id=?`, d.f.user, d.f.workspace)
		rr := d.answer(version, "read-key", "Answer")
		if rr.Code != 404 {
			t.Fatalf("revoked replay %d %s", rr.Code, rr.Body.String())
		}
	})
}

func TestChatDecisionEscalationHiddenAndSensitiveSources(t *testing.T) {
	d := newDecisionTestRig(t)
	for _, row := range []struct {
		id, kind string
		run      any
	}{{"sensitive", "CREDENTIAL", d.f.run}, {"unanchored", "TEXT", nil}, {"unverified", "TEXT", "unknown-run"}} {
		execOrFatal(t, d.f.h.db, `INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at,source_run_id) VALUES(?,?,?,?,?,'HIDDEN_SOURCE_CANARY',?,'PENDING','2026-10-16T10:00:00Z',?)`, row.id, d.f.workspace, d.f.crew, d.f.chat, d.f.agent, row.kind, row.run)
	}
	rr := httptest.NewRecorder()
	d.router.chatDecisionList(rr, d.request("GET", "", "", ""))
	if rr.Code != 200 {
		t.Fatalf("list %d %s", rr.Code, rr.Body.String())
	}
	var page ChatCardPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Refs) != 1 || page.Refs[0].Entity.ID != d.id {
		t.Fatalf("hidden sources disclosed: %s", rr.Body.String())
	}
	for _, id := range []string{"sensitive", "unanchored", "unverified", "missing"} {
		req := d.request("GET", "", "", "")
		req.SetPathValue("sourceId", id)
		hidden := httptest.NewRecorder()
		d.router.chatDecisionGet(hidden, req)
		if hidden.Code != 404 || hidden.Body.String() != `{"error":"Card not found"}`+"\n" {
			t.Fatalf("hidden source differs: %s %d %s", id, hidden.Code, hidden.Body.String())
		}
	}
}

func TestChatDecisionEscalationSourceFixtures(t *testing.T) {
	for _, state := range []string{"pending", "answered", "expired"} {
		t.Run(state, func(t *testing.T) {
			d := newDecisionTestRig(t)
			execOrFatal(t, d.f.h.db, `UPDATE escalations SET id='decision-source-fixture',created_at='2026-10-16T10:00:00Z',answer_deadline_at='2099-01-01T00:00:00Z' WHERE id=?`, d.id)
			execOrFatal(t, d.f.h.db, `UPDATE inbox_items SET source_id='decision-source-fixture' WHERE source_id=? AND kind='escalation'`, d.id)
			d.id = "decision-source-fixture"
			if state == "answered" {
				version := d.get(t).Decision.Source.Version
				rr := d.answer(version, "fixture-answer-key", "Tomorrow")
				if rr.Code != 200 {
					t.Fatal(rr.Body.String())
				}
				execOrFatal(t, d.f.h.db, `UPDATE escalations SET resolved_at='2026-10-16T10:01:00Z' WHERE id=?`, d.id)
			}
			if state == "expired" {
				execOrFatal(t, d.f.h.db, `UPDATE escalations SET answer_deadline_at='2000-01-01T00:00:00Z' WHERE id=?`, d.id)
				_ = d.get(t)
				execOrFatal(t, d.f.h.db, `UPDATE escalations SET resolved_at='2026-10-16T10:01:00Z' WHERE id=?`, d.id)
			}
			rr := httptest.NewRecorder()
			d.router.chatDecisionGet(rr, d.request("GET", "", "", ""))
			if rr.Code != 200 {
				t.Fatal(rr.Body.String())
			}
			path := filepath.Join("testdata", "chat-decisions", state+".json")
			if os.Getenv("UPDATE_CHAT_DECISION_FIXTURES") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, rr.Body.Bytes(), 0644); err != nil {
					t.Fatal(err)
				}
			}
			expected, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(expected, rr.Body.Bytes()) {
				t.Fatalf("source fixture drift: %s", path)
			}
		})
	}
}

func TestChatDecisionEscalationInformationCannotApproveOrSupplySecrets(t *testing.T) {
	d := newDecisionTestRig(t)
	version := d.get(t).Decision.Source.Version
	for _, action := range []string{"approve", "reject"} {
		req := d.request("POST", version, "scope-key", "Information")
		raw, _ := json.Marshal(map[string]any{"source_id": d.id, "expected_version": version, "action": action, "values": map[string]any{"answer": "Information"}})
		req.Body = io.NopCloser(bytes.NewReader(raw))
		rr := httptest.NewRecorder()
		d.router.chatDecisionAnswer(rr, req)
		if rr.Code != 400 {
			t.Fatalf("information became approval: %d %s", rr.Code, rr.Body.String())
		}
	}
	rr := d.answer(version, "secret-key", "password=synthetic-sensitive-value")
	if rr.Code != 400 {
		t.Fatalf("credential value accepted in chat: %d %s", rr.Code, rr.Body.String())
	}
	if d.get(t).Decision.Status != "pending" {
		t.Fatal("refused input mutated source")
	}
}

func TestChatDecisionEscalationPaginationFiltersBeforeBoundaries(t *testing.T) {
	d := newDecisionTestRig(t)
	for i := 0; i < 104; i++ {
		execOrFatal(t, d.f.h.db, `INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at,answer_deadline_at,source_run_id) SELECT ?,workspace_id,crew_id,chat_id,from_agent_id,reason,'TEXT','PENDING',created_at,answer_deadline_at,source_run_id FROM escalations WHERE id=?`, fmt.Sprintf("visible-%03d", i), d.id)
	}
	for i := 0; i < 120; i++ {
		execOrFatal(t, d.f.h.db, `INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at,source_run_id) SELECT ?,workspace_id,crew_id,chat_id,from_agent_id,'HIDDEN_PAGER_CANARY','CREDENTIAL','PENDING',created_at,source_run_id FROM escalations WHERE id=?`, fmt.Sprintf("hidden-%03d", i), d.id)
	}
	firstReq := d.request("GET", "", "", "")
	first := httptest.NewRecorder()
	d.router.chatDecisionList(first, firstReq)
	if first.Code != 200 {
		t.Fatalf("first page %d %s", first.Code, first.Body.String())
	}
	var a ChatCardPage
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.Refs) != 100 || !a.HasMore || a.SnapshotComplete || !a.Reset || a.NextCursor == "" {
		t.Fatalf("first page shape %+v", a)
	}
	req := d.request("GET", "", "", "")
	req.URL.RawQuery = url.Values{"cursor": {a.NextCursor}}.Encode()
	second := httptest.NewRecorder()
	d.router.chatDecisionList(second, req)
	if second.Code != 200 {
		t.Fatalf("second page %d %s", second.Code, second.Body.String())
	}
	var b ChatCardPage
	if err := json.Unmarshal(second.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Refs) != 5 || b.HasMore || !b.SnapshotComplete || b.Reset || b.SnapshotID != a.SnapshotID {
		t.Fatalf("second page shape %+v", b)
	}
	// Purpose separation: the same encrypted token cannot be interpreted by E1.
	if _, err := d.router.decodeEntityCursor(a.NextCursor); err == nil {
		t.Fatal("decision cursor accepted in entity-reference protocol")
	}
	req = d.request("GET", "", "", "")
	req.URL.RawQuery = url.Values{"cursor": {a.NextCursor + "tampered"}}.Encode()
	invalid := httptest.NewRecorder()
	d.router.chatDecisionList(invalid, req)
	if invalid.Code != 400 {
		t.Fatalf("tampered cursor %d", invalid.Code)
	}
	execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='VIEWER',access_mode='restricted' WHERE user_id=? AND workspace_id=?`, d.f.user, d.f.workspace)
	guest := httptest.NewRecorder()
	d.router.chatDecisionList(guest, d.request("GET", "", "", ""))
	if guest.Code != 404 {
		t.Fatalf("guest source existence disclosed: %d %s", guest.Code, guest.Body.String())
	}
}
