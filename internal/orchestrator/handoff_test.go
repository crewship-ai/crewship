package orchestrator

import "testing"

func TestParseHandoff(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantParsed bool
		wantSummary    string
		wantConfidence string
		wantArtifacts  string
	}{
		{
			name:       "valid handoff block",
			input: `Some agent output here...
---HANDOFF---
summary: Implemented the login API endpoint with JWT auth
confidence: high
artifacts: internal/api/auth.go, internal/api/auth_test.go
---END HANDOFF---`,
			wantParsed:     true,
			wantSummary:    "Implemented the login API endpoint with JWT auth",
			wantConfidence: "high",
			wantArtifacts:  "internal/api/auth.go, internal/api/auth_test.go",
		},
		{
			name:       "no handoff block",
			input:      "Just some regular agent output without any handoff",
			wantParsed: false,
		},
		{
			name:       "handoff with low confidence",
			input: `Done.
---HANDOFF---
summary: Attempted the task but ran into issues with the API
confidence: low
artifacts: none
---END HANDOFF---`,
			wantParsed:     true,
			wantSummary:    "Attempted the task but ran into issues with the API",
			wantConfidence: "low",
			wantArtifacts:  "none",
		},
		{
			name: "handoff block missing end marker",
			input: `---HANDOFF---
summary: incomplete handoff`,
			wantParsed: false,
		},
		{
			name: "handoff with extra whitespace",
			input: `---HANDOFF---
  summary:   Cleaned up the codebase
  confidence:   medium
  artifacts:   none
---END HANDOFF---`,
			wantParsed:     true,
			wantSummary:    "Cleaned up the codebase",
			wantConfidence: "medium",
			wantArtifacts:  "none",
		},
		{
			name: "handoff with only summary",
			input: `---HANDOFF---
summary: Did the thing
---END HANDOFF---`,
			wantParsed:     true,
			wantSummary:    "Did the thing",
			wantConfidence: "",
			wantArtifacts:  "",
		},
		{
			name: "multiple handoff blocks takes last one",
			input: `---HANDOFF---
summary: First attempt
confidence: low
artifacts: none
---END HANDOFF---
Some more work...
---HANDOFF---
summary: Second attempt succeeded
confidence: high
artifacts: report.md
---END HANDOFF---`,
			wantParsed:     true,
			wantSummary:    "Second attempt succeeded",
			wantConfidence: "high",
			wantArtifacts:  "report.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hd := parseHandoff(tt.input)
			if hd.Parsed != tt.wantParsed {
				t.Errorf("Parsed = %v, want %v", hd.Parsed, tt.wantParsed)
			}
			if !tt.wantParsed {
				return
			}
			if hd.Summary != tt.wantSummary {
				t.Errorf("Summary = %q, want %q", hd.Summary, tt.wantSummary)
			}
			if hd.Confidence != tt.wantConfidence {
				t.Errorf("Confidence = %q, want %q", hd.Confidence, tt.wantConfidence)
			}
			if hd.Artifacts != tt.wantArtifacts {
				t.Errorf("Artifacts = %q, want %q", hd.Artifacts, tt.wantArtifacts)
			}
		})
	}
}
