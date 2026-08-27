package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MDX parses a page as JSX, so `<` opens a tag unless something protects it.
// A backtick code span protects it; a fenced block protects it. Nothing else
// does — and a code span that opens on one line and closes on the next does
// NOT, because the tag is seen first.
//
// That is how `docs/guides/routines.mdx` reached main broken:
//
//	Set it with `crewship routine save --author-agent
//	<slug|id>` (the agent must belong to ...
//
// Mintlify refused the whole deployment with "Unexpected character `|`
// (U+007C) in name", naming the page but not the line. Nothing in CI caught
// it: the docs build is an external status, and on the merge commit it
// reported `skipped: Changes superseded by downstream commit`, so the failure
// only surfaced days later on unrelated pull requests that merged main.
//
// This pass is deliberately narrow. It does not flag every unguarded `<…>` —
// prose legitimately contains `<Note>` and friends, and placeholders like
// `<id>` are everywhere. It flags only a bare tag whose name contains a
// character MDX cannot accept in one, which is unambiguously a broken code
// span rather than a component.
var (
	// A `<…>` with no whitespace, holding at least one character that cannot
	// appear in a JSX tag name. `|` is the one that bit; `,` `;` `&` `=` and
	// quotes fail the same way.
	mdxHostileTag = regexp.MustCompile(`<[^<>\s]*[|,;&='"][^<>\s]*>`)
	// Backtick spans, so we can blank them before looking.
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	// A fence line, capturing the marker run and whatever follows it.
	fenceLine = regexp.MustCompile("^\\s*(`{3,}|~{3,})\\s*(.*)$")
)

// fenceState tracks whether a line is inside a fenced block, implementing the
// two CommonMark rules a naive toggle gets wrong:
//
//   - a closing fence may NOT carry an info string, so ```yaml nested inside
//     an outer ```markdown block is content, not a close;
//   - a closing fence must be at least as long as the one that opened it.
//
// Both matter here. `docs/guides/skills-authoring.mdx` embeds a ```yaml
// sample inside a ```markdown block, and a naive toggle reads that as a close
// — then reports every placeholder in the sample as unguarded prose.
type fenceState struct {
	open   bool
	marker byte
	length int
}

func (f *fenceState) feed(line string) (isFence bool) {
	m := fenceLine.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	run, info := m[1], strings.TrimSpace(m[2])
	if !f.open {
		f.open, f.marker, f.length = true, run[0], len(run)
		return true
	}
	// Only the same marker, at least as long, with no info string, closes.
	if run[0] == f.marker && len(run) >= f.length && info == "" {
		f.open = false
		return true
	}
	return true // a fence-looking line inside a block is content, but never prose
}

func (f *fenceState) inside() bool { return f.open }

type mdxSyntaxIssue struct {
	page string
	line int
	text string
}

// unguardedMDXTags reports tags that will fail the MDX parse, scanning every
// page under docs/ regardless of whether the navigation lists it — an
// unlisted page still fails the build for every listed one.
func unguardedMDXTags(root string) ([]mdxSyntaxIssue, int, error) {
	issues := []mdxSyntaxIssue{}
	scanned := 0

	docsDir := filepath.Join(root, "docs")
	err := filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext != ".mdx" && ext != ".md" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++

		rel, relErr := filepath.Rel(docsDir, path)
		if relErr != nil {
			rel = path
		}
		page := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))

		var fence fenceState
		for i, line := range strings.Split(string(body), "\n") {
			if fence.feed(line) {
				continue
			}
			if fence.inside() {
				continue
			}
			// Blank out complete, single-line code spans. What survives is
			// either genuine prose or a span broken across lines — which is
			// the case we are here to catch.
			bare := inlineCode.ReplaceAllString(line, "")
			for _, m := range mdxHostileTag.FindAllString(bare, -1) {
				issues = append(issues, mdxSyntaxIssue{page: page, line: i + 1, text: m})
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return issues, scanned, nil
}

func reportUnguardedMDXTags(root string) error {
	issues, scanned, err := unguardedMDXTags(root)
	if err != nil {
		return fmt.Errorf("scan for unguarded MDX tags: %w", err)
	}
	if len(issues) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d unguarded MDX tag(s) — the docs build will refuse the whole deployment:\n", len(issues))
		for _, is := range issues {
			fmt.Fprintf(&b, "  %s:%d  %s\n", is.page, is.line, is.text)
		}
		b.WriteString("  A `<…>` outside a code span is parsed as JSX. If this is meant to be\n")
		b.WriteString("  literal, keep the whole `code span` on one line — a span broken across\n")
		b.WriteString("  a line ending does not protect the tag that follows it.\n")
		return fmt.Errorf("%s", b.String())
	}
	fmt.Printf("docs-surface-check: MDX tag safety %d pages scanned, 0 unguarded\n", scanned)
	return nil
}
