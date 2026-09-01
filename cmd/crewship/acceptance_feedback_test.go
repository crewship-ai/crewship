package main

// #1617 acceptance — `crewship feedback create|list|delete` driven through
// the BUILT BINARY against the REAL api router.
//
// #1617's second half reported POST /api/v1/feedback answering 404 against a
// real seeded daemon, with the route registered, and pointed at authedSelfMut
// (used for POST/DELETE on this path) vs the plain r.mux.Handle used for GET
// on the adjacent line as the likely cause.
//
// Driving this through the real router shows that theory is wrong: the 404
// the E2E specs saw was never a routing miss. It was
// MessageFeedbackHandler.Create's own, correctly-functioning "message not
// found" response (see internal/api/message_feedback.go's #1208 comment) —
// message_id must resolve to a real row in the conversation_messages search
// mirror, a check added in #1213 to close a cross-tenant message-existence
// oracle. The E2E specs predate that hardening and POST fabricated message
// ids, so they hit this 404 legitimately; it has nothing to do with
// authedSelfMut's registration shape.
//
// This test seeds a REAL conversation_messages row the way production code
// does it (conversation.Store.Append writes there; a direct INSERT stands in
// for that here, same as internal/api's seedConvMessage) and drives the full
// create → list → delete round trip through the CLI binary and the real
// router — proving both the route registration AND the CLI's request shape
// are correct end to end.
import (
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const feedbackAcceptanceWorkspaceID = "cfeedbackws0000000001a"

// startFeedbackAcceptanceServer builds a real router over a migrated SQLite
// DB holding one workspace, one OWNER with a CLI token, one chat, one agent,
// and one real conversation_messages row — everything
// MessageFeedbackHandler.Create needs to resolve message_id → chat →
// workspace and find the caller a member, so a POST reaches a genuine 201
// rather than the handler's own 404.
func startFeedbackAcceptanceServer(t *testing.T) (serverURL, cfgPath, messageID string) {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Feedback', 'feedback-ws')`, feedbackAcceptanceWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('fb-owner', 'owner@fb-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('fbm-owner', ?, 'fb-owner', 'OWNER')`,
		feedbackAcceptanceWorkspaceID)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES ('fb-crew', ?, 'Crew', 'fb-crew', 'free', 4096, 2.0)`,
		feedbackAcceptanceWorkspaceID)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('fb-agent', ?, 'fb-crew', 'Agent', 'fb-agent', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
		feedbackAcceptanceWorkspaceID)
	mustExec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title) VALUES ('fb-chat', 'fb-agent', ?, 'fb-owner', 'Feedback chat')`,
		feedbackAcceptanceWorkspaceID)

	messageID = "msg-fb-acceptance-1"
	mustExec(`INSERT INTO conversation_messages (id, session_id, agent_id, role, content) VALUES (?, 'fb-chat', 'fb-agent', 'user', 'hello')`,
		messageID)

	const ownerToken = "crewship_cli_fbowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-fb-owner', 'fb-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	serverURL = srv.URL

	cfgPath = filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: " + feedbackAcceptanceWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return serverURL, cfgPath, messageID
}

func runFeedbackCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAcceptance_Feedback_CreateListDelete_RoundTrip drives POST, GET, and
// DELETE /api/v1/feedback — the exact three verbs #1617 named, two of them
// (POST, DELETE) registered via authedSelfMut and one (GET) via the plain
// r.mux.Handle beside it — through the CLI binary against the real router.
// A router-registration regression on any of the three (a stray character in
// authedSelfMut's method+pattern string, a pattern that collides with a
// broader registration under Go 1.22+ ServeMux precedence, etc.) would show
// up here as the CLI reporting failure where this test expects success.
func TestAcceptance_Feedback_CreateListDelete_RoundTrip(t *testing.T) {
	_, cfgPath, messageID := startFeedbackAcceptanceServer(t)

	createOut, err := runFeedbackCLI(t, cfgPath, "feedback", "create",
		"--message", messageID, "--signal", "helpful", "--reason", "acceptance round trip")
	if err != nil {
		t.Fatalf("feedback create failed: %v\n%s", err, createOut)
	}
	if !strings.Contains(createOut, messageID) {
		t.Errorf("feedback create output doesn't echo the message id:\n%s", createOut)
	}

	listOut, err := runFeedbackCLI(t, cfgPath, "feedback", "list", "--message", messageID)
	if err != nil {
		t.Fatalf("feedback list failed: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, "helpful") {
		t.Errorf("feedback list output doesn't show the signal we just created:\n%s", listOut)
	}

	deleteOut, err := runFeedbackCLI(t, cfgPath, "feedback", "delete",
		"--message", messageID, "--signal", "helpful")
	if err != nil {
		t.Fatalf("feedback delete failed: %v\n%s", err, deleteOut)
	}

	listAfterOut, err := runFeedbackCLI(t, cfgPath, "feedback", "list", "--message", messageID)
	if err != nil {
		t.Fatalf("feedback list (after delete) failed: %v\n%s", err, listAfterOut)
	}
	if strings.Contains(listAfterOut, "helpful") {
		t.Errorf("feedback row still present after delete:\n%s", listAfterOut)
	}
}

// TestAcceptance_Feedback_Create_UnknownMessage_404ByName pins the
// distinction #1617 actually turned on: a fabricated message_id gets a real
// 404 from the handler's own validation, not from a routing miss. If
// authedSelfMut's registration ever broke, this would fail differently — the
// CLI would report a bare "not found" with no trace of the handler's
// specific wording, or the router-level test in internal/api would catch the
// generic net/http 404 body first.
func TestAcceptance_Feedback_Create_UnknownMessage_404ByName(t *testing.T) {
	_, cfgPath, _ := startFeedbackAcceptanceServer(t)

	out, err := runFeedbackCLI(t, cfgPath, "feedback", "create",
		"--message", "msg-that-was-never-persisted", "--signal", "helpful")
	if err == nil {
		t.Fatalf("expected failure for a message_id with no conversation_messages row, got success:\n%s", out)
	}
	if !strings.Contains(out, "message not found") {
		t.Errorf("want the handler's own 'message not found' 404, got:\n%s", out)
	}
}
