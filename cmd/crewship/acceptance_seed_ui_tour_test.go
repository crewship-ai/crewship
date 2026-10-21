package main

// Acceptance for `crewship seed --ui-tour`, driven through the BUILT BINARY
// against the REAL api router over a REAL migrated database. The fixture is
// what `crew reset` leaves behind for the tour to build on: the Acme Company
// workspace with the default demo crews and agents. Everything the tour adds
// goes through HTTP; the database is only read here, to count what landed.
//
// The router is wired the way cmd_start.go wires it for routines (schedule,
// run and waitpoint stores, the token-zero code runner); the agent runner is
// the refusing stub, so a tour step that reached for a model would fail the
// run instead of silently passing.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const uiTourAcceptanceWorkspaceID = "cuitourws0000000000001"

func startUITourAcceptanceServer(t *testing.T) (serverURL, cfgPath string, db *sql.DB) {
	t.Helper()
	db = testutil.MigratedDB(t).DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	const ws = uiTourAcceptanceWorkspaceID
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Acme Company', 'acme-company')`, ws)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('uit-owner', 'demo@crewship.ai', 'Demo User')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('uitm-owner', ?, 'uit-owner', 'OWNER')`, ws)
	for _, c := range []struct{ id, slug, name string }{
		{"uit-crew-sales", "engineering", "Sales & Shipping"},
		{"uit-crew-finance", "quality", "Finance & Marketing"},
		{"uit-crew-ops", "ops", "Operations"},
	} {
		mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
			VALUES (?, ?, ?, ?, 'free', 4096, 2.0)`, c.id, ws, c.name, c.slug)
	}
	for _, a := range seeddata.ActiveAgents() {
		crew := map[string]string{"engineering": "uit-crew-sales", "quality": "uit-crew-finance", "ops": "uit-crew-ops"}[a.CrewSlug]
		if crew == "" {
			continue
		}
		mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role) VALUES (?, ?, ?, ?, ?, ?)`,
			"uit-agent-"+a.Slug, crew, ws, a.Name, a.Slug, a.AgentRole)
	}
	const ownerToken = "crewship_cli_uitourowner000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-uit-owner', 'uit-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger, api.WithAllowSignup(false))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.PipelinesHandler.SetSaveTokenSecret([]byte("acceptance-test-save-token-secret-32b"))
	router.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	router.PipelinesHandler.SetRunner(unusedAgentRunner{})
	router.PipelinesHandler.SetCodeRunner(pipeline.NewMultiCodeRunner())
	router.PipelinesHandler.SetRunStore(pipeline.NewRunStore(db))
	router.PipelinesHandler.SetRunRegistry(pipeline.NewRunRegistry())
	waitpoints := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(waitpoints.Close)
	router.PipelinesHandler.SetWaitpointStore(waitpoints)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath = filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + ws + "\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return srv.URL, cfgPath, db
}

func runUITourSeedCLI(t *testing.T, serverURL, cfgPath string, extra ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	args := append([]string{"seed", "--server", serverURL, "--ui-tour"}, extra...)
	cmd := exec.CommandContext(ctx, buildCrewshipBinary(t), args...)
	cmd.Dir = t.TempDir() // not a git checkout: no .env.local
	// No provider credentials anywhere in the environment: the tour must not
	// need them.
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=",
		"SEED_ANTHROPIC_API_KEY=", "SEED_CODEX_AUTH_FILE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// uiTourCounts is everything the tour writes, counted from the database the
// router persists to.
type uiTourCounts struct {
	routines, schedules, upcoming, runs, completed, waiting, failed int
	taggedRoutines, folders, issues, issueStates, projects          int
	comments, inbox, inboxKinds, pendingApprovals                   int
	routineFolders, filedRoutines                                   int
}

func readUITourCounts(t *testing.T, db *sql.DB) uiTourCounts {
	t.Helper()
	const ws = uiTourAcceptanceWorkspaceID
	q := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
		return n
	}
	return uiTourCounts{
		routines:  q(`SELECT COUNT(*) FROM pipelines WHERE workspace_id=? AND deleted_at IS NULL`, ws),
		schedules: q(`SELECT COUNT(*) FROM pipeline_schedules WHERE workspace_id=? AND deleted_at IS NULL AND enabled=1`, ws),
		upcoming: q(`SELECT COUNT(*) FROM pipeline_schedules WHERE workspace_id=? AND deleted_at IS NULL AND enabled=1
			AND next_run_at IS NOT NULL AND next_run_at > datetime('now')`, ws),
		runs:             q(`SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id=?`, ws),
		completed:        q(`SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id=? AND status='completed'`, ws),
		waiting:          q(`SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id=? AND status='waiting'`, ws),
		failed:           q(`SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id=? AND status='failed' AND COALESCE(error_message,'')<>''`, ws),
		taggedRoutines:   q(`SELECT COUNT(DISTINCT pipeline_id) FROM pipeline_tags WHERE workspace_id=?`, ws),
		folders:          q(`SELECT COUNT(DISTINCT tag) FROM pipeline_tags WHERE workspace_id=?`, ws),
		issues:           q(`SELECT COUNT(*) FROM missions WHERE workspace_id=?`, ws),
		issueStates:      q(`SELECT COUNT(DISTINCT status) FROM missions WHERE workspace_id=?`, ws),
		projects:         q(`SELECT COUNT(DISTINCT project_id) FROM missions WHERE workspace_id=? AND project_id IS NOT NULL`, ws),
		comments:         q(`SELECT COUNT(*) FROM mission_comments c JOIN missions m ON m.id=c.mission_id WHERE m.workspace_id=?`, ws),
		inbox:            q(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id=?`, ws),
		inboxKinds:       q(`SELECT COUNT(DISTINCT kind) FROM inbox_items WHERE workspace_id=?`, ws),
		pendingApprovals: q(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE workspace_id=? AND status='pending'`, ws),
		routineFolders:   q(`SELECT COUNT(*) FROM routine_folders WHERE workspace_id=?`, ws),
		filedRoutines:    q(`SELECT COUNT(*) FROM routine_folder_members WHERE workspace_id=?`, ws),
	}
}

func TestAcceptance_SeedUITour_PopulatesTheTourAndIsIdempotent(t *testing.T) {
	serverURL, cfgPath, db := startUITourAcceptanceServer(t)

	out, err := runUITourSeedCLI(t, serverURL, cfgPath)
	if err != nil {
		t.Fatalf("seed --ui-tour failed: %v\n%s", err, out)
	}
	first := readUITourCounts(t, db)
	t.Logf("after the first pass: %+v", first)

	for _, c := range []struct {
		name     string
		got, min int
	}{
		{"routines", first.routines, 8},
		{"enabled schedules", first.schedules, 6},
		{"schedules with an upcoming fire", first.upcoming, 6},
		{"completed runs", first.completed, 10},
		{"runs waiting for a person", first.waiting, 1},
		{"failed runs with an error", first.failed, 1},
		{"pending approvals", first.pendingApprovals, 1},
		{"routines in a folder", first.taggedRoutines, 8},
		{"routine folder tags", first.folders, 3},
		{"routine folders", first.routineFolders, 4},
		{"routines filed in a folder", first.filedRoutines, 10},
		{"issues", first.issues, 15},
		{"issue states", first.issueStates, 5},
		{"projects with issues", first.projects, 4},
		{"issue comments", first.comments, 3},
		{"inbox items", first.inbox, 5},
		{"inbox kinds", first.inboxKinds, 2},
	} {
		if c.got < c.min {
			t.Errorf("%s = %d, want at least %d", c.name, c.got, c.min)
		}
	}
	if first.issues > 25 {
		t.Errorf("issues = %d, want a tour-sized list (at most 25)", first.issues)
	}
	// The tour is a showroom, not a fixture dump: nothing it names may read
	// like a test artefact.
	rows, err := db.Query(`SELECT name FROM pipelines WHERE workspace_id=? UNION ALL SELECT title FROM missions WHERE workspace_id=?
		UNION ALL SELECT name FROM pipeline_schedules WHERE workspace_id=?`, uiTourAcceptanceWorkspaceID, uiTourAcceptanceWorkspaceID, uiTourAcceptanceWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(name)
		for _, bad := range []string{"e2e", "smoke", "test", "fixture", "h2-", "tour"} {
			if strings.Contains(lower, bad) {
				t.Errorf("tour name %q reads like a fixture (%q)", name, bad)
			}
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	// Each tour routine sits in the folder the catalogue names.
	folderNames := map[string]string{}
	for _, f := range seeddata.TourFolders {
		folderNames[f.Tag] = f.Name
	}
	for _, r := range seeddata.TourRoutines {
		var folder string
		err := db.QueryRow(`SELECT f.name FROM routine_folder_members m
			JOIN routine_folders f ON f.id = m.folder_id
			JOIN pipelines p ON p.id = m.pipeline_id
			WHERE p.workspace_id = ? AND p.slug = ?`, uiTourAcceptanceWorkspaceID, r.Slug).Scan(&folder)
		if err != nil {
			t.Errorf("routine %s is not in a folder: %v", r.Slug, err)
			continue
		}
		if want := folderNames[r.Folder]; folder != want {
			t.Errorf("routine %s is in folder %q, want %q", r.Slug, folder, want)
		}
	}

	// A person's later move survives a re-run: the tour files only routines
	// that are in no folder.
	if _, err := db.Exec(`DELETE FROM routine_folder_members WHERE pipeline_id =
		(SELECT id FROM pipelines WHERE workspace_id = ? AND slug = 'weekly-sales-summary')`, uiTourAcceptanceWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO routine_folder_members (pipeline_id, folder_id, workspace_id)
		SELECT p.id, f.id, p.workspace_id FROM pipelines p, routine_folders f
		WHERE p.workspace_id = ? AND p.slug = 'weekly-sales-summary' AND f.workspace_id = p.workspace_id AND f.name = 'Finance'`, uiTourAcceptanceWorkspaceID); err != nil {
		t.Fatal(err)
	}

	again, err := runUITourSeedCLI(t, serverURL, cfgPath)
	if err != nil {
		t.Fatalf("second seed --ui-tour failed: %v\n%s", err, again)
	}
	if second := readUITourCounts(t, db); second != first {
		t.Fatalf("second run changed the workspace:\nfirst  %+v\nsecond %+v\n%s", first, second, again)
	}
	var moved string
	if err := db.QueryRow(`SELECT f.name FROM routine_folder_members m JOIN routine_folders f ON f.id = m.folder_id
		JOIN pipelines p ON p.id = m.pipeline_id WHERE p.workspace_id = ? AND p.slug = 'weekly-sales-summary'`, uiTourAcceptanceWorkspaceID).Scan(&moved); err != nil || moved != "Finance" {
		t.Fatalf("re-run undid a person's folder move: folder = %q, err = %v", moved, err)
	}
}

func TestAcceptance_SeedUITour_RefusesNuke(t *testing.T) {
	serverURL, cfgPath, db := startUITourAcceptanceServer(t)
	out, err := runUITourSeedCLI(t, serverURL, cfgPath, "--nuke", "--yes")
	if err == nil {
		t.Fatalf("seed --ui-tour --nuke must be refused, got:\n%s", out)
	}
	if !strings.Contains(out, "--ui-tour") || !strings.Contains(out, "--nuke") {
		t.Fatalf("refusal should name both flags, got:\n%s", out)
	}
	if c := readUITourCounts(t, db); c.routines != 0 || c.issues != 0 {
		t.Fatalf("a refused seed wrote data: %+v", c)
	}
}
