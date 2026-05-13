package cli

import (
	"strings"
	"testing"
)

func TestParseSlashReader_Frontmatter(t *testing.T) {
	src := `---
name: review
description: Review a diff
agent: viktor
plan: true
vars:
  - target
  - tone
---
Review this ${target} in a ${tone} tone:

$args
`
	sc, err := parseSlashReader(strings.NewReader(src), "/tmp/review.md")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Name != "review" {
		t.Errorf("name=%s", sc.Name)
	}
	if sc.Agent != "viktor" {
		t.Errorf("agent=%s", sc.Agent)
	}
	if !sc.Plan {
		t.Error("plan should be true")
	}
	if len(sc.Vars) != 2 || sc.Vars[0] != "target" {
		t.Errorf("vars=%v", sc.Vars)
	}
	if !strings.Contains(sc.Body, "${target}") {
		t.Errorf("body missing target placeholder")
	}
}

func TestParseSlashReader_NoFrontmatter(t *testing.T) {
	src := "Just plain prompt body"
	sc, err := parseSlashReader(strings.NewReader(src), "/tmp/plain.md")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Name != "plain" {
		t.Errorf("name=%s", sc.Name)
	}
	if sc.Body != "Just plain prompt body" {
		t.Errorf("body=%q", sc.Body)
	}
}

func TestSlashCommand_Render_Args(t *testing.T) {
	sc := SlashCommand{
		Body: "Review the ${target} in a ${tone} tone.\nExtras: $args",
		Vars: []string{"target", "tone"},
	}
	out := sc.Render([]string{"diff", "terse"})
	if !strings.Contains(out, "Review the diff in a terse tone.") {
		t.Errorf("output: %s", out)
	}
}

func TestSlashCommand_Render_PrefixOverlap(t *testing.T) {
	// Regression: when one var name is a prefix of another (`a` vs
	// `args`), the previous implementation iterated map keys in random
	// order, so `$a` could chew off the front of any `$args` token.
	// Sorting by descending length fixes it; run many iterations to
	// guarantee we exercise every map order.
	sc := SlashCommand{
		Body: "x=$a y=$args",
		Vars: []string{"a", "args"},
	}
	for i := 0; i < 200; i++ {
		got := sc.Render([]string{"AAA", "BBB"})
		want := "x=AAA y=BBB"
		if got != want {
			t.Fatalf("iter %d: got %q want %q", i, got, want)
		}
	}
}

func TestSlashCommand_Render_ImplicitArgs(t *testing.T) {
	sc := SlashCommand{
		Body: "Summarise $args",
	}
	out := sc.Render([]string{"yesterday", "morning"})
	if out != "Summarise yesterday morning" {
		t.Errorf("output=%q", out)
	}
}

func TestSlashNameValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"simple word", "review", true},
		{"hyphenated", "do-thing", true},
		{"digits", "r12", true},
		{"empty rejected", "", false},
		{"leading hyphen rejected", "-bad", false},
		{"uppercase rejected", "Foo", false},
		{"shell metacharacter rejected", "rm -rf", false},
		{"slash rejected", "plan/act", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := slashNameValid(tc.in); got != tc.want {
				t.Errorf("slashNameValid(%q) = %v want %v", tc.in, got, tc.want)
			}
		})
	}
}
