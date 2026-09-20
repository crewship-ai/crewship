package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/decisions"
)

func TestDecisionsDryRun(t *testing.T) {
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
