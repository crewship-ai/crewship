package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestVerifiedRunApprovalCanonicalPrivacy(t *testing.T) {
	for _, scenario := range []string{"current", "generation", "restricted", "suspended", "missing-chat", "nonparticipant"} {
		t.Run(scenario, func(t *testing.T) {
			d := newApprovalDecisionRig(t)
			viewer, role := d.f.user, "OWNER"
			switch scenario {
			case "generation":
				execOrFatal(t, d.f.h.db, `DELETE FROM chats WHERE id=?`, d.f.chat)
				execOrFatal(t, d.f.h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, d.f.chat, d.f.workspace, d.f.agent, d.f.user)
			case "restricted":
				execOrFatal(t, d.f.h.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE id=?`, d.a.MemberID)
			case "suspended":
				execOrFatal(t, d.f.h.db, `UPDATE users SET suspended_at='2026-10-16' WHERE id=?`, viewer)
			case "missing-chat":
				execOrFatal(t, d.f.h.db, `DELETE FROM chats WHERE id=?`, d.f.chat)
			case "nonparticipant":
				viewer, role = "approval-outsider", "MANAGER"
				execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES(?,?)`, viewer, viewer+"@example.test")
				execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, viewer+"-member", d.f.workspace, viewer, role)
			}
			h := NewApprovalsHandler(d.f.h.db, newTestLogger(), nil)
			request := func(handler http.HandlerFunc, query string) *httptest.ResponseRecorder {
				req := withWorkspaceUser(httptest.NewRequest("GET", "/"+query, nil), viewer, d.f.workspace, role)
				req.SetPathValue("id", d.id)
				req.SetPathValue("agentId", d.f.agent)
				rr := httptest.NewRecorder()
				handler(rr, req)
				return rr
			}
			want := 0
			if scenario == "current" {
				want = 1
			}
			get := request(h.Get, "")
			if want == 1 && get.Code != 200 || want == 0 && get.Code != 404 {
				t.Fatalf("get %d %s", get.Code, get.Body.String())
			}
			list := request(h.List, "?limit=1")
			var page struct {
				Rows  []harbormaster.Request `json:"rows"`
				Count int                    `json:"count"`
				More  bool                   `json:"has_more"`
			}
			if json.Unmarshal(list.Body.Bytes(), &page) != nil || list.Code != 200 || page.Count != want || len(page.Rows) != want || page.More {
				t.Fatalf("page %d %s", list.Code, list.Body.String())
			}
			badge := request(NewAgentInboxHandler(d.f.h.db, newTestLogger()).Handle, "")
			var agent agentInboxResponse
			if json.Unmarshal(badge.Body.Bytes(), &agent) != nil || badge.Code != 200 || agent.ApprovalsPending != want {
				t.Fatalf("badge %d %s", badge.Code, badge.Body.String())
			}
			if want == 0 && (strings.Contains(list.Body.String(), d.id) || strings.Contains(list.Body.String(), "Review this entire run")) {
				t.Fatal("private source leaked")
			}
			// Legacy/per-tool/async/autonomous records retain their existing
			// visibility, even when the new private source has become hidden.
			for i, payload := range []string{`{}`, `{"tool":"shell.exec","gate_mode":"sync"}`, `{"tool":"agent_run","gate_mode":"async","correlation":{"trigger_type":"human_chat"}}`, `{"tool":"agent_run","gate_mode":"sync","correlation":{"trigger_type":"cron"}}`} {
				execOrFatal(t, d.f.h.db, `INSERT INTO approvals_queue(id,workspace_id,agent_id,requested_by,kind,reason,payload,status,created_at) VALUES(?,?,?,'legacy','custom','Legacy',?,'pending','2099-01-01T00:00:00Z')`, fmt.Sprintf("legacy-%d", i), d.f.workspace, d.f.agent, payload)
			}
			list = request(h.List, "?limit=1&offset=3")
			if json.Unmarshal(list.Body.Bytes(), &page) != nil || list.Code != 200 || len(page.Rows) != 1 || page.Rows[0].ID != "legacy-0" || page.More != (want == 1) {
				t.Fatalf("filtered offset/legacy %d %s", list.Code, list.Body.String())
			}
		})
	}
}

func TestVerifiedRunApprovalRetirementIsAtomicAndSurvivesHardDelete(t *testing.T) {
	for _, door := range []string{"retention", "gdpr", "marker-failure"} {
		t.Run(door, func(t *testing.T) {
			d := newApprovalDecisionRig(t)
			execOrFatal(t, d.f.h.db, `UPDATE approvals_queue SET requested_by=?,status='approved',decided_by=?,decided_at='2000-01-01T00:00:00.000Z' WHERE id=?`, d.f.user, d.f.user, d.id)
			if door == "marker-failure" {
				execOrFatal(t, d.f.h.db, `CREATE TRIGGER reject_retirement BEFORE INSERT ON journal_entries WHEN NEW.entry_type='approval.source_retired' BEGIN SELECT RAISE(ABORT,'retirement unavailable'); END`)
			}
			if door == "gdpr" {
				h := NewAdminGDPRHandler(d.f.h.db, newTestLogger(), t.TempDir())
				h.SetJournal(d.r.Journal())
				req := withWorkspaceUser(httptest.NewRequest("DELETE", "/", strings.NewReader(`{"reason":"Synthetic erasure regression"}`)), d.f.user, d.f.workspace, "OWNER")
				req.SetPathValue("userId", d.f.user)
				rr := httptest.NewRecorder()
				h.DeleteUserData(rr, req)
				if rr.Code != 202 {
					t.Fatalf("GDPR %d %s", rr.Code, rr.Body.String())
				}
			} else {
				n, _, err := harbormaster.SweepApprovalsRetention(t.Context(), d.f.h.db, d.f.workspace, 90, d.r.Journal())
				if door == "marker-failure" {
					if err == nil || n != 0 {
						t.Fatalf("marker failure did not roll back %d %v", n, err)
					}
				} else if err != nil || n != 1 {
					t.Fatalf("retention %d %v", n, err)
				}
			}
			row, err := harbormaster.Get(t.Context(), d.f.h.db, d.f.workspace, d.id)
			var markers int
			if err != nil {
				t.Fatal(err)
			}
			if err = d.f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type='approval.source_retired'`, d.f.workspace).Scan(&markers); err != nil {
				t.Fatal(err)
			}
			if door == "marker-failure" {
				if row == nil || markers != 0 {
					t.Fatal("failed marker left partial deletion or journal effect")
				}
				return
			}
			if row != nil || markers != 1 {
				t.Fatalf("hard delete/marker row=%+v count=%d", row, markers)
			}
			supported, chat, err := chataudience.RunApprovalJournalSource(t.Context(), d.f.h.db, d.f.workspace, d.id)
			if err != nil || !supported || chat != "" {
				t.Fatalf("source deletion lost private classification %v %q %v", supported, chat, err)
			}
			var leaked bool
			if err = d.f.h.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM journal_entries WHERE workspace_id=? AND entry_type='approval.source_retired' AND (payload<>'{}' OR agent_id IS NOT NULL AND agent_id<>'' OR trace_id IS NOT NULL AND trace_id<>''))`, d.f.workspace).Scan(&leaked); err != nil || leaked {
				t.Fatalf("retirement retained private metadata %v %v", leaked, err)
			}
		})
	}
}

func TestVerifiedRunApprovalRetirementCannotBeForgedBySidecar(t *testing.T) {
	d := newApprovalDecisionRig(t)
	req := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"workspace_id":%q,"agent_id":%q,"crew_id":%q,"type":"approval.source_retired","summary":"Fake retirement","refs":{"approval_id":%q}}`, d.f.workspace, d.f.agent, d.f.crew, d.id)))
	rr := httptest.NewRecorder()
	d.r.handleSidecarEmit(rr, req)
	if rr.Code != 400 {
		t.Fatalf("sidecar forged private classification %d %s", rr.Code, rr.Body.String())
	}
	supported, chat, err := chataudience.RunApprovalJournalSource(t.Context(), d.f.h.db, d.f.workspace, d.id)
	if err != nil || !supported || chat != d.f.chat {
		t.Fatalf("forged marker changed current source %v %q %v", supported, chat, err)
	}
}

func TestVerifiedRunApprovalRetirementUsesConfiguredChainAndNoDuplicate(t *testing.T) {
	db := setupTestDB(t)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('retirement-key-space','Synthetic private source','retirement-key-space')`)
	key := journal.DeriveChainKey("synthetic-custom-retirement-key")
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{ChainKey: key, FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	id, err := harbormaster.Enqueue(t.Context(), db, writer, harbormaster.Request{WorkspaceID: "retirement-key-space", RequestedBy: "synthetic", Kind: harbormaster.KindCustom, Reason: "Private request", Payload: map[string]any{"tool": "agent_run", "gate_mode": "sync", "correlation": map[string]any{"trigger_type": "human_chat", "chat_id": "synthetic-chat", "run_id": "synthetic-run", "trigger_message_id": "synthetic-trigger"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		n, err := harbormaster.DeleteSources(journal.WithRunID(t.Context(), "must-not-retain-this-run"), db, writer, "retirement-key-space", `aq.id=?`, id)
		if err != nil || n != int64(1-i) {
			t.Fatalf("delete%d %d %v", i, n, err)
		}
	}
	checked, err := journal.VerifyChainWithKey(t.Context(), db, "retirement-key-space", key)
	if err != nil || !checked.OK {
		t.Fatalf("configured key not retained %+v %v", checked, err)
	}
	wrong, err := journal.VerifyChain(t.Context(), db, "retirement-key-space")
	if err != nil || wrong.OK {
		t.Fatalf("test must distinguish default key %+v %v", wrong, err)
	}
	var count, correlated int
	if err = db.QueryRow(`SELECT COUNT(*),SUM(CASE WHEN COALESCE(trace_id,'')<>'' OR COALESCE(span_id,'')<>'' THEN 1 ELSE 0 END) FROM journal_entries WHERE workspace_id='retirement-key-space' AND entry_type='approval.source_retired'`).Scan(&count, &correlated); err != nil || count != 1 || correlated != 0 {
		t.Fatalf("duplicate/correlated retirement count%d correlated%d %v", count, correlated, err)
	}
}
