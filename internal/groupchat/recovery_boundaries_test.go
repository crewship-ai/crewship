package groupchat

import (
	"errors"
	"fmt"

	"testing"
)

func TestActivitySettingsRoundTripDefaultsAndReadFailure(t *testing.T) {
	s, db, c := activityFixture(t)
	got, err := s.Activity(t.Context(), "w", "u0", c.ID)
	if err != nil || got != (ActivitySettings{}) {
		t.Fatalf("defaults=%+v %v", got, err)
	}
	want := ActivitySettings{Routines: true}
	if err := s.SetActivity(t.Context(), "w", "u0", c.ID, want); err != nil {
		t.Fatal(err)
	}
	got, err = s.Activity(t.Context(), "w", "u1", c.ID)
	if err != nil || got != want {
		t.Fatalf("persisted=%+v %v", got, err)
	}
	private := create(t, s, "group")
	if _, err := s.Activity(t.Context(), "w", "u0", private.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("private history opted in: %v", err)
	}
	if err := s.SetActivity(t.Context(), "other", "u104", c.ID, want); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign settings accepted: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE workspace_conversation_activity`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activity(t.Context(), "w", "u0", c.ID); err == nil {
		t.Fatal("unavailable settings became defaults")
	}
	if err := s.ProjectActivity(t.Context()); err == nil {
		t.Fatal("unavailable settings became completed projection")
	}
}

func TestOutboxAcknowledgementIsDurableIdempotentAndDoesNotConsumeNextEvent(t *testing.T) {
	s, db, _ := fixture(t)
	c := create(t, s, "channel")
	for _, id := range []string{"one", "two"} {
		if _, _, err := s.Send(t.Context(), "w", "u0", c.ID, SendInput{ClientID: id, Content: id}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.PendingEvents(t.Context(), 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("first event=%v %v", events, err)
	}
	id := events[0].ID
	if err := s.AcknowledgeEvent(t.Context(), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty acknowledgement=%v", err)
	}
	if err := s.AcknowledgeEvent(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := db.QueryRow(`SELECT delivered_at FROM workspace_conversation_outbox WHERE id=?`, id).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeEvent(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	var again string
	if err := db.QueryRow(`SELECT delivered_at FROM workspace_conversation_outbox WHERE id=?`, id).Scan(&again); err != nil || first != again {
		t.Fatalf("ack moved delivery time: %q %q %v", first, again, err)
	}
	events, err = New(db).PendingEvents(t.Context(), 1000)
	if err != nil || len(events) != 1 || events[0].ID == id {
		t.Fatalf("replay did not preserve next event: %v %v", events, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeEvent(t.Context(), events[0].ID); err == nil {
		t.Fatal("uncommitted acknowledgement accepted")
	}
	if _, err := s.PendingEvents(t.Context(), 0); err == nil {
		t.Fatal("outbox failure became empty queue")
	}
}

func TestActivityProjectionRollsBackEveryDurableBoundaryAndCanRetry(t *testing.T) {
	for _, failure := range []struct{ name, sql string }{
		{"message", `CREATE TRIGGER reject_write BEFORE INSERT ON workspace_conversation_messages BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`},
		{"sequence", `CREATE TRIGGER reject_write BEFORE UPDATE ON workspace_conversations BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`},
		{"cursor", `CREATE TRIGGER reject_write BEFORE UPDATE ON workspace_conversation_activity BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`},
	} {
		t.Run(failure.name, func(t *testing.T) {
			s, db, c := activityFixture(t)
			if err := s.SetActivity(t.Context(), "w", "u0", c.ID, ActivitySettings{Issues: true}); err != nil {
				t.Fatal(err)
			}
			emitActivity(t, db, 1, "mission.created", "issue", `{}`)
			if _, err := db.Exec(failure.sql); err != nil {
				t.Fatal(err)
			}
			if err := s.ProjectActivity(t.Context()); err == nil {
				t.Fatal("failed projection accepted")
			}
			for _, table := range []string{"workspace_conversation_messages", "workspace_conversation_outbox"} {
				if n := activityCount(t, db, table); n != 0 {
					t.Fatalf("partial %s=%d", table, n)
				}
			}
			if _, err := db.Exec(`DROP TRIGGER reject_write`); err != nil {
				t.Fatal(err)
			}
			if err := s.ProjectActivity(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := s.ProjectActivity(t.Context()); err != nil {
				t.Fatal(err)
			}
			if n := activityCount(t, db, "workspace_conversation_messages"); n != 1 {
				t.Fatalf("retry lost/duplicated event: %d", n)
			}
		})
	}
}

func TestActivityCannotAdvanceCursorPastUnreadableSource(t *testing.T) {
	for _, source := range []string{"journal_entries", "missions", "pipelines"} {
		t.Run(source, func(t *testing.T) {
			s, db, c := activityFixture(t)
			if err := s.SetActivity(t.Context(), "w", "u0", c.ID, ActivitySettings{Issues: true, Routines: true}); err != nil {
				t.Fatal(err)
			}
			emitActivity(t, db, 1, "mission.created", "issue", `{}`)
			emitActivity(t, db, 2, "pipeline.run.completed", "", `{"pipeline_id":"routine"}`)
			if _, err := db.Exec(`ALTER TABLE ` + source + ` RENAME TO saved_source`); err != nil {
				t.Fatal(err)
			}
			if err := s.ProjectActivity(t.Context()); err == nil {
				t.Fatal("missing source accepted")
			}
			if n := activityCount(t, db, "workspace_conversation_messages"); n != 0 {
				t.Fatalf("partial projection=%d", n)
			}
			if _, err := db.Exec(`ALTER TABLE saved_source RENAME TO ` + source); err != nil {
				t.Fatal(err)
			}
			if err := s.ProjectActivity(t.Context()); err != nil {
				t.Fatal(err)
			}
			if n := activityCount(t, db, "workspace_conversation_messages"); n != 2 {
				t.Fatalf("retry skipped events=%d", n)
			}
		})
	}
}

func TestHumanMessageFailuresDoNotLeaveSequenceMembershipOrJobs(t *testing.T) {
	for _, table := range []string{"workspace_conversation_messages", "workspace_conversation_members", "workspace_conversation_agent_jobs", "workspace_conversations"} {
		t.Run(table, func(t *testing.T) {
			s, db, _ := fixture(t)
			c := create(t, s, "channel")
			if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,name) VALUES('agent','w','Agent')`); err != nil {
				t.Fatal(err)
			}
			if err := s.AddAgent(t.Context(), "w", "u0", c.ID, "agent"); err != nil {
				t.Fatal(err)
			}
			before := activityCount(t, db, "workspace_conversation_messages")
			operation := "INSERT"
			if table == "workspace_conversations" {
				operation = "UPDATE"
			}
			if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_write BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`, operation, table)); err != nil {
				t.Fatal(err)
			}
			input := SendInput{ClientID: "retry", Content: "please help", MentionedAgentIDs: []string{"agent"}}
			if _, _, err := s.Send(t.Context(), "w", "u1", c.ID, input); err == nil {
				t.Fatal("failed message accepted")
			}
			if n := activityCount(t, db, "workspace_conversation_messages"); n != before {
				t.Fatalf("partial message=%d", n)
			}
			if n := activityCount(t, db, "workspace_conversation_agent_jobs"); n != 0 {
				t.Fatalf("partial job=%d", n)
			}
			if _, err := db.Exec(`DROP TRIGGER reject_write`); err != nil {
				t.Fatal(err)
			}
			m, duplicate, err := s.Send(t.Context(), "w", "u1", c.ID, input)
			if err != nil || duplicate || m.Sequence != int64(before+1) {
				t.Fatalf("retry=%+v duplicate=%v %v", m, duplicate, err)
			}
		})
	}
}

func TestConversationCreationRollsBackWhenMembershipCannotPersist(t *testing.T) {
	for _, table := range []string{"workspace_conversations", "workspace_conversation_members"} {
		t.Run(table, func(t *testing.T) {
			s, db, _ := fixture(t)
			if _, err := db.Exec(`CREATE TRIGGER reject_write BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(t.Context(), "w", "u0", CreateInput{Title: "Room", Kind: "group", MemberIDs: []string{"u1"}}); err == nil {
				t.Fatal("partial conversation accepted")
			}
			if n := activityCount(t, db, "workspace_conversations"); n != 0 {
				t.Fatalf("partial conversation=%d", n)
			}
			if n := activityCount(t, db, "workspace_conversation_members"); n != 0 {
				t.Fatalf("partial members=%d", n)
			}
		})
	}
}

func TestActivityProjectionIgnoresRemovedChannel(t *testing.T) {
	s, _, _ := activityFixture(t)
	if err := s.projectChannelActivity(t.Context(), "gone"); err != nil {
		t.Fatal(err)
	}
}

func TestUnavailableAgentCatalogCannotBecomeEmptyHistoryOrMembership(t *testing.T) {
	s, db, _ := fixture(t)
	c := create(t, s, "channel")
	if _, err := db.Exec(`ALTER TABLE agents RENAME TO saved_agents`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Agents(t.Context(), "w", "u0", c.ID); err == nil {
		t.Fatal("missing agent catalog became empty list")
	}
	if _, err := s.Messages(t.Context(), "w", "u0", c.ID, 0, 50); err == nil {
		t.Fatal("missing agent identities became empty history")
	}
	if _, err := s.MessagesBefore(t.Context(), "w", "u0", c.ID, 0, 50); err == nil {
		t.Fatal("missing identities became empty page")
	}
	if err := s.AddAgent(t.Context(), "w", "u0", c.ID, "a"); err == nil {
		t.Fatal("missing catalog accepted membership")
	}
	if _, err := db.Exec(`ALTER TABLE assignments RENAME TO saved_assignments`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Jobs(t.Context(), "w", "u0", c.ID); err == nil {
		t.Fatal("unreadable job state became idle")
	}
}

func TestMalformedMessageDoesNotBecomePartialHistoryOrOverwriteRetry(t *testing.T) {
	s, db, _ := fixture(t)
	c := create(t, s, "channel")
	input := SendInput{ClientID: "idempotent", Content: "hello"}
	m, _, err := s.Send(t.Context(), "w", "u0", c.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workspace_conversation_messages SET mentioned_agent_ids_json='invalid' WHERE id=?`, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Messages(t.Context(), "w", "u0", c.ID, 0, 10); err == nil {
		t.Fatal("corrupt history became partial success")
	}
	if _, err := s.MessagesBefore(t.Context(), "w", "u0", c.ID, 0, 10); err == nil {
		t.Fatal("corrupt page became partial success")
	}
	if _, _, err := s.Send(t.Context(), "w", "u0", c.ID, input); err == nil {
		t.Fatal("corrupt idempotency record overwritten")
	}
	if n := activityCount(t, db, "workspace_conversation_messages"); n != 1 {
		t.Fatalf("duplicated damaged message: %d", n)
	}
}

func TestAgentRemovalRollsBackMembershipAndCancellationOnStorageFailure(t *testing.T) {
	for _, table := range []string{"workspace_conversation_agents", "assignments", "workspace_conversation_agent_jobs", "workspace_conversation_messages", "workspace_conversations"} {
		t.Run(table, func(t *testing.T) {
			s, db, _ := fixture(t)
			c := create(t, s, "channel")
			if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,name) VALUES('a','w','Agent'); INSERT INTO assignments(id,status) VALUES('assignment','QUEUED')`); err != nil {
				t.Fatal(err)
			}
			if err := s.AddAgent(t.Context(), "w", "u0", c.ID, "a"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Send(t.Context(), "w", "u0", c.ID, SendInput{ClientID: "request", Content: "hello", MentionedAgentIDs: []string{"a"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE workspace_conversation_agent_jobs SET state='queued',assignment_id='assignment'`); err != nil {
				t.Fatal(err)
			}
			operation := "UPDATE"
			if table == "workspace_conversation_agents" {
				operation = "DELETE"
			}
			if table == "workspace_conversation_messages" {
				operation = "INSERT"
			}
			if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_write BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`, operation, table)); err != nil {
				t.Fatal(err)
			}
			if err := s.RemoveAgent(t.Context(), "w", "u0", c.ID, "a"); err == nil {
				t.Fatal("partial removal accepted")
			}
			members, err := s.Agents(t.Context(), "w", "u0", c.ID)
			if err != nil || len(members) != 1 {
				t.Fatalf("membership lost: %v %v", members, err)
			}
			var canceled int
			if err := db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE cancel_requested_at IS NOT NULL`).Scan(&canceled); err != nil || canceled != 0 {
				t.Fatalf("partial cancellation=%d %v", canceled, err)
			}
			if _, err := db.Exec(`DROP TRIGGER reject_write`); err != nil {
				t.Fatal(err)
			}
			if err := s.RemoveAgent(t.Context(), "w", "u0", c.ID, "a"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMissingConversationDoesNotGrantReadOrWriteAccess(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := t.Context()
	checks := []struct {
		name string
		err  error
	}{}
	_, err := s.Agents(ctx, "w", "u0", "missing")
	checks = append(checks, struct {
		name string
		err  error
	}{"agents", err})
	_, err = s.Jobs(ctx, "w", "u0", "missing")
	checks = append(checks, struct {
		name string
		err  error
	}{"jobs", err})
	_, err = s.MessagesBefore(ctx, "w", "u0", "missing", 0, 50)
	checks = append(checks, struct {
		name string
		err  error
	}{"history", err})
	checks = append(checks, struct {
		name string
		err  error
	}{"mute", s.SetMuted(ctx, "w", "u0", "missing", true)}, struct {
		name string
		err  error
	}{"join agent", s.AddAgent(ctx, "w", "u0", "missing", "a")})
	for _, c := range checks {
		if !errors.Is(c.err, ErrForbidden) {
			t.Errorf("%s=%v", c.name, c.err)
		}
	}
}

func TestDirectConversationCreationRemainsAtomicAcrossStorageFailures(t *testing.T) {
	for _, table := range []string{"workspace_conversations", "workspace_conversation_members", "workspace_conversation_direct_pairs"} {
		t.Run(table, func(t *testing.T) {
			s, db, _ := fixture(t)
			if _, err := db.Exec(`CREATE TRIGGER reject_write BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			if _, created, err := s.OpenDirect(t.Context(), "w", "u0", "u1"); err == nil || created {
				t.Fatalf("partial DM accepted: %v %v", created, err)
			}
			if n := activityCount(t, db, "workspace_conversations"); n != 0 {
				t.Fatalf("orphan conversation=%d", n)
			}
			if _, err := db.Exec(`DROP TRIGGER reject_write`); err != nil {
				t.Fatal(err)
			}
			first, created, err := s.OpenDirect(t.Context(), "w", "u0", "u1")
			if err != nil || !created {
				t.Fatalf("retry=%+v %v %v", first, created, err)
			}
			second, created, err := s.OpenDirect(t.Context(), "w", "u1", "u0")
			if err != nil || created || first.ID != second.ID {
				t.Fatalf("reverse pair duplicated: %+v %v %v", second, created, err)
			}
		})
	}
}

func TestContinuationRollsBackNewChannelWhenHistoryOrMembershipCannotPersist(t *testing.T) {
	for _, table := range []string{"workspace_conversations", "workspace_conversation_members", "workspace_conversation_agents", "workspace_conversation_messages", "workspace_conversation_continuations"} {
		t.Run(table, func(t *testing.T) {
			s, db, _ := fixture(t)
			source := create(t, s, "group", "u1")
			if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,name) VALUES('a','w','Agent')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER reject_write BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			input := ContinueInput{Kind: "channel", Title: "Public continuation", AgentID: "a", ClientID: "retry"}
			if _, created, err := s.Continue(t.Context(), "w", "u0", source.ID, input); err == nil || created {
				t.Fatalf("partial continuation accepted: %v %v", created, err)
			}
			if n := activityCount(t, db, "workspace_conversations"); n != 1 {
				t.Fatalf("orphan continuation=%d", n)
			}
			if n := activityCount(t, db, "workspace_conversation_messages"); n != 0 {
				t.Fatalf("partial history=%d", n)
			}
			if _, err := db.Exec(`DROP TRIGGER reject_write`); err != nil {
				t.Fatal(err)
			}
			first, created, err := s.Continue(t.Context(), "w", "u0", source.ID, input)
			if err != nil || !created {
				t.Fatalf("retry=%+v %v %v", first, created, err)
			}
			second, created, err := s.Continue(t.Context(), "w", "u0", source.ID, input)
			if err != nil || created || first.ID != second.ID {
				t.Fatalf("idempotency lost: %+v %v %v", second, created, err)
			}
		})
	}
}

func TestAgentReplyRejectsMissingJobOrInvalidContent(t *testing.T) {
	s, _, _ := fixture(t)
	if _, err := s.SendAgentReply(t.Context(), "missing", "answer"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unowned reply=%v", err)
	}
	if _, err := s.SendAgentReply(t.Context(), "missing", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty reply=%v", err)
	}
}
