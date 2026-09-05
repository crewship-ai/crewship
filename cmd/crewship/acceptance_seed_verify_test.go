package main

// Acceptance for `crewship seed verify`, driven through the BUILT BINARY.
//
// The project rule ("every API endpoint gets a CLI command, and its
// acceptance test drives the CLI binary") applies to the verifier as much as
// to any command: the unit tests above exercise seedVerify's branches, this
// one proves the shipped binary wires the subcommand, reads its config, hits
// the routes in the shapes the server answers, and prints the JSON a script
// would consume. The stub plays the workspace and GitHub; the GitHub base is
// overridden through CREWSHIP_SEED_VERIFY_GITHUB_API.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
)

func seedVerifyAcceptanceStub(t *testing.T) *httptest.Server {
	t.Helper()
	now := time.Now().UTC()
	probe := `{"repo":"crewship-ai/crewship","checked":2,"red":1,"stale":0,"running":0,"ok":1,"wake":true,"detail":[],` +
		`"panel":{"red_state":"critical","red_label":"1 failed: Security","stale_state":"ok","stale_label":"every scheduled workflow ran on time","checked_label":"2 scheduled workflows checked"}}`
	triage := "## Nightly CI — 1 to handle\n\n### Regressions (act now)\n- **Security** — govulncheck High. [run](https://github.com/x/y/actions/runs/9)\n\nCOUNTS: regressions=1 flaky=0 infra=0 stale=0 unclear=0"
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/api/v1/crews":
			write(w, 200, []map[string]string{{"id": "crew_ops", "slug": "ops"}})
		case r.Method == http.MethodGet && p == "/api/v1/crews/crew_ops/files/download":
			for _, f := range seeddata.Packs[0].Files {
				if f.Dest == r.URL.Query().Get("path") {
					b, _ := seeddata.PackFileContent(f.Src)
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write(b)
					return
				}
			}
			w.WriteHeader(404)
		case r.Method == http.MethodPost && strings.HasPrefix(p, "/api/v1/workspaces/ws_test/pipelines/") && strings.HasSuffix(p, "/run"):
			// The deferred path's real answer: a pending id, no run id.
			write(w, 202, map[string]any{"status": "scheduled", "pending_id": "pend_1", "fire_at": now.Add(time.Second).Format(time.RFC3339Nano)})
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/workspaces/ws_test/pipelines/") && strings.HasSuffix(p, "/run-records"):
			slug := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/workspaces/ws_test/pipelines/"), "/run-records")
			write(w, 200, []map[string]any{{"id": "run_" + slug, "status": "running", "started_at": time.Now().UTC().Format(time.RFC3339Nano)}})
		case r.Method == http.MethodGet && p == "/api/v1/workspaces/ws_test/pipeline-runs/run_ci-probe":
			write(w, 200, map[string]any{"id": "run_ci-probe", "status": "completed", "inputs": map[string]any{"repo": "crewship-ai/crewship"},
				"step_outputs": map[string]any{"probe": probe, "wake": "true"}})
		case r.Method == http.MethodGet && p == "/api/v1/workspaces/ws_test/pipeline-runs/run_ci-nightly-triage":
			write(w, 200, map[string]any{"id": "run_ci-nightly-triage", "status": "completed",
				"step_outputs": map[string]any{"probe": probe, "triage": triage}})
		case r.Method == http.MethodGet && p == "/repos/crewship-ai/crewship/actions/workflows":
			write(w, 200, map[string]any{"workflows": []map[string]any{
				{"id": 1, "name": "Security", "state": "active"}, {"id": 2, "name": "Nightly", "state": "active"}}})
		case r.Method == http.MethodGet && p == "/repos/crewship-ai/crewship/actions/workflows/1/runs":
			write(w, 200, map[string]any{"workflow_runs": []map[string]any{{"conclusion": "failure", "created_at": now.Add(-3 * time.Hour).Format(time.RFC3339)}}})
		case r.Method == http.MethodGet && p == "/repos/crewship-ai/crewship/actions/workflows/2/runs":
			write(w, 200, map[string]any{"workflow_runs": []map[string]any{{"conclusion": "success", "created_at": now.Add(-2 * time.Hour).Format(time.RFC3339)}}})
		case r.Method == http.MethodGet && p == "/api/v1/inbox":
			write(w, 200, map[string]any{"rows": []map[string]any{{"title": "Nightly CI — something to handle", "created_at": now.Add(time.Minute).Format(time.RFC3339)}}})
		case r.Method == http.MethodGet && p == "/api/v1/pages/ci-watch":
			write(w, 200, map[string]any{"panels": []map[string]any{
				{"id": "status", "provenance": map[string]any{"run_id": "run_ci-nightly-triage"}}, {"id": "summary", "provenance": map[string]any{"run_id": "run_ci-nightly-triage"}}}})
		default:
			write(w, 404, map[string]string{"detail": "no stub for " + r.Method + " " + p})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runSeedVerifyCLI(t *testing.T, serverURL string, env []string, args ...string) (string, error) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Dir = t.TempDir() // not a git checkout: no .env.local, no fact-check
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptance_SeedVerify_JSONVerdictThroughTheBinary(t *testing.T) {
	srv := seedVerifyAcceptanceStub(t)
	out, err := runSeedVerifyCLI(t, srv.URL, []string{
		"SEED_GITHUB_TOKEN=ghp_acceptance_00000000000000000000",
		"CREWSHIP_SEED_VERIFY_GITHUB_API=" + srv.URL,
	}, "seed", "verify", "--pack", "ci-watch", "--timeout", "30s", "--format", "json")
	if err != nil {
		t.Fatalf("seed verify exited non-zero: %v\n%s", err, out)
	}
	// stdout carries the JSON; stderr carries the progress lines. Both are
	// in `out`, so find the object.
	start := strings.Index(out, "{\n")
	if start < 0 {
		t.Fatalf("no JSON object in output:\n%s", out)
	}
	var verdict struct {
		Checks []verifyCheck `json:"checks"`
		Failed int           `json:"failed"`
		Passed int           `json:"passed"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &verdict); err != nil {
		t.Fatalf("json: %v\n%s", err, out[start:])
	}
	if verdict.Failed != 0 {
		t.Errorf("failed = %d\n%s", verdict.Failed, out)
	}
	steps := map[string]string{}
	for _, c := range verdict.Checks {
		steps[c.Step] = c.Result
	}
	for _, step := range []string{"crew", "files", "env", "probe", "report", "inbox", "page"} {
		if steps[step] != verifyPass {
			t.Errorf("step %s = %q, want PASS\n%s", step, steps[step], out)
		}
	}
}

func TestAcceptance_SeedVerify_SkippedPackExitsZeroUnlessStrict(t *testing.T) {
	srv := seedVerifyAcceptanceStub(t)
	out, err := runSeedVerifyCLI(t, srv.URL, []string{"SEED_GITHUB_TOKEN="}, "seed", "verify", "--pack", "ci-watch")
	if err != nil {
		t.Fatalf("a skipped pack must not fail the command: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SKIP") || !strings.Contains(out, "SEED_GITHUB_TOKEN") {
		t.Errorf("the table must show the skip and its reason:\n%s", out)
	}
	out, err = runSeedVerifyCLI(t, srv.URL, []string{"SEED_GITHUB_TOKEN="}, "seed", "verify", "--pack", "ci-watch", "--strict")
	if err == nil {
		t.Fatalf("--strict must exit non-zero on a skip:\n%s", out)
	}
	if code := exitCodeOf(t, err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestAcceptance_SeedVerify_UnknownPackIsAUsageError(t *testing.T) {
	srv := seedVerifyAcceptanceStub(t)
	out, err := runSeedVerifyCLI(t, srv.URL, nil, "seed", "verify", "--pack", "no-such-pack")
	if err == nil || !strings.Contains(out, "unknown pack") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}
