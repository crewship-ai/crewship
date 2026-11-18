package groupchat

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func seedInboxJob(t *testing.T) (*Store, *sql.DB, string, string) {
	t.Helper()
	s, db, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "channel")
	if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('target','w','crew','Target','target')`); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", c.ID, "target"); err != nil {
		t.Fatal(err)
	}
	message, _, err := s.Send(ctx, "w", "u0", c.ID, SendInput{ClientID: "inbox-source", Content: "actual human source", MentionedAgentIDs: []string{"target"}})
	if err != nil {
		t.Fatal(err)
	}
	var job string
	if err := db.QueryRow(`SELECT id FROM workspace_conversation_agent_jobs WHERE message_id=?`, message.ID).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO assignments(id,status,workspace_id,assigned_to_id) VALUES('inbox-assignment','RUNNING','w','target')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workspace_conversation_agent_jobs SET assignment_id='inbox-assignment',state='queued' WHERE id=?`, job); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO work_items VALUES('inbox-work','assignment','inbox-assignment','w','target','running',1)`,
		`INSERT INTO work_attempts VALUES('inbox-run','inbox-work',1,NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return s, db, c.ID, job
}

func TestChannelInboxProjectionIsPureAndSourceLinked(t *testing.T) {
	s, db, _, job := seedInboxJob(t)
	for i := 0; i < 2; i++ {
		rows, err := s.AgentInbox(t.Context(), "w", "u0", "target")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ID != job || rows[0].Content != "actual human source" || rows[0].ReadAt != nil || rows[0].State != "RUNNING" {
			t.Fatalf("projection=%+v", rows)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_inbox_receipts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("operator read forged an agent receipt")
	}
	actual, content, err := ReadInboxAssignment(t.Context(), db, "w", "crew", "target", "inbox-assignment", "inbox-run")
	if err != nil || actual != job || content != "actual human source" {
		t.Fatalf("bound source=%q %q %v", actual, content, err)
	}
	if _, _, err := ReadInboxAssignment(t.Context(), db, "w", "crew", "target", "other-assignment", "inbox-run"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unrelated source=%v", err)
	}
	rows, err := s.AgentInbox(t.Context(), "other", "u104", "target")
	if err != nil || len(rows) != 0 {
		t.Fatalf("foreign workspace=%+v %v", rows, err)
	}
}

func TestChannelInboxRevocationFencesProjectionAndAgentRead(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"sender member epoch", `UPDATE workspace_members SET access_revision=access_revision+1 WHERE workspace_id='w' AND user_id='u0'`},
		{"sender account epoch", `UPDATE users SET authority_revision=authority_revision+1 WHERE id='u0'`},
		{"sender suspended", `UPDATE users SET suspended_at='now' WHERE id='u0'`},
		{"sender deleted", `DELETE FROM workspace_members WHERE workspace_id='w' AND user_id='u0'`},
		{"agent removed", `DELETE FROM workspace_conversation_agents WHERE agent_id='target'`},
		{"agent deleted", `UPDATE agents SET deleted_at='now' WHERE id='target'`},
		{"agent moved", `UPDATE agents SET workspace_id='other' WHERE id='target'`},
		{"channel deleted", `UPDATE workspace_conversations SET deleted_at='now'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _, _ := seedInboxJob(t)
			if _, err := db.Exec(tc.query); err != nil {
				t.Fatal(err)
			}
			rows, err := s.AgentInbox(t.Context(), "w", "u1", "target")
			if err != nil || len(rows) != 0 {
				t.Fatalf("revoked source projection=%+v %v", rows, err)
			}
			if _, _, err := ReadInboxAssignment(t.Context(), db, "w", "crew", "target", "inbox-assignment", "inbox-run"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("revoked agent source=%v", err)
			}
		})
	}
}

func TestChannelInboxRemovedAndReaddedTargetCannotReviveReadOrReply(t *testing.T) {
	s, db, channel, job := seedInboxJob(t)
	if err := s.RemoveAgent(t.Context(), "w", "u0", channel, "target"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(t.Context(), "w", "u0", channel, "target"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadInboxAssignment(t.Context(), db, "w", "crew", "target", "inbox-assignment", "inbox-run"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cancel window revived read=%v", err)
	}
	if err := ValidateAgentJob(t.Context(), db, job); !errors.Is(err, ErrForbidden) {
		t.Fatalf("failed job revived source=%v", err)
	}
	if _, err := s.SendAgentReply(t.Context(), job, "Should not be published"); err == nil {
		t.Fatal("revoked target published a reply")
	}
}
