package services

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

// A failed write at any stage must leave onboarding retryable and must not
// expose an agent, membership or credential from a partially created crew.
func TestOnboardingSetupRollsBackEveryPublicationStage(t *testing.T) {
	for _, stage := range []struct{ table, operation string }{
		{"users", "UPDATE"}, {"workspaces", "UPDATE"}, {"crews", "INSERT"},
		{"crew_members", "INSERT"}, {"agents", "INSERT"}, {"credentials", "INSERT"}, {"agent_credentials", "INSERT"},
	} {
		t.Run(stage.table, func(t *testing.T) {
			var key [32]byte
			if _, err := rand.Read(key[:]); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENCRYPTION_KEY", hex.EncodeToString(key[:]))
			db, user, workspace := setupOnboardingDB(t)
			svc := newTestSvc(t, db)
			trigger := fmt.Sprintf("CREATE TEMP TRIGGER reject_stage BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'injected storage failure'); END", stage.operation, stage.table)
			// The temporary trigger and transaction must use the same connection.
			db.DB.SetMaxOpenConns(1)
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			params := SetupParams{UserID: user, WorkspaceID: workspace, WorkspaceName: "Renamed", CrewName: "Crew", CrewSlug: "new-crew", AgentName: "Agent", AgentSlug: "new-agent", CliAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", EnvVarName: "ANTHROPIC_API_KEY", CredentialName: "Token", CredentialValue: "fixture-token", Now: time.Now().UTC().Format(time.RFC3339)}
			result, err := svc.Setup(t.Context(), params)
			if err == nil || result != nil {
				t.Fatalf("partial setup published: %+v, %v", result, err)
			}
			var completed int
			if err := db.QueryRow(`SELECT onboarding_completed FROM users WHERE id=?`, user).Scan(&completed); err != nil {
				t.Fatal(err)
			}
			if completed != 0 {
				t.Fatal("failed setup consumed the onboarding claim")
			}
			var name string
			if err := db.QueryRow(`SELECT name FROM workspaces WHERE id=?`, workspace).Scan(&name); err != nil {
				t.Fatal(err)
			}
			if name != "WS" {
				t.Fatalf("workspace rename escaped rollback: %s", name)
			}
			for _, table := range []string{"crews", "crew_members", "agents", "credentials", "agent_credentials"} {
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("partial %s rows survived: %d", table, count)
				}
			}
			if _, err := db.Exec("DROP TRIGGER reject_stage"); err != nil {
				t.Fatal(err)
			}
			if result, err := svc.Setup(t.Context(), params); err != nil || result == nil {
				t.Fatalf("retry failed: %+v, %v", result, err)
			}
		})
	}
}
