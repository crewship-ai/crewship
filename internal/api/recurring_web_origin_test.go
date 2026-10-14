package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestLegacyRecurringRequiresAuthenticatedSaveForFutureExecutionAuthority(t *testing.T) {
	h, user, workspace, crew := newRecurringHandler(t)
	execOrFatal(t, h.db, `INSERT INTO recurring_issues(id,workspace_id,crew_id,title,cron_expression,created_by,enabled,next_run) VALUES('legacy-web-template',?,?, 'Legacy','0 9 * * *',?,1,'2020-01-01T00:00:00Z')`, workspace, crew, user)
	dispatcher := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	row := recurringDueRow{id: "legacy-web-template", workspaceID: workspace, crewID: crew, title: "Legacy", cronExpr: "0 9 * * *", createdBy: sql.NullString{String: user, Valid: true}}
	dispatcher.fireOne(t.Context(), row)
	var legacyOrigin string
	if err := h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM missions WHERE workspace_id=?`, workspace).Scan(&legacyOrigin); err != nil || legacyOrigin == "" {
		t.Fatalf("legacy creator fabricated authority %q %v", legacyOrigin, err)
	}
	if _, err := runauthority.ValidateOriginReceipt(t.Context(), h.db, legacyOrigin, workspace); err == nil {
		t.Fatal("legacy creator inferred a human execution grant")
	}
	// Saving an unchanged valid field is an explicit new authenticated admission.
	req := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"title":"Legacy"}`))
	req.SetPathValue("recurringId", row.id)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("authenticated re-admission %d %s", rr.Code, rr.Body.String())
	}
	var origin string
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM recurring_issues WHERE id=?`, row.id).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	proof, err := runauthority.ValidateOriginReceipt(t.Context(), h.db, origin, workspace)
	if err != nil || proof.HumanAuthority == nil || proof.HumanAuthority.Kind != "web_recurring" || proof.HumanAuthority.UserID != user {
		t.Fatalf("saved template receipt %+v %v", proof, err)
	}
	execOrFatal(t, h.db, `UPDATE recurring_issues SET next_run='2020-01-02T00:00:00Z' WHERE id=?`, row.id)
	dispatcher.fireOne(t.Context(), row)
	var copied string
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM missions WHERE chat_origin_id IS NOT NULL AND chat_origin_id<>? AND chat_origin_id<>? AND workspace_id=?`, origin, legacyOrigin, workspace).Scan(&copied); err != nil || copied == "" || copied == origin {
		t.Fatalf("future occurrence origin %q %v", copied, err)
	}
	occurrence, err := runauthority.ValidateOriginReceipt(t.Context(), h.db, copied, workspace)
	if err != nil || occurrence.HumanAuthority.Kind != "web_issue" || occurrence.HumanAuthority.MemberID != proof.HumanAuthority.MemberID || occurrence.HumanAuthority.MemberRevision != proof.HumanAuthority.MemberRevision {
		t.Fatalf("occurrence widened template grant: %+v %v", occurrence, err)
	}
	// Already created occurrences are not retroactively reauthorized.
	var unchanged string
	if err := h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM missions WHERE chat_origin_id=? AND workspace_id=?`, legacyOrigin, workspace).Scan(&unchanged); err != nil || unchanged != legacyOrigin {
		t.Fatalf("historical occurrence rebound %q %v", unchanged, err)
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	execOrFatal(t, h.db, `UPDATE recurring_issues SET next_run='2020-01-03T00:00:00Z' WHERE id=?`, row.id)
	dispatcher.fireOne(t.Context(), row)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != 2 {
		t.Fatalf("revoked template fired count%d %v", count, err)
	}
}

func TestRecurringStaleTickUsesCurrentAuthenticatedDefinitionAndGrant(t *testing.T) {
	h, user, workspace, crew := newRecurringHandler(t)
	execOrFatal(t, h.db, `INSERT INTO recurring_issues(id,workspace_id,crew_id,title,cron_expression,created_by,enabled,next_run) VALUES('edited-web-template',?,?, 'Old snapshot','0 9 * * *',?,1,'2020-01-01T00:00:00Z')`, workspace, crew, user)
	stale := recurringDueRow{id: "edited-web-template", workspaceID: workspace, crewID: crew, title: "Old snapshot", cronExpr: "0 9 * * *", nextRunBucket: "2020-01-01T00:00:00Z"}
	req := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"title":"Current authenticated definition"}`))
	req.SetPathValue("recurringId", stale.id)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("authenticated edit %d %s", rr.Code, rr.Body.String())
	}
	dispatcher := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	dispatcher.fireOne(t.Context(), stale)
	var title, origin, currentOrigin string
	if err := h.db.QueryRow(`SELECT title,chat_origin_id FROM missions WHERE workspace_id=?`, workspace).Scan(&title, &origin); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM recurring_issues WHERE id=?`, stale.id).Scan(&currentOrigin); err != nil {
		t.Fatal(err)
	}
	if title != "Current authenticated definition" || origin == "" || origin == currentOrigin {
		t.Fatalf("mixed stale content with current grant: %s %s %s", title, origin, currentOrigin)
	}
	child, err := runauthority.ValidateOriginReceipt(t.Context(), h.db, origin, workspace)
	source, sourceErr := runauthority.ValidateOriginReceipt(t.Context(), h.db, currentOrigin, workspace)
	if err != nil || sourceErr != nil || child.HumanAuthority.Kind != "web_issue" || child.HumanAuthority.MemberRevision != source.HumanAuthority.MemberRevision {
		t.Fatalf("occurrence/source receipt mismatch %+v %+v %v %v", child, source, err, sourceErr)
	}
	// The old due snapshot cannot fire twice after the schedule advanced.
	dispatcher.fireOne(t.Context(), stale)
	execOrFatal(t, h.db, `UPDATE recurring_issues SET next_run='2020-01-01T00:00:00Z',enabled=0 WHERE id=?`, stale.id)
	dispatcher.fireOne(t.Context(), stale)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stale/disabled work executed count%d %v", count, err)
	}
}

func TestLegacyRecurringWithoutCreatorCannotBecomeAutonomousAssignment(t *testing.T) {
	h, _, workspace, crew := newRecurringHandler(t)
	execOrFatal(t, h.db, `INSERT INTO recurring_issues(id,workspace_id,crew_id,title,cron_expression,enabled,next_run) VALUES('unattributed-template',?,?,'Unknown admission','0 9 * * *',1,'2020-01-01T00:00:00Z')`, workspace, crew)
	NewRecurringIssueDispatcher(h.db, nil, newTestLogger()).fireOne(t.Context(), recurringDueRow{id: "unattributed-template", workspaceID: workspace})
	var issueID, originID, agentID, raw, rootRun, chat, trigger string
	var creator sql.NullString
	if err := h.db.QueryRow(`SELECT id,chat_origin_id,lead_agent_id,created_by_user_id FROM missions WHERE workspace_id=?`, workspace).Scan(&issueID, &originID, &agentID, &creator); err != nil {
		t.Fatal(err)
	}
	if originID == "" || creator.Valid {
		t.Fatalf("unverified occurrence origin=%q creator=%+v", originID, creator)
	}
	if err := h.db.QueryRow(`SELECT human_authority,root_run_id,chat_id,trigger_message_id FROM execution_origins WHERE id=?`, originID).Scan(&raw, &rootRun, &chat, &trigger); err != nil {
		t.Fatal(err)
	}
	if raw != "{}" || rootRun != "unverified-recurring:"+issueID || chat != "" || trigger != "" {
		t.Fatalf("fabricated principal: %s %s %s %s", raw, rootRun, chat, trigger)
	}
	if _, err := runauthority.ValidateOriginReceipt(t.Context(), h.db, originID, workspace); err == nil {
		t.Fatal("unknown admission validated")
	}
	// Copy the durable origin exactly as the planning/task insertion does.
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,title) VALUES(?,?,?,'Issue chat')`, issueID, workspace, agentID)
	execOrFatal(t, h.db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at,mission_id,chat_origin_id) SELECT 'unknown-recurring-assignment',workspace_id,id,lead_agent_id,lead_agent_id,'Planning','PENDING',datetime('now'),id,chat_origin_id FROM missions WHERE id=?`, issueID)
	var copied string
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM assignments WHERE id='unknown-recurring-assignment'`).Scan(&copied); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"host_admitted": true, "trigger_type": "ASSIGNMENT", "chat_id": issueID, "chat_origin_id": copied})
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,trace_id,entry_type,actor_type,summary,payload) VALUES('unknown-recurring-start',?,?,'unknown-recurring-run','run.started','orchestrator','Planning',?)`, workspace, agentID, string(payload))
	if _, err := runauthority.Resolve(t.Context(), h.db, "unknown-recurring-run", agentID, workspace); err == nil {
		t.Fatal("unknown recurring assignment classified autonomous")
	}
	var enabled, count int
	var templateOrigin sql.NullString
	if err := h.db.QueryRow(`SELECT enabled,run_count,chat_origin_id FROM recurring_issues WHERE id='unattributed-template'`).Scan(&enabled, &count, &templateOrigin); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || count != 1 || templateOrigin.Valid {
		t.Fatalf("record generation changed: enabled=%d count=%d origin=%+v", enabled, count, templateOrigin)
	}
}

func TestLegacyRecurringNoticeIsOnceAndNoRecipientDoesNotBlockGeneration(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "eligible owner", true: "no eligible recipient"}[restricted], func(t *testing.T) {
			h, user, workspace, crew := newRecurringHandler(t)
			if restricted {
				execOrFatal(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=?`, user)
			}
			execOrFatal(t, h.db, `INSERT INTO recurring_issues(id,workspace_id,crew_id,title,cron_expression,enabled,next_run) VALUES('legacy-notice-template',?,?,'Legacy','0 9 * * *',1,'2020-01-01T00:00:00Z')`, workspace, crew)
			d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
			row := recurringDueRow{id: "legacy-notice-template", workspaceID: workspace}
			d.fireOne(t.Context(), row)
			execOrFatal(t, h.db, `UPDATE recurring_issues SET next_run='2020-01-02T00:00:00Z' WHERE id='legacy-notice-template'`)
			d.fireOne(t.Context(), row)
			var count, enabled, runs, notices int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE workspace_id=? AND chat_origin_id IS NOT NULL`, workspace).Scan(&count); err != nil || count != 2 {
				t.Fatalf("generation %d %v", count, err)
			}
			if err := h.db.QueryRow(`SELECT enabled,run_count FROM recurring_issues WHERE id='legacy-notice-template'`).Scan(&enabled, &runs); err != nil || enabled != 1 || runs != 2 {
				t.Fatalf("schedule stalled %d %d %v", enabled, runs, err)
			}
			want := 1
			if restricted {
				want = 0
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='recurring-reauthorize:legacy-notice-template'`).Scan(&notices); err != nil || notices != want {
				t.Fatalf("notice count %d want%d %v", notices, want, err)
			}
		})
	}
}
