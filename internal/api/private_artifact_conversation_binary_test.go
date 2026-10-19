package api

import (
	"context"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/conversation"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	var chat, agent string
	if err := pages.db.QueryRowContext(t.Context(), `SELECT source_chat_id,agent_id FROM document_creations WHERE page_id=?`, result.PageID).Scan(&chat, &agent); err != nil {
		t.Fatal(err)
	}
	const admin, owner = "private-binary-admin", "private-binary-owner"
	pagesSeedUser(t, pages, workspace, admin, "private-binary-admin@example.test", "ADMIN")
	pagesSeedUser(t, pages, workspace, owner, "private-binary-owner@example.test", "OWNER")
	message := conversation.Message{ID: "private-binary-message", ChatID: chat, AgentID: agent, Role: conversation.RoleAssistant, Content: result.Title + " " + result.URL, Timestamp: time.Now().UTC()}
	socket := covIPCJSON(t, map[string]any{"/chats/" + chat + "/messages": map[string]any{"messages": []conversation.Message{message}}})
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
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "chat", chat, "--format", "json")
			cmd.Dir = dir
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "CREWSHIP_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1")
			output, err := cmd.CombinedOutput()
			if user == admin {
				if err == nil || !strings.Contains(string(output), "404") || strings.Contains(string(output), result.Title) || strings.Contains(string(output), result.Slug) {
					t.Fatalf("ADMIN binary disclosed private conversation: exit=%v", err)
				}
			} else if err != nil || !strings.Contains(string(output), result.Title) {
				t.Fatalf("authorized binary transcript: %v %s", err, output)
			}
		})
	}
	var count int
	if err := pages.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_logs WHERE user_id=? AND action='artifact.private_owner_accessed' AND entity_type='chat' AND entity_id=?`, owner, chat).Scan(&count); err != nil || count < 1 {
		t.Fatalf("actual binary OWNER inspection not audited: %d %v", count, err)
	}
}
