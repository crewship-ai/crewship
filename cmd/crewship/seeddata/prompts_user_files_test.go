package seeddata

import (
	"io/fs"
	"strings"
	"testing"
)

// A seeded agent's demo role never limits what it does with a person's own file or Sheet: an
// agent once refused a workbook its person uploaded as "unrelated to the Harbor Goods agenda".
// Every prompt carries the rule, word for word.
func TestSeedPrompts_HelpWithThePersonsOwnFiles(t *testing.T) {
	names, err := fs.Glob(promptsFS, "prompts/*.md")
	if err != nil || len(names) == 0 {
		t.Fatal(names, err)
	}
	for _, name := range names {
		slug := strings.TrimSuffix(strings.TrimPrefix(name, "prompts/"), ".md")
		if !strings.Contains(AgentPrompt(slug), UserFilesRule) {
			t.Errorf("%s does not carry the user files rule", name)
		}
	}
}
