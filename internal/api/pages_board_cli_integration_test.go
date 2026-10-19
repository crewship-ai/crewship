package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Actual built CLI, real HTTP credential middleware and SQLite persistence.
// Uses the production Router with the explicit feature flag enabled. This is
// not editor, agent, multi-writer or full B1 acceptance.
func TestBoardHumanActualCLI(t *testing.T) {
	h, f, _ := boardHumanFixture(t)
	// Production CLI resolves human-readable workspace references. Use an
	// opaque ID here so a pages-scoped credential need not enumerate workspaces.
	const workspaceID = "cboardhumanactualcli000001"
	mustExec(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES(?,'Board CLI','board-cli')`, workspaceID)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) SELECT 'board-cli-'||id,?,user_id,role FROM workspace_members WHERE workspace_id=?`, workspaceID, f.wsID)
	mustExec(t, h.db, `UPDATE pages SET workspace_id=? WHERE id='board-human-page'`, workspaceID)
	f.wsID = workspaceID
	router, err := NewRouter(h.db, "board-router-qualification-secret", newTestLogger(), WithBoardEnabled(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	defer server.Close()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("actual CLI build: %v %s", err, output)
	}
	home := t.TempDir()
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), binary, append([]string{"page", "board"}, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") && !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "HTTP_PROXY=") && !strings.HasPrefix(entry, "HTTPS_PROXY=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+home, "CREWSHIP_SERVER="+server.URL, "CREWSHIP_WORKSPACE="+f.wsID, "CREWSHIP_TOKEN=crewship_cli_board_"+f.userID, "CREWSHIP_NO_SLUG_CACHE=1")
		return cmd.CombinedOutput()
	}
	if output, err := run("patch"); err == nil {
		t.Fatalf("unsupported Board operation exited successfully: %s", output)
	}
	initialized, err := run("init", "@board-human-page", "--mutation-id", "cli-first")
	var snapshot struct {
		PageID  string `json:"page_id"`
		Records []any  `json:"records"`
	}
	decodeErr := json.Unmarshal(initialized, &snapshot)
	if err != nil || decodeErr != nil || snapshot.PageID != "board-human-page" || snapshot.Records == nil || len(snapshot.Records) != 0 {
		t.Fatalf("CLI initialize: %v %s", err, initialized)
	}
	shown, err := run("show", "@board-human-page")
	if err != nil || !bytes.Equal(initialized, shown) {
		t.Fatalf("CLI read: %v %s / %s", err, initialized, shown)
	}
	retried, err := run("init", "@board-human-page", "--mutation-id", "cli-first")
	if err != nil || !bytes.Equal(initialized, retried) {
		t.Fatalf("CLI lost ACK retry: %v %s", err, retried)
	}
	// Recreate the handler: no in-memory scene or idempotency state is required.
	reloaded := NewPageHandler(h.db, nil, newTestLogger())
	if got := boardHumanCall(t, f, reloaded, f.userID, "GET", "private-board", ""); got.Code != 200 {
		t.Fatalf("reloaded persistence=%d %s", got.Code, got.Body.String())
	}
	var boards, receipts int
	if err = h.db.QueryRow(`SELECT count(*) FROM page_boards`).Scan(&boards); err != nil {
		t.Fatal(err)
	}
	if err = h.db.QueryRow(`SELECT count(*) FROM page_board_mutations`).Scan(&receipts); err != nil || boards != 1 || receipts != 1 {
		t.Fatalf("duplicate durable effect boards=%d receipts=%d err=%v", boards, receipts, err)
	}
	mustExec(t, h.db, `DELETE FROM pages WHERE id='board-human-page'`)
	f.seedPage(t, "replacement-board-page", "private-board", "Replacement", f.userID, "")
	mustExec(t, h.db, `UPDATE pages SET private_audience=1 WHERE id='replacement-board-page'`)
	if output, err := run("show", "@board-human-page"); err == nil || bytes.Contains(output, []byte("Replacement")) {
		t.Fatalf("old identity followed reused slug: %v %s", err, output)
	}
	if output, err := run("show", "private-board"); err == nil {
		t.Fatalf("uninitialized replacement became empty Board: %s", output)
	}
}
