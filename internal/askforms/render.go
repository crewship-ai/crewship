package askforms

// Template rendering — the server and CLI half of a renderer that exists
// twice (PRD §7). The other half is lib/ask-template.ts, for the composer.
//
// Two implementations of the same thing is a smell, and here it is a
// deliberate one: the composer must render the preview the user approves
// WITHOUT a round trip (a preview that needs the network is a preview nobody
// opens), and the server must render the same message for `crewship agent
// ask-preview` and for anyone testing a template without a browser. What is
// not negotiable is that they agree, so both are tested against ONE golden
// fixture, testdata/ask-templates.json, at the repo root. A rule added here
// and not there — or a cap moved on one side — is a red run in both suites.
//
// The grammar is {{field}} substitution and nothing else. No conditionals,
// no loops, no expressions. That is not a simplification to be regretted
// later: the output is a user message, and a template language is a program
// whose bugs get sent to somebody's agent.
//
// ─── The one piece of magic ────────────────────────────────────────────────
//
// An empty optional value drops the WHOLE LINE it sits on, static label and
// all, as long as no other placeholder on that line produced anything. So
//
//	Supplier: {{supplier}}
//	Category: {{category}}
//
// with no category sends "Supplier: Vodafone" — not "Supplier: Vodafone" plus
// a dangling "Category:". This is the only rule an author has to be told
// about, which is why it is stated in the config tab, in docs/cli/agent.mdx,
// and in the API reference rather than only here.

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	// MaxValueRunes caps ONE substituted value. Characters, not bytes, and
	// not UTF-16 units either — the fixture pins an emoji case precisely
	// because a naive JS slice and a naive Go byte slice both get it wrong.
	MaxValueRunes = 2000
	// MaxMessageRunes caps the finished message. With six fields at 2000 and
	// a 2000-rune template a valid form cannot reach it, so this is a
	// backstop for definitions that never met the validator — a row written
	// before it existed, or by hand.
	MaxMessageRunes = 32000
)

// placeholderRE is deliberately lax about what sits between the braces. A
// strict [a-z_][a-z0-9_]* pattern would simply not match {{ not a name }},
// which would then survive into the rendered message as literal braces — the
// exact thing the user must never see. Matching it here means save-time
// validation can refuse it (checkPlaceholders) and the renderer can drop it.
var placeholderRE = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

// Values are the answers, keyed by field name. A money field named `amount`
// takes its currency under `amount_currency`.
//
// Accepted value types: string, []string, bool, and JSON numbers. Anything
// else renders as empty. Strings are what the composer actually sends;
// numbers and bools are accepted so a CLI `--var` and a hand-written JSON
// body do not have to quote everything.
type Values map[string]any

// Render turns one form plus one set of answers into the message that will be
// sent. It cannot fail: every way a definition could produce nonsense was
// refused at save time (Validate), and everything else has a defined empty
// behaviour. A renderer that can return an error is a renderer whose error
// ends up in front of a user mid-send.
//
// chatID is the chat the attachments were uploaded into; file and photo
// values render as the agent-visible path attachments/<chatId>/<name>, the
// form fixed by PRD §7.4 and already used by lib/attachment-message.ts.
func Render(f Form, values Values, chatID string) string {
	byName := make(map[string]Field, len(f.Fields)+1)
	for _, fl := range f.Fields {
		byName[fl.Name] = fl
		if fl.Type == "money" {
			cur := CurrencyPlaceholder(fl.Name)
			byName[cur] = Field{Name: cur, Type: "text"}
		}
	}

	lines := strings.Split(normalizeTemplate(f.Template), "\n")
	kept := make([]string, 0, len(lines))

	for _, line := range lines {
		spans := placeholderRE.FindAllStringSubmatchIndex(line, -1)
		if len(spans) == 0 {
			// A line with no placeholder is static text. It is never dropped,
			// including when it is blank: the blank lines in a template are
			// the author's paragraph breaks.
			kept = append(kept, line)
			continue
		}

		rendered := make([]string, len(spans))
		anyFilled := false
		for i, s := range spans {
			name := strings.TrimSpace(line[s[2]:s[3]])
			rendered[i] = renderValue(byName, name, values, chatID)
			if rendered[i] != "" {
				anyFilled = true
			}
		}
		if !anyFilled {
			// The magic, stated once: every placeholder on this line came
			// back empty, so the line had no dynamic content of its own and
			// goes away with them.
			continue
		}

		var b strings.Builder
		last := 0
		for i, s := range spans {
			b.WriteString(line[last:s[0]])
			b.WriteString(rendered[i])
			last = s[1]
		}
		b.WriteString(line[last:])
		kept = append(kept, b.String())
	}

	// Cap first, then trim: the cap is a hard guarantee about what leaves
	// here, and trimming after it keeps the result stable regardless of where
	// the cut landed.
	return trimEdges(truncateRunes(strings.Join(kept, "\n"), MaxMessageRunes))
}

// RenderByID picks a form out of an agent's list and renders it. This is what
// `crewship agent ask-preview` drives, so the error names the id and lists
// what is actually there — a preview that answers "not found" and stops is a
// second lookup the operator has to do by hand.
func RenderByID(forms []Form, formID string, values Values, chatID string) (string, error) {
	for _, f := range forms {
		if f.ID == formID {
			return Render(f, values, chatID), nil
		}
	}
	ids := make([]string, 0, len(forms))
	for _, f := range forms {
		ids = append(ids, f.ID)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no form %q — this agent has no ask forms configured", formID)
	}
	return "", fmt.Errorf("no form %q — this agent has: %s", formID, strings.Join(ids, ", "))
}

// FormByID is the lookup on its own, for callers that want the definition
// (the CLI prints the field list when asked for a form it cannot render).
func FormByID(forms []Form, formID string) (Form, bool) {
	for _, f := range forms {
		if f.ID == formID {
			return f, true
		}
	}
	return Form{}, false
}

func renderValue(byName map[string]Field, name string, values Values, chatID string) string {
	fl, known := byName[name]
	if !known {
		// Unreachable through the API — an unknown placeholder is refused at
		// save time. Kept defined anyway: a definition that predates the
		// validator, or one edited straight in the database, must degrade to
		// an empty value and a dropped line, never to visible braces.
		return ""
	}

	parts := coerceValue(values[name])
	if len(parts) == 0 {
		return ""
	}

	var out string
	if fl.Type == "file" || fl.Type == "photo" {
		paths := make([]string, 0, len(parts))
		for _, p := range parts {
			paths = append(paths, attachmentPath(chatID, p))
		}
		// One path per line, unquoted — the same choice
		// lib/attachment-message.ts made and for the same reason: spaces,
		// quotes and brackets are common in filenames and the line break is
		// the only delimiter none of them can forge. The "- " bullet that
		// module adds belongs to its own block, which supplies its own
		// lead-in sentence; inside a template the author writes the lead-in.
		out = strings.Join(paths, "\n")
	} else {
		out = strings.Join(parts, ", ")
	}
	return truncateRunes(out, MaxValueRunes)
}

// coerceValue flattens whatever the caller passed into the list of non-empty
// strings the value is made of.
func coerceValue(v any) []string {
	switch val := v.(type) {
	case nil:
		return nil
	case string:
		if s := cleanValue(val); s != "" {
			return []string{s}
		}
	case bool:
		// A ticked box reads "yes"; an unticked one is empty, so the line it
		// sits on drops like any other empty optional value. Rendering
		// "no" instead would put a negative claim in the user's message that
		// they never typed.
		if val {
			return []string{"yes"}
		}
	case float64:
		return []string{formatNumber(val)}
	case int:
		return []string{strconv.Itoa(val)}
	case []string:
		out := make([]string, 0, len(val))
		for _, s := range val {
			if c := cleanValue(s); c != "" {
				out = append(out, c)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			out = append(out, coerceValue(item)...)
		}
		return out
	}
	return nil
}

// attachmentPath is the agent-visible path (PRD §7.4). The agent's working
// directory IS its output directory, so the RELATIVE path opens with no
// further guessing — see the reasoning in lib/attachment-message.ts.
//
// A value that already carries the prefix is passed through: the upload
// response hands the composer `attachments/<chatId>/<file>` directly, and
// prefixing that a second time would name a file that does not exist.
func attachmentPath(chatID, name string) string {
	if strings.HasPrefix(name, "attachments/") {
		return name
	}
	if chatID == "" {
		return "attachments/" + name
	}
	return "attachments/" + chatID + "/" + name
}

// cleanValue strips what must never reach the wire and normalises what can.
//
// Control characters go (PRD §7.3). A newline does not: it is content in a
// textarea and the separator in a file list. CR is folded rather than dropped
// so a value pasted from Windows keeps its line breaks instead of losing
// them.
func cleanValue(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return trimEdges(stripControl(s))
}

// normalizeTemplate applies the same treatment to the stored template. The
// write path already folds and strips, so this is for rows that predate it.
func normalizeTemplate(t string) string {
	t = strings.ReplaceAll(t, "\r\n", "\n")
	t = strings.ReplaceAll(t, "\r", "\n")
	return stripControl(t)
}

func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7F {
			return -1
		}
		return r
	}, s)
}

// trimEdges removes leading and trailing space, tab and newline — and only
// those. strings.TrimSpace and JavaScript's String.trim disagree about a
// handful of Unicode spaces (and about U+FEFF), and "the two renderers
// disagree" is the one outcome this package exists to prevent.
func trimEdges(s string) string { return strings.Trim(s, " \t\n") }

func truncateRunes(s string, max int) string {
	if len(s) <= max {
		// Fast path: a byte length at or under the cap can never be more
		// runes than the cap.
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// formatNumber renders a JSON number the way both languages can agree on:
// plain decimal, no exponent. Values large or small enough for JavaScript to
// switch to exponent notation (|x| >= 1e21, or very small fractions) are
// outside what a money or number field collects; send those as strings.
func formatNumber(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return ""
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e21 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
