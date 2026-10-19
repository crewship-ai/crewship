package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/conversation"
)

// The actual binary reaches migrated persistence and production Router auth.
// Only Node preparation and transcript IPC are fixtures; no provider run.
func TestPrivateArtifactConversationAudienceThroughBinary(t *testing.T) {
	pages, workspace, author, _, create := documentCreationFixture(t)
	execOrFatal(t, pages.db, `UPDATE workspaces SET slug='private-conversation-cli' WHERE id=?`, workspace)
	response := create("private-binary", documentCreationBody)
	creationExpect(t, response, http.StatusCreated)
	var result documentCreationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var chat, agent, agentSlug string
	if err := pages.db.QueryRowContext(t.Context(), `SELECT d.source_chat_id,d.agent_id,a.slug FROM document_creations d JOIN agents a ON a.id=d.agent_id WHERE d.page_id=?`, result.PageID).Scan(&chat, &agent, &agentSlug); err != nil {
		t.Fatal(err)
	}
	const admin, owner = "private-binary-admin", "private-binary-owner"
	pagesSeedUser(t, pages, workspace, admin, "private-binary-admin@example.test", "ADMIN")
	pagesSeedUser(t, pages, workspace, owner, "private-binary-owner@example.test", "OWNER")
	message := conversation.Message{ID: "private-binary-message", ChatID: chat, AgentID: agent, Role: conversation.RoleAssistant, Content: result.Title + " " + result.URL, Timestamp: time.Now().UTC()}
	logs := map[string]any{"logs": []any{map[string]any{"content": result.Title + " " + result.URL}}}
	socket := covIPCJSON(t, map[string]any{
		"/chats/" + chat + "/messages":   map[string]any{"messages": []conversation.Message{message}},
		"/agents/" + agentSlug + "/logs": logs, "/agents/" + agent + "/logs": logs,
		"/debug/logs": logs, "/debug/info": map[string]string{"version": "fixture"},
		"/agents/" + agent + "/status": map[string]string{"status": "running"},
	})
	const secret = "private-binary-fixture-jwt-secret-32"
	router, err := NewRouter(pages.db, secret, newTestLogger(), WithSocketPath(socket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	host := httptest.NewServer(router)
	t.Cleanup(host.Close)
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual binary: %v %s", err, out)
	}
	for _, user := range []string{author, admin, owner} {
		t.Run(user, func(t *testing.T) {
			session, err := sessions.NewDBStore(pages.db).Create(t.Context(), user, "fixture", "127.0.0.1", auth.RefreshTokenTTL)
			if err != nil {
				t.Fatal(err)
			}
			bearer, err := validator.IssueAccessToken(user, session.ID, "Fixture", "fixture@example.test")
			if err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(t.TempDir(), "cli.yaml")
			if err := os.WriteFile(config, []byte("server: "+host.URL+"\nworkspace: private-conversation-cli\ntoken: "+bearer+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"chat", chat}, {"agent", "logs", agentSlug}, {"agent", "debug", agentSlug}} {
				var before int
				if err := pages.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_logs WHERE user_id=? AND action='artifact.private_owner_accessed' AND entity_type='chat' AND entity_id=?`, owner, chat).Scan(&before); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, append(args, "--format", "json")...)
				cmd.Dir = dir
				for _, entry := range os.Environ() {
					if !strings.HasPrefix(entry, "CREWSHIP_") {
						cmd.Env = append(cmd.Env, entry)
					}
				}
				cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1")
				output, err := cmd.CombinedOutput()
				expectedDenial := ""
				if user == admin {
					expectedDenial = "404"
				} else if user == author && args[1] == "debug" {
					expectedDenial = "403" // MEMBER author retains the existing manage-role gate.
				}
				if expectedDenial != "" {
					if err == nil || !strings.Contains(string(output), expectedDenial) || strings.Contains(string(output), result.Title) || strings.Contains(string(output), result.Slug) {
						t.Fatalf("binary %v denial: exit=%v expected=%s", args[:2], err, expectedDenial)
					}
				} else if err != nil || !strings.Contains(string(output), result.Title) {
					t.Fatalf("authorized binary %v response: %v", args[:2], err)
				}
				if user == owner {
					var after int
					if err := pages.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_logs WHERE user_id=? AND action='artifact.private_owner_accessed' AND entity_type='chat' AND entity_id=?`, owner, chat).Scan(&after); err != nil || after <= before {
						t.Fatalf("binary %v OWNER inspection missing fresh audit: %v", args[:2], err)
					}
				}
			}
		})
	}
	var count int
	if err := pages.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_logs WHERE user_id=? AND action='artifact.private_owner_accessed' AND entity_type='chat' AND entity_id=?`, owner, chat).Scan(&count); err != nil || count < 1 {
		t.Fatalf("actual binary OWNER inspection not audited: %d %v", count, err)
	}
}
