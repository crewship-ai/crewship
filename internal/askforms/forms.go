// Package askforms is the storage-and-validation half of per-agent ask
// forms: the questionnaires an agent offers in the chat composer, stored as
// a JSON array in the single column agents.ask_forms.
//
// It is the second half of the same idea as agents.suggested_prompts. That
// column holds the questions with no blanks to fill in, one per line; this
// one holds the questions that need a supplier, an amount and a photo of the
// receipt before they can be asked. Both are per-agent, both ride the ordinary
// agent PATCH, and neither introduces a table, an endpoint or a pack library —
// the workspace-level library in docs/prd/agent-ask-packs-and-document-intake.md
// §6 is still waiting for its second user.
//
// Two things in here carry the design:
//
//  1. VALIDATION HAPPENS AT SAVE TIME (PRD §7 rule 1). A template naming a
//     field that does not exist is refused while the author is still looking
//     at the form. Rendering never fails, never explains, and never shows a
//     user a stray {{placeholder}} — because a definition that could do that
//     was never allowed into the column.
//
//  2. THE RENDERER EXISTS TWICE — here and in lib/ask-template.ts — and is
//     pinned by one shared fixture, testdata/ask-templates.json, that both
//     test suites read. See render.go.
package askforms

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The caps. Server-side is where they are ENFORCED; the config tab shows the
// counts so the refusal is rarely how anyone finds out, and the shared
// fixture pins them so the two renderers cannot drift apart on the two that
// affect output.
const (
	// MaxForms per agent. Four is what fits on the composer's rail without
	// the rail becoming its own screen (PRD §5.1 allows six chips total, and
	// the suggested questions share that space).
	MaxForms = 4
	// MaxFieldsPerForm. Six is the point past which a chat sheet stops being
	// a question and becomes a data-entry screen — PRD §3 N4: one form, one
	// screen, one message.
	MaxFieldsPerForm = 6
	// MaxLabelRunes for a form label and a field label, in CHARACTERS not
	// bytes. §5.1 caps chip labels at 48; a byte cap would silently give a
	// Czech or Japanese author a shorter field than an English one.
	MaxLabelRunes = 48
	// MaxTemplateRunes for one prompt template.
	MaxTemplateRunes = 2000
	// MaxIDRunes for a form id.
	MaxIDRunes = 48
)

// Attachment policies (PRD §6, attachment_policy).
const (
	AttachmentNone     = "none"
	AttachmentOptional = "optional"
	AttachmentRequired = "required"
)

var attachmentPolicies = []string{AttachmentNone, AttachmentOptional, AttachmentRequired}

var (
	// formIDRE — an id appears in a URL-ish position (`agent ask-preview
	// <slug> <form-id>`) and in the composer's state, so it is a slug.
	formIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	// fieldNameRE — a field name IS the placeholder token, so it has to be
	// something {{braces}} can hold unambiguously. Lowercase only: a
	// template that says {{Supplier}} for a field named `supplier` would
	// otherwise be a silent no-op at render time.
	fieldNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Field is one input in a form. It extends the SlashFormField shape the
// slash palette already uses (`{name, type, required?, default?}`) rather
// than inventing a second one — the field renderer is shared with
// components/features/chat/composer/slash-action-modal.tsx.
//
// Type is deliberately NOT an enum. PRD §6.1 lists text, textarea, number,
// money, date, month, select, multiselect, checkbox, file, photo — but an
// unrecognised type falls back to a text input, exactly as the slash modal
// documents at slash-action-modal.tsx:31-34, and that fallback is what lets
// the server ship a new field type without a coordinated frontend release.
// Rejecting unknown types here would throw that property away.
type Field struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	LabelCS     string   `json:"label_cs,omitempty"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	// Currency is the picker offered next to a `money` amount. A money field
	// named `amount` answers to TWO placeholders: {{amount}} for the number
	// and {{amount_currency}} for the chosen currency. Deriving the second
	// name from the first is what keeps two money fields on one form from
	// fighting over a single {{currency}}.
	Currency []string `json:"currency,omitempty"`
	Multiple bool     `json:"multiple,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Pattern  string   `json:"pattern,omitempty"`
}

// Form is one questionnaire: a chip that opens a sheet, and the template its
// answers are rendered into. Submitting it sends an ORDINARY USER MESSAGE
// (PRD decision 2) — not a structured payload — which is the only shape that
// works across every CLI adapter without the agent being trained for it.
type Form struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	LabelCS  string `json:"label_cs,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Template string `json:"template"`
	// Attachment is none | optional | required. Always written out, even at
	// its default, because the value decides whether the sheet can be
	// submitted without a file and an author reading the stored JSON should
	// not have to know what the default is.
	Attachment string  `json:"attachment"`
	Fields     []Field `json:"fields"`
}

// CurrencyPlaceholder is the second name a money field answers to.
func CurrencyPlaceholder(fieldName string) string { return fieldName + "_currency" }

// Parse decodes and validates the stored (or submitted) JSON. An empty or
// whitespace-only input, and an empty array, both mean "no forms" and are
// returned as nil with no error — the column's unset state.
func Parse(raw string) ([]Form, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	dec := json.NewDecoder(strings.NewReader(trimmed))
	// A misspelled key is the most common authoring mistake in a JSON blob
	// typed by hand, and silently dropping it produces a form that looks
	// saved and behaves differently ("attachments" for "attachment" is a
	// required document that never blocks submit). Refuse it.
	dec.DisallowUnknownFields()

	var forms []Form
	if err := dec.Decode(&forms); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return nil, fmt.Errorf("ask_forms: %s — check the spelling of that key", err.Error())
		}
		if strings.Contains(err.Error(), "cannot unmarshal") {
			return nil, fmt.Errorf("ask_forms must be a JSON array of form definitions")
		}
		return nil, fmt.Errorf("ask_forms is not valid JSON: %w", err)
	}

	if err := Validate(forms); err != nil {
		return nil, err
	}
	return forms, nil
}

// Normalize returns the canonical JSON to store, or "" for "not configured"
// (which the caller stores as NULL, so the column has one representation of
// unset — the contract suggested_prompts settled on).
//
// Canonical means: defaults spelled out, key order fixed, two-space indent.
// The authoring UI edits this text directly in a textarea, so what comes back
// after a save has to be readable — and stable, or every save would show a
// diff nobody made.
func Normalize(raw string) (string, error) {
	forms, err := Parse(raw)
	if err != nil {
		return "", err
	}
	if len(forms) == 0 {
		return "", nil
	}
	for i := range forms {
		if forms[i].Attachment == "" {
			forms[i].Attachment = AttachmentNone
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// A template is prose, not markup: "a < b" must not be stored as
	// "a < b" and shown back to the author that way.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(forms); err != nil {
		return "", fmt.Errorf("ask_forms could not be re-encoded: %w", err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// Validate applies every rule that has to hold before a definition is allowed
// near a user. Errors name the form — by id where there is one, by position
// where there is not — because a caller editing four forms needs to know
// which one to go back to.
func Validate(forms []Form) error {
	if len(forms) > MaxForms {
		return fmt.Errorf("at most %d forms are allowed per agent (got %d)", MaxForms, len(forms))
	}

	seenID := map[string]bool{}
	for i, f := range forms {
		where := fmt.Sprintf("form %d", i+1)
		if f.ID != "" {
			where = fmt.Sprintf("form %q", f.ID)
		}

		if f.ID == "" {
			return fmt.Errorf("%s: id is required — it is how the composer and "+
				"`crewship agent ask-preview` name this form", where)
		}
		if utf8.RuneCountInString(f.ID) > MaxIDRunes || !formIDRE.MatchString(f.ID) {
			return fmt.Errorf("%s: id must be lowercase letters, digits, %q or %q, "+
				"start with a letter or digit, and be at most %d characters",
				where, "-", "_", MaxIDRunes)
		}
		if seenID[f.ID] {
			return fmt.Errorf("two forms share the id %q — the id is what the "+
				"composer stores when someone opens a form, so it has to be unique", f.ID)
		}
		seenID[f.ID] = true

		if strings.TrimSpace(f.Label) == "" {
			return fmt.Errorf("%s: label is required — it is the chip's text", where)
		}
		if err := checkLabel(where, "", f.Label); err != nil {
			return err
		}
		if err := checkLabel(where, "", f.LabelCS); err != nil {
			return err
		}

		if strings.TrimSpace(f.Template) == "" {
			return fmt.Errorf("%s: template is required — a form with nothing to "+
				"render sends an empty message", where)
		}
		if n := utf8.RuneCountInString(f.Template); n > MaxTemplateRunes {
			return fmt.Errorf("%s: template exceeds %d characters (it has %d)",
				where, MaxTemplateRunes, n)
		}

		if f.Attachment != "" && f.Attachment != AttachmentNone &&
			f.Attachment != AttachmentOptional && f.Attachment != AttachmentRequired {
			return fmt.Errorf("%s: attachment must be one of %s (got %q)",
				where, strings.Join(attachmentPolicies, ", "), f.Attachment)
		}

		if len(f.Fields) == 0 {
			return fmt.Errorf("%s: has no fields — a form with nothing to fill in is a "+
				"chip that sends a fixed message, which is what suggested_prompts is for", where)
		}
		if len(f.Fields) > MaxFieldsPerForm {
			return fmt.Errorf("%s: at most %d fields are allowed (got %d)",
				where, MaxFieldsPerForm, len(f.Fields))
		}

		// known collects every name a placeholder may legitimately use:
		// each field, plus the derived currency name of each money field.
		known := map[string]bool{}
		declaredBy := map[string]string{}
		for _, fl := range f.Fields {
			if fl.Name == "" {
				return fmt.Errorf("%s: every field needs a name — the name IS the "+
					"placeholder the template uses", where)
			}
			if !fieldNameRE.MatchString(fl.Name) || utf8.RuneCountInString(fl.Name) > MaxIDRunes {
				return fmt.Errorf("%s: field name %q cannot be a placeholder — use "+
					"lowercase letters, digits and underscores, starting with a letter",
					where, fl.Name)
			}
			if declaredBy[fl.Name] != "" {
				return fmt.Errorf("%s: two fields are named %q — {{%s}} would have to "+
					"choose between them", where, fl.Name, fl.Name)
			}
			declaredBy[fl.Name] = fl.Name
			known[fl.Name] = true
		}
		for _, fl := range f.Fields {
			if strings.TrimSpace(fl.Label) == "" {
				return fmt.Errorf("%s: field %q has no label", where, fl.Name)
			}
			if err := checkLabel(where, fl.Name, fl.Label); err != nil {
				return err
			}
			if err := checkLabel(where, fl.Name, fl.LabelCS); err != nil {
				return err
			}
			if fl.Type == "" {
				return fmt.Errorf("%s: field %q has no type", where, fl.Name)
			}

			switch fl.Type {
			case "select", "multiselect":
				if len(fl.Options) == 0 {
					return fmt.Errorf("%s: field %q is a %s with no options — it would "+
						"open as a picker with nothing to pick", where, fl.Name, fl.Type)
				}
				seenOpt := map[string]bool{}
				for _, o := range fl.Options {
					if strings.TrimSpace(o) == "" {
						return fmt.Errorf("%s: field %q has a blank option", where, fl.Name)
					}
					if seenOpt[o] {
						return fmt.Errorf("%s: field %q lists the option %q twice", where, fl.Name, o)
					}
					seenOpt[o] = true
				}
			case "money":
				cur := CurrencyPlaceholder(fl.Name)
				if declaredBy[cur] != "" {
					return fmt.Errorf("%s: field %q is a money field, so %q is reserved "+
						"for its currency and cannot also be a field", where, fl.Name, cur)
				}
				for _, c := range fl.Currency {
					if strings.TrimSpace(c) == "" {
						return fmt.Errorf("%s: field %q has a blank currency", where, fl.Name)
					}
				}
				known[cur] = true
			}
		}

		// PRD §7 rule 1 — the reason this whole function runs on write.
		if err := checkPlaceholders(where, f.Template, known); err != nil {
			return err
		}
	}
	return nil
}

func checkLabel(where, fieldName, label string) error {
	n := utf8.RuneCountInString(label)
	if n <= MaxLabelRunes {
		return nil
	}
	if fieldName != "" {
		return fmt.Errorf("%s: field %q: label exceeds %d characters (it has %d)",
			where, fieldName, MaxLabelRunes, n)
	}
	return fmt.Errorf("%s: label exceeds %d characters (it has %d)", where, MaxLabelRunes, n)
}

// checkPlaceholders is PRD §7 rule 1: every {{placeholder}} must name a field
// on THIS form. The error names the placeholder and the form, because that is
// the pair the author needs — the fix is a typo away and they are still in
// the editor.
//
// The scan is deliberately lax about what is between the braces (see
// placeholderRE in render.go). {{ not a name }} and {{}} have to be caught
// here rather than skipped as "not a placeholder": whatever the renderer
// would do with them, the user would see braces.
func checkPlaceholders(where, template string, known map[string]bool) error {
	for _, m := range placeholderRE.FindAllStringSubmatch(template, -1) {
		name := strings.TrimSpace(m[1])
		if known[name] {
			continue
		}
		if name == "" {
			return fmt.Errorf("%s: template contains {{}}, which names no field", where)
		}
		if !fieldNameRE.MatchString(name) {
			return fmt.Errorf("%s: template names {{%s}}, which is not a field name — "+
				"a placeholder is lowercase letters, digits and underscores, nothing else",
				where, name)
		}
		return fmt.Errorf("%s: template names {{%s}}, which is not a field on that form",
			where, name)
	}
	return nil
}
