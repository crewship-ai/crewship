package chataudience

import (
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestPrivateArtifactContextMigrationBackfillsAndCannotBeCleared(t *testing.T) {
	db := testutil.NewMemDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.MigrateSkipping(t.Context(), db, logger, "private_artifact_conversation_context"); err != nil {
		if !strings.Contains(err.Error(), `no migration is named "private_artifact_conversation_context"`) {
			t.Fatal(err)
		}
		// RED before registration still uses a real fully migrated old schema.
		if err := database.Migrate(t.Context(), db, logger); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('marker-ws','Marker','marker')`,
		`INSERT INTO users(id,email) VALUES('marker-author','marker-author@example.test'),('marker-other','marker-other@example.test')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('marker-agent','marker-ws','Agent','marker-agent')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('marker-valid','marker-ws','marker-agent','marker-author','private'),('marker-mixed','marker-ws','marker-agent','marker-author','private'),('marker-ordinary','marker-ws','marker-agent','marker-author','private')`,
		`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,author_user_id) VALUES('marker-message','marker-valid','marker-agent','user','create','marker-author'),('marker-mixed-message','marker-mixed','marker-agent','user','create','marker-other')`,
		`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('marker-message','marker-ws','marker-author','marker-valid','marker-valid','2026-10-19T00:00:00Z'),('marker-mixed-message','marker-ws','marker-other','marker-mixed','marker-mixed','2026-10-19T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	// A pre-classification NULL actor is ambiguous, unlike later annotation loss.
	for _, q := range []string{
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('marker-null-origin','marker-ws','marker-agent','marker-author','private')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('marker-page','marker-ws','marker-page','Fixture','marker-author','{}')`,
		`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('marker-ws','marker-page',3,x'00','[]')`,
		`INSERT INTO document_creations(page_id,workspace_id,run_id,agent_id,human_user_id,source_chat_id,submission_id,mutation_id,request_digest,created_at) VALUES('marker-page','marker-ws','marker-run','marker-agent',NULL,'marker-null-origin','marker-submission','marker-mutation',printf('%064d',0),'2026-10-19T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		chat string
		want sql.NullString
	}{
		{"marker-valid", sql.NullString{String: "marker-author", Valid: true}},
		{"marker-mixed", sql.NullString{String: "", Valid: true}},
		{"marker-ordinary", sql.NullString{}},
		{"marker-null-origin", sql.NullString{String: "", Valid: true}},
	} {
		var marker sql.NullString
		if err := db.QueryRowContext(t.Context(), `SELECT private_artifact_author_user_id FROM chats WHERE id=?`, tc.chat).Scan(&marker); err != nil {
			t.Fatal(err)
		}
		if marker != tc.want {
			t.Errorf("%s marker=%v want%v", tc.chat, marker, tc.want)
		}
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM document_creation_consents`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE chats SET private_artifact_author_user_id=NULL WHERE id='marker-valid'`); err == nil {
		t.Fatal("source privacy marker could be cleared")
	}
	value, err := ResolvePrivateArtifactContext(t.Context(), db, "marker-ws", "marker-valid")
	if err != nil || !value.Sealed || value.AuthorUserID != "marker-author" {
		t.Fatalf("consent cleanup erased source classification: %+v %v", value, err)
	}
}
