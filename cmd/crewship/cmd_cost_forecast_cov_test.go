package main

// Coverage tests for cmd_cost_forecast.go — rate-table loading, row math,
// rendering, usage extraction, and both RunE modes.

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestLoadProviderRates(t *testing.T) {
	t.Run("default table without override", func(t *testing.T) {
		t.Setenv("CREWSHIP_FORECAST_RATES", "")
		got := loadProviderRates()
		if len(got) != len(providerRates) || got[0].Name != providerRates[0].Name {
			t.Errorf("got %+v, want hardcoded defaults", got)
		}
	})

	t.Run("valid override", func(t *testing.T) {
		t.Setenv("CREWSHIP_FORECAST_RATES", "Custom A,2.5,10; Custom B , 1 , 4 ;")
		got := loadProviderRates()
		if len(got) != 2 {
			t.Fatalf("got %+v", got)
		}
		if got[0].Name != "Custom A" || got[0].InputUSDPerMTok != 2.5 || got[0].OutputUSDPerMTok != 10 {
			t.Errorf("entry 0 = %+v", got[0])
		}
		if got[1].Name != "Custom B" || got[1].InputUSDPerMTok != 1 || got[1].OutputUSDPerMTok != 4 {
			t.Errorf("entry 1 = %+v", got[1])
		}
	})

	t.Run("malformed entry falls back to defaults", func(t *testing.T) {
		t.Setenv("CREWSHIP_FORECAST_RATES", "OnlyTwoFields,3")
		got := loadProviderRates()
		if len(got) != len(providerRates) {
			t.Errorf("expected fallback to defaults, got %+v", got)
		}
	})

	t.Run("bad float falls back to defaults", func(t *testing.T) {
		t.Setenv("CREWSHIP_FORECAST_RATES", "X,abc,5")
		got := loadProviderRates()
		if len(got) != len(providerRates) {
			t.Errorf("expected fallback, got %+v", got)
		}
	})

	t.Run("only-separators env falls back", func(t *testing.T) {
		t.Setenv("CREWSHIP_FORECAST_RATES", " ; ; ")
		got := loadProviderRates()
		if len(got) != len(providerRates) {
			t.Errorf("expected fallback, got %+v", got)
		}
	})
}

func TestBuildForecastRows(t *testing.T) {
	t.Setenv("CREWSHIP_FORECAST_RATES", "Flat,1,2")
	rows := buildForecastRows(1_000_000, 500_000)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if math.Abs(r.InputUSD-1.0) > 1e-9 || math.Abs(r.OutputUSD-1.0) > 1e-9 || math.Abs(r.TotalUSD-2.0) > 1e-9 {
		t.Errorf("math wrong: %+v", r)
	}
	if r.InTokens != 1_000_000 || r.OutTokens != 500_000 || r.Model != "Flat" {
		t.Errorf("metadata wrong: %+v", r)
	}
}

func TestStructuredForecast(t *testing.T) {
	rows := []forecastRow{{Model: "M"}}
	m := structuredForecast("prompt", 10, 20, rows)
	if m["source"] != "prompt" || m["input_tokens"] != 10 || m["output_tokens"] != 20 {
		t.Errorf("map = %+v", m)
	}
	if got, ok := m["rows"].([]forecastRow); !ok || len(got) != 1 {
		t.Errorf("rows field = %#v", m["rows"])
	}
}

func TestRenderForecast(t *testing.T) {
	rows := []forecastRow{
		{Model: "M1", InputUSD: 0.5, OutputUSD: 1.5, TotalUSD: 2.0, InTokens: 100, OutTokens: 200},
	}

	t.Run("json", func(t *testing.T) {
		out, err := covCaptureStdoutCli7(t, func() error {
			return renderForecast(cli.NewFormatter("json"), "prompt", 100, 200, rows)
		})
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("json output undecodable: %v\n%s", err, out)
		}
		if decoded["source"] != "prompt" {
			t.Errorf("decoded = %+v", decoded)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		out, err := covCaptureStdoutCli7(t, func() error {
			return renderForecast(cli.NewFormatter("yaml"), "prompt", 100, 200, rows)
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "source: prompt") {
			t.Errorf("yaml output = %q", out)
		}
	})

	t.Run("ndjson emits one row per model", func(t *testing.T) {
		out, err := covCaptureStdoutCli7(t, func() error {
			return renderForecast(cli.NewFormatter("ndjson"), "prompt", 100, 200, rows)
		})
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 1 || !strings.Contains(lines[0], `"model":"M1"`) {
			t.Errorf("ndjson output = %q", out)
		}
	})

	t.Run("table", func(t *testing.T) {
		out, err := covCaptureStdoutCli7(t, func() error {
			return renderForecast(cli.NewFormatter("table"), "prompt", 100, 200, rows)
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Cost forecast", "M1", "$0.5000", "$2.0000", "input ≈ 100 tok"} {
			if !strings.Contains(out, want) {
				t.Errorf("table missing %q:\n%s", want, out)
			}
		}
	})
}

func TestExtractUsageTokens(t *testing.T) {
	cases := []struct {
		name   string
		md     map[string]any
		in     int
		out    int
		wantOK bool
	}{
		{"nil metadata", nil, 0, 0, false},
		{"nested usage", map[string]any{"usage": map[string]any{"input_tokens": 100.0, "output_tokens": 50.0}}, 100, 50, true},
		{"flat fields", map[string]any{"input_tokens": 70.0, "output_tokens": 30.0}, 70, 30, true},
		{"nested wins, flat fills gaps", map[string]any{
			"usage":         map[string]any{"input_tokens": 100.0},
			"output_tokens": 25.0,
		}, 100, 25, true},
		{"no usage anywhere", map[string]any{"foo": "bar"}, 0, 0, false},
		{"non-numeric values ignored", map[string]any{"input_tokens": "many"}, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out, ok := extractUsageTokens(tc.md)
			if in != tc.in || out != tc.out || ok != tc.wantOK {
				t.Errorf("got (%d,%d,%v), want (%d,%d,%v)", in, out, ok, tc.in, tc.out, tc.wantOK)
			}
		})
	}
}

// covResetForecastFlags restores the forecast command's flags after a test.
func covResetForecastFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = costForecastCmd.Flags().Set("prompt", "")
		_ = costForecastCmd.Flags().Set("from-history", "")
		_ = costForecastCmd.Flags().Set("output-ratio", "2.0")
	})
}

func TestCostForecastRunE_FlagValidation(t *testing.T) {
	saveCLIState(t)
	covResetForecastFlags(t)

	_ = costForecastCmd.Flags().Set("prompt", "")
	_ = costForecastCmd.Flags().Set("from-history", "")
	if err := costForecastCmd.RunE(costForecastCmd, nil); err == nil || !strings.Contains(err.Error(), "provide --prompt or --from-history") {
		t.Errorf("neither flag: %v", err)
	}

	_ = costForecastCmd.Flags().Set("prompt", "x")
	_ = costForecastCmd.Flags().Set("from-history", "viktor")
	if err := costForecastCmd.RunE(costForecastCmd, nil); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("both flags: %v", err)
	}
}

func TestCostForecastRunE_PromptMode(t *testing.T) {
	saveCLIState(t)
	covResetForecastFlags(t)
	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })
	t.Setenv("CREWSHIP_FORECAST_RATES", "Flat,1,2")

	// ~40 chars → 10 tokens at 4 chars/token, output ratio 3 → 30 tokens.
	_ = costForecastCmd.Flags().Set("prompt", strings.Repeat("abcd", 10))
	_ = costForecastCmd.Flags().Set("from-history", "")
	_ = costForecastCmd.Flags().Set("output-ratio", "3")

	out, err := covCaptureStdoutCli7(t, func() error {
		return costForecastCmd.RunE(costForecastCmd, nil)
	})
	if err != nil {
		t.Fatalf("prompt mode: %v", err)
	}
	var decoded struct {
		Source       string        `json:"source"`
		InputTokens  int           `json:"input_tokens"`
		OutputTokens int           `json:"output_tokens"`
		Rows         []forecastRow `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if decoded.Source != "prompt" {
		t.Errorf("source = %q", decoded.Source)
	}
	if decoded.InputTokens <= 0 {
		t.Errorf("input tokens = %d", decoded.InputTokens)
	}
	if decoded.OutputTokens != decoded.InputTokens*3 {
		t.Errorf("output ratio not applied: in=%d out=%d", decoded.InputTokens, decoded.OutputTokens)
	}
	if len(decoded.Rows) != 1 || decoded.Rows[0].Model != "Flat" {
		t.Errorf("rows = %+v", decoded.Rows)
	}
}

func TestCostForecastRunE_HistoryMode(t *testing.T) {
	agentID := "cagent123456789012345678"

	newStub := func(t *testing.T, runs []map[string]any) *clitest.StubServer {
		t.Helper()
		s := clitest.NewStubServer()
		t.Cleanup(s.Close)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": agentID, "slug": "viktor"},
		}))
		s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": runs}))
		return s
	}

	t.Run("averages recorded usage", func(t *testing.T) {
		s := newStub(t, []map[string]any{
			{"id": "r1", "metadata": map[string]any{"usage": map[string]any{"input_tokens": 100.0, "output_tokens": 10.0}}},
			{"id": "r2", "metadata": map[string]any{"usage": map[string]any{"input_tokens": 300.0, "output_tokens": 30.0}}},
			{"id": "r3", "metadata": map[string]any{}}, // skipped: no usage
		})
		covSetupCLI(t, s)
		covResetForecastFlags(t)
		origFormat := flagFormat
		flagFormat = "json"
		t.Cleanup(func() { flagFormat = origFormat })
		t.Setenv("CREWSHIP_FORECAST_RATES", "Flat,1,2")

		_ = costForecastCmd.Flags().Set("prompt", "")
		_ = costForecastCmd.Flags().Set("from-history", "viktor")

		out, err := covCaptureStdoutCli7(t, func() error {
			return costForecastCmd.RunE(costForecastCmd, nil)
		})
		if err != nil {
			t.Fatalf("history mode: %v", err)
		}
		var decoded struct {
			Source       string `json:"source"`
			InputTokens  int    `json:"input_tokens"`
			OutputTokens int    `json:"output_tokens"`
		}
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("decode: %v\n%s", err, out)
		}
		if decoded.InputTokens != 200 || decoded.OutputTokens != 20 {
			t.Errorf("averages = in %d out %d, want 200/20", decoded.InputTokens, decoded.OutputTokens)
		}
		if !strings.Contains(decoded.Source, "avg of 2 runs") {
			t.Errorf("source = %q", decoded.Source)
		}
		// The runs query must carry the resolved agent id and the limit.
		gets := s.CallsFor("GET", "/api/v1/runs")
		if len(gets) != 1 || !strings.Contains(gets[0].Query, "agent_id="+agentID) || !strings.Contains(gets[0].Query, "limit=20") {
			t.Errorf("runs query = %+v", gets)
		}
	})

	t.Run("no past runs", func(t *testing.T) {
		s := newStub(t, []map[string]any{})
		covSetupCLI(t, s)
		covResetForecastFlags(t)
		_ = costForecastCmd.Flags().Set("prompt", "")
		_ = costForecastCmd.Flags().Set("from-history", "viktor")
		err := costForecastCmd.RunE(costForecastCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "no past runs") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("runs without usage", func(t *testing.T) {
		s := newStub(t, []map[string]any{
			{"id": "r1", "metadata": map[string]any{}},
			{"id": "r2"},
		})
		covSetupCLI(t, s)
		covResetForecastFlags(t)
		_ = costForecastCmd.Flags().Set("prompt", "")
		_ = costForecastCmd.Flags().Set("from-history", "viktor")
		err := costForecastCmd.RunE(costForecastCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "none of the last 2 runs had recorded token usage") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("unknown agent slug", func(t *testing.T) {
		s := newStub(t, nil)
		covSetupCLI(t, s)
		covResetForecastFlags(t)
		_ = costForecastCmd.Flags().Set("prompt", "")
		_ = costForecastCmd.Flags().Set("from-history", "ghost")
		err := costForecastCmd.RunE(costForecastCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
			t.Fatalf("got %v", err)
		}
	})
}
