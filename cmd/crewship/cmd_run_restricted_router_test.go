package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type cliRouterSession struct{ done chan struct{} }

func (s cliRouterSession) Output(context.Context) (string, error) {
	return "{\"type\":\"text\",\"text\":\"isolated answer\"}\n{\"type\":\"done\"}\n", nil
}
func (s cliRouterSession) Done() <-chan struct{} { return s.done }
func (s cliRouterSession) Stop(string)           {}

func TestRunCmdRestrictedNewContextAgainstActualRouter(t *testing.T) {
	for _, operation := range []string{"run", "chat"} {
		t.Run(operation, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", strings.Repeat("31", 32))
			t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
			db := testutil.MigratedSQLDB(t)
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO users(id,email) VALUES('cli-owner','owner@cli.test'),('cli-human','human@cli.test')`)
			exec(`INSERT INTO workspaces(id,name,slug) VALUES('cli-w','CLI','cli-w')`)
			exec(`INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('cli-mo','cli-owner','cli-w','OWNER'),('cli-mh','cli-human','cli-w','MEMBER')`)
			exec(`INSERT INTO agents(id,workspace_id,name,slug,agent_role,llm_provider,llm_model,restricted_execution_profile) VALUES(?,'cli-w','CLI Agent','cli-agent','AGENT','OPENAI','fixture-model','responses_text')`, covAgentID)
			cipher, err := encryption.Encrypt("synthetic-cli-key")
			if err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('cli-key','cli-w','Key',?,'API_KEY','OPENAI','cli-owner')`, cipher)
			exec(`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('cli-key-grant',?,'cli-key','OPENAI_API_KEY')`, covAgentID)
			store := access.Store{DB: db}
			m, err := store.Membership(t.Context(), "cli-human", "cli-w")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.Replace(t.Context(), "cli-owner", "cli-human", "cli-w", "restricted", m, []access.Right{{Kind: "agent", ID: covAgentID, Operation: operation}, {Kind: "agent", ID: covAgentID, Operation: "discover"}}); err != nil {
				t.Fatal(err)
			}
			const secret = "synthetic-cli-router-secret-long-enough-for-tests"
			validator, err := auth.NewJWTValidator(secret)
			if err != nil {
				t.Fatal(err)
			}
			session, err := sessions.NewDBStore(db).Create(t.Context(), "cli-human", "test", "127.0.0.1", auth.RefreshTokenTTL)
			if err != nil {
				t.Fatal(err)
			}
			token, err := validator.IssueAccessToken("cli-human", session.ID, "Human", "human@cli.test")
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			runner := &restricteddispatch.TextRunner{Authority: restricteddispatch.Authority{Store: store}, MaxOutputTokens: 128}
			runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
				a, err := store.Resolve(ctx, handle)
				if err != nil {
					return nil, err
				}
				if a.AdmissionOperation != "run" {
					t.Fatal("CLI selected chat admission")
				}
				starts++
				done := make(chan struct{})
				close(done)
				return cliRouterSession{done}, nil
			}
			router, err := api.NewRouter(db, secret, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithInternalToken("synthetic-cli-host-token"), api.WithRestrictedTextRunner(runner))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(router)
			defer server.Close()
			saveCLIState(t)
			t.Cleanup(ResetAIFirstLatches)
			t.Setenv("CREWSHIP_SERVER", "")
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagServer = ""
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: token, Workspace: "cli-w", Server: server.URL}
			setFlagCov(t, runCmd, "chat", "")
			setFlagCov(t, runCmd, "no-stream", "true")
			setFormatCov(t, "json")
			output, err := captureStdoutCov(t, func() error { return runCmd.RunE(runCmd, []string{"cli-agent", "new private run"}) })
			var count int
			if scanErr := db.QueryRowContext(t.Context(), `SELECT count(*) FROM chats`).Scan(&count); scanErr != nil {
				t.Fatal(scanErr)
			}
			if operation == "chat" {
				if err == nil || starts != 0 || count != 0 {
					t.Fatalf("chat-only ran err=%v starts=%d contexts=%d", err, starts, count)
				}
				return
			}
			if err != nil || starts != 1 || count != 1 || !strings.Contains(output, "isolated answer") {
				t.Fatalf("run-only err=%v starts=%d contexts=%d output=%s", err, starts, count, output)
			}
			var principal, visibility, origin string
			if err = db.QueryRowContext(t.Context(), `SELECT created_by,visibility,origin FROM chats`).Scan(&principal, &visibility, &origin); err != nil || principal != "cli-human" || visibility != "private" || origin != "CLI" {
				t.Fatalf("context identity %s %s %s %v", principal, visibility, origin, err)
			}
		})
	}
}
