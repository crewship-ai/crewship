package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covCompareRunPath(slug string) string {
	return "/api/v1/workspaces/" + covWSCli9 + "/pipelines/" + slug + "/run"
}

// covCompareStub serves the /run endpoint, dispatching the response on
// the tier_override in the request body so side A and side B can get
// different results from the same route.
func covCompareStub(t *testing.T, s *clitest.StubServer, slug string, byTier map[string]clitest.Handler) {
	t.Helper()
	s.OnPost(covCompareRunPath(slug), func(r *http.Request, body []byte) (int, []byte, string) {
		var req struct {
			Tier string `json:"tier_override"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("run request body not JSON: %v", err)
		}
		h, ok := byTier[req.Tier]
		if !ok {
			t.Errorf("unexpected tier_override %q", req.Tier)
			return 500, []byte(`{"error":"unexpected tier"}`), ""
		}
		return h(r, body)
	})
}

func TestRunEvalCompare_TableDiverge(t *testing.T) {
	s := covStubCli9(t)
	covCompareStub(t, s, "eval-x", map[string]clitest.Handler{
		"fast": clitest.JSONResponse(200, map[string]any{
			"run_id": "r-fast", "status": "COMPLETED", "output": "hello from fast",
			"duration_ms": 120, "cost_usd": 0.01,
		}),
		"smart": clitest.JSONResponse(200, map[string]any{
			"run_id": "r-smart", "status": "FAILED", "output": "",
			"duration_ms": 300, "cost_usd": 0.05,
			"failed_at_step": "s2", "error_message": "gate unsatisfied",
		}),
	})
	covSetFlagCli9(t, evalCompareCmd, "inputs", `{"k":"v"}`)

	var buf bytes.Buffer
	evalCompareCmd.SetOut(&buf)
	t.Cleanup(func() { evalCompareCmd.SetOut(nil) })

	if err := runEvalCompare(evalCompareCmd, []string{"eval-x"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Scenario: eval-x", "Verdict:  DIVERGE-A-PASS", "COMPLETED", "FAILED", "B error @ s2: gate unsatisfied", "Side A output", "hello from fast"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}

	// Both sides must have received the authored inputs.
	calls := s.CallsFor("POST", covCompareRunPath("eval-x"))
	if len(calls) != 2 {
		t.Fatalf("expected 2 run POSTs, got %d", len(calls))
	}
	for i, c := range calls {
		if !strings.Contains(string(c.Body), `"inputs":{"k":"v"}`) {
			t.Errorf("call %d missing inputs: %s", i, c.Body)
		}
	}
}

func TestRunEvalCompare_Markdown(t *testing.T) {
	s := covStubCli9(t)
	covCompareStub(t, s, "eval-md", map[string]clitest.Handler{
		"fast": clitest.JSONResponse(200, map[string]any{
			"run_id": "ra", "status": "COMPLETED", "output": "out-a",
		}),
		"smart": clitest.JSONResponse(200, map[string]any{
			"run_id": "rb", "status": "COMPLETED", "output": "out-b",
			"failed_at_step": "sx", "error_message": "soft warning",
		}),
	})
	flagFormat = "markdown"

	var buf bytes.Buffer
	evalCompareCmd.SetOut(&buf)
	t.Cleanup(func() { evalCompareCmd.SetOut(nil) })

	if err := runEvalCompare(evalCompareCmd, []string{"eval-md"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"## Eval compare — `eval-md` (AGREE-PASS)", "| Side | Tier | Status |", "### Side A output", "### Side B output", "### Errors", "soft warning"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown output missing %q:\n%s", want, out)
		}
	}
}

func TestRunEvalCompare_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	covCompareStub(t, s, "eval-json", map[string]clitest.Handler{
		"fast":  clitest.JSONResponse(200, map[string]any{"run_id": "ra", "status": "COMPLETED"}),
		"smart": clitest.JSONResponse(200, map[string]any{"run_id": "rb", "status": "DEDUPED"}),
	})
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := runEvalCompare(evalCompareCmd, []string{"eval-json"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"agreement": "AGREE-PASS"`) && !strings.Contains(out, `"agreement":"AGREE-PASS"`) {
		t.Errorf("json output missing agreement verdict:\n%s", out)
	}
}

func TestRunEvalCompare_HTTPErrorSideIsAmbiguous(t *testing.T) {
	s := covStubCli9(t)
	covCompareStub(t, s, "eval-amb", map[string]clitest.Handler{
		"fast":  clitest.ErrorResponse(500, "boom"),
		"smart": clitest.JSONResponse(200, map[string]any{"run_id": "rb", "status": "COMPLETED"}),
	})

	var buf bytes.Buffer
	evalCompareCmd.SetOut(&buf)
	t.Cleanup(func() { evalCompareCmd.SetOut(nil) })

	if err := runEvalCompare(evalCompareCmd, []string{"eval-amb"}); err != nil {
		t.Fatalf("HTTP-level side failure should not crash the comparison: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "HTTP_500") || !strings.Contains(out, "AMBIGUOUS") {
		t.Errorf("expected HTTP_500 status and AMBIGUOUS verdict:\n%s", out)
	}
}

func TestRunEvalCompare_DecodeErrorSideA(t *testing.T) {
	s := covStubCli9(t)
	covCompareStub(t, s, "eval-dec", map[string]clitest.Handler{
		"fast":  clitest.TextResponse(200, "{not json"),
		"smart": clitest.JSONResponse(200, map[string]any{"status": "COMPLETED"}),
	})

	err := runEvalCompare(evalCompareCmd, []string{"eval-dec"})
	if err == nil || !strings.Contains(err.Error(), "run side A (tier=fast)") {
		t.Errorf("expected side-A decode error; got %v", err)
	}
}

func TestRunEvalCompare_InputValidation(t *testing.T) {
	t.Run("same tier", func(t *testing.T) {
		covStubCli9(t)
		covSetFlagCli9(t, evalCompareCmd, "tier-a", "smart")
		covSetFlagCli9(t, evalCompareCmd, "tier-b", "smart")
		err := runEvalCompare(evalCompareCmd, []string{"x"})
		if err == nil || !strings.Contains(err.Error(), "must differ") {
			t.Errorf("expected tier-equality error; got %v", err)
		}
	})
	t.Run("bad inputs json", func(t *testing.T) {
		covStubCli9(t)
		covSetFlagCli9(t, evalCompareCmd, "inputs", "{broken")
		err := runEvalCompare(evalCompareCmd, []string{"x"})
		if err == nil || !strings.Contains(err.Error(), "parse --inputs JSON") {
			t.Errorf("expected inputs parse error; got %v", err)
		}
	})
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := runEvalCompare(evalCompareCmd, []string{"x"}); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
	t.Run("no workspace", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{Token: "tok"}
		err := runEvalCompare(evalCompareCmd, []string{"x"})
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("expected workspace error; got %v", err)
		}
	})
}

type covFailingPoster struct{ err error }

func (p covFailingPoster) Post(string, any) (*http.Response, error) { return nil, p.err }

func TestRunOneSide_TransportError(t *testing.T) {
	t.Parallel()
	side, err := runOneSide(covFailingPoster{err: errors.New("dial tcp: refused")}, "ws", "slug", "fast", nil)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("expected transport error; got %v", err)
	}
	if side.Tier != "fast" {
		t.Errorf("tier should be preserved on error, got %q", side.Tier)
	}
}

func TestRunOneSide_EmptyTierOmitsOverride(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost(covCompareRunPath("eval-noover"), clitest.JSONResponse(200, map[string]any{"status": "COMPLETED"}))

	client := cli.NewClient(s.URL(), "tok", covWSCli9)
	side, err := runOneSide(client, covWSCli9, "eval-noover", "", nil)
	if err != nil {
		t.Fatalf("runOneSide: %v", err)
	}
	if side.Status != "COMPLETED" {
		t.Errorf("status = %q, want COMPLETED", side.Status)
	}
	calls := s.CallsFor("POST", covCompareRunPath("eval-noover"))
	if len(calls) != 1 {
		t.Fatalf("expected one POST, got %d", len(calls))
	}
	if strings.Contains(string(calls[0].Body), "tier_override") {
		t.Errorf("empty tier must omit tier_override: %s", calls[0].Body)
	}
	if !strings.Contains(string(calls[0].Body), `"inputs":{}`) {
		t.Errorf("nil inputs must serialise as empty object: %s", calls[0].Body)
	}
}

func TestLabelTier(t *testing.T) {
	t.Parallel()
	if got := labelTier(""); got != "(authored)" {
		t.Errorf("empty tier label = %q, want (authored)", got)
	}
	if got := labelTier("smart"); got != "smart" {
		t.Errorf("named tier label = %q, want smart", got)
	}
}
