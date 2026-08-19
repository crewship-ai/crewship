package seeddata

// The demo seed's half of the chat-as-a-primary-surface work: an agent that
// ships with its own suggested questions and, for one of them, a real ask
// form. These tests are the harness that proves the seeded content without a
// live server — an ask form the API would refuse at save time must fail HERE,
// during `go test`, and not halfway through somebody's first `crewship seed`.
//
// The rules mirrored below are enforced in two different packages:
//
//   - ask forms — internal/askforms, imported directly, so the caps and the
//     placeholder rule are the SAME code the server runs on the PATCH.
//   - suggested prompts — internal/api's normalizeSuggestedPrompts, whose caps
//     are unexported consts. They are restated here the same way
//     lib/agent-suggestions.ts restates them (MAX_SUGGESTED_PROMPTS /
//     MAX_SUGGESTED_PROMPT_LENGTH); a change on either side is a red run.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/askforms"
)

const (
	// Mirrors maxSuggestedPrompts in internal/api/agents_suggested_prompts.go.
	seedMaxSuggestedPrompts = 8
	// Mirrors maxSuggestedPromptLength — characters, not bytes.
	seedMaxSuggestedPromptLength = 120
)

// genericFiller is the ROLE_PACKS default pack in lib/agent-suggestions.ts —
// the chips an unconfigured agent shows today. Seeding one of these back would
// mean the demo replaced the fallback with itself.
var genericFiller = []string{
	"help me get started",
	"what can you do?",
	"show me your skills",
	"run a quick task",
}

// seededPromptLines splits a seeded suggested_prompts block the way
// normalizeSuggestedPrompts does.
func seededPromptLines(raw string) []string {
	out := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// The seed must actually configure the feature. Zero agents with prompts is
// the state this work exists to end, and it is what a fresh `crewship seed`
// produced before it.
func TestSeedAgents_SuggestedPromptsAreConfigured(t *testing.T) {
	t.Parallel()
	withPrompts := []string{}
	for _, a := range Agents {
		if strings.TrimSpace(a.SuggestedPrompts) != "" {
			withPrompts = append(withPrompts, a.Slug)
		}
	}
	if len(withPrompts) < 2 {
		t.Fatalf("only %d seeded agents have suggested_prompts (%v) — the demo workspace "+
			"is supposed to show the feature, not the generic fallback chips",
			len(withPrompts), withPrompts)
	}
}

// Every seeded list has to survive the server's normaliser unchanged: the caps
// hold, nothing is blank, nothing is duplicated, and the questions are the
// ones this agent is actually good at rather than the fallback pack.
func TestSeedAgents_SuggestedPromptsRespectTheCaps(t *testing.T) {
	t.Parallel()
	for _, a := range Agents {
		a := a
		raw := a.SuggestedPrompts
		if strings.TrimSpace(raw) == "" {
			continue
		}
		t.Run(a.Slug, func(t *testing.T) {
			t.Parallel()
			lines := seededPromptLines(raw)
			if len(lines) == 0 {
				t.Fatal("suggested_prompts is set but holds no prompt")
			}
			if len(lines) > seedMaxSuggestedPrompts {
				t.Errorf("%d prompts, at most %d are allowed", len(lines), seedMaxSuggestedPrompts)
			}
			seen := map[string]bool{}
			for i, p := range lines {
				if n := utf8.RuneCountInString(p); n > seedMaxSuggestedPromptLength {
					t.Errorf("prompt %d exceeds %d characters (it has %d): %q",
						i+1, seedMaxSuggestedPromptLength, n, p)
				}
				if seen[p] {
					t.Errorf("prompt %d is a duplicate: %q", i+1, p)
				}
				seen[p] = true
				for _, filler := range genericFiller {
					if strings.EqualFold(strings.TrimSpace(p), filler) {
						t.Errorf("prompt %d is the generic fallback chip %q — the seeded "+
							"questions are meant to be the ones this agent's owner would "+
							"otherwise type every morning", i+1, p)
					}
				}
			}
			// The raw block is what the seeder PATCHes. If it needs
			// normalising to be storable, the file is not what gets stored and
			// a reader of agents.yaml is looking at something else.
			if raw != strings.Join(lines, "\n")+"\n" && raw != strings.Join(lines, "\n") {
				t.Errorf("suggested_prompts is not already canonical (trimmed lines, no "+
					"blank lines, LF) — stored value would differ from agents.yaml:\n%q", raw)
			}
		})
	}
}

// The trap this whole change is about, stated as data: at least one agent
// carries BOTH columns, so the seeder's create-then-PATCH path is exercised
// for both at once.
func TestSeedAgents_OneAgentCarriesPromptsAndAForm(t *testing.T) {
	t.Parallel()
	for _, a := range Agents {
		if strings.TrimSpace(a.SuggestedPrompts) != "" && a.AskFormsSlug != "" {
			return
		}
	}
	t.Fatal("no seeded agent has both suggested_prompts and ask_forms — one must, " +
		"because both columns are dropped by POST /api/v1/agents and only the " +
		"follow-up PATCH sets them")
}

// Every seeded ask form is parsed and validated by the SAME package the server
// runs on the way in. A form that fails here is a form that would 400 the seed.
func TestSeedAgents_AskFormsValidateLikeTheServerWould(t *testing.T) {
	t.Parallel()
	seeded := 0
	for _, a := range Agents {
		a := a
		if a.AskFormsSlug == "" {
			continue
		}
		seeded++
		t.Run(a.Slug, func(t *testing.T) {
			t.Parallel()
			raw := AgentAskForms(a.AskFormsSlug)
			forms, err := askforms.Parse(raw)
			if err != nil {
				t.Fatalf("askforms.Parse: %v\n(this is verbatim what the server "+
					"would reply with when the seed PATCHes it)", err)
			}
			if len(forms) == 0 {
				t.Fatal("ask_forms_slug is set but the document holds no form")
			}
			if len(forms) > askforms.MaxForms {
				t.Errorf("%d forms, at most %d are allowed", len(forms), askforms.MaxForms)
			}

			// The stored document must already be canonical, so what the
			// console shows back after the first save is byte-identical to
			// what is in the repo.
			canonical, err := askforms.Normalize(raw)
			if err != nil {
				t.Fatalf("askforms.Normalize: %v", err)
			}
			if strings.TrimSpace(raw) != canonical {
				t.Errorf("the seeded JSON is not canonical — re-save it as what "+
					"askforms.Normalize returns, or the first PATCH silently rewrites it:\n%s", canonical)
			}

			for _, f := range forms {
				if utf8.RuneCountInString(f.ID) > askforms.MaxIDRunes {
					t.Errorf("form %q: id exceeds %d characters", f.ID, askforms.MaxIDRunes)
				}
				if n := utf8.RuneCountInString(f.Label); n > askforms.MaxLabelRunes {
					t.Errorf("form %q: label exceeds %d characters (it has %d)",
						f.ID, askforms.MaxLabelRunes, n)
				}
				if n := utf8.RuneCountInString(f.Template); n > askforms.MaxTemplateRunes {
					t.Errorf("form %q: template exceeds %d characters (it has %d)",
						f.ID, askforms.MaxTemplateRunes, n)
				}
				if len(f.Fields) > askforms.MaxFieldsPerForm {
					t.Errorf("form %q: %d fields, at most %d are allowed",
						f.ID, len(f.Fields), askforms.MaxFieldsPerForm)
				}
				if len(f.Fields) < 3 {
					t.Errorf("form %q: %d fields — a demo form is meant to show a "+
						"questionnaire, not a single box", f.ID, len(f.Fields))
				}

				types := map[string]bool{}
				attachment := false
				for _, fl := range f.Fields {
					types[fl.Type] = true
					if askforms.IsAttachmentType(fl.Type) {
						attachment = true
					}
					if n := utf8.RuneCountInString(fl.Label); n > askforms.MaxLabelRunes {
						t.Errorf("form %q: field %q label exceeds %d characters (it has %d)",
							f.ID, fl.Name, askforms.MaxLabelRunes, n)
					}
				}
				if len(types) < 2 {
					t.Errorf("form %q: every field is a %v — the demo form is supposed to "+
						"exercise more than one field type", f.ID, types)
				}
				if !attachment {
					t.Errorf("form %q: no file or photo field — the attachment path is "+
						"then never demonstrated by the seed", f.ID)
				}
			}
		})
	}
	if seeded == 0 {
		t.Fatal("no seeded agent has an ask form — a fresh workspace then has no " +
			"questionnaire at all, which is the state this seed change exists to end")
	}
}

// A seeded form has to accept an answer set a demo user could actually
// produce: the required fields filled in and nothing else. ValidateAnswers is
// the same check the composer and `crewship agent ask-preview` run at submit,
// so a form that fails here is one whose chip opens and never closes.
func TestSeedAgents_AskFormsAcceptTheirRequiredAnswers(t *testing.T) {
	t.Parallel()
	for _, a := range Agents {
		a := a
		if a.AskFormsSlug == "" {
			continue
		}
		t.Run(a.Slug, func(t *testing.T) {
			t.Parallel()
			forms, err := askforms.Parse(AgentAskForms(a.AskFormsSlug))
			if err != nil {
				t.Fatalf("askforms.Parse: %v", err)
			}
			for _, f := range forms {
				values := askforms.Values{}
				for _, fl := range f.Fields {
					if !fl.Required {
						continue
					}
					switch {
					case fl.Type == "select" || fl.Type == "multiselect":
						values[fl.Name] = fl.Options[0]
					case fl.Type == "checkbox":
						values[fl.Name] = true
					case fl.Type == "number" || fl.Type == "money":
						values[fl.Name] = 1
					default:
						values[fl.Name] = "answer for " + fl.Name
					}
				}
				if problems := askforms.ValidateAnswers(f, values); len(problems) > 0 {
					for _, p := range problems {
						t.Errorf("form %q refuses its own required-only answers: %s", f.ID, p.Message)
					}
				}
			}
		})
	}
}

// A form only renders what its template names, and the template is validated
// against the field list at save time. Rendering one here with every field
// answered is the cheap end-to-end check that the message a demo user would
// send is the message the author meant — no stray braces, no dropped answer.
func TestSeedAgents_AskFormsRenderEveryAnswer(t *testing.T) {
	t.Parallel()
	for _, a := range Agents {
		a := a
		if a.AskFormsSlug == "" {
			continue
		}
		t.Run(a.Slug, func(t *testing.T) {
			t.Parallel()
			forms, err := askforms.Parse(AgentAskForms(a.AskFormsSlug))
			if err != nil {
				t.Fatalf("askforms.Parse: %v", err)
			}
			for _, f := range forms {
				values := askforms.Values{}
				for _, fl := range f.Fields {
					switch fl.Type {
					case "checkbox":
						values[fl.Name] = true
					case "select", "multiselect":
						values[fl.Name] = fl.Options[0]
					case "number", "money":
						values[fl.Name] = 1
					default:
						values[fl.Name] = "answer-" + fl.Name
					}
					if fl.Type == "money" {
						values[askforms.CurrencyPlaceholder(fl.Name)] = "CZK"
					}
				}
				msg := askforms.Render(f, values, "chat_seed")
				if strings.Contains(msg, "{{") || strings.Contains(msg, "}}") {
					t.Errorf("form %q rendered with braces still in it:\n%s", f.ID, msg)
				}
				if strings.TrimSpace(msg) == "" {
					t.Errorf("form %q rendered an empty message", f.ID)
				}
				for _, fl := range f.Fields {
					if fl.Required && !strings.Contains(msg, "answer-"+fl.Name) &&
						!askforms.IsAttachmentType(fl.Type) && fl.Type != "select" &&
						fl.Type != "multiselect" && fl.Type != "checkbox" &&
						fl.Type != "number" && fl.Type != "money" {
						t.Errorf("form %q: required field %q is not in the template, so its "+
							"answer would be collected and then thrown away", f.ID, fl.Name)
					}
				}
			}
		})
	}
}
