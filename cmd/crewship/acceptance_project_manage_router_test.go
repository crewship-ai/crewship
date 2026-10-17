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

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// The built CLI talks to the production Router and migrated SQLite. No response
// mocks or provider/runtime operations are involved. Project discovery is a
// separate right: these clients know their own project ID but have no List.
func TestAcceptanceProjectManageRouterCLI(t *testing.T) {
	t.Setenv("CREWSHIP_RATELIMIT_DISABLED", "true")
	db := testutil.MigratedDB(t).DB
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("manage-fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	sql(`INSERT INTO workspaces(id,name,slug) VALUES('manage-ws','Manage fixture','manage-ws')`)
	for _, user := range []string{"owner", "issuer", "client-a", "client-b"} {
		sql(`INSERT INTO users(id,email,hashed_password) VALUES(?,?,?)`, user, user+"@manage.invalid", string(hash))
		role := "VIEWER"
		if user == "owner" {
			role = "OWNER"
		} else if user == "issuer" {
			role = "ADMIN"
		}
		sql(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,'manage-ws',?,?)`, user, user, role)
	}
	store := access.Store{DB: db}
	for _, suffix := range []string{"a", "b"} {
		project, user := "project-"+suffix, "client-"+suffix
		sql(`INSERT INTO projects(id,workspace_id,name,slug,description) VALUES(?,'manage-ws',?,?,?)`, project, "Client "+suffix, project, "PRIVATE_BRIEF_"+suffix)
		m, err := store.Membership(t.Context(), user, "manage-ws")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), "owner", user, "manage-ws", "restricted", m, []access.Right{{Kind: "project", ID: project, Operation: "read"}}); err != nil {
			t.Fatal(err)
		}
		m, err = store.Membership(t.Context(), user, "manage-ws")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), "issuer", user, "manage-ws", "restricted", m, []access.Right{{Kind: "project", ID: project, Operation: "read"}, {Kind: "project", ID: project, Operation: "manage"}}); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := journal.NewWriter(db, logger, journal.WriterOptions{})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, "manage-cli-router-fixture-secret-long", logger, api.WithJournal(writer), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	var mu sync.Mutex
	var requests []string
	var statuses []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Observe the real Router response; do not substitute any response.
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
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
	for _, user := range []string{"owner", "client-a", "client-b"} {
		token, err := exchangeCredentialsForSession(cli.NewClient(server.URL, "", ""), server.URL, user+"@manage.invalid", "manage-fixture-password")
		if err != nil {
			t.Fatal(err)
		}
		tokens[user] = token
		t.Cleanup(func() {
			closeTransientSeedSession(context.Background(), cli.NewClient(server.URL, token, "manage-ws"), token)
		})
	}
	binary := buildCrewshipBinary(t)
	run := func(user string, want int, args ...string) string {
		t.Helper()
		mu.Lock()
		requests, statuses = nil, nil
		mu.Unlock()
		config := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: manage-ws\ntoken: "+tokens[user]+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append(args, "--server", server.URL, "--workspace", "manage-ws", "--format", "json")...)
		command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_PROFILE=", "CREWSHIP_SERVER=", "CREWSHIP_WORKSPACE=", "CREWSHIP_TOKEN=", "NO_COLOR=1")
		command.Dir = t.TempDir()
		out, err := command.CombinedOutput()
		if (err == nil) != (want < 400) {
			t.Fatalf("%s %v: error=%v output=%s", user, args, err, out)
		}
		mu.Lock()
		last := len(statuses) - 1
		if last < 0 || statuses[last] != want {
			t.Errorf("%s %v: observed HTTP statuses %v, want final %d", user, args, statuses, want)
		}
		mu.Unlock()
		if user == "client-a" && strings.Contains(string(out), "PRIVATE_BRIEF_b") {
			t.Fatal("foreign project description leaked")
		}
		return string(out)
	}
	read := func(user, project string) sharedProjectDetails {
		t.Helper()
		var value sharedProjectDetails
		if err := json.Unmarshal([]byte(run(user, 200, "project", "shared", project)), &value); err != nil {
			t.Fatal(err)
		}
		if value.ID != project || !value.CanManage || len(value.Version) != 64 {
			t.Fatal(value)
		}
		return value
	}
	current := read("client-a", "project-a")
	read("client-b", "project-b")
	run("client-a", 404, "project", "shared", "project-b")
	run("client-b", 404, "project", "shared", "project-a")
	update := func(version, name string, want int) {
		t.Helper()
		mu.Lock()
		requests = nil
		mu.Unlock()
		run("client-a", want, "project", "shared", "update", "project-a", "--name", name, "--description", "Reviewed client brief", "--version", version)
		mu.Lock()
		defer mu.Unlock()
		writes := 0
		for _, request := range requests {
			if request == "PUT /api/v1/workspaces/manage-ws/restricted-projects/project-a" {
				writes++
			}
			if request == "GET /api/v1/workspaces/manage-ws/restricted-projects/project-a" {
				t.Fatal("update silently fetched a new version")
			}
		}
		if writes != 1 {
			t.Fatalf("update issued %d writes", writes)
		}
	}
	update(current.Version, "Reviewed client A", 200)
	changed := read("client-a", "project-a")
	if changed.Name != "Reviewed client A" || changed.Version == current.Version {
		t.Fatal(changed)
	}
	update(current.Version, "Stale must not win", 409)
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE actor_id='client-a' AND summary='Guest updated shared project details'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("project audit count=%d error=%v", audits, err)
	}
	// Real milestone create/update/delete uses reviewed versions, including a
	// stale deletion failure. The other client cannot see this resource.
	var milestone sharedProjectMilestone
	if err := json.Unmarshal([]byte(run("client-a", 201, "project", "shared", "milestones", "create", "project-a", "--name", "Release A")), &milestone); err != nil {
		t.Fatal(err)
	}
	if milestone.ProjectID != "project-a" || !milestone.CanManage || !milestone.CanDelete {
		t.Fatal(milestone)
	}
	run("client-b", 404, "project", "shared", "milestones", "project-a", milestone.ID)
	var revised sharedProjectMilestone
	if err := json.Unmarshal([]byte(run("client-a", 200, "project", "shared", "milestones", "update", "project-a", milestone.ID, "--name", "Release reviewed", "--description", "", "--target-date", "", "--status", "active", "--position", "0", "--version", milestone.Version)), &revised); err != nil {
		t.Fatal(err)
	}
	run("client-a", 409, "project", "shared", "milestones", "delete", "project-a", milestone.ID, "--version", milestone.Version, "--confirm")
	run("client-a", 204, "project", "shared", "milestones", "delete", "project-a", milestone.ID, "--version", revised.Version, "--confirm")
	// Audit rejection must roll back the real handler's transaction.
	sql(`CREATE TRIGGER reject_manage_cli_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='Guest updated shared project details' BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END`)
	update(changed.Version, "Must roll back", 500)
	if read("client-a", "project-a").Name != changed.Name {
		t.Fatal("audit failure committed project mutation")
	}
	sql(`DROP TRIGGER reject_manage_cli_audit`)
	// Demotion via the owner's normal membership route invalidates the live
	// Manage issuer for both existing client sessions, but not OWNER Read.
	owner := cli.NewClient(server.URL, tokens["owner"], "manage-ws")
	response, err := owner.Patch("/api/v1/workspaces/manage-ws/members/issuer", map[string]string{"role": "MEMBER"})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("issuer demotion: %d", response.StatusCode)
	}
	run("client-a", 404, "project", "shared", "project-a")
	run("client-b", 404, "project", "shared", "project-b")
	update(changed.Version, "Revoked must not win", 404)
	if err := store.Check(t.Context(), "client-a", "manage-ws", access.Right{Kind: "project", ID: "project-a", Operation: "read"}); err != nil {
		t.Fatal("independent OWNER Read grant was lost", err)
	}
	if err := store.Check(t.Context(), "client-a", "manage-ws", access.Right{Kind: "project", ID: "project-a", Operation: "list"}); err == nil {
		t.Fatal("known-ID management implicitly granted List")
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM projects WHERE id='project-a'`).Scan(&name); err != nil || name != changed.Name {
		t.Fatalf("denied writes changed project: %q %v", name, err)
	}
}
