package orchestrator

import (
	"encoding/base64"
	"io/fs"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
	"github.com/crewship-ai/crewship/internal/skills/bundled"
)

func TestArtifactSkillReleaseReachesEveryCLIDiscoveryFile(t *testing.T) {
	body, err := fs.ReadFile(bundled.FS(), "crewship/crewship-artifacts/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	c := covSkillsContainer(nil, false, false, false)
	if err := writeAgentSkills(t.Context(), c, "fixture", "/work", []SkillBundle{{Slug: "crewship-artifacts", Vendor: "crewship", Content: string(body)}}, covQuietLogger()); err != nil {
		t.Fatal(err)
	}
	written := 0
	for _, call := range c.snapshotCalls() {
		script := covScript(call)
		before, _, ok := strings.Cut(script, " | base64 -d >")
		if !ok {
			continue
		}
		_, encoded, ok := strings.Cut(before, " && echo ")
		if !ok {
			t.Fatal("missing encoded skill")
		}
		actual, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || string(actual) != string(body) {
			t.Fatalf("skill changed during materialization: %v", err)
		}
		parsed, err := skills.ParseSKILLMD(string(actual))
		if err != nil || parsed.Meta.Version != "1.1.0" {
			t.Fatalf("CLI lost package release: %v", err)
		}
		written++
	}
	if written != 5 {
		t.Fatalf("wrote %d CLI files, want 5", written)
	}
}
