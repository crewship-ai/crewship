package main

// #1797 acceptance — `crewship crew update --issue-prefix` drives the REAL api
// router, not a stubbed response, because the contract that matters is the one
// an agent actually invokes: the CLI binary against a live server.
//
// issue_prefix is what turns a crew into "ENG-42", and until now it had no CLI
// flag at all (`grep issue_prefix cmd/crewship/` came back empty) even though
// PATCH /api/v1/crews/{id} has accepted the field since v38. CONTRIBUTING: every
// API endpoint gets a CLI command, and its acceptance test drives the CLI.
//
// The second test is the payoff of the fix itself, end to end through the two
// commands a user would actually run: two crews in one workspace, both given the
// prefix ENG, both filing issues. That used to be a 500 the second crew never
// recovered from — the counter upsert shared the mission insert's transaction,
// so the rejected insert rolled the increment back and every retry asked for the
// same identifier again. They now share the (workspace, ENG) sequence and
// interleave.

import (
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/spf13/cobra"
)

const (
	prefixWorkspaceID = "cprefixws0000000001a"
	// >= 21 chars and CUID-shaped, or resolveCrewID falls through to a slug
	// scan and reports "crew not found".
	prefixCrewAID = "cprefixcrewa000000001"
	prefixCrewBID = "cprefixcrewb000000001"
)

// setupIssuePrefixServer builds a real router over a migrated SQLite DB with one
// workspace, one OWNER holding a CLI token, and two crews — each with the LEAD
// agent an issue create requires.
func setupIssuePrefixServer(t *testing.T) *sql.DB {
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

	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Prefix', 'prefix-ws')`, prefixWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('pf-owner', 'owner@ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('pfm-owner', ?, 'pf-owner', 'OWNER')`,
		prefixWorkspaceID)

	ownerToken := "crewship_cli_pfowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-pf-owner', 'pf-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	for _, c := range []struct{ id, name, slug string }{
		{prefixCrewAID, "Engineering", "engineering"},
		{prefixCrewBID, "Engine", "engine"},
	} {
		mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode) VALUES (?, ?, ?, ?, 'free')`,
			c.id, prefixWorkspaceID, c.name, c.slug)
		mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status)
		          VALUES (?, ?, ?, 'Lead', ?, 'LEAD', 'IDLE')`,
			c.id+"-lead", c.id, prefixWorkspaceID, c.slug+"-lead")
	}

	r, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	cliCfg = &cli.CLIConfig{
		Token:     ownerToken,
		Workspace: prefixWorkspaceID,
		Server:    srv.URL,
	}
	return db
}

func prefixDeclareUpdateFlags(c *cobra.Command) {
	c.Flags().String("name", "", "")
	c.Flags().String("description", "", "")
	c.Flags().String("color", "", "")
	c.Flags().String("icon", "", "")
	c.Flags().Int("memory-mb", 0, "")
	c.Flags().Float64("cpus", 0, "")
	c.Flags().Int("ttl", -1, "")
	c.Flags().String("network-mode", "", "")
	c.Flags().String("allowed-domains", "", "")
	c.Flags().Bool("allow-package-registries", false, "")
	c.Flags().Bool("allow-private-endpoints", false, "")
	c.Flags().String("issue-prefix", "", "")
}

func prefixDeclareIssueCreateFlags(c *cobra.Command) {
	c.Flags().String("crew", "", "")
	c.Flags().String("title", "", "")
	c.Flags().String("description", "", "")
	c.Flags().String("priority", "none", "")
	c.Flags().String("assignee", "", "")
	c.Flags().String("assignee-type", "agent", "")
	c.Flags().String("labels", "", "")
	c.Flags().String("due-date", "", "")
	c.Flags().String("project-id", "", "")
	c.Flags().String("milestone-id", "", "")
	c.Flags().String("parent-issue-id", "", "")
	c.Flags().Int("estimate", 0, "")
	c.Flags().Float64("sort-order", 0, "")
	c.Flags().String("routine-id", "", "")
}

// prefixSetIssuePrefix runs `crewship crew update <crew> --issue-prefix <v>`.
func prefixSetIssuePrefix(t *testing.T, crewID, value string) error {
	t.Helper()
	c := covFreshCmd(crewUpdateCmd, prefixDeclareUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"issue-prefix": value})
	return c.RunE(c, []string{crewID})
}

// prefixCreateIssue runs `crewship issue create --crew <slug> --title <t>` and
// returns the command's terminal transcript, which is where the identifier the
// user is told about appears.
func prefixCreateIssue(t *testing.T, crewSlug, title string) (string, error) {
	t.Helper()
	c := covFreshCmd(issueCreateCmd, prefixDeclareIssueCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"crew": crewSlug, "title": title})
	return covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
}

// TestCrewUpdateCLI_SetsAndClearsIssuePrefix is the flag's own contract: it
// reaches crews.issue_prefix, and the empty string clears it rather than storing
// "" — a cleared prefix is what makes identifiers fall back to the slug.
func TestCrewUpdateCLI_SetsAndClearsIssuePrefix(t *testing.T) {
	saveCLIState(t)
	db := setupIssuePrefixServer(t)

	if err := prefixSetIssuePrefix(t, prefixCrewAID, "ENG"); err != nil {
		t.Fatalf("`crew update --issue-prefix ENG`: %v", err)
	}
	var stored sql.NullString
	if err := db.QueryRow(`SELECT issue_prefix FROM crews WHERE id = ?`, prefixCrewAID).Scan(&stored); err != nil {
		t.Fatalf("read issue_prefix: %v", err)
	}
	if !stored.Valid || stored.String != "ENG" {
		t.Fatalf("issue_prefix = %#v, want ENG — the flag never reached the crew", stored)
	}

	// A newly created issue must use it.
	out, err := prefixCreateIssue(t, "engineering", "first")
	if err != nil {
		t.Fatalf("`issue create`: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "ENG-1") {
		t.Errorf("issue create said %q, want an ENG-1 identifier", strings.TrimSpace(out))
	}

	if err := prefixSetIssuePrefix(t, prefixCrewAID, ""); err != nil {
		t.Fatalf("`crew update --issue-prefix \"\"`: %v", err)
	}
	if err := db.QueryRow(`SELECT issue_prefix FROM crews WHERE id = ?`, prefixCrewAID).Scan(&stored); err != nil {
		t.Fatalf("read cleared issue_prefix: %v", err)
	}
	if stored.Valid {
		t.Errorf("issue_prefix = %q after clearing, want NULL — an empty string is not a prefix, "+
			"and the identifier generator's fallback only fires on NULL or \"\"", stored.String)
	}
}

// TestCrewUpdateCLI_TwoCrewsMayShareAPrefix is #1797 end to end. Both crews get
// ENG and both file issues; the second crew used to get a 500 it could never
// recover from, and its identifier is now simply the next one in the shared
// sequence.
func TestCrewUpdateCLI_TwoCrewsMayShareAPrefix(t *testing.T) {
	saveCLIState(t)
	db := setupIssuePrefixServer(t)

	for _, crewID := range []string{prefixCrewAID, prefixCrewBID} {
		if err := prefixSetIssuePrefix(t, crewID, "ENG"); err != nil {
			t.Fatalf("`crew update --issue-prefix ENG` on %s: %v", crewID, err)
		}
	}

	outA, err := prefixCreateIssue(t, "engineering", "from engineering")
	if err != nil {
		t.Fatalf("first crew's issue create: %v (output: %s)", err, outA)
	}
	outB, err := prefixCreateIssue(t, "engine", "from engine")
	if err != nil {
		t.Fatalf("second crew's issue create failed: %v (output: %s)\n"+
			"both crews carry the prefix ENG; the counter must be keyed on the workspace and the "+
			"prefix, or they each mint ENG-1 and the loser is rejected by "+
			"idx_mission_workspace_identifier (#1797)", err, outB)
	}
	// And again, because the failure was permanent: the rejected insert used to
	// roll back the counter increment it shared a transaction with.
	outB2, err := prefixCreateIssue(t, "engine", "from engine, again")
	if err != nil {
		t.Fatalf("second crew's SECOND issue create failed: %v (output: %s)\n"+
			"this is the wedge — the crew could never file an issue again", err, outB2)
	}

	if !strings.Contains(outA, "ENG-1") {
		t.Errorf("first issue: %q, want ENG-1", strings.TrimSpace(outA))
	}
	if !strings.Contains(outB, "ENG-2") {
		t.Errorf("second crew's first issue: %q, want ENG-2 — the two crews share one ENG sequence "+
			"and interleave", strings.TrimSpace(outB))
	}
	if !strings.Contains(outB2, "ENG-3") {
		t.Errorf("second crew's second issue: %q, want ENG-3", strings.TrimSpace(outB2))
	}

	var identifiers int
	if err := db.QueryRow(
		`SELECT COUNT(DISTINCT identifier) FROM missions WHERE workspace_id = ? AND identifier IS NOT NULL`,
		prefixWorkspaceID).Scan(&identifiers); err != nil {
		t.Fatalf("count identifiers: %v", err)
	}
	if identifiers != 3 {
		t.Errorf("%d distinct identifiers in the workspace, want 3", identifiers)
	}
}

// TestCrewUpdateCLI_RefusesUnaddressableIssuePrefix is #2035 through the CLI
// door that #1797 opened. The prefix becomes the leading half of the issue
// identifier, and the identifier is a single URL path segment on every route
// that addresses an issue — so `--issue-prefix "A/B"` used to file A/B-1, an
// issue that exists, lists, and can never be opened, patched, closed or
// commented on again.
//
// The guard is on the API (crews_update.go), not in the flag handler, so the
// web UI is behind the same rule. This test drives the CLI against the real
// router to prove the rule reaches the operator holding the flag, message and
// all.
func TestCrewUpdateCLI_RefusesUnaddressableIssuePrefix(t *testing.T) {
	saveCLIState(t)
	db := setupIssuePrefixServer(t)

	for _, bad := range []string{"A/B", "A B", "A%B", "ENGINEERING-PLATFORM-TEAM"} {
		err := prefixSetIssuePrefix(t, prefixCrewAID, bad)
		if err == nil {
			t.Fatalf("`crew update --issue-prefix %q` succeeded, want a rejection — "+
				"that prefix mints an identifier no route can address", bad)
		}
		if !strings.Contains(err.Error(), "issue_prefix") ||
			!strings.Contains(err.Error(), "A-Za-z0-9_-") {
			t.Errorf("rejection of %q reads %q; it must name the field and state the rule", bad, err)
		}

		var stored sql.NullString
		if qerr := db.QueryRow(`SELECT issue_prefix FROM crews WHERE id = ?`,
			prefixCrewAID).Scan(&stored); qerr != nil {
			t.Fatalf("read issue_prefix: %v", qerr)
		}
		if stored.Valid {
			t.Fatalf("issue_prefix = %q after a rejected write, want it untouched", stored.String)
		}
	}

	// The rule refuses only what it must: a normal prefix still lands.
	if err := prefixSetIssuePrefix(t, prefixCrewAID, "ENG-2"); err != nil {
		t.Fatalf("`crew update --issue-prefix ENG-2`: %v", err)
	}
}
