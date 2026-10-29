package askforms

import (
	"encoding/json"
	"os"
	"testing"
)

// The browser matcher consumes this same corpus. Keep accepted definitions,
// whole-value matching, and invalid legacy rows aligned with the Go boundary.
func TestAskPatternMatchingParity(t *testing.T) {
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
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			f := Form{ID: "parity", Label: "Parity", Template: "{{answer}}", Fields: []Field{{Name: "answer", Label: "Answer", Type: "text", Pattern: tc.Pattern}}}
			if err := Validate([]Form{f}); (err == nil) != tc.DefinitionValid {
				t.Fatalf("definition error=%v, want valid=%v", err, tc.DefinitionValid)
			}
			errors := ValidateAnswers(f, Values{"answer": tc.Value})
			if tc.Matches {
				if len(errors) != 0 {
					t.Fatalf("matching answer refused: %v", errors)
				}
			} else if len(errors) != 1 || errors[0].Code != CodePattern {
				t.Fatalf("want one pattern error, got %v", errors)
			}
		})
	}
}
