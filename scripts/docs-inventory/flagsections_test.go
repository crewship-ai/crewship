package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHeadingCommand(t *testing.T) {
	tests := []struct {
		name, line, root, parent, want string
	}{
		{"full spelling", "## `crewship routine list`", "routine", "", "routine list"},
		{"full spelling with arguments", "## `crewship routine diff <slug> --from N --to M`", "routine", "", "routine diff"},
		{"page title", "# crewship chat", "chat", "", "chat"},
		{"root-first without crewship", "### `chat attachments list <chat-id>`", "chat", "chat", "chat attachments list"},
		{"relative with overlap", "### `step-override set <slug> <step_id>`", "routine", "routine step-override", "routine step-override set"},
		{"relative with no overlap", "### list", "routine", "routine state", "routine state list"},
		{"relative under a full-spelling parent", "### `routine budget set <slug> --amount <N>`", "routine", "routine budget", "routine budget set"},
		{"capitalised prose is not a command", "## Flags", "ask", "ask", ""},
		{"see also is not a command", "## See also", "ask", "ask", ""},
		{"fence comment is not a heading", "# 10 runs at the routine's authored tier", "routine", "routine bench", ""},
		{"alias page", "## `crewship pipeline list`", "pipeline", "", "pipeline list"},
		{"other root on a page", "## `crewship credential create`", "oauth", "oauth", "credential create"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := headingCommand(tt.line, tt.root, tt.parent); got != tt.want {
				t.Fatalf("headingCommand(%q, root=%q, parent=%q) = %q, want %q", tt.line, tt.root, tt.parent, got, tt.want)
			}
		})
	}
}

// A section runs to the next heading of the same or a higher level, so a
// command's `### Common errors` stays inside it and the next `##` command
// ends it. Fenced `#` lines are content, not headings.
func TestCLIDocSectionsNestAndSkipFences(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(`
# crewship routine
## Subcommands
## `+"`crewship routine run <slug>`"+`
`+"```bash"+`
# Force every step onto a tier
crewship routine run nightly --tier sonnet
`+"```"+`
### Common errors
--tier is refused for routines without agent_run steps.
## `+"`crewship routine state`"+`
### list
Lists keys. --prefix narrows.
### `+"`routine state set <slug> <key> <value>`"+`
--ttl sets an expiry.
## See also
`), "\n")
	got := cliDocSections(lines, "routine")
	byCommand := map[string]cliDocSection{}
	for _, section := range got {
		if section.Command != "" {
			byCommand[section.Command] = section
		}
	}
	want := map[string][2]int{
		"routine":            {0, len(lines)},
		"routine run":        {2, 9},
		"routine state":      {9, 14},
		"routine state list": {10, 12},
		"routine state set":  {12, 14},
	}
	for command, span := range want {
		section, ok := byCommand[command]
		if !ok {
			t.Errorf("no section for %q; got %+v", command, got)
			continue
		}
		if section.Start != span[0] || section.End != span[1] {
			t.Errorf("section %q = lines [%d,%d), want [%d,%d)", command, section.Start, section.End, span[0], span[1])
		}
	}
	for _, section := range got {
		if strings.Contains(section.Command, "force") {
			t.Errorf("a fenced comment line became a section: %+v", section)
		}
	}
}

// The case the audit counted 167 of: the flag string is on the page, under a
// sibling command's heading, and nowhere near this one.
func TestFlagInCommandSectionIsNotSatisfiedBySibling(t *testing.T) {
	docs := []docFile{{Path: "docs/cli/saved-view.mdx", Text: strings.TrimSpace(`
# Saved View
## ` + "`crewship saved-view create`" + `
| ` + "`--filters <json>`" + ` | Filter JSON. |
| ` + "`--shared`" + ` | Share it. |
## ` + "`crewship saved-view update <view-id>`" + `
Takes the same flags as create.
` + "```bash" + `
crewship saved-view update v1 --name "Renamed"
` + "```" + `
`)}}
	index := newCLIDocIndex(docs, map[string]bool{"saved-view": true, "saved-view create": true, "saved-view update": true})
	update := []string{"saved-view update"}

	if index.flagInCommandSection(update, "shared") {
		t.Error("--shared is documented under create only; update's section must not count it")
	}
	if !index.flagInCommandSection(update, "name") {
		t.Error("--name is on a line that invokes `crewship saved-view update`; that counts")
	}
	if !index.flagInCommandSection([]string{"saved-view create"}, "shared") {
		t.Error("--shared is in create's own section")
	}
	// The invocation has to be the whole command: `crewship saved-view` on
	// the update line does not document a --name on the parent command.
	if index.flagInCommandSection([]string{"saved-view"}, "name") {
		t.Error("`crewship saved-view update --name` is not an invocation of `crewship saved-view`")
	}
}

// A page may head the section with an alias (`crewship pipeline list`) or a
// relative spelling; both are the command's own section.
func TestFlagInCommandSectionAcceptsAliasAndRelativeHeadings(t *testing.T) {
	docs := []docFile{
		{Path: "docs/cli/pipeline.mdx", Text: "# crewship pipeline\n## `crewship pipeline list`\n`--limit` caps the rows.\n"},
		{Path: "docs/cli/routine.mdx", Text: "# crewship routine\n## `crewship routine step-override`\n### `step-override set <slug> <step_id>`\n`--tier` pins the step.\n"},
	}
	index := newCLIDocIndex(docs, nil)
	if !index.flagInCommandSection([]string{"routine list", "pipeline list"}, "limit") {
		t.Error("the alias page's section for `pipeline list` documents --limit for `routine list`")
	}
	if !index.flagInCommandSection([]string{"routine step-override set"}, "tier") {
		t.Error("the relative heading `step-override set` under `routine step-override` is the command's section")
	}
}

func TestCLIFlagSectionEvidenceLeavesPageLevelMissesToTheOtherGate(t *testing.T) {
	node := commandNode{Path: "saved-view update", Flags: []flagManifest{{Name: "shared"}, {Name: "nowhere"}}}
	index := newCLIDocIndex([]docFile{{Path: "docs/cli/saved-view.mdx", Text: "# Saved View\n## `crewship saved-view create`\n`--shared`\n## `crewship saved-view update`\n"}}, nil)
	got := cliFlagSectionEvidence(node, []string{"saved-view update"}, []string{"nowhere"}, index)
	if !slices.Equal(got, []string{"shared"}) {
		t.Fatalf("outside-section flags = %v, want [shared]: --nowhere is already a page-level miss", got)
	}
}

func TestFlagSectionBaselineRatchet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.txt")
	if err := os.WriteFile(path, []byte("# tolerated today\nsaved-view update --shared\nrecurring update --filter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := readCLIFlagSectionBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	records := []cliRecord{
		{Path: "saved-view update", FlagsOutsideSection: []string{"shared", "view-type"}},
		{Path: "chat room create", FlagsOutsideSection: []string{"kind"}},
	}
	newMisses := newFlagSectionMisses(records, baseline)
	if !slices.Equal(newMisses, []string{"chat room create --kind", "saved-view update --view-type"}) {
		t.Errorf("new misses = %v; the baselined --shared must not be among them", newMisses)
	}
	stale := staleFlagSectionBaseline(records, baseline)
	if len(stale) != 1 || !strings.Contains(stale[0], ":3: recurring update --filter") {
		t.Errorf("stale = %v, want the fixed recurring line named with its line number", stale)
	}

	if err := os.WriteFile(path, []byte("saved-view update\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCLIFlagSectionBaseline(path); err == nil || !strings.Contains(err.Error(), "baseline.txt:1") {
		t.Fatalf("a line without a flag must fail and name the line, got %v", err)
	}
	if got, err := readCLIFlagSectionBaseline(filepath.Join(dir, "absent.txt")); err != nil || len(got) != 0 {
		t.Fatalf("a missing baseline tolerates nothing, got %v, %v", got, err)
	}
}

// -strict enforces the new misses, and names them as `<command> --<flag>` so
// the row is the baseline line to add — or, better, the section to write.
func TestStrictGateNamesTheFlagOutsideItsSection(t *testing.T) {
	r := report{
		CLI: []cliRecord{{Path: "chat room create", Status: "documented_root", Flags: []string{"kind"},
			DocumentedFlags: []string{"kind"}, FlagsOutsideSection: []string{"kind"}}},
		flagSectionMissesNew: []string{"chat room create --kind"},
	}
	r.Summary = summarize(r)
	if r.Summary.CLIFlagsOutsideSection != 1 || r.Summary.CLIFlagsOutsideSectionNew != 1 {
		t.Fatalf("summary = %d outside, %d new; want 1 and 1", r.Summary.CLIFlagsOutsideSection, r.Summary.CLIFlagsOutsideSectionNew)
	}
	err := enforce(r)
	if err == nil {
		t.Fatal("a flag outside its section and outside the baseline must fail -strict")
	}
	if !strings.Contains(err.Error(), "chat room create --kind") {
		t.Errorf("the offender is not named:\n%s", err)
	}

	// The same miss, baselined: the count is published, the gate is quiet.
	r.flagSectionMissesNew = nil
	r.Summary = summarize(r)
	if err := enforce(r); err != nil {
		t.Fatalf("a baselined miss must not fail -strict: %v", err)
	}
	if r.Summary.CLIFlagsOutsideSection != 1 {
		t.Error("the baselined miss must still be counted in the published figure")
	}
}
