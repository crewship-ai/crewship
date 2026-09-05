package seeddata

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// SkillDef defines a skill to seed. Content is the system prompt extension
// that gets injected into the agent's prompt during task execution.
type SkillDef struct {
	Name        string `yaml:"name"`
	Slug        string `yaml:"slug"`
	DisplayName string `yaml:"display_name"`
	Category    string `yaml:"category"`
	Description string `yaml:"description"`
	Icon        string `yaml:"icon"`
	Content     string `yaml:"content"`
}

// SkillMD returns the SKILL.md format (YAML frontmatter + content) required
// by the POST /api/v1/workspaces/{id}/skills/import endpoint.
func (s SkillDef) SkillMD() string {
	return fmt.Sprintf(`---
name: "%s"
display_name: "%s"
version: "1.0.0"
description: "%s"
category: "%s"
icon: "%s"
---

%s`, s.Name, s.DisplayName, s.Description, s.Category, s.Icon, s.Content)
}

// Skills with real content — system prompt extensions injected as <skill>
// blocks into the agent's prompt during execution.
//
// Loaded from builtin/skills.yaml at init time. Migrated from a Go-literal
// list in F2 step 6 so a non-Go contributor can edit a skill body without
// reformatting Go string concatenation. The on-disk shape, slug list, and
// SkillMD() output are unchanged — a re-seed produces byte-identical
// SKILL.md uploads.
var Skills = mustLoadSkills()

func mustLoadSkills() []SkillDef {
	data, err := builtinFS.ReadFile("builtin/skills.yaml")
	if err != nil {
		panic(fmt.Sprintf("seeddata: read builtin/skills.yaml: %v", err))
	}
	var doc struct {
		Skills []SkillDef `yaml:"skills"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		panic(fmt.Sprintf("seeddata: parse builtin/skills.yaml: %v", err))
	}
	if len(doc.Skills) == 0 {
		panic("seeddata: builtin/skills.yaml decoded to zero skills — schema drift?")
	}
	return doc.Skills
}

// SkillAssignments maps agent slug → list of skill slugs to assign.
// Assignments match agents to the demo issues they'll handle. Stays as a
// Go map (not YAML) because it's a relationship, not a catalogue — the
// data is most useful when right next to the agent fixture review.
// "routine-author" is a BUNDLED first-party skill (installed on server
// startup, not in Skills above); the seed assignment loop resolves it via
// GET /api/v1/skills, so referencing its slug here is enough. It goes to
// the three crew LEADs — they field the "make a routine that…" requests and
// own routine authoring for their crew.
var SkillAssignments = map[string][]string{
	// Engineering — scripting, file ops, inspection (+ routine authoring for
	// the lead). site-replica is the pack skill: how the crew hands a page
	// copy across analyst, data, build and test through its shared volume.
	"alex":   {"network-probe", "script-runner", "file-crafter", "routine-author", "site-replica"},
	"sam":    {"script-runner", "file-crafter", "system-inspector", "site-replica"},
	"robin":  {"file-crafter", "web-scraper", "site-replica"},
	"taylor": {"web-scraper", "file-crafter", "site-replica"},
	"jamie":  {"script-runner", "file-crafter", "site-replica"},
	// Quality — testing, validation, review. docs-drift is the pack skill.
	"jordan": {"script-runner", "file-crafter", "routine-author", "docs-drift"},
	"casey":  {"system-inspector", "script-runner", "file-crafter", "docs-drift"},
	// Ops — network, system inspection, automation. ci-triage + known-flaky
	// are the pack skills: how to read a red run and when NOT to call it flaky.
	"morgan": {"network-probe", "system-inspector", "routine-author", "ci-triage", "known-flaky"},
	"riley":  {"web-scraper", "script-runner", "network-probe", "ci-triage", "known-flaky"},
}
