package groupchat

import (
	"context"
	"errors"
	"testing"
)

func TestChannelAgentLifecycleAndIdempotentReply(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	channel := create(t, s, "channel")
	private := create(t, s, "group", "u1")
	if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,name) VALUES('a','w','Agent'),('foreign','other','Foreign')`); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u1", channel.ID, "a"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", private.ID, "a"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", channel.ID, "foreign"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, "w", "u0", channel.ID, SendInput{ClientID: "before-join", Content: "hello", MentionedAgentIDs: []string{"a"}}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", channel.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", channel.ID, "a"); err != nil {
		t.Fatal(err)
	}
	history, err := s.Messages(ctx, "w", "u1", channel.ID, 0, 10)
	if err != nil || len(history) != 1 || history[0].Kind != "agent_joined" || history[0].SubjectAgentID != "a" {
		t.Fatalf("join history %+v %v", history, err)
	}
	in := SendInput{ClientID: "mention", Content: "Please answer", MentionedAgentIDs: []string{"a", "a"}}
	m, duplicate, err := s.Send(ctx, "w", "u1", channel.ID, in)
	if err != nil || duplicate || len(m.MentionedAgentIDs) != 1 {
		t.Fatalf("message %+v dup%v err%v", m, duplicate, err)
	}
	again, duplicate, err := s.Send(ctx, "w", "u1", channel.ID, in)
	if err != nil || !duplicate || again.ID != m.ID {
		t.Fatalf("retry %+v dup%v err%v", again, duplicate, err)
	}
	var count int
	var job string
	if err = db.QueryRow(`SELECT COUNT(*),MIN(id) FROM workspace_conversation_agent_jobs`).Scan(&count, &job); err != nil || count != 1 {
		t.Fatalf("jobs %d %v", count, err)
	}
	if _, err = s.SendAgentReply(ctx, job, "premature"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE workspace_conversation_agent_jobs SET state='queued',assignment_id='assignment' WHERE id=?`, job); err != nil {
		t.Fatal(err)
	}
	reply, err := s.SendAgentReply(ctx, job, "Agent answer")
	if err != nil || reply.AuthorAgentID != "a" || reply.AuthorUserID != "" || reply.Sequence != 3 {
		t.Fatalf("reply %+v %v", reply, err)
	}
	retry, err := s.SendAgentReply(ctx, job, "Agent answer")
	if err != nil || retry.ID != reply.ID {
		t.Fatalf("reply retry %+v %v", retry, err)
	}
	if err = s.RemoveAgent(ctx, "w", "u0", channel.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Send(ctx, "w", "u0", channel.ID, SendInput{ClientID: "after-removal", Content: "hello", MentionedAgentIDs: []string{"a"}}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	history, err = s.MessagesBefore(ctx, "w", "u1", channel.ID, 0, 2)
	if err != nil || len(history) != 2 || history[0].Sequence != 3 || history[1].Kind != "agent_left" {
		t.Fatalf("latest %+v %v", history, err)
	}
	previous, err := s.MessagesBefore(ctx, "w", "u1", channel.ID, history[0].Sequence, 2)
	if err != nil || len(previous) != 2 || previous[0].Sequence != 1 || previous[1].Sequence != 2 {
		t.Fatalf("previous %+v %v", previous, err)
	}
	if _, err = db.Exec(`DELETE FROM agents WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	history, err = s.Messages(ctx, "w", "u1", channel.ID, 0, 10)
	if err != nil || len(history) != 4 {
		t.Fatalf("deletion lost history %+v %v", history, err)
	}
}
func TestRemovedAgentCannotCompleteJob(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "channel")
	if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,name) VALUES('a','w','Agent')`); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", c.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, "w", "u0", c.ID, SendInput{ClientID: "m", Content: "answer", MentionedAgentIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	var job string
	if err := db.QueryRow(`UPDATE workspace_conversation_agent_jobs SET state='queued' RETURNING id`).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO assignments(id,status) VALUES('queued-assignment','QUEUED')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workspace_conversation_agent_jobs SET assignment_id='queued-assignment' WHERE id=?`, job); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAgent(ctx, "w", "u0", c.ID, "a"); err != nil {
		t.Fatal(err)
	}
	var canceled string
	if err := db.QueryRow(`SELECT cancel_requested_at FROM assignments WHERE id='queued-assignment'`).Scan(&canceled); err != nil || canceled == "" {
		t.Fatalf("cancellation %q %v", canceled, err)
	}
	if err := s.AddAgent(ctx, "w", "u0", c.ID, "a"); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Jobs(ctx, "w", "u1", c.ID)
	if err != nil || len(jobs) != 1 || jobs[0].State != "failed" {
		t.Fatalf("jobs %+v %v", jobs, err)
	}
	if _, err := s.SendAgentReply(ctx, job, "No longer allowed"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestRevokedRequesterCannotReceiveAgentReply(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "channel")
	if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,name) VALUES('a','w','Agent')`); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", c.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, "w", "u1", c.ID, SendInput{ClientID: "m", Content: "answer", MentionedAgentIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	var job string
	if err := db.QueryRow(`UPDATE workspace_conversation_agent_jobs SET state='queued' RETURNING id`).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM workspace_members WHERE workspace_id='w' AND user_id='u1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendAgentReply(ctx, job, "Access changed"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}
