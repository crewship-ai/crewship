package groupchat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func activityFixture(t *testing.T) (*Store, *sql.DB, Conversation) {
	t.Helper()
	s, db, _ := fixture(t)
	var err error
	for _, sql := range []string{
		`CREATE TABLE journal_entries(id TEXT PRIMARY KEY,workspace_id TEXT,seq INTEGER,entry_type TEXT,mission_id TEXT,payload TEXT)`,
		`CREATE UNIQUE INDEX journal_seq ON journal_entries(workspace_id,seq)`,
		`CREATE TABLE missions(id TEXT PRIMARY KEY,workspace_id TEXT,identifier TEXT,crew_id TEXT,title TEXT)`,
		`CREATE TABLE pipelines(id TEXT PRIMARY KEY,workspace_id TEXT,slug TEXT,deleted_at TEXT,workspace_visible INTEGER,ephemeral INTEGER,name TEXT)`,
		`INSERT INTO missions VALUES('issue','w','ENG-1','crew','Test issue'),('foreign','other','PRIVATE-1','foreign-crew','Other issue')`,
		`INSERT INTO pipelines VALUES('routine','w','daily-check',NULL,1,0,'Daily check'),('hidden','w','private-routine',NULL,0,0,'Hidden'),('ephemeral','w','ephemeral',NULL,1,1,'Ephemeral')`,
	} {
		if _, err = db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.Create(t.Context(), "w", "u0", CreateInput{Title: "Activity", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	return s, db, c
}
func emitActivity(t *testing.T, db *sql.DB, seq int, kind, mission, payload string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO journal_entries VALUES(?,'w',?,?,?,?)`, fmt.Sprint(seq), seq, kind, mission, payload); err != nil {
		t.Fatal(err)
	}
}
func activityCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestActivityOptInScopeRestartAndNoAgentJobs(t *testing.T) {
	s, db, c := activityFixture(t)
	ctx := t.Context()
	emitActivity(t, db, 1, "mission.created", "issue", `{}`)
	if err := s.ProjectActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if activityCount(t, db, "workspace_conversation_messages") != 0 {
		t.Fatal("default delivered historical activity")
	}
	if err := s.SetActivity(ctx, "w", "u1", c.ID, ActivitySettings{Issues: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("noncreator: %v", err)
	}
	if _, err := s.Activity(ctx, "other", "u104", c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign access: %v", err)
	}
	private, err := s.Create(ctx, "w", "u0", CreateInput{Title: "private", Kind: "group"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetActivity(ctx, "w", "u0", private.ID, ActivitySettings{Issues: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("private: %v", err)
	}
	if err = s.SetActivity(ctx, "w", "u0", c.ID, ActivitySettings{Issues: true, Routines: true}); err != nil {
		t.Fatal(err)
	}
	emitActivity(t, db, 2, "mission.status_change", "issue", `{"to":"DONE","details":"SECRET @Ava"}`)
	emitActivity(t, db, 3, "mission.created", "foreign", `{}`)
	emitActivity(t, db, 4, "pipeline.run.completed", "", `{"pipeline_id":"routine","output":"SECRET"}`)
	emitActivity(t, db, 5, "pipeline.run.completed", "", `{"pipeline_id":"hidden"}`)
	emitActivity(t, db, 6, "chat.agent_response", "", `{"content":"SECRET"}`)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := New(db).ProjectActivity(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	messages, err := s.Messages(ctx, "w", "u1", c.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages: %#v", messages)
	}
	for _, m := range messages {
		if m.SourceKind != "activity" || m.AuthorAgentID != "" || m.AuthorUserID != "" || len(m.MentionedAgentIDs) != 0 || strings.Contains(m.Content, "SECRET") {
			t.Fatalf("unsafe system card %#v", m)
		}
	}
	if !strings.Contains(messages[0].Content, "DONE") || !strings.Contains(messages[0].Content, "/issues/ENG-1?workspace_id=w") || !strings.Contains(messages[1].Content, "slug=daily-check") {
		t.Fatalf("links/events %#v", messages)
	}
	if activityCount(t, db, "workspace_conversation_outbox") != 2 || activityCount(t, db, "workspace_conversation_agent_jobs") != 0 {
		t.Fatal("outbox/jobs mismatch")
	}
	if err = s.SetActivity(ctx, "w", "u0", c.ID, ActivitySettings{}); err != nil {
		t.Fatal(err)
	}
	emitActivity(t, db, 7, "mission.created", "issue", `{}`)
	if err = s.SetActivity(ctx, "w", "u0", c.ID, ActivitySettings{Issues: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.ProjectActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if activityCount(t, db, "workspace_conversation_messages") != 2 {
		t.Fatal("reenabling backfilled disabled period")
	}
	emitActivity(t, db, 8, "mission.assigned", "issue", `{}`)
	if err = s.SetActivity(ctx, "w", "u0", c.ID, ActivitySettings{Issues: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.ProjectActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if activityCount(t, db, "workspace_conversation_messages") != 3 {
		t.Fatal("idempotent settings write lost pending event")
	}
}
func TestActivityCursorAndOutboxRollbackTogether(t *testing.T) {
	s, db, c := activityFixture(t)
	ctx := t.Context()
	if err := s.SetActivity(ctx, "w", "u0", c.ID, ActivitySettings{Issues: true}); err != nil {
		t.Fatal(err)
	}
	emitActivity(t, db, 1, "mission.created", "issue", `{}`)
	if _, err := db.Exec(`CREATE TRIGGER fail_activity BEFORE INSERT ON workspace_conversation_outbox BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.ProjectActivity(ctx); err == nil {
		t.Fatal("expected outbox failure")
	}
	if activityCount(t, db, "workspace_conversation_messages") != 0 {
		t.Fatal("partial commit")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_activity`); err != nil {
		t.Fatal(err)
	}
	if err := s.ProjectActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if activityCount(t, db, "workspace_conversation_messages") != 1 {
		t.Fatal("cursor advanced on rollback")
	}
}
func TestActivityBoundedCatchup(t *testing.T) {
	s, db, c := activityFixture(t)
	ctx := t.Context()
	if err := s.SetActivity(ctx, "w", "u0", c.ID, ActivitySettings{Issues: true}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 55; i++ {
		emitActivity(t, db, i, "mission.created", "issue", `{}`)
	}
	if err := s.ProjectActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if activityCount(t, db, "workspace_conversation_messages") != 50 {
		t.Fatal("batch was not bounded")
	}
	if err := s.ProjectActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if activityCount(t, db, "workspace_conversation_messages") != 55 {
		t.Fatal("batch cursor lost tail")
	}
}

func TestActivityOmitsDeletedAndEphemeralResources(t *testing.T) {
	s, db, c := activityFixture(t)
	if err := s.SetActivity(t.Context(), "w", "u0", c.ID, ActivitySettings{Issues: true, Routines: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE crews SET deleted_at='removed' WHERE id='crew'; UPDATE pipelines SET deleted_at='removed' WHERE id='routine'`); err != nil {
		t.Fatal(err)
	}
	emitActivity(t, db, 1, "mission.created", "issue", `{}`)
	emitActivity(t, db, 2, "pipeline.run.completed", "", `{"pipeline_id":"routine"}`)
	emitActivity(t, db, 3, "pipeline.run.completed", "", `{"pipeline_id":"ephemeral"}`)
	if err := s.ProjectActivity(t.Context()); err != nil {
		t.Fatal(err)
	}
	if activityCount(t, db, "workspace_conversation_messages") != 0 {
		t.Fatal("invisible resource was published")
	}
	var issues, routines int64
	if err := db.QueryRow(`SELECT issues_cursor,routines_cursor FROM workspace_conversation_activity WHERE conversation_id=?`, c.ID).Scan(&issues, &routines); err != nil || issues != 3 || routines != 3 {
		t.Fatalf("invisible events not acknowledged: %d %d %v", issues, routines, err)
	}
}

func TestDeletedHumanCannotImpersonateActivityWithClientID(t *testing.T) {
	s, db, c := activityFixture(t)
	m, _, err := s.Send(t.Context(), "w", "u1", c.ID, SendInput{ClientID: "activity:123", Content: "I am not Crewship"})
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceKind != "" {
		t.Fatal("human send acquired trusted source")
	}
	if _, err = db.Exec(`DELETE FROM users WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Messages(t.Context(), "w", "u0", c.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].AuthorUserID != "" || messages[0].SourceKind != "" {
		t.Fatalf("deleted author became system: %#v", messages)
	}
}

func TestActivityLabelsUseRealStatesAndEscapeBoundedTitles(t *testing.T) {
	s, db, c := activityFixture(t)
	if _, err := db.Exec(`UPDATE missions SET title=? WHERE id='issue'`, "[click](javascript:bad) <script> @Ava "+strings.Repeat("ř", 250)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivity(t.Context(), "w", "u0", c.ID, ActivitySettings{Issues: true}); err != nil {
		t.Fatal(err)
	}
	emitActivity(t, db, 1, "mission.status_change", "issue", `{"to":"REVIEW"}`)
	emitActivity(t, db, 2, "mission.status_change", "issue", `{"to":"DUPLICATE"}`)
	if err := s.ProjectActivity(t.Context()); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Messages(t.Context(), "w", "u0", c.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || !strings.Contains(messages[0].Content, "REVIEW") || !strings.Contains(messages[1].Content, "DUPLICATE") {
		t.Fatalf("real statuses absent: %#v", messages)
	}
	for _, m := range messages {
		if strings.Contains(m.Content, "[click]") || strings.Contains(m.Content, "<script>") || len([]rune(m.Content)) > 320 || len(m.MentionedAgentIDs) != 0 {
			t.Fatalf("unsafe title %q", m.Content)
		}
	}
}
