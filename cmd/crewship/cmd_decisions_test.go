package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/decisions"
)

func TestDecisionsDryRun(t *testing.T) {
	guardCLIState(t)
	flagFormat = "yaml" // Dry runs remain reusable provider JSON documents.
	for _, tc := range []struct {
		mode, input, provider, model string
		n                            int
	}{
		{"triage", "Oprav navigaci na mobilu.", "typesafe", "jev-1.13.0", 4},
		{"rerank", `{"query":"q","candidates":[{"id":"a","text":"passage"}]}`, "openrouter", "typesafe/jev-1.13", 1},
		{"evaluate", `{"state":"x","questions":{"q":{"type":"noul","instructions":"Relevant?"}}}`, "typesafe", "jev-1.13.0", 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cmd := newDecisionsCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetIn(strings.NewReader(tc.input))
			cmd.SetArgs([]string{tc.mode, "--dry-run", "--provider", tc.provider})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var req decisions.Request
			if err := json.Unmarshal(out.Bytes(), &req); err != nil {
				t.Fatal(err)
			}
			if req.Model != tc.model || len(req.Questions) != tc.n {
				t.Fatalf("wrong recipe %+v", req)
			}
		})
	}
}
func TestDecisionsMissingKeyAndInvalidInput(t *testing.T) {
	guardCLIState(t)
	t.Setenv("TYPESAFE_API_KEY", "")
	for _, tc := range []struct {
		args        []string
		input, want string
	}{
		{[]string{"triage"}, "sample", "TYPESAFE_API_KEY"},
		{[]string{"triage", "--dry-run", "--threshold", "NaN"}, "sample", "threshold"},
		{[]string{"triage", "--dry-run", "--timeout", "0s"}, "sample", "timeout"},
		{[]string{"evaluate", "--dry-run"}, `{"state":null}`, "state"},
		{[]string{"rerank", "--dry-run"}, `{"query":"x","candidates":[]}`, "candidates"},
	} {
		cmd := newDecisionsCommand()
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetIn(strings.NewReader(tc.input))
		cmd.SetArgs(tc.args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q, got %v", tc.want, err)
		}
	}
}

func TestDecisionsResultFormats(t *testing.T) {
	guardCLIState(t)
	tokens := int64(12)
	score := 0.75
	result := decisions.Response{Model: "test", Usage: decisions.Usage{InputTokens: &tokens}, Answers: map[string]decisions.Answer{"q": {Type: "score", Score: &score}}}
	for _, format := range []string{"table", "json", "yaml", "ndjson", "quiet"} {
		t.Run(format, func(t *testing.T) {
			flagFormat = format
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := writeDecisionResult(cmd, result); err != nil {
				t.Fatal(err)
			}
			if format == "quiet" {
				if out.Len() != 0 {
					t.Fatal("quiet emitted a result")
				}
				return
			}
			var got struct {
				Usage struct {
					InputTokens int64 `json:"input_tokens" yaml:"input_tokens"`
				} `json:"usage" yaml:"usage"`
				Answers map[string]struct {
					Score float64 `json:"score" yaml:"score"`
				} `json:"answers" yaml:"answers"`
			}
			var err error
			if format == "yaml" {
				err = yaml.Unmarshal(out.Bytes(), &got)
			} else {
				err = json.Unmarshal(out.Bytes(), &got)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Usage.InputTokens != tokens || got.Answers["q"].Score != score {
				t.Fatalf("lost typed values: %s", out.String())
			}
			if format == "ndjson" && strings.Count(out.String(), "\n") != 1 {
				t.Fatal("NDJSON must be one record")
			}
		})
	}
}
