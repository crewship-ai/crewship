package main

// #5348: inside foreach, a held crewship action blocks until a person
// decides it. A synchronous `crewship routine run` (no --async) kept that
// block inside its HTTP request: the CLI hung until someone decided, and a
// client that went away cancelled the run, after which the approval answered
// "already decided or expired". A MANAGER's refusal also began with "an agent
// cannot", although the decider is a person.
//
// Driven through the BUILT BINARY against the REAL api router, executor and
// waitpoint store. Only the action dispatcher is a stand-in: it holds the
// call the way a strict crew's autonomy gate does (CrewshipApprovalRequired
// until the request carries the token of an approved waitpoint of this run),
// so the test needs no crew container or autonomy fixture.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const heldEachWorkspaceID = "cheldeachws00000000001"

const heldEachDef = `{"dsl_version":"1.0","name":"held-each","steps":[
 {"id":"each","type":"foreach","foreach":{"items":"[\"a\"]","as":"host","parallelism":1,"steps":[
  {"id":"create","type":"crewship","action":"issue.create","args":{"title":"Status {{ inputs.host }}"}}
 ]}}
]}`

// strictCrewActions holds every call until its request carries the token of
// an APPROVED approval waitpoint of the same run.
type strictCrewActions struct {
	db   *sql.DB
	mu   sync.Mutex
	done int
}

func (s *strictCrewActions) Do(ctx context.Context, req pipeline.CrewshipRequest) (string, error) {
	held := &pipeline.CrewshipApprovalRequired{Verb: req.Verb, Action: "mission_create", Level: "strict", CrewID: "held-crew", Decision: "inbox_approve"}
	if req.ApprovalToken == "" {
		return "", held
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM pipeline_waitpoints WHERE token = ? AND pipeline_run_id = ?`, req.ApprovalToken, req.RunID).Scan(&status); err != nil {
		return "", err
	}
	if status != "approved" {
		return "", held
	}
	s.mu.Lock()
	s.done++
	s.mu.Unlock()
	return `{"identifier":"HLD-1"}`, nil
}

func startHeldEachServer(t *testing.T) (owner, manager string, actions *strictCrewActions) {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	for _, q := range []string{
		`INSERT INTO workspaces (id, name, slug) VALUES ('` + heldEachWorkspaceID + `', 'Held', 'held-ws')`,
		`INSERT INTO users (id, email, full_name) VALUES ('hld-owner', 'owner@hld-ex.com', 'Owner'), ('hld-manager', 'manager@hld-ex.com', 'Manager')`,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('hldm-owner', '` + heldEachWorkspaceID + `', 'hld-owner', 'OWNER'), ('hldm-manager', '` + heldEachWorkspaceID + `', 'hld-manager', 'MANAGER')`,
		`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus) VALUES ('held-crew', '` + heldEachWorkspaceID + `', 'Crew', 'held-crew', 'free', 4096, 2.0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	tokens := map[string]string{"hld-owner": "crewship_cli_hldowner00000000000000000000", "hld-manager": "crewship_cli_hldmanager000000000000000000"}
	for user, tok := range tokens {
		if _, err := db.Exec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, 't', ?, datetime('now'))`, "clt-"+user, user, sha256HexToken(tok)); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	actions = &strictCrewActions{db: db}
	h := router.PipelinesHandler
	h.SetSaveTokenSecret([]byte("acceptance-test-save-token-secret-32b"))
	h.SetRunner(unusedAgentRunner{})
	h.SetRunStore(pipeline.NewRunStore(db))
	h.SetRunRegistry(pipeline.NewRunRegistry())
	waitpoints := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(waitpoints.Close)
	h.SetWaitpointStore(waitpoints)
	h.SetCrewshipActions(actions)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	write := func(user string) string {
		p := filepath.Join(dir, user+".yaml")
		cfg := "server: " + srv.URL + "\nworkspace: " + heldEachWorkspaceID + "\ntoken: " + tokens[user] + "\nformat: table\n"
		if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return write("hld-owner"), write("hld-manager"), actions
}

var approveTokenRE = regexp.MustCompile(`waitpoints approve (\S+)`)

// runCLIWithin runs the CLI and fails the test when it does not exit within
// limit: a synchronous run that holds its request open hangs here.
func runCLIWithin(t *testing.T, cfgPath string, limit time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath, "NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%v did not return within %s; a held action inside foreach must not hold a "+
			"synchronous run's request open (#5348). Output so far:\n%s", args, limit, out)
	}
	return string(out), err
}

// startHeldEachRun starts the routine synchronously and returns the run id
// and the pending token from its WAITING receipt.
func startHeldEachRun(t *testing.T, cfg string) (runID, token string) {
	t.Helper()
	out, err := runCLIWithin(t, cfg, 30*time.Second, "routine", "run", "held-each")
	if err != nil {
		t.Fatalf("routine run: %v\n%s", err, out)
	}
	for _, want := range []string{"WAITING", "paused at approval step: create"} {
		if !strings.Contains(out, want) {
			t.Fatalf("synchronous run output lacks %q:\n%s", want, out)
		}
	}
	id := routineDetailRunIDRE.FindStringSubmatch(out)
	tok := approveTokenRE.FindStringSubmatch(out)
	if id == nil || tok == nil {
		t.Fatalf("no run id or approve command in:\n%s", out)
	}
	return id[1], tok[1]
}

func TestAcceptance_SyncRoutineRunWithHeldForeachItem(t *testing.T) {
	owner, manager, actions := startHeldEachServer(t)
	if out, err := runCLIWithin(t, owner, 60*time.Second, "routine", "save", "--name", "held-each",
		"--definition", writeRoutineDetailDefinition(t, "held-each", heldEachDef), "--author-crew", "held-crew"); err != nil {
		t.Fatalf("routine save: %v\n%s", err, out)
	}

	// The CLI that started the run has exited; the run must still wait.
	runID, token := startHeldEachRun(t, owner)

	out, err := runCLIWithin(t, manager, 30*time.Second, "routine", "waitpoints", "approve", token)
	if err == nil || !strings.Contains(out, "403") {
		t.Fatalf("a MANAGER must not decide a held action:\n%s", out)
	}
	if !strings.Contains(out, "needs a workspace OWNER or ADMIN") || strings.Contains(out, "an agent cannot") {
		t.Fatalf("the refusal must lead with the OWNER/ADMIN rule, not blame an agent:\n%s", out)
	}

	if out, err := runCLIWithin(t, owner, 30*time.Second, "routine", "waitpoints", "approve", token); err != nil {
		t.Fatalf("owner approve: %v\n%s", err, out)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, err := runRoutineDetailCLI(t, owner, "routine", "result", runID)
		low := strings.ToLower(out)
		if err == nil && strings.Contains(low, "completed") {
			break
		}
		if strings.Contains(low, "cancelled") || strings.Contains(low, "failed") || time.Now().After(deadline) {
			t.Fatalf("run %s did not complete after the approval (err=%v):\n%s", runID, err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	actions.mu.Lock()
	done := actions.done
	actions.mu.Unlock()
	if done != 1 {
		t.Fatalf("the approved action executed %d times, want once", done)
	}

	// A cancelled run's card says so instead of "already decided or expired".
	runID, token = startHeldEachRun(t, owner)
	if out, err := runCLIWithin(t, owner, 30*time.Second, "routine", "cancel", runID); err != nil {
		t.Fatalf("routine cancel: %v\n%s", err, out)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		out, err := runCLIWithin(t, owner, 30*time.Second, "routine", "waitpoints", "approve", token)
		if err != nil && strings.Contains(out, "409") {
			if !strings.Contains(out, "run was cancelled") {
				t.Fatalf("approving a cancelled run's waitpoint must say the run was cancelled:\n%s", out)
			}
			break
		}
		if err == nil || time.Now().After(deadline) {
			t.Fatalf("approve after cancel: want a 409 naming the cancellation (err=%v):\n%s", err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
