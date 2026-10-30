package main

// acceptance_cli_fidelity_test.go — #4508: the CLI printed less than the
// server said. Each test drives the BUILT BINARY against the REAL router over
// a migrated SQLite database, and checks two things the bug broke: that
// `-f json` carries the server's fields (the struct a command decoded into
// used to decide what survived), and that the human view names what an
// operator needs from them.

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// fidelityRig is one real server plus a CLI config pointing at it.
type fidelityRig struct {
	t      *testing.T
	url    string
	config string
}

// startFidelityServer seeds the statements, mints a CLI token for userID and
// serves the real router. workspace is the CLI's configured workspace.
func startFidelityServer(t *testing.T, workspace, userID string, seed []string, opts ...api.RouterOption) (*fidelityRig, *sql.DB) {
	t.Helper()
	dbh := testutil.MigratedDB(t)
	for _, q := range seed {
		if _, err := dbh.DB.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	token := "crewship_cli_fidelity" + strings.Repeat("0", 20)
	if _, err := dbh.DB.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('fid-token',?,'t',?,datetime('now'))`,
		userID, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(dbh.DB, "this-is-a-32-char-test-secret-pad",
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), opts...)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: table\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &fidelityRig{t: t, url: srv.URL, config: cfg}, dbh.DB
}

func (r *fidelityRig) run(env []string, args ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command(buildCrewshipBinary(r.t), args...)
	cmd.Env = append(append(os.Environ(), "CREWSHIP_CONFIG="+r.config, "NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE="), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// must runs the CLI and fails the test on a non-zero exit.
func (r *fidelityRig) must(args ...string) string {
	r.t.Helper()
	out, err := r.run(nil, args...)
	if err != nil {
		r.t.Fatalf("crewship %v: %v\n%s", args, err, out)
	}
	return out
}

// stdout runs the CLI with stdout alone captured (receipts go to stderr).
func (r *fidelityRig) stdout(args ...string) string {
	r.t.Helper()
	cmd := exec.Command(buildCrewshipBinary(r.t), args...)
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+r.config, "NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("crewship %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, out, stderr.String())
	}
	return string(out)
}

func decodeDoc(t *testing.T, out string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, out)
	}
	return doc
}

func decodeRows(t *testing.T, out string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, out)
	}
	return rows
}

func wantContains(t *testing.T, what, out string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(out, n) {
			t.Errorf("%s: missing %q in:\n%s", what, n, out)
		}
	}
}

// ── 1 + 2: routine list/get carry the draft, versions the test result ────

func TestAcceptance_CLIFidelity_RoutineDraftAndTestResults(t *testing.T) {
	cfg, db := startRoutineDetailAcceptanceServer(t)
	saveRoutineDetailV1(t, cfg)
	rig := &fidelityRig{t: t, config: cfg}
	ctx := t.Context()
	store := pipeline.NewStore(db)

	// A draft is cut from the published recipe it will replace.
	var baseID string
	var baseRevision int
	if err := db.QueryRow(`SELECT id, COALESCE(publication_revision,0) FROM pipelines WHERE workspace_id=? AND slug='acceptance-detail' AND deleted_at IS NULL`,
		routineDetailAcceptanceWorkspaceID).Scan(&baseID, &baseRevision); err != nil {
		t.Fatal(err)
	}
	draft, err := store.SaveDraft(ctx, pipeline.Draft{
		WorkspaceID: routineDetailAcceptanceWorkspaceID, Slug: "acceptance-detail", UpdatedBy: "dtl-owner",
		BasePipelineID: baseID, BaseRevision: baseRevision,
		Document: json.RawMessage(`{"slug":"acceptance-detail","name":"acceptance-detail","definition":` + routineDetailV2Def + `}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTestResult(ctx, routineDetailAcceptanceWorkspaceID, pipeline.DefinitionHash([]byte(routineDetailV2Def)), true, "completed", "dtl-owner"); err != nil {
		t.Fatal(err)
	}

	// get -f json: the draft with its test result, and the detail-only
	// documents (files, manifest) the struct never declared.
	doc := decodeDoc(t, rig.must("routine", "get", "acceptance-detail", "-f", "json"))
	d, ok := doc["draft"].(map[string]any)
	if !ok {
		t.Fatalf("routine get -f json has no draft: %v", doc)
	}
	if d["id"] != draft.ID || d["revision"] != float64(draft.Revision) || d["updated_by"] != "dtl-owner" {
		t.Errorf("draft = %v, want id %s revision %d", d, draft.ID, draft.Revision)
	}
	if tr, _ := d["test_result"].(map[string]any); tr == nil || tr["passed"] != true {
		t.Errorf("draft.test_result = %v, want passed", d["test_result"])
	}
	for _, key := range []string{"files", "manifest", "definition"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("routine get -f json dropped %q", key)
		}
	}

	// get, human: the draft is named, with its test verdict, and the
	// operator is told Run does not use it.
	wantContains(t, "routine get", rig.must("routine", "get", "acceptance-detail"),
		"Draft:", "DRAFT r", "TESTED PASSED", "run uses the published version")

	// list -f json and -f ndjson carry the draft too; ndjson is a line per row.
	rows := decodeRows(t, rig.must("routine", "list", "-f", "json"))
	if len(rows) != 1 || rows[0]["draft"] == nil {
		t.Fatalf("routine list -f json lost the draft: %v", rows)
	}
	sc := bufio.NewScanner(strings.NewReader(rig.stdout("routine", "list", "-f", "ndjson")))
	lines := 0
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		lines++
		if !strings.Contains(sc.Text(), `"draft"`) {
			t.Errorf("ndjson row without draft: %s", sc.Text())
		}
	}
	if lines != 1 {
		t.Errorf("routine list -f ndjson printed %d rows, want 1", lines)
	}

	// versions: the per-version test result, in JSON and as a TESTED column.
	versions := decodeRows(t, rig.must("routine", "versions", "acceptance-detail", "-f", "json"))
	if len(versions) == 0 {
		t.Fatal("no versions")
	}
	hash, _ := versions[0]["definition_hash"].(string)
	// The save ran a dry run of the sample inputs, which is a recorded test.
	if tr, _ := versions[0]["test_result"].(map[string]any); tr == nil || tr["passed"] != true {
		t.Fatalf("saved version has no passing test_result: %v", versions[0])
	}
	if err := store.RecordTestResult(ctx, routineDetailAcceptanceWorkspaceID, hash, false, "timeout", "dtl-owner"); err != nil {
		t.Fatal(err)
	}
	versions = decodeRows(t, rig.must("routine", "versions", "acceptance-detail", "-f", "json"))
	if tr, _ := versions[0]["test_result"].(map[string]any); tr == nil || tr["status"] != "timeout" || tr["passed"] != false {
		t.Errorf("versions -f json test_result = %v, want failed/timeout", versions[0]["test_result"])
	}
	wantContains(t, "routine versions", rig.must("routine", "versions", "acceptance-detail"), "TESTED", "FAILED (timeout)")
	// A version nobody tested reads as untested, not as a pass.
	if _, err := db.Exec(`DELETE FROM routine_test_results`); err != nil {
		t.Fatal(err)
	}
	if versions = decodeRows(t, rig.must("routine", "versions", "acceptance-detail", "-f", "json")); versions[0]["test_result"] != nil {
		t.Errorf("untested version carries a test_result: %v", versions[0])
	}
	wantContains(t, "routine versions", rig.must("routine", "versions", "acceptance-detail"), "untested")
}

// ── 3: skill get/list show lifecycle, credentials, usage; --lifecycle ─────

func TestAcceptance_CLIFidelity_SkillLifecycleAndUsage(t *testing.T) {
	rig, _ := startFidelityServer(t, "fid-ws", "fid-owner", []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('fid-ws','Fidelity','fid-ws')`,
		`INSERT INTO users(id,email,full_name) VALUES('fid-owner','owner@fid.invalid','Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('fid-m1','fid-ws','fid-owner','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('fid-crew','fid-ws','Crew','fid-crew','free')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('fid-agent','fid-crew','fid-ws','Viktor','viktor')`,
		`INSERT INTO skills(id,name,slug,display_name,description,source,maturity,lifecycle_state,credential_requirements)
			VALUES('sk-live','live','live-skill','Live skill','in use','CUSTOM','COMMUNITY','active','["GITHUB_TOKEN"]'),
			      ('sk-old','old','old-skill','Old skill','nobody calls it','CUSTOM','COMMUNITY','stale','[]')`,
		`INSERT INTO agent_skills(id,agent_id,skill_id,enabled) VALUES('fid-as1','fid-agent','sk-live',1)`,
	})

	doc := decodeDoc(t, rig.must("skill", "get", "live-skill", "-f", "json"))
	for _, key := range []string{"lifecycle_state", "needs_credentials", "usage", "installed_on", "maturity", "scan_status"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("skill get -f json dropped %q: %v", key, doc)
		}
	}
	if usage, _ := doc["usage"].(map[string]any); usage == nil || usage["uses_7d"] == nil || usage["uses_30d"] == nil {
		t.Errorf("usage = %v", doc["usage"])
	}
	wantContains(t, "skill get", rig.must("skill", "get", "live-skill"),
		"Lifecycle", "active", "Needs credentials", "GITHUB_TOKEN", "Uses 7d/30d", "Installed on", "viktor")

	table := rig.must("skill", "list")
	wantContains(t, "skill list", table, "LIFECYCLE", "stale")

	stale := decodeRows(t, rig.must("skill", "list", "--lifecycle", "stale", "-f", "json"))
	if len(stale) != 1 || stale[0]["slug"] != "old-skill" {
		t.Fatalf("--lifecycle stale = %v, want only old-skill", stale)
	}
	if _, ok := stale[0]["needs_credentials"]; !ok {
		t.Errorf("skill list -f json is not the server's row: %v", stale[0])
	}
	if out := rig.must("skill", "list", "--lifecycle", "active"); strings.Contains(out, "old-skill") || !strings.Contains(out, "live-skill") {
		t.Errorf("--lifecycle active table:\n%s", out)
	}
	if out, err := rig.run(nil, "skill", "list", "--lifecycle", "gone"); err == nil {
		t.Errorf("an unknown --lifecycle was accepted:\n%s", out)
	}
}

// ── 4: workspace get/list carry kind, personal owner, guest run limit ─────

func TestAcceptance_CLIFidelity_WorkspaceKind(t *testing.T) {
	rig, _ := startFidelityServer(t, "mine", "fid-owner", []string{
		`INSERT INTO workspaces(id,name,slug,kind,guest_routine_max_run_minutes) VALUES('fid-personal','Mine','mine','personal',45),('fid-team','Team','team','team',30)`,
		`INSERT INTO users(id,email,full_name) VALUES('fid-owner','owner@fid.invalid','Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('fid-m1','fid-personal','fid-owner','OWNER'),('fid-m2','fid-team','fid-owner','OWNER')`,
	})

	doc := decodeDoc(t, rig.must("workspace", "get", "-f", "json"))
	if doc["kind"] != "personal" || doc["personal_owner_id"] != "fid-owner" || doc["guest_routine_max_run_minutes"] != float64(45) {
		t.Errorf("workspace get -f json = %v", doc)
	}
	if _, ok := doc["approvals_retention_days"]; !ok {
		t.Errorf("workspace get -f json is not the server's document: %v", doc)
	}
	wantContains(t, "workspace get", rig.must("workspace", "get"), "Kind", "personal", "Personal owner", "fid-owner", "Guest routine max run", "45 min")

	rows := decodeRows(t, rig.must("workspace", "list", "-f", "json"))
	kinds := map[string]any{}
	for _, r := range rows {
		kinds[r["slug"].(string)] = r["kind"]
	}
	if kinds["mine"] != "personal" || kinds["team"] != "team" {
		t.Errorf("workspace list -f json kinds = %v", kinds)
	}
	wantContains(t, "workspace list", rig.must("workspace", "list"), "KIND", "personal", "team")
}
