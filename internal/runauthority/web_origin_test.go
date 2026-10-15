package runauthority

import (
	"errors"
	"testing"
)

func TestWebOriginPinsAuthenticatedUserMembershipResourceAndExecutor(t *testing.T) {
	db, _ := authorityFixture(t)
	for _, q := range []string{
		`UPDATE workspace_members SET role='OWNER' WHERE id='member'`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('web-crew','ws','Web','web')`,
		`UPDATE agents SET crew_id='web-crew' WHERE id='agent'`,
		`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,title,trace_id,status,mission_type) VALUES('web-issue','ws','web-crew','agent','Web','web-trace','TODO','issue')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := CaptureWebOrigin(t.Context(), tx, "web_issue", "web-issue", "human", "ws")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := ValidateOrigin(t.Context(), db, origin.ID, "agent", "ws")
	if err != nil || got.HumanAuthority == nil || got.HumanAuthority.Kind != "web_issue" || got.ChatID != "" || got.TriggerMessageID != "" {
		t.Fatalf("web admission %+v %v", got, err)
	}
	if _, err = ValidateOrigin(t.Context(), db, origin.ID, "unknown", "ws"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("foreign executor %v", err)
	}
	if _, err = ValidateOrigin(t.Context(), db, origin.ID, "agent", "foreign"); err == nil {
		t.Fatal("cross-workspace origin accepted")
	}
	if _, err = db.Exec(`UPDATE users SET suspended_at='2026-10-14T08:00:00Z' WHERE id='human'`); err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateOrigin(t.Context(), db, origin.ID, "agent", "ws"); !errors.Is(err, ErrRequesterStale) {
		t.Fatalf("suspended user %v", err)
	}
	if _, err = db.Exec(`UPDATE users SET suspended_at=NULL WHERE id='human'; UPDATE workspace_members SET role='VIEWER' WHERE id='member';UPDATE workspace_members SET role='OWNER' WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateOrigin(t.Context(), db, origin.ID, "agent", "ws"); !errors.Is(err, ErrRequesterStale) {
		t.Fatalf("membership epoch restored old grant %v", err)
	}
}

func TestWebOriginSurvivesUnrelatedChatAudienceChanges(t *testing.T) {
	db, _ := authorityFixture(t)
	for _, q := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('web-crew','ws','Web','web')`,
		`UPDATE agents SET crew_id='web-crew' WHERE id='agent'`,
		`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,title,trace_id,status,mission_type) VALUES('web-issue','ws','web-crew','agent','Web','web-trace','TODO','issue')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('unrelated-group','ws','agent','human','group')`,
		`INSERT INTO chat_participants(chat_id,user_id,role) VALUES('unrelated-group','human','member')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := CaptureWebOrigin(t.Context(), tx, "web_issue", "web-issue", "human", "ws")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var before, after, webBefore, webAfter int64
	if err := db.QueryRow(`SELECT access_revision,web_execution_revision FROM workspace_members WHERE id='member'`).Scan(&before, &webBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM chat_participants WHERE chat_id='unrelated-group' AND user_id='human'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT access_revision,web_execution_revision FROM workspace_members WHERE id='member'`).Scan(&after, &webAfter); err != nil {
		t.Fatal(err)
	}
	if after <= before || webAfter != webBefore {
		t.Fatalf("independent audience fence broad=%d→%d web=%d→%d", before, after, webBefore, webAfter)
	}
	if _, err := ValidateOrigin(t.Context(), db, origin.ID, "agent", "ws"); err != nil {
		t.Fatalf("unrelated chat removal revoked issue work: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM chats WHERE id='unrelated-group'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateOrigin(t.Context(), db, origin.ID, "agent", "ws"); err != nil {
		t.Fatalf("unrelated chat deletion revoked issue work: %v", err)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET capabilities='["issue.create"]' WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateOrigin(t.Context(), db, origin.ID, "agent", "ws"); !errors.Is(err, ErrRequesterStale) {
		t.Fatalf("execution capability change failed to fence old work: %v", err)
	}
}
