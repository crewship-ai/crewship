package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// TestAcceptanceLabelsProjects drives the built CLI binary against the
// production Router and a migrated SQLite database for the label and project
// planning verbs that had no binary-level coverage: label list/create/update,
// project list/create/update/stats, project milestone update and issue bulk
// update. It checks the success path, --format json output, write denial for
// MEMBER and VIEWER, and the error text for duplicate names and invalid input.
// No HTTP response is mocked; the proxy only records the Router's status codes.
func TestAcceptanceLabelsProjects(t *testing.T) {
	t.Setenv("CREWSHIP_RATELIMIT_DISABLED", "true")
	db := testutil.MigratedDB(t).DB
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const ws = "lp-ws"
	hash, err := bcrypt.GenerateFromPassword([]byte("lp-fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	sql(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Labels and projects fixture',?)`, ws, ws)
	roles := map[string]string{"owner": "OWNER", "admin": "ADMIN", "manager": "MANAGER", "member": "MEMBER", "viewer": "VIEWER"}
	for user, role := range roles {
		sql(`INSERT INTO users(id,email,hashed_password) VALUES(?,?,?)`, "lp-"+user, user+"@lp.invalid", string(hash))
		sql(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, "lp-m-"+user, ws, "lp-"+user, role)
	}
	sql(`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('lp-crew',?,'Planning','planning','free')`, ws)
	sql(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('lp-lead',?,'lp-crew','Lead fixture','lead','LEAD')`, ws)
	for i, issue := range []string{"lp-issue-1", "lp-issue-2"} {
		sql(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,number,identifier,priority,sort_order,mission_type,created_at,updated_at) VALUES(?,?,'lp-crew','lp-lead',?,?,'BACKLOG',?,?,'medium',0,'issue',datetime('now'),datetime('now'))`,
			issue, ws, "trace-"+issue, "Bulk fixture "+issue, i+1, "PLAN-"+string(rune('1'+i)))
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := journal.NewWriter(db, logger, journal.WriterOptions{})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, "labels-projects-cli-router-fixture-secret", logger, api.WithJournal(writer), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	var mu sync.Mutex
	var statuses []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		mu.Lock()
		statuses = append(statuses, response.Code)
		mu.Unlock()
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	tokens := map[string]string{}
	for user := range roles {
		token, err := exchangeCredentialsForSession(cli.NewClient(server.URL, "", ""), server.URL, user+"@lp.invalid", "lp-fixture-password")
		if err != nil {
			t.Fatal(err)
		}
		tokens[user] = token
		t.Cleanup(func() {
			closeTransientSeedSession(context.Background(), cli.NewClient(server.URL, token, ws), token)
		})
	}
	binary := buildCrewshipBinary(t)

	// invoke runs the binary as user with the given output format. want is the
	// final HTTP status the Router must have returned; 0 means the CLI must
	// reject the input itself without sending any request.
	invoke := func(user, format string, want int, args ...string) string {
		t.Helper()
		mu.Lock()
		statuses = nil
		mu.Unlock()
		config := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+ws+"\ntoken: "+tokens[user]+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		full := append(args, "--server", server.URL, "--workspace", ws)
		if format != "" {
			full = append(full, "--format", format)
		}
		command := exec.CommandContext(ctx, binary, full...)
		command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_PROFILE=", "CREWSHIP_SERVER=", "CREWSHIP_WORKSPACE=", "CREWSHIP_TOKEN=", "NO_COLOR=1")
		command.Dir = t.TempDir()
		out, err := command.CombinedOutput()
		if (err == nil) != (want != 0 && want < 400) {
			t.Fatalf("%s %v: error=%v output=%s", user, args, err, out)
		}
		mu.Lock()
		defer mu.Unlock()
		if want == 0 {
			if len(statuses) != 0 {
				t.Errorf("%s %v: CLI-side validation still sent requests: %v", user, args, statuses)
			}
		} else if last := len(statuses) - 1; last < 0 || statuses[last] != want {
			t.Errorf("%s %v: observed HTTP statuses %v, want final %d", user, args, statuses, want)
		}
		return string(out)
	}
	run := func(user string, want int, args ...string) string {
		t.Helper()
		return invoke(user, "json", want, args...)
	}
	decode := func(raw string, into any) {
		t.Helper()
		if err := json.Unmarshal([]byte(raw), into); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, raw)
		}
	}
	mustContain := func(out, want string) {
		t.Helper()
		// JSON error envelopes escape quotes; accept either rendering.
		if !strings.Contains(out, want) && !strings.Contains(out, strings.ReplaceAll(want, `"`, `\"`)) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}

	// ── Labels ──────────────────────────────────────────────────────────
	var label labelItem
	decode(run("manager", 201, "label", "create", "--name", "bug", "--color", "#EF4444", "--group", "kind"), &label)
	if label.ID == "" || label.Name != "bug" || label.Color != "#EF4444" || label.Group == nil || *label.Group != "kind" {
		t.Fatalf("created label = %+v", label)
	}
	mustContain(invoke("owner", "", 201, "label", "create", "--name", "backend", "--color", "#3B82F6"), "Label created: backend")
	mustContain(run("owner", 409, "label", "create", "--name", "bug", "--color", "#000000"), `A label named "bug" already exists in this workspace`)
	mustContain(run("owner", 0, "label", "create", "--name", "bad", "--color", "red"), `invalid --color "red": expected hex like #3B82F6 or #abc`)
	mustContain(run("owner", 0, "label", "create", "--color", "#000"), "--name is required")
	for _, user := range []string{"member", "viewer"} {
		run(user, 403, "label", "create", "--name", "denied-"+user, "--color", "#111111")
	}

	var labels []labelItem
	decode(run("viewer", 200, "label", "list"), &labels)
	if len(labels) != 2 {
		t.Fatalf("label list = %+v, want bug and backend", labels)
	}

	// Rename is ADMIN+ at the route; MANAGER may create but not rename.
	run("manager", 403, "label", "update", label.ID, "--name", "manager-rename")
	run("member", 403, "label", "update", label.ID, "--name", "member-rename")
	var renamed labelItem
	decode(run("admin", 200, "label", "update", label.ID, "--name", "regression", "--color", "#10B981"), &renamed)
	if renamed.ID != label.ID || renamed.Name != "regression" || renamed.Color != "#10B981" {
		t.Fatalf("updated label = %+v", renamed)
	}
	mustContain(invoke("admin", "", 200, "label", "update", label.ID, "--group", "triage"), "Label updated: regression")
	mustContain(run("admin", 0, "label", "update", label.ID), "no fields to update")
	mustContain(run("admin", 404, "label", "update", "lp-missing-label", "--name", "x"), "Label not found")

	// ── Projects ────────────────────────────────────────────────────────
	var project struct {
		ID, Name, Slug string
	}
	decode(run("manager", 201, "project", "create", "--name", "Billing", "--priority", "high", "--description", "Invoices"), &project)
	if project.ID == "" || project.Name != "Billing" || project.Slug == "" {
		t.Fatalf("created project = %+v", project)
	}
	mustContain(run("owner", 409, "project", "create", "--name", "billing"), `A project with the slug "billing" already exists in this workspace`)
	mustContain(run("owner", 0, "project", "create"), "--name is required")
	for _, user := range []string{"member", "viewer"} {
		run(user, 403, "project", "create", "--name", "Denied "+user)
		run(user, 403, "project", "update", project.ID, "--name", "Denied "+user)
	}

	var projects []projectItem
	decode(run("viewer", 200, "project", "list"), &projects)
	if len(projects) != 1 || projects[0].ID != project.ID || projects[0].Priority != "high" {
		t.Fatalf("project list = %+v", projects)
	}

	var updated map[string]any
	decode(run("manager", 200, "project", "update", project.ID, "--status", "in_progress", "--health", "at_risk", "--description", ""), &updated)
	if updated["id"] != project.ID || updated["status"] != "in_progress" || updated["health"] != "at_risk" {
		t.Fatalf("updated project = %v", updated)
	}
	mustContain(invoke("manager", "", 200, "project", "update", project.ID, "--name", "Billing v2"), "Project "+project.ID+" updated.")
	mustContain(run("manager", 0, "project", "update", project.ID), "no fields to update")
	mustContain(run("manager", 404, "project", "update", "lp-missing-project", "--name", "x"), "Project not found")

	// ── Milestone update ───────────────────────────────────────────────
	var milestone milestoneItem
	decode(run("manager", 201, "project", "milestone", "create", project.ID, "--name", "Beta"), &milestone)
	for _, user := range []string{"member", "viewer"} {
		run(user, 403, "project", "milestone", "update", milestone.ID, "--name", "Denied")
	}
	var revised milestoneItem
	decode(run("manager", 200, "project", "milestone", "update", milestone.ID, "--status", "completed", "--name", "Beta shipped", "--position", "2"), &revised)
	if revised.ID != milestone.ID || revised.Name != "Beta shipped" || revised.Status != "completed" || revised.Position != 2 {
		t.Fatalf("updated milestone = %+v", revised)
	}
	mustContain(invoke("manager", "", 200, "project", "milestone", "update", milestone.ID, "--description", "done"), "Milestone updated: Beta shipped")
	mustContain(run("manager", 0, "project", "milestone", "update", milestone.ID), "no fields to update")
	mustContain(run("manager", 404, "project", "milestone", "update", "lp-missing-milestone", "--name", "x"), "Milestone not found")

	// ── Issue bulk update ──────────────────────────────────────────────
	for _, user := range []string{"member", "viewer"} {
		run(user, 403, "issue", "bulk", "update", "--ids", "lp-issue-1,lp-issue-2", "--priority", "high")
	}
	var bulk struct {
		Updated int `json:"updated"`
	}
	decode(run("manager", 200, "issue", "bulk", "update", "--ids", "lp-issue-1, lp-issue-2", "--priority", "urgent", "--project", project.ID, "--labels", label.ID), &bulk)
	if bulk.Updated != 2 {
		t.Fatalf("bulk updated = %d, want 2", bulk.Updated)
	}
	mustContain(invoke("manager", "", 200, "issue", "bulk", "update", "--ids", "lp-issue-1,lp-missing-issue", "--priority", "low"), "Bulk update applied to 1/2 issue(s).")
	mustContain(run("manager", 0, "issue", "bulk", "update", "--priority", "low"), "--ids is required")
	mustContain(run("manager", 0, "issue", "bulk", "update", "--ids", "lp-issue-1"), "at least one of --status / --priority / --assignee / --project / --labels is required")

	// ── Project stats reflect the bulk update ─────────────────────────
	var stats struct {
		TotalIssues int `json:"total_issues"`
		ByLabel     []struct {
			LabelName string `json:"label_name"`
			Count     int    `json:"count"`
		} `json:"by_label"`
	}
	decode(run("viewer", 200, "project", "stats", project.ID), &stats)
	if stats.TotalIssues != 2 || len(stats.ByLabel) != 1 || stats.ByLabel[0].LabelName != "regression" || stats.ByLabel[0].Count != 2 {
		t.Fatalf("project stats = %+v", stats)
	}
	mustContain(invoke("viewer", "", 200, "project", "stats", project.ID), "Issues: 2 total / 0 completed (0%)")
}
