package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// --- unit: fetchModels decodes the API payload ---

func TestFetchModels_Decodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("provider"); got != "anthropic" {
			t.Errorf("provider = %q", got)
		}
		_, _ = w.Write([]byte(`{"provider":"ANTHROPIC","source":"live","models":[
			{"id":"claude-opus-4-8","display_name":"Claude Opus 4.8","provider":"anthropic"},
			{"id":"claude-haiku-4-5","provider":"anthropic"}
		]}`))
	}))
	defer srv.Close()

	res, err := fetchModels(cli.NewClient(srv.URL, "t", "c000000000000000000000ws"), "anthropic")
	if err != nil {
		t.Fatalf("fetchModels: %v", err)
	}
	if res.Source != "live" || res.Provider != "ANTHROPIC" {
		t.Errorf("res = %+v", res)
	}
	if len(res.Models) != 2 || res.Models[0].ID != "claude-opus-4-8" {
		t.Errorf("models = %+v", res.Models)
	}
}

func TestFetchModels_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"provider query parameter is required"}`))
	}))
	defer srv.Close()

	if _, err := fetchModels(cli.NewClient(srv.URL, "t", "c000000000000000000000ws"), "x"); err == nil {
		t.Fatalf("expected error on 400")
	}
}

func TestFetchModels_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := fetchModels(cli.NewClient(url, "t", "c000000000000000000000ws"), "anthropic"); err == nil {
		t.Fatalf("expected transport error against closed server")
	}
}

func TestFetchModels_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	if _, err := fetchModels(cli.NewClient(srv.URL, "t", "c000000000000000000000ws"), "anthropic"); err == nil {
		t.Fatalf("expected decode error on malformed body")
	}
}

func TestPrintModelList_NoPanic(t *testing.T) {
	// Exercises the table formatter on both display-name and bare-id rows.
	printModelList(&modelListResult{
		Provider: "OPENAI",
		Source:   "curated",
		Models: []modelInfoRow{
			{ID: "gpt-4o", DisplayName: "GPT-4o", Provider: "openai"},
			{ID: "o3", Provider: "openai"},
		},
	})
}

// --- acceptance: drive the BUILT crewship binary against a stub server ---

var (
	modelBinOnce sync.Once
	modelBinPath string
	modelBinErr  error
)

// buildCrewshipBinary compiles the CLI once per test binary and caches the
// path. Acceptance tests drive this binary (not hand-rolled HTTP) so the
// `crewship model list` contract is exercised end-to-end: flag parsing,
// config resolution, the HTTP call, and stdout rendering.
func buildCrewshipBinary(t *testing.T) string {
	t.Helper()
	modelBinOnce.Do(func() {
		// Build into a stable temp dir (not t.TempDir, which is cleaned per
		// test) so the once-built binary survives across all tests in this pkg.
		buildDir, err := os.MkdirTemp("", "crewship-bin-")
		if err != nil {
			modelBinErr = err
			return
		}
		out := filepath.Join(buildDir, "crewship")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Env = os.Environ()
		if combined, err := cmd.CombinedOutput(); err != nil {
			modelBinErr = err
			t.Logf("build output: %s", combined)
			return
		}
		modelBinPath = out
	})
	if modelBinErr != nil {
		t.Fatalf("build crewship binary: %v", modelBinErr)
	}
	return modelBinPath
}

func TestAcceptance_ModelList(t *testing.T) {
	bin := buildCrewshipBinary(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("provider"); got != "anthropic" {
			t.Errorf("provider query = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q, want exact \"Bearer test-token\"", got)
		}
		_, _ = w.Write([]byte(`{"provider":"ANTHROPIC","source":"live","models":[
			{"id":"claude-opus-4-8","display_name":"Claude Opus 4.8","provider":"anthropic"},
			{"id":"claude-sonnet-4-6","provider":"anthropic"}
		]}`))
	}))
	defer srv.Close()

	// Minimal config file: token + workspace satisfy requireAuth/requireWorkspace.
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "model", "list", "--provider", "anthropic", "--server", srv.URL, "--no-color")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	got := string(out)
	for _, want := range []string{"claude-opus-4-8", "claude-sonnet-4-6", "source=live"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestAcceptance_ModelList_JSON(t *testing.T) {
	bin := buildCrewshipBinary(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"provider":"OPENAI","source":"curated","models":[{"id":"gpt-4o","provider":"openai"}]}`))
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "model", "list", "--provider", "openai", "--server", srv.URL, "--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, `"source": "curated"`) && !strings.Contains(got, `"source":"curated"`) {
		t.Errorf("json output missing source=curated:\n%s", got)
	}
	if !strings.Contains(got, "gpt-4o") {
		t.Errorf("json output missing gpt-4o:\n%s", got)
	}
}

func TestAcceptance_ModelList_MissingProvider(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "model", "list", "--server", "http://127.0.0.1:0")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for missing --provider; output: %s", out)
	}
	if !strings.Contains(string(out), "--provider is required") {
		t.Errorf("output missing required-provider error:\n%s", out)
	}
}
