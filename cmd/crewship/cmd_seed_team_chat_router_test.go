package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Real routes, auth setup/login, persisted avatars, roles and messages. A single
// representative keeps this contract test inside the default auth burst; the
// six-person fixture and throttled transport have independent regressions.
func TestSeedTeamChatRealRouterContract(t *testing.T) {
	catalog := seeddata.TeamChatPeople
	seeddata.TeamChatPeople = append([]seeddata.TeamChatPerson(nil), catalog[2:3]...)
	t.Cleanup(func() { seeddata.TeamChatPeople = catalog })
	t.Setenv("CREWSHIP_PUBLIC_URL", "http://127.0.0.1:8082")
	db := testutil.MigratedSQLDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('seed-owner','seed-owner@example.test','Original owner')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cseedchatabcdefghijklmn','Seed chat','seed-chat')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('seed-owner-member','cseedchatabcdefghijklmn','seed-owner','OWNER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	secret := "team-chat-router-test-secret-32bytes"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router, err := api.NewRouter(db, secret, logger, api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", api.ServeOpenAPISpec)
	mux.Handle("/", router)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), "seed-owner", "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken("seed-owner", session.ID, "Original owner", "seed-owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	client := cli.NewClient(server.URL, token, "cseedchatabcdefghijklmn")
	dir := teamSeedTempDir(t)
	result, err := seedTeamChat(t.Context(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.People) != 1 || result.People[0].Role != "MEMBER" {
		t.Fatalf("bad role result %#v", result)
	}
	var avatar string
	var humanMessages int
	if err = db.QueryRow(`SELECT avatar_url FROM users WHERE id=?`, result.People[0].UserID).Scan(&avatar); err != nil || avatar == "" {
		t.Fatalf("avatar not persisted: %q %v", avatar, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_messages WHERE author_user_id=?`, result.People[0].UserID).Scan(&humanMessages); err != nil || humanMessages != 2 {
		t.Fatalf("messages not human-authored: %d %v", humanMessages, err)
	}
	again, err := seedTeamChat(t.Context(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.ChannelID != result.ChannelID {
		t.Fatal("real rerun duplicated channel")
	}
}
