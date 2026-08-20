package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// What an agent is told a routine WANTS.
//
// The block used to carry slug + description + usage and nothing else,
// so an agent asked to run the monthly accounting pack could not name a
// single value the routine expected. Its only moves were an empty inputs
// object or invented key names — and a routine run spends money and
// touches the crew's integrations, so guessing is the expensive kind of
// wrong. These tests pin what it now says.

func TestPromptInputLines(t *testing.T) {
	cases := []struct {
		name       string
		definition string
		wantHas    []string
		wantLacks  []string
	}{
		{
			name:       "no inputs declared renders nothing",
			definition: `{"name":"x","steps":[]}`,
			wantLacks:  []string{"inputs"},
		},
		{
			name:       "empty inputs array renders nothing",
			definition: `{"name":"x","inputs":[],"steps":[]}`,
			wantLacks:  []string{"inputs"},
		},
		{
			// A definition that no longer decodes costs this routine its
			// input list, not the whole block — the entry above it still
			// names the slug.
			name:       "undecodable definition renders nothing",
			definition: `{"inputs":`,
			wantLacks:  []string{"inputs"},
		},
		{
			name: "msn-etn-podklady, as the agent sees it",
			definition: `{"inputs":[
				{"name":"obdobi","type":"string","description":"YYYY-MM; empty means the previous month"},
				{"name":"ucetnictvi_root","type":"string","default":"Unify - Účetnictví"},
				{"name":"vypis_odesilatel","type":"string","default":"info@rb.cz"}
			]}`,
			wantHas: []string{
				"inputs (ask the user for any you do not have):",
				`- obdobi (string) — YYYY-MM; empty means the previous month`,
				`- ucetnictvi_root (string, default "Unify - Účetnictví")`,
				`- vypis_odesilatel (string, default "info@rb.cz")`,
			},
		},
		{
			// The one word here that changes what the agent must do
			// before calling the tool.
			name:       "required is shouted",
			definition: `{"inputs":[{"name":"amount","type":"number","required":true}]}`,
			wantHas:    []string{"- amount (number, REQUIRED)"},
		},
		{
			// JSON hands every number over as a float64. "default 42.0"
			// would have the agent repeat a value back to the user that
			// the routine does not use.
			name:       "integer default keeps its shape",
			definition: `{"inputs":[{"name":"limit","type":"integer","default":42}]}`,
			wantHas:    []string{"- limit (integer, default 42)"},
			wantLacks:  []string{"42.0"},
		},
		{
			name:       "boolean default",
			definition: `{"inputs":[{"name":"dry","type":"boolean","default":false}]}`,
			wantHas:    []string{"- dry (boolean, default false)"},
		},
		{
			// An empty default tells the agent less than silence does:
			// "default " is noise, and the routine's real behaviour for an
			// empty value belongs in its description.
			name:       "empty string default is not announced",
			definition: `{"inputs":[{"name":"obdobi","type":"string","default":""}]}`,
			wantHas:    []string{"- obdobi (string)"},
			wantLacks:  []string{"default"},
		},
		{
			name:       "undeclared type reads as string",
			definition: `{"inputs":[{"name":"mystery"}]}`,
			wantHas:    []string{"- mystery (string)"},
		},
		{
			// A required input has no default to state — required IS the
			// statement, and printing both invites the agent to use the
			// default instead of asking.
			name:       "required wins over default",
			definition: `{"inputs":[{"name":"a","type":"string","required":true,"default":"x"}]}`,
			wantHas:    []string{"- a (string, REQUIRED)"},
			wantLacks:  []string{`default "x"`},
		},
		{
			name:       "unnamed input is dropped",
			definition: `{"inputs":[{"name":"","type":"string"},{"name":"kept","type":"string"}]}`,
			wantHas:    []string{"- kept (string)"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := promptInputLines(c.definition)
			for _, want := range c.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range c.wantLacks {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpectedly contains %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}

// One pathological routine must not eat the budget the other routines in
// the workspace need to be visible at all.
func TestPromptInputLinesCapsRunawayInputs(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"inputs":[`)
	for i := 0; i < promptInputCap+5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":"field`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(`","type":"string"}`)
	}
	b.WriteString(`]}`)

	got := promptInputLines(b.String())
	if n := strings.Count(got, "    - "); n != promptInputCap {
		t.Errorf("rendered %d input lines, want the cap of %d", n, promptInputCap)
	}
	if !strings.Contains(got, "…5 more") {
		t.Errorf("truncation was silent — the agent must be told the list is partial:\n%s", got)
	}
}

// The truncation line counts what it did NOT show, and the loop skips
// unnamed inputs — so counting off the raw slice overstates the remainder.
// One unnamed input among fourteen named ones rendered twelve and claimed
// three were left, when two were. A number in a system prompt is a claim
// the model repeats to the user.
func TestPromptInputLinesTruncationCountsOnlyRenderableInputs(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"inputs":[{"name":"","type":"string"}`)
	for i := 0; i < promptInputCap+2; i++ {
		fmt.Fprintf(&b, `,{"name":"field%d","type":"string"}`, i)
	}
	b.WriteString(`]}`)

	got := promptInputLines(b.String())
	if n := strings.Count(got, "    - "); n != promptInputCap {
		t.Errorf("rendered %d input lines, want the cap of %d", n, promptInputCap)
	}
	if !strings.Contains(got, "…2 more") {
		t.Errorf("remainder counted the unnamed input it never rendered:\n%s", got)
	}
}

// Only-unnamed inputs render nothing at all — not a header over an empty
// list, which would tell the agent to ask the user for something the
// routine does not declare.
func TestPromptInputLinesAllUnnamedRendersNothing(t *testing.T) {
	if got := promptInputLines(`{"inputs":[{"name":""},{"name":""}]}`); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// The instruction itself. It is the whole point of listing the inputs,
// and it is the line that stops an agent firing a spend-bearing routine
// on values it made up.
func TestSystemPromptBlockTellsTheAgentToAsk(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	in := validSaveInput("msn-etn-podklady")
	in.DefinitionJSON = `{"name":"msn-etn-podklady","inputs":[` +
		`{"name":"obdobi","type":"string","description":"YYYY-MM; empty means the previous month"}` +
		`],"steps":[]}`
	if _, err := store.Save(context.Background(), in); err != nil {
		t.Fatalf("save: %v", err)
	}

	block, err := BuildSystemPromptBlock(context.Background(), store, "ws_test", nil)
	if err != nil {
		t.Fatalf("BuildSystemPromptBlock: %v", err)
	}
	for _, want := range []string{
		"ASK THE USER",
		"Do not invent values",
		"not send an empty inputs object when the routine declares inputs",
		// And the inputs that make the instruction actionable.
		"- obdobi (string) — YYYY-MM; empty means the previous month",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block is missing %q:\n%s", want, block)
		}
	}
}

// A workspace whose routines declare no inputs gets the block it always
// got, plus the instruction — which costs a handful of lines and is what
// makes the rule stable rather than per-routine.
func TestSystemPromptBlockUnchangedForInputlessRoutines(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	if _, err := store.Save(context.Background(), validSaveInput("plain")); err != nil {
		t.Fatalf("save: %v", err)
	}

	block, err := BuildSystemPromptBlock(context.Background(), store, "ws_test", nil)
	if err != nil {
		t.Fatalf("BuildSystemPromptBlock: %v", err)
	}
	if strings.Contains(block, "inputs (ask the user") {
		t.Errorf("a routine with no declared inputs grew an inputs section:\n%s", block)
	}
	if !strings.Contains(block, "- slug: plain") {
		t.Errorf("the entry itself is missing:\n%s", block)
	}
}
