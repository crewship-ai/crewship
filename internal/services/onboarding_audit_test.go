package services

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestOnboardingCrewMembershipAuditAtomic(t *testing.T) {
	for _, fault := range []string{"healthy", "abort", "ignore"} {
		t.Run(fault, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", strings.Repeat("27", 32))
			db, user, workspace := setupOnboardingDB(t)
			svc := newTestSvc(t, db)
			// Calibrate real native audit storage separately. The existing service
			// constructor has no configured-emitter option; this is not router wiring
			// or custom-key continuity evidence for Setup.
			key := journal.ChainKeyFromEnv()
			writer := journal.NewWriter(db.DB, svc.logger, journal.WriterOptions{ChainKey: key})
			t.Cleanup(func() { _ = writer.Close() })
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			notify, err := writer.AppendTx(t.Context(), tx, journal.Entry{WorkspaceID: workspace, ActorType: journal.ActorUser, ActorID: user, Type: journal.EntryAuditEntityCreated, Summary: "Synthetic onboarding audit calibration"})
			if err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			notify()
			chain, err := journal.VerifyChainWithKey(t.Context(), db.DB, workspace, key)
			if err != nil || !chain.OK {
				t.Fatal("audit calibration failed", err)
			}
			if fault != "healthy" {
				statement := "RAISE(ABORT,'synthetic onboarding audit failure')"
				if fault == "ignore" {
					statement = "RAISE(IGNORE)"
				}
				if _, err = db.Exec(`CREATE TRIGGER onboarding_membership_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='crew.member_changed' BEGIN SELECT ` + statement + `; END`); err != nil {
					t.Fatal(err)
				}
			}
			result, err := svc.Setup(t.Context(), SetupParams{UserID: user, WorkspaceID: workspace, CrewName: "Blank owned crew", CrewSlug: "blank-owned-crew", AgentName: "Inert agent", AgentSlug: "inert-agent", CliAdapter: "CLAUDE_CODE", LLMProvider: "OLLAMA", Now: time.Now().UTC().Format(time.RFC3339)})
			var completed, crews, members, agents, events int
			if e := db.QueryRow(`SELECT onboarding_completed FROM users WHERE id=?`, user).Scan(&completed); e != nil {
				t.Fatal(e)
			}
			for _, q := range []struct {
				sql    string
				target *int
			}{
				{`SELECT COUNT(*) FROM crews WHERE workspace_id=?`, &crews},
				{`SELECT COUNT(*) FROM crew_members cm JOIN crews c ON c.id=cm.crew_id WHERE c.workspace_id=?`, &members},
				{`SELECT COUNT(*) FROM agents WHERE workspace_id=?`, &agents},
				{`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type='crew.member_changed'`, &events},
			} {
				if e := db.QueryRow(q.sql, workspace).Scan(q.target); e != nil {
					t.Fatal(e)
				}
			}
			if fault != "healthy" {
				if err == nil {
					t.Error("onboarding ignored audit refusal")
				}
				if completed != 0 || crews != 0 || members != 0 || agents != 0 || events != 0 {
					t.Errorf("audit refusal committed onboarding effects: completed=%d crews=%d members=%d agents=%d events=%d", completed, crews, members, agents, events)
				}
				return
			}
			if err != nil || result == nil || result.CrewID == "" || result.AgentID == "" || result.CredentialID != "" || completed != 1 || crews != 1 || members != 1 || agents != 1 {
				t.Fatalf("native blank Setup calibration failed: error=%v completed=%d crews=%d members=%d agents=%d", err, completed, crews, members, agents)
			}
			if events != 1 {
				t.Fatalf("committed blank membership lacks canonical audit: events=%d", events)
			}
			var memberID, actor, crew, payload string
			if err = db.QueryRow(`SELECT id FROM crew_members WHERE crew_id=? AND user_id=? AND role IS NULL`, result.CrewID, user).Scan(&memberID); err != nil {
				t.Fatal("native NULL membership missing", err)
			}
			if err = db.QueryRow(`SELECT actor_id,crew_id,payload FROM journal_entries WHERE workspace_id=? AND entry_type='crew.member_changed'`, workspace).Scan(&actor, &crew, &payload); err != nil {
				t.Fatal(err)
			}
			var p map[string]any
			if err = json.Unmarshal([]byte(payload), &p); err != nil {
				t.Fatal(err)
			}
			previous, hasPrevious := p["previous_membership"]
			membership, ok := p["membership"].(map[string]any)
			role, hasRole := membership["role"]
			if actor != user || crew != result.CrewID || p["member_id"] != memberID || p["subject_user_id"] != user || p["action"] != "added" || !hasPrevious || previous != nil || !ok || !hasRole || role != nil {
				t.Error("canonical membership audit lost actor, identity or NULL-role semantics")
			}
			chain, err = journal.VerifyChainWithKey(t.Context(), db.DB, workspace, key)
			if err != nil || !chain.OK {
				t.Fatal("membership audit chain invalid", err)
			}
		})
	}
}
