package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Exercise the preview's actual binary with the same definitions and answers
// used by the browser boundary tests. No live instance or chat is contacted.
func TestAcceptance_AgentAskPreview_PatternParity(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/ask-patterns.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name            string `json:"name"`
		Pattern         string `json:"pattern"`
		Value           string `json:"value"`
		DefinitionValid bool   `json:"definition_valid"`
		Matches         bool   `json:"matches"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bin := buildCrewshipBinary(t)
	cfg := askCLIConfig(t)
	for _, tc := range cases {
		// ask-preview validates the fetched definition before rendering. Legacy
		// invalid rows are intentionally tested at the answer-validator layer.
		if !tc.DefinitionValid {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			forms, err := json.Marshal([]map[string]any{{"id": "pattern", "label": "Pattern", "template": "Answer: {{answer}}", "fields": []map[string]any{{"name": "answer", "label": "Answer", "type": "text", "pattern": tc.Pattern}}}})
			if err != nil {
				t.Fatal(err)
			}
			var agent map[string]any
			if err := json.Unmarshal([]byte(askAgentJSON), &agent); err != nil {
				t.Fatal(err)
			}
			agent["ask_forms"] = string(forms)
			body, err := json.Marshal(agent)
			if err != nil {
				t.Fatal(err)
			}
			srv := askStubServer(t, string(body), nil)
			defer srv.Close()
			out, err := runCrewship(t, bin, cfg, srv.URL, "agent", "ask-preview", "lucy", "pattern", "--var", "answer="+tc.Value)
			if tc.Matches {
				if err != nil {
					t.Fatalf("matching preview refused: %v\n%s", err, out)
				}
				// Validation sees the full answer; only the message renderer caps it.
				want := []rune(tc.Value)
				if len(want) > 2000 {
					want = want[:2000]
				}
				if !strings.Contains(out, "Answer: "+string(want)) {
					t.Fatalf("preview changed matching answer: %q", out)
				}
			} else if err == nil || !strings.Contains(out, "Answer is not in the expected format.") {
				t.Fatalf("mismatching preview: err=%v output=%q", err, out)
			}
		})
	}
}
