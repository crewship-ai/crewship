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
	// MaxPatternRunes for one field's `pattern`. A regular expression longer
	// than this is not a format check, it is an enumeration, and it has to be
	// compiled by two engines on every keystroke of a preview.
	MaxPatternRunes = 200
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
	// Multiple is a POINTER because absent and false are different answers:
	// an upload field says nothing about arity by default (several photos of
	// one invoice are one answer), and `"multiple": false` is an author
	// deliberately capping it at one. A plain bool cannot tell those apart,
	// and the difference is a constraint the submit path enforces.
	Multiple *bool `json:"multiple,omitempty"`
	// Min, Max and Pattern are the constraints ValidateAnswers applies. What
	// they MEAN depends on the field — a value range on a number, a length on
	// text, a count on an upload or a multiselect — and a combination this
	// package would not enforce is refused on save rather than stored as a
	// promise nothing keeps. See answers.go and checkConstraints.
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
}

// Form is one questionnaire: a chip that opens a sheet, and the template its
// answers are rendered into. Submitting it sends an ORDINARY USER MESSAGE
// (PRD decision 2) — not a structured payload — which is the only shape that
// works across every CLI adapter without the agent being trained for it.
type Form struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	LabelCS string `json:"label_cs,omitempty"`
	Icon    string `json:"icon,omitempty"`
	// Version is the author's revision number for this questionnaire, carried
	// into every submission envelope (envelope.go) so a transcript written
	// last month can still be read against the form that produced it — the
	// definition moves, the answers do not. Optional, and absent means 1:
	// every form stored before this field existed is the first version of
	// itself, and omitempty keeps their canonical JSON byte-identical.
	Version  int    `json:"version,omitempty"`
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

	canonicalizeOptions(forms)
	if err := Validate(forms); err != nil {
		return nil, err
	}
	return forms, nil
}

// canonicalizeOptions rewrites every option to the text an ANSWER will be
// compared against.
//
// An answer reaches this package through coerceValue → cleanValue, which
// trims the edges and strips control characters. Nothing did that to the
// option list, so `["Travel ", "Food"]` stored fine and then refused the only
// answer the form renders: the sheet shows "Travel", the user picks "Travel",
// the cleaned answer is compared against the raw "Travel " and the error says
// their choice is not one of the listed options. The definition was at fault
// and the message pointed at the user.
//
// Trimming on the way in rather than refusing is the same call Normalize
// already makes for a missing `attachment`: the author's intent is
// unambiguous, and a refusal over an invisible trailing space is a puzzle,
// not a correction. What trimming may NOT do is merge two options the author
// wrote as different ones — Validate's duplicate check reads them the same
// way, so `["Travel", "Travel "]` is refused rather than silently collapsed.
//
// This runs before Validate rather than inside Normalize so that every reader
// of a stored document sees the same option list, including one written
// before this rule existed.
func canonicalizeOptions(forms []Form) {
	for i := range forms {
		for j := range forms[i].Fields {
			for k, o := range forms[i].Fields[j].Options {
				forms[i].Fields[j].Options[k] = cleanValue(o)
			}
		}
	}
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
			// The guarantee behind the open type list (fieldtypes.go): the
			// server may not ship a type the client would mishandle, so the
			// place that has to refuse one is here, on the way in.
			if verdict, reason := ClassifyFieldType(fl.Type); verdict == TypeUnsafe {
				if reason == ReasonSensitive {
					return fmt.Errorf("%s: field %q has type %q, which names a secret — an ask "+
						"form renders into an ordinary chat message that is stored, searched and "+
						"read by the agent, so it cannot carry one. Ask for the credential in "+
						"the vault (`crewship credential add`) and reference it by name here",
						where, fl.Name, fl.Type)
				}
				return fmt.Errorf("%s: field %q has type %q, which nothing can render — a type is "+
					"lowercase letters, digits, %q or %q, starts with a letter and is at most %d "+
					"characters", where, fl.Name, fl.Type, "-", "_", MaxTypeRunes)
			}
			if err := checkConstraints(where, fl); err != nil {
				return err
			}

			switch fl.Type {
			case "select", "multiselect":
				if len(fl.Options) == 0 {
					return fmt.Errorf("%s: field %q is a %s with no options — it would "+
						"open as a picker with nothing to pick", where, fl.Name, fl.Type)
				}
				// Compared as the options will be STORED and RENDERED, not as
				// they were typed: Parse canonicalises them (cleanValue), so
				// "Travel" and "Travel " are one option wearing two spellings
				// and a picker would show the same choice twice. Reading the
				// raw text here is what let that through — and Validate is
				// callable on a hand-built form, so the rule lives here rather
				// than only on the way in.
				seenOpt := map[string]bool{}
				for _, o := range fl.Options {
					clean := cleanValue(o)
					if clean == "" {
						return fmt.Errorf("%s: field %q has a blank option", where, fl.Name)
					}
					if seenOpt[clean] {
						return fmt.Errorf("%s: field %q lists the option %q twice", where, fl.Name, clean)
					}
					seenOpt[clean] = true
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

// constraintKind is which question `min`/`max` answer for a given field.
type constraintKind int

const (
	boundsNone   constraintKind = iota // the field has no numeric constraint at all
	boundsValue                        // number, money — the VALUE must be in range
	boundsLength                       // text-ish — the LENGTH in characters
	boundsCount                        // multiselect, file, photo — how many answers
)

func boundsFor(fieldType string) constraintKind {
	switch {
	case fieldType == "number" || fieldType == "money":
		return boundsValue
	case fieldType == "multiselect" || IsAttachmentType(fieldType):
		return boundsCount
	case fieldType == "text" || fieldType == "textarea":
		return boundsLength
	case KnownFieldTypes[fieldType]:
		// date, month, select, checkbox: a bound would need calendar or
		// option semantics that ValidateAnswers does not implement.
		return boundsNone
	default:
		// An OPEN type renders as a text input, so it is checked as one.
		return boundsLength
	}
}

// checkConstraints refuses a constraint that the submit path would not
// enforce.
//
// This is the same principle as the placeholder rule, applied to the other
// half of a definition: `{{typo}}` is refused rather than rendered blank, and
// `"pattern"` on a checkbox is refused rather than stored as a promise nothing
// keeps. An author who writes a rule and is told nothing believes their form
// enforces it; the first time anyone finds out otherwise is from the data.
func checkConstraints(where string, fl Field) error {
	kind := boundsFor(fl.Type)
	if kind == boundsNone && (fl.Min != nil || fl.Max != nil) {
		which := "min"
		if fl.Min == nil {
			which = "max"
		}
		return fmt.Errorf("%s: field %q is a %s, and %s is not checked for one — remove it, or "+
			"the form would claim a rule nothing enforces", where, fl.Name, fl.Type, which)
	}
	if fl.Min != nil && fl.Max != nil && *fl.Min > *fl.Max {
		return fmt.Errorf("%s: field %q has min %s above max %s, which no answer can satisfy",
			where, fl.Name, formatBound(*fl.Min), formatBound(*fl.Max))
	}
	if kind == boundsCount || kind == boundsLength {
		for name, v := range map[string]*float64{"min": fl.Min, "max": fl.Max} {
			if v != nil && (*v < 0 || *v != float64(int64(*v))) {
				return fmt.Errorf("%s: field %q counts characters or answers, so %s must be a "+
					"whole number that is not negative (got %s)", where, fl.Name, name, formatBound(*v))
			}
		}
	}

	if fl.Pattern != "" {
		if kind != boundsLength {
			return fmt.Errorf("%s: field %q is a %s, and pattern is only matched against a typed "+
				"value — remove it, or the form would claim a rule nothing enforces",
				where, fl.Name, fl.Type)
		}
		if utf8.RuneCountInString(fl.Pattern) > MaxPatternRunes {
			return fmt.Errorf("%s: field %q has a pattern longer than %d characters",
				where, fl.Name, MaxPatternRunes)
		}
		if _, err := compilePattern(fl.Pattern); err != nil {
			return fmt.Errorf("%s: field %q has a pattern that is not a valid regular "+
				"expression: %v", where, fl.Name, err)
		}
	}

	if fl.Multiple != nil && !IsAttachmentType(fl.Type) {
		return fmt.Errorf("%s: field %q is a %s, and multiple only caps how many FILES a field "+
			"takes — a multiselect is already multi-valued, and everything else is one answer",
			where, fl.Name, fl.Type)
	}
	return nil
}

// compilePattern anchors the author's pattern at both ends, which is the same
// thing an HTML `pattern=` attribute means and the only reading that does not
// silently accept `xxCZ25788001xx` for `CZ[0-9]{8}`.
//
// Compiled with Go's RE2 on the way IN, which is what makes it safe to hand
// the same string to JavaScript's engine on the way out: RE2 has no
// backreferences and no lookaround, so a pattern that compiles here is a
// pattern the other side can run without a catastrophic-backtracking case.
// The reverse would not hold.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + pattern + ")$")
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
