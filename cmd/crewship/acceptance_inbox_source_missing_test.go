package main

// Acceptance for `source_missing` on `crewship inbox get`, driven through the
// BUILT BINARY against a REAL api.Router over a REAL migrated database.
//
// GET /api/v1/inbox/{id} reports, for the two source-managed kinds
// (waitpoint, escalation), whether a row still exists anywhere that can
// decide the item. That single field is what tells an agent whether
// `inbox resolve` will succeed (the source is gone, the orphan may be
// dismissed here) or answer 409 (the source is live, decide it there). The
// CLI decoded the detail into a fixed struct without the field, so
// `--format json` never showed it and the human view could not say which
// case the operator was looking at. Nothing here is faked: the rows are
// written by internal/inbox and internal/harbormaster, the probe is the
// server's own, and the CLI is a subprocess.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const inboxSourceMissingWorkspaceID = "cibxsrc0000000000001"

// startInboxSourceMissingServer seeds three waitpoint inbox rows: one whose
// source is gone (no backing row anywhere), one still backed by a pending
// approvals_queue row whose payload target_id is the item's source_id —
// the third arm of the server's waitpointHasBackingRow probe — and one
// orphan dismissed in place (`inbox resolve <id>` with no action), the
// shape that made the CLI's resolve guidance fire on a decided row.
func startInboxSourceMissingServer(t *testing.T) (cfgPath, orphanID, liveID, resolvedID string) {
	t.Helper()
	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	const ws = inboxSourceMissingWorkspaceID
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'InboxSrc', 'inbox-src-ws')`, ws)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('ibs-owner', 'owner@ibs-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('ibsm-owner', ?, 'ibs-owner', 'OWNER')`, ws)

	ctx := context.Background()
	write := func(sourceID, title string) string {
		t.Helper()
		if err := inbox.Insert(ctx, db, logger, inbox.Item{
			WorkspaceID: ws, Kind: inbox.KindWaitpoint, SourceID: sourceID, TargetRole: "OWNER",
			Title: title, SenderType: "pipeline", SenderName: "nightly", Priority: "high", Blocking: true,
		}); err != nil {
			t.Fatalf("insert inbox row %s: %v", sourceID, err)
		}
		var id string
		if err := db.QueryRow(`SELECT id FROM inbox_items WHERE kind = ? AND source_id = ?`, inbox.KindWaitpoint, sourceID).Scan(&id); err != nil {
			t.Fatalf("inbox row id for %s: %v", sourceID, err)
		}
		return id
	}
	orphanID = write("wp-orphan-token", "Gate whose run was pruned")
	liveID = write("wp-live-token", "Gate still waiting for a decision")
	resolvedID = write("wp-resolved-token", "Gate dismissed from the feed")
	mustExec(`UPDATE inbox_items SET state = 'resolved', resolved_at = datetime('now'),
		resolved_by_user_id = 'ibs-owner', updated_at = datetime('now') WHERE id = ?`, resolvedID)
	if _, err := harbormaster.Enqueue(ctx, db, nil, harbormaster.Request{
		WorkspaceID: ws, RequestedBy: "ibs-owner", Kind: harbormaster.KindAutonomyGate,
		Reason:  "acceptance: backing row for the live waitpoint",
		Payload: map[string]any{"target_id": "wp-live-token"},
	}); err != nil {
		t.Fatalf("enqueue backing approval: %v", err)
	}

	const ownerToken = "crewship_cli_ibsowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-ibs-owner', 'ibs-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath = filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + ws + "\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath, orphanID, liveID, resolvedID
}

func runInboxSourceMissingCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAcceptance_InboxGet_SourceMissingIsPassedThrough pins that the field
// reaches the caller in both arms: `true` for the orphan, `false` (present,
// not omitted) for the backed row.
func TestAcceptance_InboxGet_SourceMissingIsPassedThrough(t *testing.T) {
	cfgPath, orphanID, liveID, resolvedID := startInboxSourceMissingServer(t)

	for _, tc := range []struct {
		id   string
		want bool
	}{{orphanID, true}, {liveID, false}} {
		out, err := runInboxSourceMissingCLI(t, cfgPath, "inbox", "get", tc.id, "--format", "json")
		if err != nil {
			t.Fatalf("inbox get %s: %v\n%s", tc.id, err, out)
		}
		var item struct {
			SourceMissing *bool `json:"source_missing"`
		}
		if err := json.Unmarshal([]byte(out), &item); err != nil {
			t.Fatalf("decode: %v\n%s", err, out)
		}
		if item.SourceMissing == nil {
			t.Fatalf("inbox get %s --format json omits source_missing:\n%s", tc.id, out)
		}
		if *item.SourceMissing != tc.want {
			t.Errorf("inbox get %s: source_missing = %v, want %v", tc.id, *item.SourceMissing, tc.want)
		}
	}

	// The human view says which case this is, in words an operator can act
	// on, rather than leaving the row looking like any other waitpoint.
	human, err := runInboxSourceMissingCLI(t, cfgPath, "inbox", "get", orphanID)
	if err != nil {
		t.Fatalf("inbox get (human): %v\n%s", err, human)
	}
	if !strings.Contains(human, "source gone") {
		t.Errorf("human view of an orphan does not say the source is gone:\n%s", human)
	}
	human, err = runInboxSourceMissingCLI(t, cfgPath, "inbox", "get", liveID)
	if err != nil {
		t.Fatalf("inbox get (human): %v\n%s", err, human)
	}
	if !strings.Contains(human, "source live") {
		t.Errorf("human view of a backed row does not say the source is live:\n%s", human)
	}

	// The dismissed orphan is the shape `inbox resolve <id>` leaves behind:
	// state resolved, resolved_action empty, source still gone. The detail
	// hint answered "nothing left to decide" on a row that was decided —
	// guidance belongs to "read" and "unread" rows only.
	human, err = runInboxSourceMissingCLI(t, cfgPath, "inbox", "get", resolvedID)
	if err != nil {
		t.Fatalf("inbox get (human, resolved): %v\n%s", err, human)
	}
	if strings.Contains(human, "source gone") || strings.Contains(human, "source live") {
		t.Errorf("human view of a resolved item prints resolve guidance:\n%s", human)
	}
	if !strings.Contains(human, "resolved") {
		t.Errorf("human view of a resolved item does not say it is resolved:\n%s", human)
	}
}

// TestAcceptance_InboxResolve_LiveWaitpointIsRefusedWithTheRemedy pins the
// behaviour the field predicts: resolving the backed row is refused and the
// CLI prints the source command with the real token; the orphan resolves.
func TestAcceptance_InboxResolve_LiveWaitpointIsRefusedWithTheRemedy(t *testing.T) {
	cfgPath, orphanID, liveID, _ := startInboxSourceMissingServer(t)

	out, err := runInboxSourceMissingCLI(t, cfgPath, "inbox", "resolve", liveID, "--action", "approved")
	if err == nil {
		t.Fatalf("resolving a live waitpoint must fail, got:\n%s", out)
	}
	for _, want := range []string{"409", "routine waitpoints approve wp-live-token", "routine waitpoints reject wp-live-token"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal output missing %q:\n%s", want, out)
		}
	}

	out, err = runInboxSourceMissingCLI(t, cfgPath, "inbox", "resolve", orphanID, "--action", "dismissed")
	if err != nil {
		t.Fatalf("resolving an orphan must succeed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "resolved") {
		t.Errorf("orphan resolve did not confirm:\n%s", out)
	}
}
