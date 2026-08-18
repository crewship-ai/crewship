package askforms

// Answer validation — the submit half of the rules the definition already
// carries.
//
// P0.7, restated: the definition model admits `min`, `max`, `pattern` and
// `multiple`, and the path where a user actually answers checked `required`
// and nothing else. A constraint that is only ever stated is not a constraint;
// it is a comment that an author believes.
//
// So the rules live here, once, and are applied by everything that turns
// answers into a message: `crewship agent ask-preview` on the server side, and
// lib/ask-validate.ts on the client side, pinned to this file by
// testdata/ask-field-types.json. Both produce the same message text for the
// same violation, because "the CLI said one thing and the sheet said another
// about the same form" is the same defect one layer up.
//
// WHERE EACH CONSTRAINT IS ENFORCED, and why:
//
//   - `required`, `min`, `max`, `pattern`, `multiple`, options membership —
//     checked wherever answers are turned into a message. There is no
//     server-side submit endpoint to enforce them at: a form submit IS an
//     ordinary chat message (PRD decision 2), and that is not being reopened
//     for a validator. What the server owns instead is the DEFINITION: a
//     constraint that this file would not enforce cannot be saved at all
//     (Validate), so "the client skipped the check" is the only remaining
//     failure and it costs a badly-formed message rather than a broken
//     guarantee.
//   - The field TYPE verdict — enforced in both places and in that order:
//     refused on save, and failed closed at submit for rows that predate the
//     rule. See fieldtypes.go.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// AnswerError is one violated rule, attributed to the field that violated it.
//
// Field is the machine handle (so a caller can focus the input); Message is
// the sentence a user reads and names the field's LABEL. "Something is
// missing" over six inputs is not an error message.
type AnswerError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e AnswerError) Error() string { return e.Message }

// Error codes. Stable strings — the fixture pins them, and a caller that wants
// to react to a kind of failure should switch on these rather than on prose.
const (
	CodeRequired   = "required"
	CodeMin        = "min"
	CodeMax        = "max"
	CodeNumber     = "number"
	CodePattern    = "pattern"
	CodeMultiple   = "multiple"
	CodeOptions    = "options"
	CodeTypeUnsafe = "type_unsafe"
)

// ValidateAnswers checks one set of answers against one form and returns every
// violation, in field order, at most one per field.
//
// One per field on purpose: the first thing wrong with a field is the thing
// its author has to fix, and three sentences about one input read as three
// separate problems. Across fields it does NOT stop at the first — a caller
// showing a single toast takes [0], and a caller with room shows them all.
func ValidateAnswers(f Form, values Values) []AnswerError {
	var out []AnswerError
	for _, field := range f.Fields {
		if err, bad := validateAnswer(field, values[field.Name]); bad {
			out = append(out, err)
		}
	}
	return out
}

func validateAnswer(field Field, raw any) (AnswerError, bool) {
	fail := func(code, message string) (AnswerError, bool) {
		return AnswerError{Field: field.Name, Code: code, Message: message}, true
	}
	label := FieldLabel(field)
	// A field with no type at all is a text field — the same default both
	// renderers apply, and an author leaving the key out is not the case the
	// fail-closed rule is about. (The definition validator refuses it outright
	// with a better message; this is for answers checked against a row that
	// never met it.)
	fieldType := field.Type
	if fieldType == "" {
		fieldType = "text"
	}

	if verdict, _ := ClassifyFieldType(fieldType); verdict == TypeUnsafe {
		// Fails closed, and says what is wrong with the FORM rather than with
		// the answer — the user did nothing wrong and cannot fix this one.
		return fail(CodeTypeUnsafe, fmt.Sprintf(
			"%s cannot be answered here — the form asks for a value of type %s, which a chat message cannot carry safely.",
			label, fieldType))
	}

	list := answerList(raw)
	single := ""
	if len(list) > 0 {
		single = list[0]
	}

	switch {
	case IsAttachmentType(fieldType):
		if field.Required && len(list) == 0 {
			return fail(CodeRequired, label+" is required — attach a file before sending.")
		}
		if len(list) == 0 {
			return AnswerError{}, false
		}
		// `multiple` is about the CONTROL, so it is checked before the counts:
		// "takes one file" is a truer sentence than "at most 1 file" when the
		// author never wrote a max.
		if field.Multiple != nil && !*field.Multiple && len(list) > 1 {
			return fail(CodeMultiple, label+" takes one file — remove the extra ones.")
		}
		if field.Min != nil && float64(len(list)) < *field.Min {
			return fail(CodeMin, fmt.Sprintf("%s needs at least %s.", label, countOf(*field.Min, "file")))
		}
		if field.Max != nil && float64(len(list)) > *field.Max {
			return fail(CodeMax, fmt.Sprintf("%s takes at most %s.", label, countOf(*field.Max, "file")))
		}

	case fieldType == "multiselect":
		if field.Required && len(list) == 0 {
			return fail(CodeRequired, label+" is required.")
		}
		if len(list) == 0 {
			return AnswerError{}, false
		}
		if !inOptions(field, list) {
			return fail(CodeOptions, label+" must be one of its listed options.")
		}
		if field.Min != nil && float64(len(list)) < *field.Min {
			return fail(CodeMin, fmt.Sprintf("%s needs at least %s.", label, countOf(*field.Min, "option")))
		}
		if field.Max != nil && float64(len(list)) > *field.Max {
			return fail(CodeMax, fmt.Sprintf("%s takes at most %s.", label, countOf(*field.Max, "option")))
		}

	case fieldType == "checkbox":
		// A required checkbox is a consent box: unticked is not an answer.
		if field.Required && !truthy(raw) {
			return fail(CodeRequired, label+" is required.")
		}

	case fieldType == "select":
		if field.Required && strings.TrimSpace(single) == "" {
			return fail(CodeRequired, label+" is required.")
		}
		if strings.TrimSpace(single) == "" {
			return AnswerError{}, false
		}
		if !inOptions(field, list) {
			return fail(CodeOptions, label+" must be one of its listed options.")
		}

	case fieldType == "number" || fieldType == "money":
		if strings.TrimSpace(single) == "" {
			if field.Required {
				return fail(CodeRequired, label+" is required.")
			}
			return AnswerError{}, false
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(single), 64)
		if err != nil {
			return fail(CodeNumber, label+" must be a number.")
		}
		if field.Min != nil && n < *field.Min {
			return fail(CodeMin, fmt.Sprintf("%s must be at least %s.", label, formatBound(*field.Min)))
		}
		if field.Max != nil && n > *field.Max {
			return fail(CodeMax, fmt.Sprintf("%s must be at most %s.", label, formatBound(*field.Max)))
		}

	default:
		// text, textarea, date, month, and every OPEN type — all of which
		// reach the user as a text input, so all of which are checked as one.
		if strings.TrimSpace(single) == "" {
			if field.Required {
				return fail(CodeRequired, label+" is required.")
			}
			return AnswerError{}, false
		}
		if n := utf8.RuneCountInString(single); field.Min != nil && float64(n) < *field.Min {
			return fail(CodeMin, fmt.Sprintf("%s must be at least %s.", label, countOf(*field.Min, "character")))
		} else if field.Max != nil && float64(n) > *field.Max {
			return fail(CodeMax, fmt.Sprintf("%s must be at most %s.", label, countOf(*field.Max, "character")))
		}
		if field.Pattern != "" {
			re, err := compilePattern(field.Pattern)
			// A pattern that does not compile was refused on save; one that
			// reaches here came from a row that predates the validator, and
			// refusing every answer to it would be a field nobody can fill in.
			if err == nil && !re.MatchString(single) {
				return fail(CodePattern, label+" is not in the expected format.")
			}
		}
	}

	return AnswerError{}, false
}

// FieldLabel is what a message calls the field: its label, or its name with
// the underscores opened out. Same rule as fieldLabelText in
// components/features/chat/asks/form-field.tsx — an error that names the field
// differently from the label above the input names a field the user cannot
// find.
func FieldLabel(field Field) string {
	if l := strings.TrimSpace(field.Label); l != "" {
		return l
	}
	return strings.ReplaceAll(field.Name, "_", " ")
}

// answerList flattens whatever the caller passed into the strings it is made
// of — the same coercion Render does, so validation and rendering can never
// disagree about how many answers a field has.
func answerList(v any) []string {
	return coerceValue(v)
}

func truthy(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val == "true" || val == "yes"
	}
	return false
}

// inOptions compares CLEANED against CLEANED. `chosen` has already been
// through coerceValue → cleanValue (answerList), so the options have to make
// the same trip or an option stored as "Travel " would refuse the "Travel"
// that the sheet renders and the user picks — an unanswerable form, with the
// error blaming their choice.
//
// The write path canonicalises the options (Parse), so a definition saved
// through the API cannot reach here misspelled. This is for the row that
// predates the rule or was edited around the API: the same call the
// uncompilable-pattern branch above makes, and for the same reason — refusing
// every answer would leave a user with a form nobody but a DBA can fix.
func inOptions(field Field, chosen []string) bool {
	if len(field.Options) == 0 {
		return true
	}
	allowed := make(map[string]bool, len(field.Options))
	for _, o := range field.Options {
		allowed[cleanValue(o)] = true
	}
	for _, c := range chosen {
		if !allowed[c] {
			return false
		}
	}
	return true
}

// countOf renders "3 files" / "1 file". Both halves of the feature build this
// sentence, and "1 files" in a toast is the kind of thing that makes a user
// distrust the rest of the message.
func countOf(n float64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return formatBound(n) + " " + noun + "s"
}

// formatBound prints a bound the way both languages agree on: plain decimal,
// no exponent, no trailing zeros. Shared with the renderer's formatNumber for
// exactly the reason that one exists.
func formatBound(v float64) string { return formatNumber(v) }
