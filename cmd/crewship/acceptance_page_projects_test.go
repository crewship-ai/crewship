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
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process -> real auth/router -> migrated SQLite -> real Git archive.
func TestAcceptance_PageProjectGitHistoryRestore(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cabcdefghijklmnopqrs"
	token := "crewship_cli_pagesproject0000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cabcdefghijklmnopqrs','Pages','pages-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('pages-cli-crew','cabcdefghijklmnopqrs','Operations','ops','free')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,author_crew_id,definition_json,definition_hash,status) VALUES('pages-cli-routine','cabcdefghijklmnopqrs','health-check','Health check','pages-cli-crew','{}','h','active')`,
		`INSERT INTO users(id,email,full_name) VALUES('pages-cli-owner','pages@example.invalid','Pages Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pages-cli-member','cabcdefghijklmnopqrs','pages-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('pages-cli-token','pages-cli-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	projectsPath := t.TempDir()
	options := []api.RouterOption{api.WithPageProjectsPath(projectsPath)}
	image := os.Getenv("PAGES_TEST_BUILD_IMAGE")
	if image != "" {
		options = append(options, api.WithPageBuildImage(image), api.WithPageRuntime("https://pages.example.net", "https://studio.example.com"))
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), options...)
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
	// Review before any draft exists: a snapshot, not an error, and the one
	// blocker that says why there is nothing to publish.
	if out := must("page", "project", "review", "health"); !strings.Contains(out, `"no_candidate"`) || !strings.Contains(out, `"initial_publication":true`) && !strings.Contains(out, `"initial_publication": true`) {
		t.Fatalf("review without a draft: %s", out)
	}
	if out, err := run("page", "project", "review", "health", "--publication", "-1"); err == nil || !strings.Contains(out, "must be positive") {
		t.Fatalf("negative publication accepted: %v %s", err, out)
	}
	directory := filepath.Join(t.TempDir(), "application")
	must("page", "project", "init", "--dir", directory)
	source := must("page", "project", "pack", directory)
	p, err := pages.ParseSourceProject(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "source.yaml")
	b, err := p.MarshalYAMLSource()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	saved := must("page", "project", "set", "health", "--file", file, "--revision", "0")
	if !strings.Contains(saved, "git_commit") {
		t.Fatal("CLI lost commit receipt: " + saved)
	}
	must("page", "project", "set", "health", "--file", file, "--revision", "1")
	exercisePageReviewBeforeBuild(t, run, must)
	exercisePageExportImportRoundTrip(t, must, p)
	var integrity struct {
		Healthy bool `json:"healthy"`
	}
	if report := must("page", "project", "fsck", "health"); json.Unmarshal([]byte(report), &integrity) != nil || !integrity.Healthy {
		t.Fatal(report)
	}
	must("page", "project", "compact")
	if _, err := run("page", "project", "compact", "--discard-history"); err == nil {
		t.Fatal("history discard accepted without confirmation")
	}
	history := must("page", "project", "history", "health")
	var decoded struct {
		Revisions []struct {
			Revision int    `json:"revision"`
			Commit   string `json:"git_commit"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal([]byte(history), &decoded); err != nil || len(decoded.Revisions) != 2 {
		t.Fatalf("history %s: %v", history, err)
	}
	if !pages.ValidProjectCommit(decoded.Revisions[0].Commit) {
		t.Fatal(history)
	}
	must("page", "project", "get", "health", "--revision", "1")
	if out, err := run("page", "project", "restore", "health", "--revision", "1", "--expected-revision", "1"); err == nil || !strings.Contains(out, "revision changed") {
		t.Fatalf("stale restore accepted %s %v", out, err)
	}
	must("page", "project", "restore", "health", "--revision", "1", "--expected-revision", "2")
	current := must("page", "project", "get", "health")
	var draft struct {
		Revision int                  `json:"revision"`
		Project  *pages.SourceProject `json:"project"`
	}
	if err := json.Unmarshal([]byte(current), &draft); err != nil || draft.Revision != 3 {
		t.Fatalf("restored draft %s %v", current, err)
	}
	expected, _ := p.Digest()
	actual, _ := draft.Project.Digest()
	if actual != expected {
		t.Fatal("CLI restore lost source bytes")
	}
	if image == "" {
		// SKIP-WAIVER: the publish-and-restart half needs a real, pinned
		// pages-build image (a tag is refused), which only the mandatory
		// `pages-apps` CI lane supplies via scripts/test-pages-apps.sh — and
		// that lane fails if this subtest is skipped, so the coverage is
		// enforced where the dependency exists. Same guard as #2472's, and on
		// the same precedent no tracking issue: there is nothing to come back
		// and fix. Skip, not log-and-return: a bare return left this half
		// unexecuted while the parent reported PASS and the run reported zero
		// skips, and this branch's own handover quoted that zero as proof.
		t.Skip("Docker publication path requires PAGES_TEST_BUILD_IMAGE (a pinned image ID or repo@sha256, not a tag)")
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(must("page", "project", "build", "health", "--revision", "3")), &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(40 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		var preview struct {
			Build struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"build"`
		}
		if err := json.Unmarshal([]byte(must("page", "project", "preview", "health")), &preview); err != nil {
			t.Fatal(err)
		}
		if preview.Build.State == "ready" {
			ready = true
			break
		}
		if preview.Build.State == "failed" {
			t.Fatal(preview.Build.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("CLI build timeout")
	}
	must("page", "project", "check", "health", "--build", job.ID, "--revision", "3")
	if _, err := run("page", "project", "publish", "health", "--build", job.ID, "--revision", "3", "--expected-publication", "0"); err == nil {
		t.Fatal("publication accepted without code review")
	}
	must("page", "project", "publish", "health", "--build", job.ID, "--revision", "3", "--expected-publication", "0", "--reviewed-code")
	must("page", "project", "publish", "health", "--build", job.ID, "--revision", "3", "--expected-publication", "0", "--reviewed-code")
	must("page", "project", "rollback", "health", "--publication", "1", "--expected-publication", "1", "--reviewed-code")
	var app struct {
		Publication struct {
			Version int `json:"version"`
		} `json:"publication"`
	}
	if err := json.Unmarshal([]byte(must("page", "project", "application", "health")), &app); err != nil || app.Publication.Version != 2 {
		t.Fatalf("CLI rollback failed: %v", err)
	}

	exercisePageRevalidation(t, db, srv.URL, ws)
	exerciseOperationalPageResults(t, must)
	var receipt struct {
		PendingID string `json:"pending_id"`
	}
	dispatch := []string{"page", "action", "health/mysql", "refresh", "--publication", "2", "--idempotency-key", "cli-once"}
	if err := json.Unmarshal([]byte(must(dispatch...)), &receipt); err != nil || receipt.PendingID == "" {
		t.Fatalf("action receipt: %v", err)
	}
	var replay struct {
		PendingID string `json:"pending_id"`
	}
	if err := json.Unmarshal([]byte(must(dispatch...)), &replay); err != nil || replay.PendingID != receipt.PendingID {
		t.Fatalf("action replay: %v", err)
	}
	status := must("page", "project", "action-status", "health", receipt.PendingID)
	if !strings.Contains(status, "pending_status") {
		t.Fatal(status)
	}
	if out, err := run("page", "action", "health/mysql", "refresh", "--publication", "1", "--idempotency-key", "stale-click"); err == nil || !strings.Contains(out, "changed") {
		t.Fatalf("stale action accepted: %s %v", out, err)
	}
	must("page", "project", "publications", "health")
	if _, err := run("page", "project", "unpublish", "health", "--expected-publication", "2"); err == nil {
		t.Fatal("withdrawal accepted without confirmation")
	}
	must("page", "project", "unpublish", "health", "--expected-publication", "2", "--yes")
	must("page", "project", "unpublish", "health", "--expected-publication", "2", "--yes")
	if out := must("page", "project", "application", "health"); !strings.Contains(out, `"publication":null`) && !strings.Contains(out, `"publication": null`) {
		t.Fatal("CLI withdrawal left application live", out)
	}
	srv.Close()
	t.Run("restart", func(t *testing.T) {
		exercisePageDaemonRestart(t, db, binary, projectsPath, ws, token, job.ID)
	})

}

// exercisePageReviewBeforeBuild reads the review snapshot for a draft that has
// never been built, and drives the publish fence flags as far as they go
// without a build worker: the CLI-side validation, and the server's shape
// check on a value passed explicitly, which proves it was sent verbatim
// rather than replaced by the snapshot's.
func exercisePageReviewBeforeBuild(t *testing.T, run func(...string) (string, error), must func(...string) string) {
	t.Helper()
	var snapshot struct {
		Candidate *struct {
			Revision int64 `json:"revision"`
			Build    *struct {
				State string `json:"state"`
			} `json:"build"`
		} `json:"candidate"`
		Baseline struct {
			DefinitionDigest string `json:"definition_digest"`
			SourceAvailable  bool   `json:"source_available"`
		} `json:"baseline"`
		Routines []struct {
			Routine       string  `json:"routine"`
			CurrentDigest *string `json:"current_digest"`
			InCandidate   bool    `json:"in_candidate"`
		} `json:"routines"`
		Blockers []struct {
			Code string `json:"code"`
		} `json:"blockers"`
		InitialPublication bool `json:"initial_publication"`
	}
	out := must("page", "project", "review", "health")
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("review: %v\n%s", err, out)
	}
	if snapshot.Candidate == nil || snapshot.Candidate.Revision != 2 || snapshot.Candidate.Build != nil {
		t.Fatalf("review candidate before a build: %s", out)
	}
	if !snapshot.InitialPublication || snapshot.Baseline.SourceAvailable || len(snapshot.Baseline.DefinitionDigest) != 64 {
		t.Fatalf("review baseline before a publication: %s", out)
	}
	codes := map[string]bool{}
	for _, blocker := range snapshot.Blockers {
		codes[blocker.Code] = true
	}
	if !codes["build_missing"] || codes["no_candidate"] || codes["baseline_unavailable"] {
		t.Fatalf("review blockers before a build: %s", out)
	}
	var routine string
	for _, row := range snapshot.Routines {
		if row.Routine == "health-check" && row.InCandidate && row.CurrentDigest != nil {
			routine = *row.CurrentDigest
		}
	}
	if len(routine) != 64 {
		t.Fatalf("review does not fence the declared call routine: %s", out)
	}

	// Refused locally: a fence pair that is not routine=sha256, and one
	// routine named twice.
	if out, err := run("page", "project", "publish", "health", "--build", "b", "--revision", "2", "--expected-publication", "0", "--reviewed-code", "--expected-routine-digest", "health-check"); err == nil || !strings.Contains(out, "expects routine=sha256") {
		t.Fatalf("malformed fence pair accepted: %v %s", err, out)
	}
	if out, err := run("page", "project", "publish", "health", "--build", "b", "--revision", "2", "--expected-publication", "0", "--reviewed-code", "--expected-routine-digest", "health-check="+routine, "--expected-routine-digest", "health-check="+routine); err == nil || !strings.Contains(out, "twice") {
		t.Fatalf("duplicate fence pair accepted: %v %s", err, out)
	}
	// Both flags explicit: no snapshot is read, and the value goes out as
	// typed — the server's shape check is the proof, since the snapshot's own
	// digest would have passed it.
	out, err := run("page", "project", "publish", "health", "--build", "b", "--revision", "2", "--expected-publication", "0", "--reviewed-code", "--expected-definition-digest", "not-a-digest", "--expected-routine-digest", "health-check="+routine)
	if err == nil || !strings.Contains(out, "64-character lowercase sha256") || strings.Contains(out, "Fencing") {
		t.Fatalf("explicit definition digest was not sent verbatim: %v %s", err, out)
	}
	// Only the definition explicit: the routine half is still read from the
	// snapshot and announced.
	out, err = run("page", "project", "publish", "health", "--build", "b", "--revision", "2", "--expected-publication", "0", "--reviewed-code", "--expected-definition-digest", snapshot.Baseline.DefinitionDigest)
	if err == nil || !strings.Contains(out, "Fencing on routine health-check sha256 "+routine) {
		t.Fatalf("omitted routine fence was not resolved from the snapshot: %v %s", err, out)
	}
	// Neither explicit: both halves come from the snapshot and are printed
	// before the request. The server's refusal is then PAST the fence shape
	// check (never a 400): 503 with no build worker configured, 404 for the
	// unknown build when the pages-apps lane supplies an image.
	out, err = run("page", "project", "publish", "health", "--build", "b", "--revision", "2", "--expected-publication", "0", "--reviewed-code")
	if err == nil || !strings.Contains(out, "Fencing this publication on definition sha256 "+snapshot.Baseline.DefinitionDigest) || !strings.Contains(out, "Fencing on routine health-check sha256 "+routine) {
		t.Fatalf("auto-fence publish: %v %s", err, out)
	}
	if strings.Contains(out, `"status": 400`) || !(strings.Contains(out, "build worker") || strings.Contains(out, "Page build not found")) {
		t.Fatalf("auto-fence publish was refused before or beyond the fence: %s", out)
	}
	if strings.Contains(out, "acknowledge-unavailable-baseline") {
		t.Fatalf("an initial publication has no baseline to acknowledge: %s", out)
	}
}

// exercisePageExportImportRoundTrip exports a Page that carries a source draft
// and imports the bundle under another slug: the v2 format, the source bytes
// and the declared call action survive the trip, and what arrives is an inert
// draft rather than a publication.
func exercisePageExportImportRoundTrip(t *testing.T, must func(...string) string, source *pages.SourceProject) {
	t.Helper()
	exported := must("page", "export", "health")
	var bundle pages.TransferBundle
	if err := json.Unmarshal([]byte(exported), &bundle); err != nil {
		t.Fatalf("export: %v\n%s", err, exported)
	}
	if bundle.Format != pages.TransferV2 || bundle.Project == nil {
		t.Fatalf("export of a Page with source is not a v2 bundle: %s", exported)
	}
	file := filepath.Join(t.TempDir(), "health.page.yaml")
	if err := os.WriteFile(file, []byte(exported), 0600); err != nil {
		t.Fatal(err)
	}
	must("page", "import", file, "--slug", "health-copy", "--bind", "crew/ops=crew/ops", "--bind", "routine/health-check=routine/health-check")
	copied := must("page", "project", "get", "health-copy")
	var draft struct {
		Revision   int64                `json:"revision"`
		Project    *pages.SourceProject `json:"project"`
		Definition *pages.Document      `json:"definition"`
	}
	if err := json.Unmarshal([]byte(copied), &draft); err != nil || draft.Revision != 1 || draft.Project == nil || draft.Definition == nil {
		t.Fatalf("imported draft: %v\n%s", err, copied)
	}
	want, _ := source.Digest()
	got, _ := draft.Project.Digest()
	if got != want {
		t.Fatal("import lost source bytes")
	}
	// The call action travels in the DRAFT definition. The live page arrives
	// inert — no actions, nothing published — until somebody reviews and
	// publishes the draft here.
	if len(draft.Definition.Spec.Panels) != 1 || len(draft.Definition.Spec.Panels[0].Actions) != 1 || draft.Definition.Spec.Panels[0].Actions[0].Routine != "health-check" {
		t.Fatalf("import lost the call action: %s", copied)
	}
	if out := must("page", "actions", "health-copy/mysql"); !strings.Contains(out, `"actions": []`) && !strings.Contains(out, `"actions":[]`) {
		t.Fatalf("import armed the live page: %s", out)
	}
	if out := must("page", "project", "application", "health-copy"); !strings.Contains(out, `"publication":null`) && !strings.Contains(out, `"publication": null`) {
		t.Fatalf("import published an application: %s", out)
	}
	must("page", "delete", "health-copy", "--yes")
}
