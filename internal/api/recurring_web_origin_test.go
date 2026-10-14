package api

import (
	"database/sql"
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
	if err := h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM missions WHERE workspace_id=?`, workspace).Scan(&legacyOrigin); err != nil || legacyOrigin != "" {
		t.Fatalf("legacy creator fabricated authority %q %v", legacyOrigin, err)
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
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM missions WHERE chat_origin_id IS NOT NULL AND chat_origin_id<>? AND workspace_id=?`, origin, workspace).Scan(&copied); err != nil || copied == "" || copied == origin {
		t.Fatalf("future occurrence origin %q %v", copied, err)
	}
	occurrence, err := runauthority.ValidateOriginReceipt(t.Context(), h.db, copied, workspace)
	if err != nil || occurrence.HumanAuthority.Kind != "web_issue" || occurrence.HumanAuthority.MemberID != proof.HumanAuthority.MemberID || occurrence.HumanAuthority.MemberRevision != proof.HumanAuthority.MemberRevision {
		t.Fatalf("occurrence widened template grant: %+v %v", occurrence, err)
	}
	// Already created occurrences are not retroactively reauthorized.
	var unchanged string
	if err := h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM missions WHERE chat_origin_id IS NULL AND workspace_id=?`, workspace).Scan(&unchanged); err != nil || unchanged != "" {
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
