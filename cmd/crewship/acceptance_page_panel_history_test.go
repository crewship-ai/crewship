package main

import (
	"encoding/json"
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

// A real CLI process against the real router on a migrated SQLite, for the
// two commands that need a live publication and used to have no CLI at all or
// no CLI test: `page project panel-history` (#2581) and the publish fence's
// baseline acknowledgement.
//
// The publication is SEEDED rather than built. Publishing for real needs the
// pinned pages-build image, which only the pages-apps CI lane has, and that
// lane already drives the build → publish → rollback half in
// acceptance_page_projects_test.go. What this test needs from a publication
// is only the live pointer and the fence rows, so it writes those and keeps
// the coverage where the dependency is not.
func TestAcceptance_PagePanelHistoryAndPublicationFence(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cpanelhistoryws00000"
	token := "crewship_cli_panelhistory000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cpanelhistoryws00000','Pages','panel-history-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('panel-history-crew','cpanelhistoryws00000','Operations','ops','free')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,author_crew_id,definition_json,definition_hash,status) VALUES('panel-history-routine','cpanelhistoryws00000','health-check','Health check','panel-history-crew','{}','h','active')`,
		`INSERT INTO users(id,email,full_name) VALUES('panel-history-owner','panel-history@example.invalid','Pages Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('panel-history-member','cpanelhistoryws00000','panel-history-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('panel-history-token','panel-history-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), api.WithPageProjectsPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	pageFile := filepath.Join(t.TempDir(), "page.yaml")
	pageYAML := "apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: health\n  name: Health\nspec:\n  panels:\n    - id: mysql\n      schema: status.v1\n      owner: crew/ops\n      producer: script/check-mysql.sh\n      sla: 30s\n      actions:\n        - id: refresh\n          kind: call\n          label: Check now\n          routine: health-check\n"
	if err := os.WriteFile(pageFile, []byte(pageYAML), 0600); err != nil {
		t.Fatal(err)
	}
	must("page", "create", "--file", pageFile)
	directory := filepath.Join(t.TempDir(), "application")
	must("page", "project", "init", "--dir", directory)
	source := filepath.Join(t.TempDir(), "source.yaml")
	if err := os.WriteFile(source, []byte(must("page", "project", "pack", directory)), 0600); err != nil {
		t.Fatal(err)
	}
	must("page", "project", "set", "health", "--file", source, "--revision", "0")

	// Before anything is live the fence refuses every read, and the CLI
	// refuses to ask without saying which version it is reading.
	if out, err := run("page", "project", "panel-history", "health", "mysql"); err == nil || !strings.Contains(out, "--publication is required") {
		t.Fatalf("history read without a publication fence: %v %s", err, out)
	}
	if out, err := run("page", "project", "panel-history", "health", "mysql", "--publication", "1"); err == nil || !strings.Contains(out, "publication changed") {
		t.Fatalf("history read with nothing live: %v %s", err, out)
	}
	for _, test := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--limit", "21"}, "--limit must be between 1 and 20"},
		{[]string{"--limit", "0"}, "--limit must be between 1 and 20"},
		{[]string{"--limit", "-1"}, "--limit must be between 1 and 20"},
		{[]string{"--before", "-1"}, "--before must be a positive sequence"},
	} {
		out, err := run(append([]string{"page", "project", "panel-history", "health", "mysql", "--publication", "1"}, test.flags...)...)
		if err == nil || !strings.Contains(out, test.want) || strings.Contains(out, `"status"`) {
			t.Fatalf("%v was not refused locally: %v %s", test.flags, err, out)
		}
	}

	// Publication 1 of draft revision 1: a ready build receipt, the
	// publication row carrying the live definition (which is what the read
	// fence compares), and the live pointer. No Git checkpoint is recorded
	// for it, so the review has a live publication whose source it cannot
	// read back — the case the acknowledgement flag exists for.
	var pageID, sourceDigest string
	if err := db.QueryRow(`SELECT p.id,d.source_digest FROM pages p JOIN page_project_drafts d ON d.page_id=p.id WHERE p.workspace_id=? AND p.slug='health'`, ws).Scan(&pageID, &sourceDigest); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,artifact_digest,error,requested_by,created_at,completed_at) VALUES('panel-history-build','` + pageID + `',1,'` + sourceDigest + `','ready','artifact','','panel-history-owner','2026-09-15T00:00:00Z','2026-09-15T00:00:01Z')`,
		`INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,actor_user_id,created_at) SELECT id,1,'panel-history-build',1,'` + sourceDigest + `','','artifact',spec_json,'{}','panel-history-owner','2026-09-15T00:00:02Z' FROM pages WHERE id='` + pageID + `'`,
		`INSERT INTO page_project_live(page_id,version,published) VALUES('` + pageID + `',1,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var detail struct {
		HasApplication     bool  `json:"has_application"`
		PublicationVersion int64 `json:"publication_version"`
	}
	if out := must("page", "get", "health"); json.Unmarshal([]byte(out), &detail) != nil || !detail.HasApplication || detail.PublicationVersion != 1 {
		t.Fatalf("seeded publication is not live: %s", out)
	}
	if out := must("page", "project", "publications", "health"); !strings.Contains(out, "panel-history-build") {
		t.Fatalf("seeded publication is not retained: %s", out)
	}

	// One real push through the CLI, two more rows straight into the ring so
	// the cursor has something to page over.
	must("page", "set", "health/mysql", "--data", `{"items":[{"name":"mysql","state":"ok","label":"up"}]}`, "--state", "ok")
	var panelRow string
	if err := db.QueryRow(`SELECT id FROM page_panels WHERE page_id=? AND panel_id='mysql'`, pageID).Scan(&panelRow); err != nil {
		t.Fatal(err)
	}
	for seq := 2; seq <= 3; seq++ {
		if _, err := db.Exec(`INSERT INTO page_panel_data(panel_id,seq,payload_json,produced_at,state) VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%SZ','now'),'failed')`, panelRow, seq, `{"items":[],"seq":`+string(rune('0'+seq))+`}`); err != nil {
			t.Fatal(err)
		}
	}
	type historyPage struct {
		Items []struct {
			Sequence   int64           `json:"sequence"`
			Data       json.RawMessage `json:"data"`
			ProducedAt string          `json:"producedAt"`
			State      string          `json:"state"`
		} `json:"items"`
		NextBefore  int64 `json:"next_before"`
		Publication int64 `json:"publication"`
	}
	read := func(args ...string) historyPage {
		out := must(append([]string{"page", "project", "panel-history", "health", "mysql", "--publication", "1"}, args...)...)
		var page historyPage
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatalf("panel-history: %v\n%s", err, out)
		}
		if strings.Contains(out, "run_id") || strings.Contains(out, "producer_run") {
			t.Fatalf("panel-history exposes producer metadata: %s", out)
		}
		return page
	}
	whole := read()
	if len(whole.Items) != 3 || whole.Items[0].Sequence != 3 || whole.Items[2].Sequence != 1 || whole.NextBefore != 0 || whole.Publication != 1 {
		t.Fatalf("whole ring: %+v", whole)
	}
	var pushed struct {
		Items []struct {
			Label string `json:"label"`
		} `json:"items"`
	}
	if whole.Items[2].State != "ok" || json.Unmarshal(whole.Items[2].Data, &pushed) != nil || len(pushed.Items) != 1 || pushed.Items[0].Label != "up" || whole.Items[0].State != "failed" {
		t.Fatalf("ring rows: %+v", whole)
	}
	first := read("--limit", "2")
	if len(first.Items) != 2 || first.Items[0].Sequence != 3 || first.NextBefore != 2 {
		t.Fatalf("first page: %+v", first)
	}
	rest := read("--limit", "2", "--before", "2")
	if len(rest.Items) != 1 || rest.Items[0].Sequence != 1 || rest.NextBefore != 0 {
		t.Fatalf("cursor page: %+v", rest)
	}
	if out, err := run("page", "project", "panel-history", "health", "mysql", "--publication", "2"); err == nil || !strings.Contains(out, "publication changed") {
		t.Fatalf("a stale publication read the ring: %v %s", err, out)
	}
	if out, err := run("page", "project", "panel-history", "health", "nope", "--publication", "1"); err == nil || !strings.Contains(out, "not found") {
		t.Fatalf("an unknown panel: %v %s", err, out)
	}

	// A second revision, so the draft is a candidate that differs from what
	// is live rather than `candidate_matches_live`.
	style := filepath.Join(directory, "src", "style.css")
	css, err := os.ReadFile(style)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(style, append(css, []byte("\nbody { margin: 2rem; }\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(must("page", "project", "pack", directory)), 0600); err != nil {
		t.Fatal(err)
	}
	must("page", "project", "set", "health", "--file", source, "--revision", "1")

	// The review of a rollback candidate reads the retained publication, and
	// the review of the draft now has a live baseline it cannot compare with.
	var retained struct {
		Candidate *struct {
			Revision int64 `json:"revision"`
			Build    *struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"build"`
		} `json:"candidate"`
		Blockers []struct {
			Code string `json:"code"`
		} `json:"blockers"`
	}
	out := must("page", "project", "review", "health", "--publication", "1")
	if err = json.Unmarshal([]byte(out), &retained); err != nil || retained.Candidate == nil || retained.Candidate.Revision != 1 || retained.Candidate.Build == nil || retained.Candidate.Build.ID != "panel-history-build" {
		t.Fatalf("review --publication: %v %s", err, out)
	}
	codes := func(blockers []struct {
		Code string `json:"code"`
	}) map[string]bool {
		set := map[string]bool{}
		for _, b := range blockers {
			set[b.Code] = true
		}
		return set
	}
	if got := codes(retained.Blockers); !got["candidate_matches_live"] || !got["baseline_unavailable"] {
		t.Fatalf("review --publication blockers: %s", out)
	}
	if out := must("page", "project", "review", "health"); !strings.Contains(out, `"source_available": false`) && !strings.Contains(out, `"source_available":false`) || !strings.Contains(out, "baseline_unavailable") {
		t.Fatalf("draft review against an unreadable baseline: %s", out)
	}

	// The acknowledgement stop. The CLI reads the snapshot, says what is
	// missing and what the flag would mean, and does not set the flag on the
	// caller's behalf; with the flag it says it is publishing anyway. Either
	// way the request is then sent — here the build worker is not configured,
	// so the server's answer is a 503, which is past the fence.
	publish := []string{"page", "project", "publish", "health", "--build", "panel-history-build", "--revision", "2", "--expected-publication", "1", "--reviewed-code"}
	out, err = run(publish...)
	if err == nil || !strings.Contains(out, "retained source is unavailable") || !strings.Contains(out, "refuses this publication unless you pass --acknowledge-unavailable-baseline") || !strings.Contains(out, "Fencing on routine health-check sha256") {
		t.Fatalf("publish against an unreadable baseline did not stop to explain: %v %s", err, out)
	}
	if strings.Contains(out, "Publishing anyway") {
		t.Fatalf("the CLI acknowledged the baseline on the caller's behalf: %s", out)
	}
	out, err = run(append(publish, "--acknowledge-unavailable-baseline")...)
	if err == nil || !strings.Contains(out, "Publishing anyway, as --acknowledge-unavailable-baseline was given") || !strings.Contains(out, "build worker") {
		t.Fatalf("acknowledged publish: %v %s", err, out)
	}
	// A rollback fences on the retained version's own source, and says so.
	out, err = run("page", "project", "rollback", "health", "--publication", "1", "--expected-publication", "1", "--reviewed-code", "--acknowledge-unavailable-baseline")
	if err == nil || !strings.Contains(out, "Fencing on the retained source of publication 1") || !strings.Contains(out, "Publishing anyway") {
		t.Fatalf("rollback fence: %v %s", err, out)
	}

	// Withdrawn: the ring is no longer readable through the application.
	must("page", "project", "unpublish", "health", "--expected-publication", "1", "--yes")
	if out, err := run("page", "project", "panel-history", "health", "mysql", "--publication", "1"); err == nil || !strings.Contains(out, "publication changed") {
		t.Fatalf("a withdrawn application read the ring: %v %s", err, out)
	}
}
