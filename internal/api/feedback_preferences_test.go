package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/usermodel"
)

func TestFeedbackPreferenceLifecycle(t *testing.T) {
	b := setupFeedbackTestBed(t)
	if _, err := b.h.db.Exec(`UPDATE agents SET memory_enabled=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.h.db.Exec(`UPDATE conversation_messages SET role='assistant' WHERE id=?`, b.messageID); err != nil {
		t.Fatal(err)
	}
	submit := func(reason string) (string, int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"message_id": b.messageID, "signal": "helpful", "reason": reason})
		b.h.Create(rec, feedbackReq("POST", "/api/v1/feedback", string(body), b.userID))
		if rec.Code != 201 {
			t.Fatalf("submit: %d %s", rec.Code, rec.Body)
		}
		var out struct {
			ID       string         `json:"id"`
			Revision int            `json:"revision"`
			Memory   feedbackMemory `json:"memory"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ID, out.Revision, out.Memory.Status
	}
	drain := func() {
		t.Helper()
		if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		body, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	id, rev, status := submit("")
	if status != "feedback_only" {
		t.Fatal(status)
	}
	drain()
	if read() != "" {
		t.Fatal("bare thumb became memory")
	}
	var candidateCount int
	if err := b.h.db.QueryRow(`SELECT count(*) FROM agent_user_preferences`).Scan(&candidateCount); err != nil || candidateCount != 0 {
		t.Fatalf("bare thumb created memory candidate: %d %v", candidateCount, err)
	}
	id2, rev2, status := submit("Next time, keep answers concise.")
	if id != id2 || rev2 != rev+1 || status != "processing" {
		t.Fatalf("idempotency/revision: %s %d %s", id2, rev2, status)
	}
	_, retry, _ := submit("Next time, keep answers concise.")
	if retry != rev2 {
		t.Fatal("retry changed revision")
	}
	drain()
	if got := read(); !strings.Contains(got, "concise") || !strings.Contains(got, fmt.Sprintf("feedback %s revision %d", id, rev2)) {
		t.Fatalf("recall: %q", got)
	}
	for _, scope := range [][3]string{{b.wsID, "other-agent", b.userID}, {b.wsID, "agent-fb", b.otherID}, {"other-workspace", "agent-fb", b.userID}} {
		got, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, scope[0], scope[1], scope[2])
		if err != nil || got != "" {
			t.Fatalf("scope %v leaked: %q %v", scope, got, err)
		}
	}
	rec := httptest.NewRecorder()
	b.h.Delete(rec, feedbackReq(http.MethodDelete, "/api/v1/feedback?message_id="+b.messageID+"&signal=helpful", "", b.userID))
	if rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	drain()
	if got := read(); got != "" {
		t.Fatalf("undo resurrected: %q", got)
	}
	var tombstone string
	if err := b.h.db.QueryRow(`SELECT status FROM agent_user_preferences WHERE id=?`, id).Scan(&tombstone); err != nil || tombstone != "withdrawn" {
		t.Fatalf("tombstone: %s %v", tombstone, err)
	}
	_, _, _ = submit("The answer was bad.")
	drain()
	if read() != "" {
		t.Fatal("criticism became preference")
	}
}

func TestFeedbackPreferenceSourceAndConsent(t *testing.T) {
	for _, tc := range []struct{ name, mutation, want string }{
		{"group", `UPDATE chats SET visibility='group'`, "not_eligible"},
		{"other opener", `UPDATE chats SET created_by='user-other-fb'`, "not_eligible"},
		{"restricted", `UPDATE workspace_members SET role='MEMBER',access_mode='restricted'`, "not_eligible"},
		{"deleted agent", `UPDATE agents SET deleted_at=datetime('now')`, "not_eligible"},
		{"profile off", `INSERT INTO app_settings(key,value) VALUES('memory.user_model_profile','off')`, "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := setupFeedbackTestBed(t)
			if _, err := b.h.db.Exec(`UPDATE agents SET memory_enabled=1`); err != nil {
				t.Fatal(err)
			}
			if _, err := b.h.db.Exec(`UPDATE conversation_messages SET role='assistant'`); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			b.h.Create(rec, feedbackReq("POST", "/api/v1/feedback", fmt.Sprintf(`{"message_id":%q,"signal":"helpful","reason":"Next time, keep answers concise."}`, b.messageID), b.userID))
			if rec.Code != 201 {
				t.Fatal(rec.Body)
			}
			if _, err := b.h.db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := b.h.db.QueryRow(`SELECT status FROM agent_user_preferences`).Scan(&status); err != nil || status != tc.want {
				t.Fatalf("status=%s err=%v", status, err)
			}
		})
	}
	b := setupFeedbackTestBed(t)
	if _, err := b.h.db.Exec(`UPDATE agents SET memory_enabled=1`); err != nil {
		t.Fatal(err)
	}
	b.h.db.Exec(`UPDATE conversation_messages SET role='assistant'`)
	rec := httptest.NewRecorder()
	b.h.Create(rec, feedbackReq("POST", "/api/v1/feedback", fmt.Sprintf(`{"message_id":%q,"signal":"helpful","reason":"Next time, keep answers concise."}`, b.messageID), b.userID))
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
		t.Fatal(err)
	}
	if _, err := b.h.db.Exec(`INSERT INTO user_peer_consent(user_id,workspace_id,opted_out) VALUES(?,?,1)`, b.userID, b.wsID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.h.db.Exec(`UPDATE user_peer_consent SET opted_out=0`); err != nil {
		t.Fatal(err)
	}
	got, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
	if err != nil || got != "" {
		t.Fatalf("opt-in resurrected: %q %v", got, err)
	}
}

func TestFeedbackPreferenceFailedWriteRetriesSameSource(t *testing.T) {
	b := setupFeedbackTestBed(t)
	for _, q := range []string{`UPDATE agents SET memory_enabled=1`, `UPDATE conversation_messages SET role='assistant'`,
		`CREATE TRIGGER fail_preference_audit BEFORE INSERT ON peer_card_audit WHEN json_extract(NEW.metadata,'$.kind')='feedback_preference' BEGIN SELECT RAISE(ABORT,'injected write failure'); END;`} {
		if _, err := b.h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	b.h.Create(rec, feedbackReq("POST", "/api/v1/feedback", fmt.Sprintf(`{"message_id":%q,"signal":"helpful","reason":"Next time, keep answers concise."}`, b.messageID), b.userID))
	if rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err == nil {
		t.Fatal("injected write failure ignored")
	}
	var status string
	if err := b.h.db.QueryRow(`SELECT status FROM agent_user_preferences`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	if _, err := b.h.db.Exec(`DROP TRIGGER fail_preference_audit; UPDATE agent_user_preferences SET retry_after=''`); err != nil {
		t.Fatal(err)
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
		t.Fatal(err)
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := b.h.db.QueryRow(`SELECT count(*) FROM peer_card_audit WHERE json_extract(metadata,'$.kind')='feedback_preference' AND action='write'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry duplicated effect: %d %v", count, err)
	}
}

func TestFeedbackPreferenceUndoPreservesIndependentSource(t *testing.T) {
	b := setupFeedbackTestBed(t)
	for _, q := range []string{`UPDATE agents SET memory_enabled=1`, `UPDATE conversation_messages SET role='assistant'`} {
		if _, err := b.h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, signal := range []string{"helpful", "not_helpful"} {
		rec := httptest.NewRecorder()
		b.h.Create(rec, feedbackReq("POST", "/api/v1/feedback", fmt.Sprintf(`{"message_id":%q,"signal":%q,"reason":"Next time, keep answers concise."}`, b.messageID, signal), b.userID))
		if rec.Code != 201 {
			t.Fatal(rec.Body)
		}
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	b.h.Delete(rec, feedbackReq("DELETE", "/api/v1/feedback?message_id="+b.messageID+"&signal=helpful", "", b.userID))
	if rec.Code != 204 {
		t.Fatal(rec.Body)
	}
	body, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
	if err != nil || strings.Count(body, "prefers concise answers") != 1 {
		t.Fatalf("independent source lost or duplicated: %q %v", body, err)
	}
}

func TestFeedbackPreferenceLiveScopeAndDisabledValues(t *testing.T) {
	for _, tc := range []struct {
		name, mutation, want string
		before, alreadySaved bool
	}{
		{"new feedback while opted out", `INSERT INTO user_peer_consent(user_id,workspace_id,opted_out) SELECT created_by,workspace_id,1 FROM chats`, "disabled", true, false},
		{"new feedback with agent memory off", `UPDATE agents SET memory_enabled=0`, "disabled", true, false},
		{"new feedback with profile off", `INSERT INTO app_settings(key,value) VALUES('memory.user_model_profile','off')`, "disabled", true, false},
		{"suspension before worker", `UPDATE users SET suspended_at=''`, "not_eligible", false, false},
		{"deleted workspace before worker", `UPDATE workspaces SET deleted_at=datetime('now')`, "not_eligible", false, false},
		{"restriction elsewhere before worker", `UPDATE workspace_members SET role='MEMBER'; INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) SELECT 'restricted-elsewhere','ws-other-fb',created_by,'MEMBER','restricted' FROM chats`, "not_eligible", false, false},
		{"suspension after save", `UPDATE users SET suspended_at=''`, "saved", false, true},
		{"deleted workspace after save", `UPDATE workspaces SET deleted_at=datetime('now')`, "saved", false, true},
		{"restriction elsewhere after save", `UPDATE workspace_members SET role='MEMBER'; INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) SELECT 'restricted-elsewhere','ws-other-fb',created_by,'MEMBER','restricted' FROM chats`, "saved", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := setupFeedbackTestBed(t)
			for _, q := range []string{`UPDATE agents SET memory_enabled=1`, `UPDATE conversation_messages SET role='assistant'`} {
				if _, err := b.h.db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			mutate := func() {
				t.Helper()
				if _, err := b.h.db.Exec(tc.mutation); err != nil {
					t.Fatal(err)
				}
			}
			if tc.before {
				mutate()
			}
			rec := httptest.NewRecorder()
			b.h.Create(rec, feedbackReq("POST", "/api/v1/feedback", fmt.Sprintf(`{"message_id":%q,"signal":"helpful","reason":"Next time, keep answers concise."}`, b.messageID), b.userID))
			if rec.Code != 201 {
				t.Fatal(rec.Body)
			}
			if tc.alreadySaved {
				if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.before {
				mutate()
			}
			if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
				t.Fatal(err)
			}
			var status, value string
			if err := b.h.db.QueryRow(`SELECT status,value FROM agent_user_preferences`).Scan(&status, &value); err != nil || status != tc.want {
				t.Fatalf("status=%s value=%q error=%v", status, value, err)
			}
			if !tc.alreadySaved && value != "" {
				t.Fatalf("disabled/refused value retained: %q", value)
			}
			body, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
			if err != nil || body != "" {
				t.Fatalf("revoked recall: %q %v", body, err)
			}
			entries, err := usermodel.ListFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != "disabled" && len(entries) != 0 {
				t.Fatalf("revoked source listed: %+v", entries)
			}
		})
	}
}

func TestFeedbackPreferencePoisonDoesNotBlockOtherSources(t *testing.T) {
	b := setupFeedbackTestBed(t)
	for _, q := range []string{`UPDATE agents SET memory_enabled=1`, `UPDATE conversation_messages SET role='assistant'`,
		`CREATE TRIGGER fail_only_poison BEFORE INSERT ON peer_card_audit WHEN json_extract(NEW.metadata,'$.feedback_id')='poison' BEGIN SELECT RAISE(ABORT,'poison source'); END;`} {
		if _, err := b.h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"poison", "healthy"} {
		signal := []string{"helpful", "not_helpful"}[i]
		if _, err := b.h.db.Exec(`INSERT INTO message_feedback(id,workspace_id,chat_id,message_id,user_id,signal,reason) VALUES(?,?,?,?,?,?,'Next time, keep answers concise.')`, id, b.wsID, b.chatID, b.messageID, b.userID, signal); err != nil {
			t.Fatal(err)
		}
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err == nil {
		t.Fatal("poison failure hidden")
	}
	var status string
	if err := b.h.db.QueryRow(`SELECT status FROM agent_user_preferences WHERE id='healthy'`).Scan(&status); err != nil || status != "saved" {
		t.Fatalf("healthy source starved: %s %v", status, err)
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
		t.Fatalf("failed source ignored backoff: %v", err)
	}
}
