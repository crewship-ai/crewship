package main

// acceptance_cli_fidelity_writes_test.go — #4508, the commands that change
// something: what they print under -f json, and the flags whose values never
// reached the server in the form it reads. Same rig as
// acceptance_cli_fidelity_test.go — built binary, real router, migrated DB.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/usermodel"
)

var fidelityBaseSeed = []string{
	`INSERT INTO workspaces(id,name,slug) VALUES('fid-ws','Fidelity','fid-ws')`,
	`INSERT INTO users(id,email,full_name) VALUES('fid-owner','owner@fid.invalid','Owner')`,
	`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('fid-m1','fid-ws','fid-owner','OWNER')`,
	`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('fid-crew-id','fid-ws','Backend','backend','free'),('fid-crew2-id','fid-ws','Ops','ops','free')`,
	`INSERT INTO agents(id,crew_id,workspace_id,name,slug,memory_enabled) VALUES('fid-agent-id','fid-crew-id','fid-ws','Martin','martin',1)`,
}

// ── 9: admin instance backups overview names the crewless workspaces ─────

func TestAcceptance_CLIFidelity_BackupOverviewCrewless(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0")
	rig, _ := startFidelityServer(t, "people", "bp-boss", []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('bp-people','People','people','2026-01-01 00:00:00'),('bp-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('bp-boss','boss@people.invalid','Boss')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('bp-m1','bp-people','bp-boss','OWNER'),('bp-m2','bp-lab','bp-boss','OWNER')`,
		`INSERT INTO crews(id,workspace_id,slug,name) VALUES('bp-crew','bp-lab','research','Research')`,
	})
	args := []string{"admin", "instance", "backups", "overview", "--scope", "workspaces"}
	doc := decodeDoc(t, rig.must(append(args, "-f", "json")...))
	if doc["crewless_workspaces"] != float64(1) {
		t.Fatalf("crewless_workspaces = %v, want 1 (People has no crew)", doc["crewless_workspaces"])
	}
	wantContains(t, "backups overview", rig.must(args...), "1 workspace(s) with no crew")
}

// ── 5: memory initialize ─────────────────────────────────────────────────

func TestAcceptance_CLIFidelity_MemoryInitialize(t *testing.T) {
	storage := t.TempDir()
	rig, _ := startFidelityServer(t, "fid-ws", "fid-owner", fidelityBaseSeed, api.WithOutputBasePath(storage))
	dir := t.TempDir()
	crewFile := filepath.Join(dir, "crew.md")
	if err := os.WriteFile(crewFile, []byte("# Backend\nShip on Tuesdays.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Crew scope by slug, one doc from a file and one literal.
	doc := decodeDoc(t, rig.must("memory", "initialize", "--crew", "backend",
		"--doc", "CREW.md=@"+crewFile, "--doc", "learned.md=Prefers small PRs.", "-f", "json"))
	if doc["written"] != float64(2) || doc["existing"] != float64(0) {
		t.Fatalf("first initialize = %v, want 2 written", doc)
	}
	got, err := os.ReadFile(filepath.Join(storage, "crews", "fid-crew-id", "shared", ".memory", "CREW.md"))
	if err != nil || !strings.Contains(string(got), "Ship on Tuesdays") {
		t.Fatalf("CREW.md not written where the crew reads it: %q %v", got, err)
	}

	// Again: nothing is replaced, and the human line says so.
	wantContains(t, "second initialize", rig.must("memory", "initialize", "--crew", "backend", "--doc", "CREW.md=different"),
		"0 written, 1 already existed")
	got, _ = os.ReadFile(filepath.Join(storage, "crews", "fid-crew-id", "shared", ".memory", "CREW.md"))
	if strings.Contains(string(got), "different") {
		t.Fatal("initialize replaced existing memory")
	}

	// Agent scope.
	doc = decodeDoc(t, rig.must("memory", "initialize", "--crew", "backend", "--agent", "martin",
		"--doc", "AGENT.md=Martin keeps the runbook.", "-f", "json"))
	if doc["written"] != float64(1) {
		t.Fatalf("agent initialize = %v", doc)
	}
	if _, err := os.Stat(filepath.Join(storage, "crews", "fid-crew-id", "agents", "martin", ".memory", "AGENT.md")); err != nil {
		t.Fatalf("AGENT.md: %v", err)
	}

	tooMany := []string{"memory", "initialize", "--crew", "backend", "--agent", "martin"}
	for day := 1; day <= memoryInitializeMaxDocs+1; day++ {
		tooMany = append(tooMany, "--doc", fmt.Sprintf("daily/2026-01-%02d.md=x", day))
	}
	// Refusals exit non-zero: a name the scope does not accept (server), and
	// a batch over the cap or without --crew (before anything is sent).
	for _, args := range [][]string{
		{"memory", "initialize", "--crew", "backend", "--doc", "AGENT.md=wrong scope"},
		{"memory", "initialize", "--doc", "CREW.md=x"},
		{"memory", "initialize", "--crew", "backend", "--doc", "CREW.md"},
		tooMany,
	} {
		if out, err := rig.run(nil, args...); err == nil {
			t.Errorf("%v succeeded:\n%s", args, out)
		}
	}
}

// ── 6: feedback preferences by agent slug, value column, -f json; peer card

func TestAcceptance_CLIFidelity_FeedbackPreferencesAndPeerCard(t *testing.T) {
	_, cfg, msg, db := startFeedbackAcceptanceServer(t)
	// The agent's slug differs from its id: the peers route takes the id, and
	// a slug passed straight through used to 404.
	for _, q := range []string{`UPDATE agents SET memory_enabled=1, slug='fb-slug'`, `UPDATE conversation_messages SET role='assistant'`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rig := &fidelityRig{t: t, config: cfg}
	rig.must("feedback", "create", "--message", msg, "--signal", "helpful", "--reason", "Next time, keep answers concise.")
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	table := rig.must("feedback", "preferences", "--agent", "fb-slug")
	wantContains(t, "preferences table", table, "VALUE", "prefers concise answers")

	var entries []usermodel.FeedbackPreference
	if err := json.Unmarshal([]byte(rig.must("feedback", "preferences", "--agent", "fb-slug", "-f", "json")), &entries); err != nil || len(entries) != 1 {
		t.Fatalf("preferences -f json: %v %v", entries, err)
	}
	entry := entries[0]

	// The reason is checked against catalogue v1 before anything is sent.
	if out, err := rig.run(nil, "feedback", "edit-preference", "--agent", "fb-slug", "--entry", entry.ID,
		"--revision", fmt.Sprint(entry.Revision), "--reason", "be nicer"); err == nil || !strings.Contains(out, "catalogue v1") {
		t.Errorf("non-catalogue reason: err=%v\n%s", err, out)
	}
	edited := decodeDoc(t, rig.stdout("feedback", "edit-preference", "--agent", "fb-slug", "--entry", entry.ID,
		"--revision", fmt.Sprint(entry.Revision), "--reason", "Next time, start with the conclusion.", "-f", "json"))
	if edited["updated"] != true || edited["entry_id"] != entry.ID || edited["agent_id"] != "fb-agent" {
		t.Errorf("edit-preference -f json = %v", edited)
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(rig.must("feedback", "preferences", "--agent", "fb-slug", "-f", "json")), &entries); err != nil {
		t.Fatal(err)
	}
	withdrawn := decodeDoc(t, rig.stdout("feedback", "delete-preference", "--agent", "fb-slug", "--entry", entry.ID,
		"--revision", fmt.Sprint(entries[0].Revision), "-f", "json"))
	if withdrawn["withdrawn"] != true {
		t.Errorf("delete-preference -f json = %v", withdrawn)
	}

	// Each preference command has its own help line.
	shorts := map[string]bool{}
	for _, op := range feedbackPreferenceOps {
		shorts[op.short] = true
	}
	if len(shorts) != len(feedbackPreferenceOps) {
		t.Errorf("preference commands share a Short: %v", feedbackPreferenceOps)
	}
}

func TestAcceptance_CLIFidelity_PeerCard(t *testing.T) {
	storage := t.TempDir()
	rig, _ := startFidelityServer(t, "fid-ws", "fid-owner", fidelityBaseSeed, api.WithOutputBasePath(storage))
	paths := memory.PeerPaths{AgentDir: filepath.Join(storage, "crews", "fid-crew-id", "agents", "martin", ".memory")}
	if err := memory.WritePeerCard(paths, "fid-owner", "fid-ws", "# Owner\nLikes the conclusion first.\n"); err != nil {
		t.Fatal(err)
	}
	wantContains(t, "peer-card", rig.stdout("feedback", "peer-card", "--agent", "martin"), "Likes the conclusion first.")
	doc := decodeDoc(t, rig.stdout("feedback", "peer-card", "--agent", "martin", "-f", "json"))
	if doc["user_id"] != "fid-owner" || !strings.Contains(fmt.Sprint(doc["content"]), "conclusion first") {
		t.Errorf("peer-card -f json = %v", doc)
	}
	if err := memory.DeletePeerCard(paths, "fid-owner", "fid-ws"); err != nil {
		t.Fatal(err)
	}
	if out, err := rig.run(nil, "feedback", "peer-card", "--agent", "martin"); err == nil {
		t.Errorf("a missing card exited 0:\n%s", out)
	}
}

// TestFeedbackPreferenceCatalogueMatchesServer pins the CLI's restated
// catalogue to the server's.
func TestFeedbackPreferenceCatalogueMatchesServer(t *testing.T) {
	if len(feedbackPreferenceCatalogueV1) != len(usermodel.FeedbackPreferenceOptionsV1) {
		t.Fatalf("CLI lists %d reasons, server %d", len(feedbackPreferenceCatalogueV1), len(usermodel.FeedbackPreferenceOptionsV1))
	}
	for _, reason := range feedbackPreferenceCatalogueV1 {
		if _, ok := usermodel.FeedbackPreferenceOptionsV1[reason]; !ok {
			t.Errorf("CLI reason %q is not in the server catalogue", reason)
		}
	}
}

// ── 7: inbox --crew by slug, --deadline today with the local day end ─────

func TestAcceptance_CLIFidelity_InboxCrewAndDayEnd(t *testing.T) {
	rig, db := startFidelityServer(t, "fid-ws", "fid-owner", fidelityBaseSeed)
	now := time.Now().UTC()
	due := now.Add(3 * time.Hour)
	for _, it := range []struct{ source, title, crew string }{
		{"fid-src-1", "Backend item due in three hours", "fid-crew-id"},
		{"fid-src-2", "Ops item", "fid-crew2-id"},
	} {
		if err := inbox.Insert(t.Context(), db, slog.Default(), inbox.Item{
			WorkspaceID: "fid-ws", Kind: inbox.KindMessage, SourceID: it.source, Title: it.title,
			SenderType: "system", SenderName: "test",
			Payload: map[string]any{"crew_id": it.crew, "timeout_at": due.Format(time.RFC3339)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	titles := func(args ...string) string {
		var rows []struct {
			Title string `json:"title"`
		}
		out := rig.stdout(append([]string{"inbox", "list", "-f", "json"}, args...)...)
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("inbox list -f json: %v\n%s", err, out)
		}
		var ts []string
		for _, r := range rows {
			ts = append(ts, r.Title)
		}
		return strings.Join(ts, "|")
	}
	if got := titles("--crew", "backend"); got != "Backend item due in three hours" {
		t.Errorf("--crew backend (a slug) = %q", got)
	}
	// --day-end decides what "today" is: two hours out excludes an item due
	// in three, four hours out includes it.
	if got := titles("--deadline", "today", "--day-end", now.Add(2*time.Hour).Format(time.RFC3339)); got != "" {
		t.Errorf("today ending in 2h = %q, want nothing", got)
	}
	if got := titles("--crew", "backend", "--deadline", "today", "--day-end", now.Add(4*time.Hour).Format(time.RFC3339)); got != "Backend item due in three hours" {
		t.Errorf("today ending in 4h = %q", got)
	}
	if out, err := rig.run(nil, "inbox", "counts", "--day-end", "tomorrow"); err == nil {
		t.Errorf("a malformed --day-end was accepted:\n%s", out)
	}
}

// TestCLIFidelity_InboxSendsLocalDayEnd records what the binary sends: with
// no --day-end, `inbox counts` (always) and `inbox list --deadline today`
// carry the end of the caller's LOCAL day — here UTC+14, where it is never
// the end of the UTC day.
func TestCLIFidelity_InboxSendsLocalDayEnd(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.URL.Query()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/inbox/counts":
			_, _ = w.Write([]byte(`{"total":0,"unread":0,"spaces":{"action":0,"updates":0,"history":0},"by_kind":{}}`))
		case "/api/v1/inbox":
			_, _ = w.Write([]byte(`{"rows":[],"total":0}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: cfidws00000000000000001\ntoken: crewship_cli_x\nformat: json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(buildCrewshipBinary(t), args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "NO_COLOR=1", "TZ=Etc/GMT-14",
			"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	zone := time.FixedZone("UTC+14", 14*3600)
	check := func(path string) {
		t.Helper()
		mu.Lock()
		q := seen[path]
		mu.Unlock()
		got, err := time.Parse(time.RFC3339, q.Get("day_end"))
		if err != nil {
			t.Fatalf("%s day_end = %q: %v", path, q.Get("day_end"), err)
		}
		local := got.In(zone)
		if _, off := got.Zone(); off != 14*3600 || local.Hour() != 23 || local.Minute() != 59 || local.Second() != 59 {
			t.Errorf("%s day_end = %s, want 23:59:59 of the local (+14:00) day", path, q.Get("day_end"))
		}
		if today := time.Now().In(zone); local.YearDay() != today.YearDay() && time.Since(got) > 0 {
			t.Errorf("%s day_end %s is not today in +14:00", path, got)
		}
	}
	run("inbox", "counts")
	check("/api/v1/inbox/counts")
	run("inbox", "list", "--deadline", "today")
	check("/api/v1/inbox")
	run("inbox", "list", "--deadline", "hour")
	mu.Lock()
	if v := seen["/api/v1/inbox"].Get("day_end"); v != "" {
		t.Errorf("--deadline hour sent day_end=%s; only today reads it", v)
	}
	mu.Unlock()
}

// ── 8: mutations print the server's outcome under -f json ────────────────

func TestAcceptance_CLIFidelity_MutationsHonourFormat(t *testing.T) {
	rig, db := startFidelityServer(t, "fid-ws", "fid-owner", append(append([]string{}, fidelityBaseSeed...),
		`INSERT INTO skills(id,name,slug,display_name,source,lifecycle_state) VALUES('sk-x','x','x-skill','X','CUSTOM','active')`,
	))
	folder := decodeDoc(t, rig.stdout("routine", "folders", "create", "Old", "-f", "json"))
	folderID, _ := folder["id"].(string)
	if folderID == "" {
		t.Fatalf("routine folders create: %v", folder)
	}

	// skill archive: the server's {skill_id, lifecycle_state}; the human
	// receipt is unchanged.
	doc := decodeDoc(t, rig.stdout("skill", "archive", "x-skill", "-f", "json"))
	if doc["skill_id"] != "sk-x" || doc["lifecycle_state"] != "archived" {
		t.Errorf("skill archive -f json = %v", doc)
	}
	wantContains(t, "skill restore", rig.must("skill", "restore", "x-skill"), "Skill x-skill restored")

	// policy set: the policy document, and the crew document when the quota
	// changed too.
	doc = decodeDoc(t, rig.stdout("policy", "set", "--crew", "backend", "--level", "strict", "--max-ephemeral", "7", "-f", "json"))
	policy, _ := doc["policy"].(map[string]any)
	if policy == nil || policy["autonomy_level"] != "strict" || doc["crew"] == nil {
		t.Errorf("policy set -f json = %v", doc)
	}
	wantContains(t, "policy set", rig.must("policy", "set", "--crew", "backend", "--level", "guided"), "Policy updated for crew")

	// routine folder delete answers 204: -f json still gets a document.
	doc = decodeDoc(t, rig.stdout("routine", "folders", "delete", folderID, "-f", "json"))
	if doc["deleted"] != true || doc["folder_id"] != folderID {
		t.Errorf("routine folders delete -f json = %v", doc)
	}

	// workspace logo remove: the updated workspace, logo gone.
	doc = decodeDoc(t, rig.stdout("workspace", "logo", "remove", "-f", "json"))
	if doc["id"] != "fid-ws" || doc["logo_url"] != nil {
		t.Errorf("workspace logo remove -f json = %v", doc)
	}

	// agent delete: the runs outcome a script needs next.
	doc = decodeDoc(t, rig.stdout("agent", "delete", "martin", "--yes", "-f", "json"))
	if len(doc) == 0 {
		t.Errorf("agent delete -f json printed an empty document")
	}

	// crew delete: the server's cleanup outcome document.
	doc = decodeDoc(t, rig.stdout("crew", "delete", "ops", "--yes", "-f", "json"))
	if _, ok := doc["sidecar_teardown"]; !ok {
		if doc["deleted"] != true {
			t.Errorf("crew delete -f json = %v", doc)
		}
	}
	var gone int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE id='fid-crew2-id' AND deleted_at IS NULL`).Scan(&gone); err != nil || gone != 0 {
		t.Errorf("crew not deleted: %d %v", gone, err)
	}
}
