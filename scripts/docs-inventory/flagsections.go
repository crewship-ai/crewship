package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Section-scoped flag evidence.
//
// The page-level check (cliFlagEvidence) asks whether `--name` appears
// anywhere on a page that mentions the command. That is satisfiable by
// accident: `routine schedules update --timezone` was "documented" because
// `schedules create` documents a --timezone two screens up, and the fourteen
// `chat room` subcommands passed on the strength of flags that other chat
// commands happen to share. The 2026-09-15 audit counted 167 flags tree-wide
// whose only mention sits under a different command's heading.
//
// This pass asks the narrower question: does the flag appear in the part of
// the page that is ABOUT this command? Two things count.
//
//   - A line that invokes the command and names the flag, anywhere under
//     docs/cli/: `crewship saved-view update v1 --filter …` in an example.
//   - The command's own SECTION: from a heading whose text names the command
//     to the next heading of the same or a higher level, sub-headings
//     included. The heading may spell the command in full (`## crewship
//     routine list`), from the root (`### chat attachments list <id>`), or
//     relative to its parent heading (`### step-override set <slug>` under
//     `## crewship routine step-override`); headingCommand resolves all three.
//
// The check is baselined rather than clean: the misses that exist today are in
// cliFlagSectionBaselinePath and -strict fails only on a miss that is not
// listed there, so the number can go down and cannot go up. A baseline line
// whose miss no longer exists is reported so it can be deleted.

const cliFlagSectionBaselinePath = "scripts/docs-inventory/flag-section-baseline.txt"

// commandWord is one word of a command path as a heading spells it. Commands
// are lowercase, so `Flags`, `Examples`, `Common errors` and `See also` are
// not commands and their headings are transparent to headingCommand.
var commandWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// cliDocSection is one heading's span on a docs/cli page.
type cliDocSection struct {
	// Command is the command path the heading resolves to, "" for a heading
	// that names no command.
	Command string
	Level   int
	Start   int // heading line, 0-based
	End     int // exclusive
}

// cliDocSections splits a page into heading sections. Fenced blocks are
// skipped: the `# 10 runs at the routine's authored tier` comments inside an
// example are not headings.
func cliDocSections(lines []string, root string) []cliDocSection {
	var sections []cliDocSection
	var stack []cliDocSection // open command headings, by level
	var fence docFence
	for i, line := range lines {
		if fence.feed(line) || fence.open {
			continue
		}
		level := markdownHeadingLevel(line)
		if level == 0 {
			continue
		}
		// Close every open section this heading ends.
		for j := len(sections) - 1; j >= 0; j-- {
			if sections[j].End == 0 && sections[j].Level >= level {
				sections[j].End = i
			}
		}
		for len(stack) > 0 && stack[len(stack)-1].Level >= level {
			stack = stack[:len(stack)-1]
		}
		parent := ""
		if len(stack) > 0 {
			parent = stack[len(stack)-1].Command
		}
		section := cliDocSection{Command: headingCommand(line, root, parent), Level: level, Start: i}
		sections = append(sections, section)
		if section.Command != "" {
			stack = append(stack, section)
		}
	}
	for j := range sections {
		if sections[j].End == 0 {
			sections[j].End = len(lines)
		}
	}
	return sections
}

// docFence tracks fenced blocks the way scripts/docs-surface-check does: a
// close needs the same marker, at least as long, with no info string, so a
// ```yaml sample nested in a ```markdown block does not end the outer block.
type docFence struct {
	open   bool
	marker byte
	length int
}

var docFenceLine = regexp.MustCompile("^\\s*(`{3,}|~{3,})\\s*(.*)$")

func (f *docFence) feed(line string) bool {
	m := docFenceLine.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	run, info := m[1], strings.TrimSpace(m[2])
	switch {
	case !f.open:
		f.open, f.marker, f.length = true, run[0], len(run)
	case run[0] == f.marker && len(run) >= f.length && info == "":
		f.open = false
	}
	return true
}

// headingCommand resolves the command a heading names.
//
// The heading's leading command words are read after stripping the `#`s and
// any backticks, dropping a leading `crewship`, and stopping at the first
// token that is not a command word (`<slug>`, `--from`, `[file]`). A heading
// that starts with `crewship` or with the page's root command is absolute. Any
// other command-word heading is relative to the nearest command heading above
// it, joined on their overlap: `step-override set` under `routine
// step-override` is `routine step-override set`, and `list` under `routine
// state` is `routine state list`.
func headingCommand(line, root, parent string) string {
	text := strings.TrimSpace(strings.TrimLeft(line, "#"))
	text = strings.ReplaceAll(text, "`", "")
	fields := strings.Fields(text)
	absolute := false
	if len(fields) > 0 && fields[0] == "crewship" {
		absolute = true
		fields = fields[1:]
	}
	var words []string
	for _, field := range fields {
		if !commandWord.MatchString(field) {
			break
		}
		words = append(words, field)
	}
	if len(words) == 0 {
		return ""
	}
	if absolute || words[0] == root || parent == "" {
		return strings.Join(words, " ")
	}
	parentWords := strings.Fields(parent)
	for overlap := min(len(parentWords), len(words)); overlap > 0; overlap-- {
		if strings.Join(parentWords[len(parentWords)-overlap:], " ") == strings.Join(words[:overlap], " ") {
			return strings.Join(append(parentWords, words[overlap:]...), " ")
		}
	}
	return strings.Join(append(parentWords, words...), " ")
}

// cliDocIndex is every docs/cli page cut into sections and invocation lines,
// built once and consulted per command.
type cliDocIndex struct {
	// sections maps a command path to the text of every section about it.
	sections map[string][]string
	// invocations are the lines under docs/cli/ that contain `crewship `.
	invocations []string
	// commands is every command spelling the manifest knows, so an
	// invocation of `saved-view update` is not read as one of `saved-view`.
	commands map[string]bool
}

func newCLIDocIndex(docs []docFile, commands map[string]bool) *cliDocIndex {
	index := &cliDocIndex{sections: map[string][]string{}, commands: commands}
	for _, doc := range docs {
		if !strings.HasPrefix(doc.Path, "docs/cli/") {
			continue
		}
		root := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(doc.Path, "docs/cli/"), ".mdx"), ".md")
		lines := strings.Split(strings.ReplaceAll(doc.Text, "\r\n", "\n"), "\n")
		for _, section := range cliDocSections(lines, root) {
			if section.Command == "" {
				continue
			}
			index.sections[section.Command] = append(index.sections[section.Command], strings.Join(lines[section.Start:section.End], "\n"))
		}
		for _, line := range lines {
			if strings.Contains(line, "crewship ") {
				index.invocations = append(index.invocations, line)
			}
		}
	}
	return index
}

// flagInCommandSection reports whether the flag is documented in a section
// about the command or on a line that invokes it. variants are the spellings
// commandVariants gives the command.
func (index *cliDocIndex) flagInCommandSection(variants []string, flag string) bool {
	for _, variant := range variants {
		for _, text := range index.sections[variant] {
			if flagMentioned(text, flag) {
				return true
			}
		}
		for _, line := range index.invocations {
			if index.invokes(line, variant) && flagMentioned(line, flag) {
				return true
			}
		}
	}
	return false
}

// invokes reports whether line contains `crewship <command>` as an invocation
// of that command and not of one below it: `crewship chat room create --kind`
// documents --kind for `chat room create`, not for `chat room` or `chat`, so
// the match must end at a word boundary and the next word must not extend the
// command to a deeper one the manifest knows.
func (index *cliDocIndex) invokes(line, command string) bool {
	needle := "crewship " + command
	for offset := 0; offset < len(line); {
		idx := strings.Index(line[offset:], needle)
		if idx < 0 {
			return false
		}
		end := offset + idx + len(needle)
		offset = offset + idx + 1
		if end < len(line) && isFlagNameByte(line[end]) {
			continue
		}
		next := strings.Fields(line[end:])
		if len(next) > 0 && index.commands[command+" "+strings.Trim(next[0], "`\\,;|()[]{}")] {
			continue
		}
		return true
	}
	return false
}

// cliFlagSectionEvidence returns the command's flags that appear nowhere in
// its own sections or invocations. Flags the page-level check already reports
// as missing are left to that gate; this one is about flags that are on the
// page but under the wrong heading.
func cliFlagSectionEvidence(node commandNode, variants []string, pageMissing []string, index *cliDocIndex) []string {
	var missing []string
	for _, flag := range node.Flags {
		if contains(pageMissing, flag.Name) {
			continue
		}
		if !index.flagInCommandSection(variants, flag.Name) {
			missing = append(missing, flag.Name)
		}
	}
	return missing
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// flagSectionKey is the baseline spelling of one miss: `<command> --<flag>`.
func flagSectionKey(command, flag string) string {
	return command + " --" + flag
}

// readCLIFlagSectionBaseline reads the misses -strict tolerates: one
// `<command> --<flag>` per line, `#` comments and blank lines skipped.
func readCLIFlagSectionBaseline(name string) (map[string]int, error) {
	file, err := os.Open(name)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	defer file.Close()
	baseline := map[string]int{}
	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		command, flag, ok := strings.Cut(line, " --")
		if !ok || command == "" || flag == "" || strings.ContainsAny(flag, " \t") {
			return nil, fmt.Errorf("%s:%d: expected `<command> --<flag>`, got %q", name, lineNo, line)
		}
		baseline[line] = lineNo
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return baseline, nil
}

// flagSectionMisses lists every miss in the report as a baseline key, sorted.
func flagSectionMisses(records []cliRecord) []string {
	var out []string
	for _, rec := range records {
		for _, flag := range rec.FlagsOutsideSection {
			out = append(out, flagSectionKey(rec.Path, flag))
		}
	}
	sort.Strings(out)
	return out
}

// newFlagSectionMisses is what -strict fails on: misses the baseline does
// not list.
func newFlagSectionMisses(records []cliRecord, baseline map[string]int) []string {
	var out []string
	for _, key := range flagSectionMisses(records) {
		if _, ok := baseline[key]; !ok {
			out = append(out, key)
		}
	}
	return out
}

// staleFlagSectionBaseline names baseline lines whose miss is gone — the flag
// moved under its command's heading, or the flag or command no longer exists.
// Reported, not failed, so the docs fix does not have to touch this file too.
func staleFlagSectionBaseline(records []cliRecord, baseline map[string]int) []string {
	current := map[string]bool{}
	for _, key := range flagSectionMisses(records) {
		current[key] = true
	}
	var out []string
	for key, lineNo := range baseline {
		if !current[key] {
			out = append(out, fmt.Sprintf("%s:%d: %s is documented in its own section now — remove the line", cliFlagSectionBaselinePath, lineNo, key))
		}
	}
	sort.Strings(out)
	return out
}
