package pipeline

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestActualRoutineJournalProducesSafeChannelActivity(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('activity-w','Activity','activity-w')`,
		`INSERT INTO users(id,email) VALUES('activity-owner','activity-owner@example.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('activity-member','activity-w','activity-owner','OWNER')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('activity-pipeline','activity-w','daily-check','Daily check','{}','hash')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(db, nil, journal.WriterOptions{})
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	store := groupchat.New(db)
	channel, err := store.Create(t.Context(), "activity-w", "activity-owner", groupchat.CreateInput{Title: "Routine activity", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetActivity(t.Context(), "activity-w", "activity-owner", channel.ID, groupchat.ActivitySettings{Routines: true}); err != nil {
		t.Fatal(err)
	}
	producer := pipelineEmitContext{emitter: writer, workspaceID: "activity-w", pipelineID: "activity-pipeline", pipelineSlug: "daily-check", runID: "activity-run"}
	producer.emitRunCompleted(t.Context(), 10, 0, false)
	producer.emitRunFailed(t.Context(), "test-step", "SECRET model output @Agent")
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = store.ProjectActivity(t.Context()); err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages(t.Context(), "activity-w", "activity-owner", channel.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("actual routine journal not projected: %#v", messages)
	}
	for _, m := range messages {
		if m.SourceKind != "activity" || !strings.Contains(m.Content, "slug=daily-check") || strings.Contains(m.Content, "SECRET") || strings.Contains(m.Content, "@Agent") {
			t.Fatalf("unsafe routine activity: %#v", m)
		}
	}
	if !strings.Contains(messages[0].Content, "completed") || !strings.Contains(messages[1].Content, "failed") {
		t.Fatalf("wrong lifecycle labels: %#v", messages)
	}
}
