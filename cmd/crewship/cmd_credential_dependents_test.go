package main

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"gopkg.in/yaml.v3"
)

func TestCredentialDependentsStructuredOutputPreservesEvidenceLimits(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": covCredIDCli3, "name": "gh-token"},
	}))
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3+"/dependents", clitest.JSONResponse(200, map[string]any{
		"routines":     []map[string]string{{"slug": "deploy", "name": "Deploy", "type": "GITHUB_TOKEN", "resolution": "would_resolve"}},
		"recorded_use": "not_attributed", "visibility_limited": true, "dynamic_uses_untracked": true,
	}))

	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			flagFormat = format
			out := covCaptureStdoutCli9(t, func() {
				if err := credDependentsCmd.RunE(credDependentsCmd, []string{"gh-token"}); err != nil {
					t.Fatalf("credential dependents: %v", err)
				}
			})
			var got struct {
				Routines []struct {
					Type string `json:"type" yaml:"type"`
				} `json:"routines" yaml:"routines"`
				RecordedUse          string `json:"recorded_use" yaml:"recorded_use"`
				VisibilityLimited    bool   `json:"visibility_limited" yaml:"visibility_limited"`
				DynamicUsesUntracked bool   `json:"dynamic_uses_untracked" yaml:"dynamic_uses_untracked"`
			}
			var err error
			if format == "json" {
				err = json.Unmarshal([]byte(out), &got)
			} else {
				err = yaml.Unmarshal([]byte(out), &got)
			}
			if err != nil {
				t.Fatalf("decode %s output: %v; output: %q", format, err, out)
			}
			if len(got.Routines) != 1 || got.Routines[0].Type != "GITHUB_TOKEN" ||
				got.RecordedUse != "not_attributed" || !got.VisibilityLimited || !got.DynamicUsesUntracked {
				t.Fatalf("structured output lost API metadata: %+v", got)
			}
		})
	}
}
