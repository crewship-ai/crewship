package askforms

// Field types, and the one rule that decides what happens to a type this
// release has never heard of.
//
// ─── The tension, and how it is resolved ──────────────────────────────────
//
// The type list is OPEN by design. `slash-action-modal.tsx` has always
// rendered an unrecognised type as a text input, the ask sheet inherited the
// property, and PRD §6.1 keeps it deliberately: it is what lets the server
// ship a new field type without a coordinated frontend release.
//
// That fallback is also a way to lie. A definition using a secret-like type —
// `password`, `api_key`, `client_secret` — renders as an ordinary input in a
// console that has not learned the type yet, and the value the user types
// lands verbatim in a durable chat message, in the transcript, in the search
// mirror, and in whatever the agent does with it. The user reasonably believed
// the field had special handling. It had none.
//
// So the list stays open, and the dangerous half fails closed:
//
//	known   the renderer has a control for it
//	open    never heard of it, but the NAME is inert — render a text input
//	unsafe  the name says the value is a secret, or the name is shaped so that
//	        nothing can be trusted to render it at all
//
// `unsafe` is refused when the form is SAVED (Validate, below) — which is the
// actual guarantee, because it means the server may never ship a type the
// client would mishandle — and it also fails closed in the sheet, for the one
// case a validator cannot reach: a row written before this rule existed, or
// edited straight in the database. The console renders no input for it and
// refuses to submit, naming the field.
//
// The rule is stated once, in testdata/ask-field-types.json, and read by this
// package's tests and by lib/__tests__/ask-validate.test.ts. It is a different
// fixture from testdata/ask-templates.json on purpose: that one pins the two
// RENDERERS to each other, and none of this is a rendering rule.

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Verdict is what may be done with a field type.
type Verdict string

const (
	// TypeKnown — this release implements a control for it.
	TypeKnown Verdict = "known"
	// TypeOpen — unrecognised, and inert enough to render as a text input.
	TypeOpen Verdict = "open"
	// TypeUnsafe — must not be saved, and must not be rendered if it already
	// was.
	TypeUnsafe Verdict = "unsafe"
)

// Reasons a type is unsafe, as the fixture spells them.
const (
	ReasonSensitive  = "sensitive"
	ReasonUnnameable = "unnameable"
)

// MaxTypeRunes caps a type name. A type is a short identifier the console
// switches on; anything longer is not a type the server invented, it is a
// value that ended up in the wrong key.
const MaxTypeRunes = 32

// fieldTypeRE is what a type name may look like. Hyphens are allowed (form
// ids already use them and a hyphenated type is inert), uppercase is not: a
// switch on `type` is case-sensitive in both languages, so `Text` would take
// the fallback branch while looking to the author like the known type.
var fieldTypeRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// KnownFieldTypes are the types with a control of their own — PRD §6.1, and
// the switch in components/features/chat/asks/form-field.tsx. Pinned to the
// fixture from both directions, so a type added to one renderer and not the
// other is a red run rather than a control that silently degrades to text.
var KnownFieldTypes = map[string]bool{
	"text":        true,
	"textarea":    true,
	"number":      true,
	"money":       true,
	"date":        true,
	"month":       true,
	"select":      true,
	"multiselect": true,
	"checkbox":    true,
	"file":        true,
	"photo":       true,
}

// sensitiveSubstrings match anywhere in the type name once separators are
// removed, so `client_secret`, `apikey` and `MyPasswordThing` all land in the
// same bucket. Deliberately broad: the cost of a false positive is an author
// renaming a field type, and the cost of a false negative is a credential in
// somebody's transcript.
var sensitiveSubstrings = []string{
	"secret", "password", "passwd", "passphrase",
	"credential", "token", "apikey", "privatekey", "oauth", "cvv",
}

// sensitiveTokens match a whole `_`/`-` separated word. These are the short
// ones — matching them as substrings would close the list on `monkey` and
// `keyword`, which is how a safety rule stops being taken seriously.
var sensitiveTokens = map[string]bool{
	"key": true, "pin": true, "otp": true, "totp": true,
	"ssn": true, "pwd": true, "auth": true,
}

// ClassifyFieldType returns the verdict for one type name, and — for an unsafe
// one — which of the two reasons applies.
//
// Order matters: the shape check runs first, so `Secret` is reported as
// unnameable rather than sensitive. Both refuse it; the author needs to be
// told the one thing that is actually wrong with what they typed, and a
// capitalised type is a typo before it is a policy problem.
func ClassifyFieldType(fieldType string) (Verdict, string) {
	if fieldType == "" || utf8.RuneCountInString(fieldType) > MaxTypeRunes || !fieldTypeRE.MatchString(fieldType) {
		return TypeUnsafe, ReasonUnnameable
	}
	if isSensitiveTypeName(fieldType) {
		return TypeUnsafe, ReasonSensitive
	}
	if KnownFieldTypes[fieldType] {
		return TypeKnown, ""
	}
	return TypeOpen, ""
}

func isSensitiveTypeName(fieldType string) bool {
	flat := strings.NewReplacer("_", "", "-", "").Replace(fieldType)
	for _, s := range sensitiveSubstrings {
		if strings.Contains(flat, s) {
			return true
		}
	}
	for _, token := range strings.FieldsFunc(fieldType, func(r rune) bool { return r == '_' || r == '-' }) {
		if sensitiveTokens[token] {
			return true
		}
	}
	return false
}

// SafeFieldType reports whether a field may be rendered and answered at all.
func SafeFieldType(fieldType string) bool {
	v, _ := ClassifyFieldType(fieldType)
	return v != TypeUnsafe
}

// IsAttachmentType — the two types whose answer is an upload rather than
// something typed.
func IsAttachmentType(fieldType string) bool {
	return fieldType == "file" || fieldType == "photo"
}

// SanitizeValues drops the answers to fields that fail closed.
//
// Belt to the validator's braces, for the one case it cannot reach: a
// definition stored before this rule existed. Whatever a CLI `--var` or an old
// client hands over, the value of an unsafe-typed field does not reach Render,
// the envelope, or anything downstream of them. Returns a new map; the
// caller's is not touched, because a caller that also logs its own input would
// otherwise log something different from what it passed.
func SanitizeValues(f Form, values Values) Values {
	out := make(Values, len(values))
	unsafe := map[string]bool{}
	for _, field := range f.Fields {
		if !SafeFieldType(field.Type) {
			unsafe[field.Name] = true
		}
	}
	for name, v := range values {
		if unsafe[name] {
			continue
		}
		out[name] = v
	}
	return out
}
