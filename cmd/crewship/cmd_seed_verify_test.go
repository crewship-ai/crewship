package main

// Unit coverage for `crewship seed verify` against a stub server. The stub
// plays the workspace (crews, files, runs, inbox, page) AND GitHub (the
// independent ground-truth read), so every branch of the reconciliation is
// exercised without a container or a token.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

var verifyNow = time.Date(2026, 9, 4, 7, 0, 0, 0, time.UTC)

// verifyStub wires a stub server that answers every call a full ci-watch
// verification makes. Knobs let a test break one link at a time.
type verifyStub struct {
	s          *clitest.StubServer
	runsPosted int64
	// probe JSON the run steps return; the report run carries the same
	// probe output plus the triage text.
	probeJSON string
	triage    string
	fileDrift bool
	panelRun  string
}

func newVerifyStub(t *testing.T) *verifyStub {
	t.Helper()
	vs := &verifyStub{s: clitest.NewStubServer(), panelRun: "run-report"}
	t.Cleanup(vs.s.Close)
	t.Setenv("CREWSHIP_SEED_VERIFY_GITHUB_API", vs.s.URL())
	t.Setenv("SEED_GITHUB_TOKEN", "ghp_test_token_0000000000000000000000")

	vs.probeJSON = `{"repo":"crewship-ai/crewship","checked":3,"red":1,"stale":1,"running":0,"ok":1,"wake":true,` +
		`"detail":[{"workflow":"Security","status":"RED"},{"workflow":"CodeQL","status":"STALE"},{"workflow":"Nightly","status":"OK"}],` +
		`"panel":{"red_state":"critical","red_label":"1 failed: Security","stale_state":"warning","stale_label":"1 not running: CodeQL","checked_label":"3 scheduled workflows checked"}}`
	vs.triage = "## Nightly CI — 2 to handle\n\n### Regressions (act now)\n- **Security** — govulncheck found a High. [run](https://github.com/x/y/actions/runs/1)\n\n" +
		"### Stale (workflow not running)\n- **CodeQL** — disabled_inactivity, last run 5 days ago.\n\nCOUNTS: regressions=1 flaky=0 infra=0 stale=1 unclear=0"

	s := vs.s
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "crew-ops", "slug": "ops"}, {"id": "crew-quality", "slug": "quality"}, {"id": "crew-eng", "slug": "engineering"},
	}))
	for _, crew := range []string{"crew-ops", "crew-quality", "crew-eng"} {
		s.OnGet("/api/v1/crews/"+crew+"/files/download", func(r *http.Request, _ []byte) (int, []byte, string) {
			dest := r.URL.Query().Get("path")
			for _, p := range seeddata.Packs {
				for _, f := range p.Files {
					if f.Dest != dest {
						continue
					}
					b, err := seeddata.PackFileContent(f.Src)
					if err != nil {
						return 500, nil, ""
					}
					if vs.fileDrift {
						b = append(b, []byte("\n# edited on the crew\n")...)
					}
					return 200, b, "application/octet-stream"
				}
			}
			return 404, []byte(`{"error":"not found"}`), "application/json"
		})
	}
	for _, slug := range []string{"ci-probe", "ci-nightly-triage", "docs-drift-audit", "site-replica-audit"} {
		slug := slug
		s.OnPost("/api/v1/workspaces/"+covWSCli7+"/pipelines/"+slug+"/run", func(_ *http.Request, body []byte) (int, []byte, string) {
			atomic.AddInt64(&vs.runsPosted, 1)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if _, ok := req["delay_seconds"]; !ok {
				return 400, []byte(`{"error":"verify must park the run (delay_seconds) — a synchronous run would hold the HTTP request"}`), "application/json"
			}
			// The real deferred path answers a pending id, never a run id:
			// the run is minted when the dispatcher fires. verify has to
			// find it through the routine's run records.
			b, _ := json.Marshal(map[string]any{"status": "scheduled", "pending_id": "pend-" + slug, "fire_at": time.Now().Add(time.Second).Format(time.RFC3339Nano)})
			return 202, b, "application/json"
		})
		s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipelines/"+slug+"/run-records", func(_ *http.Request, _ []byte) (int, []byte, string) {
			id := "run-" + strings.TrimSuffix(strings.TrimPrefix(slug, "ci-"), "-audit")
			if slug == "ci-nightly-triage" {
				id = "run-report"
			}
			b, _ := json.Marshal([]map[string]any{
				{"id": "run-yesterday", "status": "completed", "started_at": time.Now().Add(-24 * time.Hour).Format(time.RFC3339Nano)},
				{"id": id, "status": "running", "started_at": time.Now().Format(time.RFC3339Nano)},
			})
			return 200, b, "application/json"
		})
	}
	runDetail := func(id string, outputs map[string]any) (int, []byte, string) {
		b, _ := json.Marshal(map[string]any{
			"id": id, "workspace_id": covWSCli7, "status": "completed",
			"inputs": map[string]any{"repo": "crewship-ai/crewship"}, "step_outputs": outputs,
		})
		return 200, b, "application/json"
	}
	s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipeline-runs/run-probe", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return runDetail("run-probe", map[string]any{"probe": vs.probeJSON, "wake": "true"})
	})
	s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipeline-runs/run-report", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return runDetail("run-report", map[string]any{"probe": vs.probeJSON, "triage": vs.triage})
	})

	// GitHub: one red (Security), one stale (CodeQL), one fresh green.
	s.OnGet("/repos/crewship-ai/crewship/actions/workflows", clitest.JSONResponse(200, map[string]any{
		"workflows": []map[string]any{
			{"id": 1, "name": "Security", "state": "active"},
			{"id": 2, "name": "CodeQL", "state": "active"},
			{"id": 3, "name": "Nightly", "state": "active"},
			{"id": 4, "name": "Disabled", "state": "disabled_manually"},
		},
	}))
	s.OnGet("/repos/crewship-ai/crewship/actions/workflows/1/runs", clitest.JSONResponse(200, map[string]any{
		"workflow_runs": []map[string]any{{"conclusion": "failure", "created_at": verifyNow.Add(-4 * time.Hour).Format(time.RFC3339)}},
	}))
	s.OnGet("/repos/crewship-ai/crewship/actions/workflows/2/runs", clitest.JSONResponse(200, map[string]any{
		"workflow_runs": []map[string]any{{"conclusion": "success", "created_at": verifyNow.Add(-130 * time.Hour).Format(time.RFC3339)}},
	}))
	s.OnGet("/repos/crewship-ai/crewship/actions/workflows/3/runs", clitest.JSONResponse(200, map[string]any{
		"workflow_runs": []map[string]any{
			{"conclusion": "failure", "created_at": verifyNow.Add(-28 * time.Hour).Format(time.RFC3339)},
			{"conclusion": "success", "created_at": verifyNow.Add(-4 * time.Hour).Format(time.RFC3339)},
		},
	}))

	s.OnGet("/api/v1/inbox", func(_ *http.Request, _ []byte) (int, []byte, string) {
		b, _ := json.Marshal(map[string]any{"rows": []map[string]any{
			{"title": "Older thing", "created_at": verifyNow.Add(-48 * time.Hour).Format(time.RFC3339)},
			{"title": "Nightly CI — something to handle", "created_at": verifyNow.Add(30 * time.Second).Format(time.RFC3339)},
		}})
		return 200, b, "application/json"
	})
	s.OnGet("/api/v1/pages/ci-watch", func(_ *http.Request, _ []byte) (int, []byte, string) {
		b, _ := json.Marshal(map[string]any{"slug": "ci-watch", "panels": []map[string]any{
			{"id": "status", "provenance": map[string]any{"run_id": vs.panelRun, "producer": "routine/ci-nightly-triage"}},
			{"id": "summary", "provenance": map[string]any{"run_id": vs.panelRun, "producer": "routine/ci-nightly-triage"}},
		}})
		return 200, b, "application/json"
	})
	return vs
}

func resultOf(checks []verifyCheck, pack, step string) (verifyCheck, bool) {
	for _, c := range checks {
		if c.Pack == pack && c.Step == step {
			return c, true
		}
	}
	return verifyCheck{}, false
}

func expectResult(t *testing.T, checks []verifyCheck, pack, step, want string) verifyCheck {
	t.Helper()
	c, ok := resultOf(checks, pack, step)
	if !ok {
		t.Fatalf("no %s/%s row in %+v", pack, step, checks)
	}
	if c.Result != want {
		t.Fatalf("%s/%s = %s (%s), want %s", pack, step, c.Result, c.Detail, want)
	}
	return c
}

func verifyOpts(packs ...string) verifyOptions {
	return verifyOptions{packs: packs, timeout: 30 * time.Second, now: func() time.Time { return verifyNow }}
}

func TestSeedVerify_CIWatchAllGreen(t *testing.T) {
	vs := newVerifyStub(t)
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	for _, step := range []string{"crew", "files", "env", "probe", "report", "inbox", "page"} {
		expectResult(t, checks, "ci-watch", step, verifyPass)
	}
	probe := expectResult(t, checks, "ci-watch", "probe", verifyPass)
	if !strings.Contains(probe.Detail, "GitHub red=1 stale=1 of 3") {
		t.Errorf("probe detail should show the independent read: %q", probe.Detail)
	}
	if got := atomic.LoadInt64(&vs.runsPosted); got != 2 {
		t.Errorf("runs posted = %d, want 2 (probe + report)", got)
	}
	if err := printVerify(checks, false); err != nil {
		t.Errorf("printVerify on a green table: %v", err)
	}
}

func TestSeedVerify_ProbeDisagreesWithGitHub(t *testing.T) {
	vs := newVerifyStub(t)
	// The probe claims a calm night; GitHub says otherwise.
	vs.probeJSON = strings.Replace(vs.probeJSON, `"red":1,"stale":1`, `"red":0,"stale":0`, 1)
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	c := expectResult(t, checks, "ci-watch", "probe", verifyFail)
	if !strings.Contains(c.Detail, "disagree") {
		t.Errorf("detail should name the disagreement: %q", c.Detail)
	}
	if _, ok := resultOf(checks, "ci-watch", "report"); ok {
		t.Error("a wrong probe must stop the pack — the report run would be meaningless")
	}
	if got := atomic.LoadInt64(&vs.runsPosted); got != 1 {
		t.Errorf("runs posted = %d, want 1 (probe only)", got)
	}
	if err := printVerify(checks, false); err == nil {
		t.Error("printVerify must return an error when a check failed")
	}
}

func TestSeedVerify_AgentCountsDoNotReconcile(t *testing.T) {
	vs := newVerifyStub(t)
	vs.triage = strings.Replace(vs.triage, "COUNTS: regressions=1 flaky=0 infra=0 stale=1 unclear=0",
		"COUNTS: regressions=1 flaky=1 infra=0 stale=1 unclear=0", 1)
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	c := expectResult(t, checks, "ci-watch", "report", verifyFail)
	if !strings.Contains(c.Detail, "do not reconcile") {
		t.Errorf("detail: %q", c.Detail)
	}
}

func TestSeedVerify_MissingCountsLineFails(t *testing.T) {
	vs := newVerifyStub(t)
	vs.triage = "## Nightly CI\n\nEverything looks fine, I think."
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	expectResult(t, checks, "ci-watch", "report", verifyFail)
}

func TestSeedVerify_LeakedTokenFailsTheReport(t *testing.T) {
	vs := newVerifyStub(t)
	vs.triage = "Authorization: Bearer ghp_abcdefghijklmnopqrstuvwxyz0123456789\n" + vs.triage
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	c := expectResult(t, checks, "ci-watch", "report", verifyFail)
	if !strings.Contains(c.Detail, "secret") {
		t.Errorf("detail: %q", c.Detail)
	}
	if strings.Contains(c.Detail, "ghp_abcdefghijklmnopqrstuvwxyz") {
		t.Error("the verdict row must not echo the token's bytes")
	}
	if !strings.Contains(c.Detail, "token") {
		t.Errorf("the verdict row should name the kind: %q", c.Detail)
	}
}

func TestSeedVerify_DriftedFileFails(t *testing.T) {
	vs := newVerifyStub(t)
	vs.fileDrift = true
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	c := expectResult(t, checks, "ci-watch", "files", verifyFail)
	if !strings.Contains(c.Detail, "drifted") {
		t.Errorf("detail: %q", c.Detail)
	}
}

func TestSeedVerify_PanelFromAnotherRunFails(t *testing.T) {
	vs := newVerifyStub(t)
	vs.panelRun = "run-yesterday"
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	c := expectResult(t, checks, "ci-watch", "page", verifyFail)
	if !strings.Contains(c.Detail, "run-yesterday") {
		t.Errorf("detail should name the stale run: %q", c.Detail)
	}
}

func TestSeedVerify_MissingRequirementSkipsAndNeverRuns(t *testing.T) {
	vs := newVerifyStub(t)
	t.Setenv("SEED_GITHUB_TOKEN", "")
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	expectResult(t, checks, "ci-watch", "files", verifyPass)
	c := expectResult(t, checks, "ci-watch", "env", verifySkip)
	if !strings.Contains(c.Detail, "SEED_GITHUB_TOKEN") {
		t.Errorf("the skip must name the missing variable: %q", c.Detail)
	}
	if got := atomic.LoadInt64(&vs.runsPosted); got != 0 {
		t.Errorf("runs posted = %d, want 0 — a pack that cannot run must not be run", got)
	}
	if err := printVerify(checks, false); err != nil {
		t.Errorf("a skip is not a failure by default: %v", err)
	}
	if err := printVerify(checks, true); err == nil {
		t.Error("--strict must turn a skip into a failure")
	}
}

func TestSeedVerify_SkipReportStopsAfterTheProbe(t *testing.T) {
	vs := newVerifyStub(t)
	opts := verifyOpts("ci-watch")
	opts.skipReport = true
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), opts)
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	expectResult(t, checks, "ci-watch", "probe", verifyPass)
	expectResult(t, checks, "ci-watch", "report", verifySkip)
	if got := atomic.LoadInt64(&vs.runsPosted); got != 1 {
		t.Errorf("runs posted = %d, want 1", got)
	}
}

func TestSeedVerify_SiteReplicaNotBuiltIsASkipNotAPass(t *testing.T) {
	vs := newVerifyStub(t)
	check := `{"ok":false,"built":false,"passed":0,"failed":1,"checks":[{"name":"index.html exists","ok":false}],"panel":{"state":"warning","label":"no replica built yet","verdict":"NOT BUILT"}}`
	vs.s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipeline-runs/run-site-replica", func(_ *http.Request, _ []byte) (int, []byte, string) {
		b, _ := json.Marshal(map[string]any{"id": "run-site-replica", "status": "completed", "step_outputs": map[string]any{"check": check}})
		return 200, b, "application/json"
	})
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("site-replica"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	expectResult(t, checks, "site-replica", "env", verifyPass) // no requirements
	c := expectResult(t, checks, "site-replica", "report", verifySkip)
	if !strings.Contains(c.Detail, "not built") && !strings.Contains(c.Detail, "no replica") {
		t.Errorf("detail: %q", c.Detail)
	}
}

func TestSeedVerify_SiteReplicaBuiltAndFailingIsAFail(t *testing.T) {
	vs := newVerifyStub(t)
	check := `{"ok":false,"built":true,"passed":6,"failed":2,"checks":[{"name":"viewport meta","ok":false},{"name":"no external runtime requests","ok":false},{"name":"title present","ok":true}]}`
	vs.s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipeline-runs/run-site-replica", func(_ *http.Request, _ []byte) (int, []byte, string) {
		b, _ := json.Marshal(map[string]any{"id": "run-site-replica", "status": "completed", "step_outputs": map[string]any{"check": check}})
		return 200, b, "application/json"
	})
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("site-replica"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	c := expectResult(t, checks, "site-replica", "report", verifyFail)
	if !strings.Contains(c.Detail, "viewport meta") {
		t.Errorf("detail should name the failed checks: %q", c.Detail)
	}
}

func TestSeedVerify_DocsDriftReconcilesAndFactChecksOnlyWithACheckout(t *testing.T) {
	vs := newVerifyStub(t)
	scan := `{"repo":"crewship-ai/crewship","sha":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef","pairs":2,"total_candidates":3,"phantoms":1,"gaps":2,"results":[],"panel":{"state":"warning","label":"1 phantom(s) and 2 gap(s) across 2 pairs","sha_label":"x"}}`
	review := "## Docs drift — 1 confirmed of 3 candidates\n\n### docs/manifest/crew.md\n- **`files`** — the crew field table omits it. Code: `internal/manifest/kinds/crew.go:120`. Docs: `docs/manifest/crew.md:40`.\n\n### Rejected (why these are not findings)\n- **`agent_run`** — enum value, not a key\n- **`config_hash`** — database column\n\nCOUNTS: candidates=3 confirmed=1 rejected=2 truncated=0"
	vs.s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipeline-runs/run-docs-drift", func(_ *http.Request, _ []byte) (int, []byte, string) {
		b, _ := json.Marshal(map[string]any{"id": "run-docs-drift", "status": "completed", "step_outputs": map[string]any{"scan": scan, "review": review}})
		return 200, b, "application/json"
	})
	vs.s.OnGet("/api/v1/pages/docs-drift", func(_ *http.Request, _ []byte) (int, []byte, string) {
		b, _ := json.Marshal(map[string]any{"panels": []map[string]any{{"id": "status", "provenance": map[string]any{"run_id": "run-docs-drift"}}, {"id": "summary", "provenance": map[string]any{"run_id": "run-docs-drift"}}}})
		return 200, b, "application/json"
	})
	vs.s.OnGet("/api/v1/inbox", func(_ *http.Request, _ []byte) (int, []byte, string) {
		b, _ := json.Marshal(map[string]any{"rows": []map[string]any{{"title": "Docs drift — weekly audit", "created_at": verifyNow.Add(time.Minute).Format(time.RFC3339)}}})
		return 200, b, "application/json"
	})
	opts := verifyOpts("docs-drift")
	opts.repoDir = t.TempDir() // not a checkout that has the commit → the fact-check cannot run
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), opts)
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	c := expectResult(t, checks, "docs-drift", "report", verifyPass)
	if !strings.Contains(c.Detail, "candidates=3 confirmed=1 rejected=2") {
		t.Errorf("detail: %q", c.Detail)
	}
	fc := expectResult(t, checks, "docs-drift", "fact-check", verifySkip)
	if !strings.Contains(fc.Detail, "does not contain commit") {
		t.Errorf("an unrelated checkout must be a skip with the reason, not a false FAIL: %q", fc.Detail)
	}
	expectResult(t, checks, "docs-drift", "inbox", verifyPass)
	expectResult(t, checks, "docs-drift", "page", verifyPass)
}

// With a checkout that has the scanned commit, every cited path is checked
// against it: a real path passes, an invented one fails the fact-check.
func TestSeedVerify_DocsDriftFactCheckAgainstARealCheckout(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, "docs", "manifest"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "manifest", "crew.md"), []byte("# crew\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "docs")
	sha := run("rev-parse", "HEAD")

	for _, tc := range []struct {
		name, cite, want, detail string
	}{
		{"real path passes", "`docs/manifest/crew.md:1`", verifyPass, "1 cited path(s) exist"},
		{"invented path fails", "`docs/manifest/crew.md:1`. Code: `internal/made/up.go:3`", verifyFail, "internal/made/up.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vs := newVerifyStub(t)
			scan := `{"repo":"x/y","sha":"` + sha + `","pairs":1,"total_candidates":1,"results":[],"panel":{"state":"ok","label":"l","sha_label":"s"}}`
			review := "## Docs drift — 1 confirmed of 1 candidates\n\n### docs/manifest/crew.md\n- **`files`** — missing. Docs: " + tc.cite + ".\n\nCOUNTS: candidates=1 confirmed=1 rejected=0 truncated=0"
			vs.s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipeline-runs/run-docs-drift", func(_ *http.Request, _ []byte) (int, []byte, string) {
				b, _ := json.Marshal(map[string]any{"id": "run-docs-drift", "status": "completed", "step_outputs": map[string]any{"scan": scan, "review": review}})
				return 200, b, "application/json"
			})
			vs.s.OnGet("/api/v1/pages/docs-drift", clitest.JSONResponse(200, map[string]any{"panels": []map[string]any{{"id": "status", "provenance": map[string]any{"run_id": "run-docs-drift"}}}}))
			vs.s.OnGet("/api/v1/inbox", clitest.JSONResponse(200, map[string]any{"rows": []map[string]any{{"title": "Docs drift — weekly audit", "created_at": verifyNow.Add(time.Minute).Format(time.RFC3339)}}}))
			opts := verifyOpts("docs-drift")
			opts.repoDir = repo
			checks, err := seedVerify(context.Background(), covStubClient(vs.s), opts)
			if err != nil {
				t.Fatalf("seedVerify: %v", err)
			}
			fc := expectResult(t, checks, "docs-drift", "fact-check", tc.want)
			if !strings.Contains(fc.Detail, tc.detail) {
				t.Errorf("detail: %q, want it to mention %q", fc.Detail, tc.detail)
			}
		})
	}
}

func TestSeedVerify_UnknownPackIsAnError(t *testing.T) {
	vs := newVerifyStub(t)
	if _, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("nope")); err == nil || !strings.Contains(err.Error(), "unknown pack") {
		t.Fatalf("err = %v, want unknown pack", err)
	}
}

func TestSeedVerify_MissingCrewFailsWithoutRunningAnything(t *testing.T) {
	vs := newVerifyStub(t)
	vs.s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	checks, err := seedVerify(context.Background(), covStubClient(vs.s), verifyOpts("ci-watch"))
	if err != nil {
		t.Fatalf("seedVerify: %v", err)
	}
	c := expectResult(t, checks, "ci-watch", "crew", verifyFail)
	if !strings.Contains(c.Detail, "crewship seed") {
		t.Errorf("the fix must be named: %q", c.Detail)
	}
	if got := atomic.LoadInt64(&vs.runsPosted); got != 0 {
		t.Errorf("runs posted = %d, want 0", got)
	}
}

func TestParseCountsLine(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]int
		ok   bool
	}{
		{"COUNTS: regressions=1 flaky=2 stale=0", map[string]int{"regressions": 1, "flaky": 2, "stale": 0}, true},
		{"first\nCOUNTS: a=1\nlater\nCOUNTS: a=5 b=6\n", map[string]int{"a": 5, "b": 6}, true},
		{"  COUNTS: Candidates = 12  ", map[string]int{"candidates": 12}, true},
		{"no line here", nil, false},
		{"COUNTS: nothing numeric", nil, false},
	}
	for _, c := range cases {
		got, ok := parseCountsLine(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok=%v want %v", c.in, ok, c.ok)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%q: %s=%d want %d", c.in, k, got[k], v)
			}
		}
	}
}

func TestLeakedSecret(t *testing.T) {
	for _, s := range []string{
		"token ghp_abcdefghijklmnopqrstuvwxyz012345",
		"github_pat_11ABCDEFG0123456789abcdefghijkl",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"https://x-access-token:ghp_x@github.com/o/r.git",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123",
		"ANTHROPIC_API_KEY=sk-ant-whatever",
	} {
		if leakedSecret(s) == "" {
			t.Errorf("not flagged: %q", s)
		}
	}
	for _, s := range []string{
		"the GH_TOKEN slot is bound", "Bearer token expected in the header", "run 12345", "sk-ant is the prefix",
	} {
		if got := leakedSecret(s); got != "" {
			t.Errorf("false positive %q in %q", got, s)
		}
	}
}

func TestCitedPaths(t *testing.T) {
	text := "Code: `internal/manifest/kinds/crew.go:120`. Docs: `docs/manifest/crew.md:40`. Again `docs/manifest/crew.md:41`. Not a path `foo:1` and `internal/x.go` without a line."
	got := citedPaths(text)
	want := []string{"docs/manifest/crew.md", "internal/manifest/kinds/crew.go"}
	if len(got) != len(want) {
		t.Fatalf("got %+v want %v", got, want)
	}
	for i := range want {
		if got[i].path != want[i] {
			t.Errorf("[%d] = %q want %q", i, got[i].path, want[i])
		}
	}
}

func TestVerifyGitHubTruth_ClassifiesLikeTheProbe(t *testing.T) {
	vs := newVerifyStub(t)
	truth, err := githubScheduledTruth(context.Background(), "crewship-ai/crewship", "tok", 48*time.Hour, verifyNow)
	if err != nil {
		t.Fatalf("truth: %v", err)
	}
	if truth.checked != 3 || truth.red != 1 || truth.stale != 1 {
		t.Errorf("truth = %+v, want checked=3 red=1 stale=1 (disabled workflow ignored, yesterday's failure overridden by today's success)", truth)
	}
	_ = vs
	if _, err := githubScheduledTruth(context.Background(), "x/y", "", time.Hour, verifyNow); err == nil {
		t.Error("no token must be an error, not an empty truth")
	}
}

func TestSeedVerifyCmd_IsRegisteredUnderSeed(t *testing.T) {
	found := false
	for _, c := range seedCmd.Commands() {
		if c.Name() == "verify" {
			found = true
		}
	}
	if !found {
		t.Fatal("`crewship seed verify` is not registered — every pack claim in the docs depends on it")
	}
	for _, f := range []string{"pack", "timeout", "strict", "skip-report", "repo-dir"} {
		if seedVerifyCmd.Flags().Lookup(f) == nil {
			t.Errorf("flag --%s missing", f)
		}
	}
	_ = fmt.Sprint
}
