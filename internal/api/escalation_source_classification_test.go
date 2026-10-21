package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestVerifiedTextClassificationIsAtomicAndContentFree(t *testing.T) {
	d := newDecisionTestRig(t)
	h := d.router.queryHandler
	if err := h.journal.(*journal.Writer).Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var unsafe bool
	if err := d.f.h.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM journal_entries WHERE entry_type='escalation.source_classified' AND (payload<>'{}' OR json_extract(refs,'$.escalation_id')<>? OR (SELECT COUNT(*) FROM json_each(refs))<>1 OR COALESCE(agent_id,'')<>'' OR COALESCE(crew_id,'')<>'' OR COALESCE(mission_id,'')<>'' OR COALESCE(trace_id,'')<>'' OR COALESCE(span_id,'')<>''))`, d.id).Scan(&unsafe); err != nil || unsafe {
		t.Fatalf("classification leaked source context%v %v", unsafe, err)
	}
	counts := func() []int {
		out := []int{}
		for _, table := range []string{"escalations", "inbox_items", "journal_entries"} {
			var count int
			if err := d.f.h.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			out = append(out, count)
		}
		return out
	}
	before := counts()
	execOrFatal(t, d.f.h.db, `CREATE TRIGGER reject_classification BEFORE INSERT ON journal_entries WHEN NEW.entry_type='escalation.source_classified' BEGIN SELECT RAISE(ABORT,'classification unavailable'); END`)
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{"workspace_id": d.f.workspace, "crew_id": d.f.crew, "chat_id": d.f.chat, "actor_agent_id": d.f.agent, "source_run_id": d.f.run, "reason": "Must roll back"}))
	req = req.WithContext(journal.WithRunID(req.Context(), "private-inherited-trace"))
	req.Header.Set("X-Crewship-Run-Token", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.internalToken, d.f.workspace, d.f.crew), d.f.workspace, d.f.agent, d.f.run))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != 500 || fmt.Sprint(counts()) != fmt.Sprint(before) {
		t.Fatalf("classification failure left source effects%d before%v after%v %s", rr.Code, before, counts(), rr.Body.String())
	}
}

func TestVerifiedTextClassificationCannotBeForgedBySidecar(t *testing.T) {
	d := newDecisionTestRig(t)
	d.router.journal = d.router.queryHandler.journal
	req := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"workspace_id":%q,"agent_id":%q,"crew_id":%q,"type":"escalation.source_classified","summary":"Fake classification","refs":{"escalation_id":"legacy-source"}}`, d.f.workspace, d.f.agent, d.f.crew)))
	rr := httptest.NewRecorder()
	d.router.handleSidecarEmit(rr, req)
	if rr.Code != 400 {
		t.Fatalf("sidecar forged source classification%d %s", rr.Code, rr.Body.String())
	}
	var count int
	if err := d.f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='escalation.source_classified' AND json_extract(refs,'$.escalation_id')='legacy-source'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("forged marker%d %v", count, err)
	}
}

func TestVerifiedTextForkKeepsMissingSourcePrivate(t *testing.T) {
	d := newDecisionTestRig(t)
	if err := d.router.queryHandler.journal.(*journal.Writer).Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	actor := backup.Actor{UserID: d.f.user, Role: "ADMIN"}
	created, err := backup.CreateBackup(t.Context(), d.f.h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: d.f.workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: "synthetic-text-source-backup"})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := backup.RestoreBackup(t.Context(), d.f.h.db, backup.RestoreOptions{Path: created.Path, Passphrase: "synthetic-text-source-backup", Actor: actor, AsWorkspace: "private-text-fork"})
	if err != nil {
		t.Fatal(err)
	}
	var inboxID, journalID string
	if err = d.f.h.db.QueryRow(`SELECT id FROM inbox_items WHERE workspace_id=? AND source_id=? AND kind='escalation'`, fork.RestoredWorkspaceID, d.id).Scan(&inboxID); err != nil {
		t.Fatal(err)
	}
	if err = d.f.h.db.QueryRow(`SELECT id FROM journal_entries WHERE workspace_id=? AND entry_type='peer.escalation' AND json_extract(refs,'$.escalation_id')=?`, fork.RestoredWorkspaceID, d.id).Scan(&journalID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, id string }{{"inbox", inboxID}, {"journal", journalID}} {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), d.f.user, fork.RestoredWorkspaceID, "OWNER")
		req.SetPathValue("id", tc.id)
		rr := httptest.NewRecorder()
		if tc.name == "inbox" {
			NewInboxHandler(d.f.h.db, newTestLogger(), nil).Get(rr, req)
		} else {
			NewJournalHandler(d.f.h.db, newTestLogger(), nil).Get(rr, req)
		}
		if rr.Code != 404 {
			t.Fatalf("fork declassified source%s%d %s", tc.name, rr.Code, rr.Body.String())
		}
	}
	for _, workspace := range []string{d.f.workspace, fork.RestoredWorkspaceID} {
		verified, err := journal.VerifyChain(t.Context(), d.f.h.db, workspace)
		if err != nil || !verified.OK {
			t.Fatalf("source/fork journal integrity%+v %v", verified, err)
		}
	}
}

func TestVerifiedTextClassificationNeverInheritsHostTrace(t *testing.T) {
	d := newDecisionTestRig(t)
	h := d.router.queryHandler
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{"workspace_id": d.f.workspace, "crew_id": d.f.crew, "chat_id": d.f.chat, "actor_agent_id": d.f.agent, "source_run_id": d.f.run, "reason": "A question with private host context"}))
	req = req.WithContext(journal.WithRunID(req.Context(), "private-host-trace"))
	req.Header.Set("X-Crewship-Run-Token", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.internalToken, d.f.workspace, d.f.crew), d.f.workspace, d.f.agent, d.f.run))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != 201 {
		t.Fatalf("contextual source%d %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var markers, unsafe int
	if err := d.f.h.db.QueryRow(`SELECT COUNT(*),SUM(CASE WHEN payload<>'{}' OR (SELECT COUNT(*) FROM json_each(refs))<>1 OR COALESCE(agent_id,'')<>'' OR COALESCE(crew_id,'')<>'' OR COALESCE(mission_id,'')<>'' OR COALESCE(trace_id,'')<>'' OR COALESCE(span_id,'')<>'' THEN 1 ELSE 0 END) FROM journal_entries WHERE entry_type='escalation.source_classified' AND json_extract(refs,'$.escalation_id')=?`, body["escalation_id"]).Scan(&markers, &unsafe); err != nil || markers != 1 || unsafe != 0 {
		t.Fatalf("contextual classification markers%d unsafe%d %v", markers, unsafe, err)
	}
}

func TestVerifiedTextSourceAsksAfterRequesterWriteRoleRevoked(t *testing.T) {
	d := newDecisionTestRig(t)
	h := d.router.queryHandler
	execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, d.f.workspace, d.f.user)
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{"workspace_id": d.f.workspace, "crew_id": d.f.crew, "chat_id": d.f.chat, "actor_agent_id": d.f.agent, "source_run_id": d.f.run, "reason": "Blocked agent still needs information"}))
	req.Header.Set("X-Crewship-Run-Token", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.internalToken, d.f.workspace, d.f.crew), d.f.workspace, d.f.agent, d.f.run))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != 201 {
		t.Fatalf("revoked requester blocked canonical question%d %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	id := body["escalation_id"].(string)
	req = d.request("GET", "", "", "")
	req.SetPathValue("sourceId", id)
	rr = httptest.NewRecorder()
	d.router.chatDecisionGet(rr, req)
	var card ChatCardResponse
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &card) != nil || card.Decision == nil || len(card.Decision.AllowedActions) != 1 || card.Decision.AllowedActions[0] != "view" {
		t.Fatalf("question minted requester WRITE grant%d %s", rr.Code, rr.Body.String())
	}
}
