package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	seedKeeperConfigPath = "/api/v1/admin/keeper/config"
	seedKeeperGovPath    = "/api/v1/admin/keeper/governance"
	seedKeeperJudgePath  = "/api/v1/admin/keeper/judge/test"
)

// seedKeeperCfgResponse is the real shape GET/PUT /admin/keeper/config answers
// with: every field is a {value,source,editable} triple, not a bare scalar.
const seedKeeperCfgResponse = `{"enabled":{"value":true,"source":"override","editable":true},"judge_endpoint_url":{"value":"http://air.local:11434","source":"override","editable":true},"judge_model":{"value":"qwen3.5:9b","source":"override","editable":true},"judge_configured":true}`

// judgeOK / judgeBroken are the two answers the four-stage judge check gives,
// and they are the whole decision this seed step makes.
const (
	judgeOK     = `{"ok":true,"endpoint":"http://air.local:11434","model":"qwen3.5:9b","stages":[{"name":"reach","ok":true},{"name":"model","ok":true},{"name":"verdict","ok":true},{"name":"budget","ok":true}]}`
	judgeBroken = `{"ok":false,"endpoint":"http://air.local:11434","model":"qwen3.5:9b","stages":[{"name":"reach","ok":false,"detail":"connection refused"}]}`
)

// A seeded Keeper is a fail-closed gate on every credential request. Turning it
// on when the judge cannot answer does not give the demo a watchdog — it gives
// it an instance where every agent is denied every credential, with the denial
// looking exactly like a considered verdict. So the seed enables it only after
// the judge has actually returned a verdict, and says so plainly when it has not.
func TestSeedKeeper_EnablesOnlyWhenTheJudgeAnswers(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPut(seedKeeperConfigPath, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(seedKeeperCfgResponse), "application/json"
	})
	s.OnPut(seedKeeperGovPath, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"enabled":true,"configured":true}`), "application/json"
	})
	s.OnPost(seedKeeperJudgePath, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(judgeOK), "application/json"
	})
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		return seedKeeper(context.Background(), client, "http://air.local:11434", "qwen3.5:9b")
	})
	if err != nil {
		t.Fatalf("seedKeeper: %v", err)
	}

	// The judge endpoint + model must be pinned BEFORE the check, or the check
	// measures whatever the server happened to boot with.
	cfg := s.CallsFor("PUT", seedKeeperConfigPath)
	if len(cfg) != 1 {
		t.Fatalf("config PUT calls = %d, want 1", len(cfg))
	}
	if !strings.Contains(string(cfg[0].Body), `"judge_endpoint_url":"http://air.local:11434"`) ||
		!strings.Contains(string(cfg[0].Body), `"judge_model":"qwen3.5:9b"`) {
		t.Errorf("config body did not pin the judge: %s", cfg[0].Body)
	}

	gov := s.CallsFor("PUT", seedKeeperGovPath)
	if len(gov) != 1 {
		t.Fatalf("governance PUT calls = %d, want 1", len(gov))
	}
	if !strings.Contains(string(gov[0].Body), `"enabled":true`) {
		t.Errorf("watchdog not enabled: %s", gov[0].Body)
	}
	if !strings.Contains(string(gov[0].Body), `"gov_model_provider":"ollama"`) ||
		!strings.Contains(string(gov[0].Body), `"gov_model_id":"qwen3.5:9b"`) {
		t.Errorf("governance model not set: %s", gov[0].Body)
	}
	if len(s.CallsFor("POST", seedKeeperJudgePath)) != 1 {
		t.Error("the judge check did not run")
	}
	if !strings.Contains(stderr, "Keeper") {
		t.Errorf("nothing reported to the operator: %q", stderr)
	}
}

func TestSeedKeeper_LeavesWatchdogOffWhenTheJudgeIsUnreachable(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPut(seedKeeperConfigPath, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(seedKeeperCfgResponse), "application/json"
	})
	s.OnPut(seedKeeperGovPath, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"enabled":true}`), "application/json"
	})
	s.OnPost(seedKeeperJudgePath, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(judgeBroken), "application/json"
	})
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		return seedKeeper(context.Background(), client, "http://air.local:11434", "qwen3.5:9b")
	})
	// A judge that cannot answer is a seed note, not a seed failure — the rest
	// of the fixture is still worth landing.
	if err != nil {
		t.Fatalf("seedKeeper should not fail the seed: %v", err)
	}
	if calls := s.CallsFor("PUT", seedKeeperGovPath); len(calls) != 0 {
		t.Errorf("watchdog was enabled against a judge that cannot answer: %s", calls[0].Body)
	}
	if !strings.Contains(stderr, "connection refused") {
		t.Errorf("the failing stage was not reported: %q", stderr)
	}
	// The operator must be told how to finish the job by hand.
	if !strings.Contains(stderr, "keeper judge test") {
		t.Errorf("no recovery hint: %q", stderr)
	}
}

// An instance with no Ollama configured anywhere must not have the seed invent
// an endpoint for it: the step is skipped whole, and the demo comes up exactly
// as it did before this step existed.
func TestSeedKeeper_SkippedWhenNoEndpointIsConfigured(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		return seedKeeper(context.Background(), client, "", "qwen3.5:9b")
	})
	if err != nil {
		t.Fatalf("seedKeeper: %v", err)
	}
	if n := len(s.Calls()); n != 0 {
		t.Errorf("made %d calls with no endpoint configured, want 0", n)
	}
	if !strings.Contains(stderr, "KEEPER_OLLAMA_URL") {
		t.Errorf("skip reason did not name the variable that turns it on: %q", stderr)
	}
}
