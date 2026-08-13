/**
 * Ask-form field types and answer constraints — the console's half of a rule
 * that also lives in `internal/askforms` (fieldtypes.go, answers.go), pinned
 * to it by `testdata/ask-field-types.json`.
 *
 * ─── Why this file exists (audit P0.7) ─────────────────────────────────────
 *
 * A form DEFINITION could always say `min`, `max`, `pattern` and `multiple`.
 * The path where a user actually answers checked `required` and nothing else.
 * A constraint that is only ever stated is not a constraint; it is a comment
 * the author believes, and the first person to find out otherwise reads it in
 * the data.
 *
 * ─── The unknown-type rule ─────────────────────────────────────────────────
 *
 * The type list is OPEN by design: an unrecognised type renders a text input,
 * which is what lets the server ship a field type without a coordinated
 * frontend release (PRD §6.1). That fallback is also a way to lie — a
 * definition using `password` or `api_key` renders as an ordinary input, and
 * what the user types lands verbatim in a durable chat message.
 *
 * So the list stays open and the dangerous half fails closed:
 *
 *   known   there is a control for it (form-field.tsx switches on it)
 *   open    never heard of it, but the NAME is inert — text input
 *   unsafe  the name says the value is a secret, or is shaped so that nothing
 *           can be trusted to render it
 *
 * `unsafe` is refused when the form is SAVED — that is the guarantee, and it
 * is the server's, because only the write path can promise the server will
 * never ship such a type. This file is the second half of it, for the case a
 * validator cannot reach: a row written before the rule existed, or edited
 * straight in the database. The sheet renders no input for it and refuses to
 * submit, naming the field.
 *
 * Nothing here is a RENDERING rule, which is why it is not in
 * `lib/ask-template.ts`: that module and `internal/askforms/render.go` are
 * pinned to each other by `testdata/ask-templates.json`, and a constraint
 * change must not have to touch the fixture whose whole job is keeping the two
 * renderers byte-identical.
 */

import type { AskForm, AskFormField, AskValues } from "@/lib/ask-template"

/** What may be done with a field type. */
export type AskFieldVerdict = "known" | "open" | "unsafe"

/** Why an unsafe type is unsafe. Spelled as the shared fixture spells them. */
export type AskFieldUnsafeReason = "" | "sensitive" | "unnameable"

/** A type name is a short identifier the console switches on. Hyphens are
 *  allowed (inert, and form ids already use them); uppercase is not, because
 *  the switch is case-sensitive in both languages, so `Text` would silently
 *  take the fallback branch while reading to its author like the known type. */
export const ASK_TYPE_SHAPE = /^[a-z][a-z0-9_-]*$/

/** Anything longer is not a type the server invented — it is a value that
 *  ended up in the wrong key. */
export const MAX_ASK_TYPE_RUNES = 32

/** The types with a control of their own (PRD §6.1, and the switch in
 *  components/features/chat/asks/form-field.tsx). Pinned to the fixture from
 *  both directions, so a type added to one renderer and not the other is a red
 *  run rather than a control that silently degrades to a text box. */
export const KNOWN_ASK_FIELD_TYPES: ReadonlySet<string> = new Set([
  "text",
  "textarea",
  "number",
  "money",
  "date",
  "month",
  "select",
  "multiselect",
  "checkbox",
  "file",
  "photo",
])

/** Matched anywhere in the name once `_` and `-` are removed, so
 *  `client_secret`, `apikey` and `oauthtoken` land in one bucket. Deliberately
 *  broad: a false positive costs an author a rename, a false negative costs a
 *  credential in somebody's transcript. */
const SENSITIVE_SUBSTRINGS = [
  "secret",
  "password",
  "passwd",
  "passphrase",
  "credential",
  "token",
  "apikey",
  "privatekey",
  "oauth",
  "cvv",
]

/** Matched as a whole `_`/`-` separated word. These are the short ones —
 *  matching them as substrings would close the list on `monkey` and
 *  `keyword`, which is how a safety rule stops being taken seriously. */
const SENSITIVE_TOKENS = new Set(["key", "pin", "otp", "totp", "ssn", "pwd", "auth"])

/**
 * The verdict for one type name, and — for an unsafe one — which reason.
 *
 * Shape first, so `Secret` is reported as unnameable rather than sensitive.
 * Both refuse it; a capitalised type is a typo before it is a policy problem
 * and the author needs to be told the thing that is actually wrong.
 */
export function classifyAskFieldType(fieldType: string): {
  verdict: AskFieldVerdict
  reason: AskFieldUnsafeReason
} {
  const type = fieldType ?? ""
  if (type === "" || runeLength(type) > MAX_ASK_TYPE_RUNES || !ASK_TYPE_SHAPE.test(type)) {
    return { verdict: "unsafe", reason: "unnameable" }
  }
  if (isSensitiveTypeName(type)) return { verdict: "unsafe", reason: "sensitive" }
  if (KNOWN_ASK_FIELD_TYPES.has(type)) return { verdict: "known", reason: "" }
  return { verdict: "open", reason: "" }
}

function isSensitiveTypeName(fieldType: string): boolean {
  const flat = fieldType.replace(/[_-]/g, "")
  if (SENSITIVE_SUBSTRINGS.some((s) => flat.includes(s))) return true
  return fieldType.split(/[_-]/).some((token) => SENSITIVE_TOKENS.has(token))
}

/** Whether a field may be rendered and answered at all. */
export function isSafeAskFieldType(fieldType: string): boolean {
  return classifyAskFieldType(fieldType).verdict !== "unsafe"
}

/** The two types whose answer is an upload rather than something typed. */
export function isAskAttachmentType(fieldType: string): boolean {
  return fieldType === "file" || fieldType === "photo"
}

/** One violated rule, attributed to the field that violated it. `field` is the
 *  machine handle; `message` is the sentence a user reads, and it names the
 *  field's LABEL — "something is missing" over six inputs is not an error. */
export interface AskAnswerError {
  field: string
  code: AskAnswerErrorCode
  message: string
}

export type AskAnswerErrorCode =
  | "required"
  | "min"
  | "max"
  | "number"
  | "pattern"
  | "multiple"
  | "options"
  | "type_unsafe"

/**
 * Check one set of answers against one form.
 *
 * At most one problem per field — the first thing wrong with an input is the
 * thing to fix, and three sentences about one field read as three problems.
 * Across fields it does not stop at the first: a caller showing one toast
 * takes `[0]`, a caller with room shows them all.
 */
export function validateAskAnswers(form: AskForm, values: AskValues): AskAnswerError[] {
  const out: AskAnswerError[] = []
  for (const field of form.fields ?? []) {
    const problem = validateAskAnswer(field, values[field.name])
    if (problem) out.push(problem)
  }
  return out
}

export function validateAskAnswer(
  field: AskFormField,
  raw: AskValues[string],
): AskAnswerError | null {
  const label = askFieldLabel(field)
  // A field with no type at all is a text field — the same default both
  // renderers apply, and an author who left the key out is not the case the
  // fail-closed rule is about. (The definition validator refuses it outright,
  // with a better message; this is for answers checked against a row that
  // never met it.)
  const type = field.type && field.type !== "" ? field.type : "text"
  const fail = (code: AskAnswerErrorCode, message: string): AskAnswerError => ({
    field: field.name,
    code,
    message,
  })

  if (classifyAskFieldType(type).verdict === "unsafe") {
    // Fails closed, and says what is wrong with the FORM rather than with the
    // answer: the user did nothing wrong and cannot fix this one.
    return fail(
      "type_unsafe",
      `${label} cannot be answered here — the form asks for a value of type ${type}, which a chat message cannot carry safely.`,
    )
  }

  const list = answerList(raw)
  const single = list.length > 0 ? list[0] : ""

  if (isAskAttachmentType(type)) {
    if (field.required && list.length === 0) {
      return fail("required", `${label} is required — attach a file before sending.`)
    }
    if (list.length === 0) return null
    // `multiple` is about the CONTROL, so it is checked before the counts:
    // "takes one file" is truer than "at most 1 file" when the author never
    // wrote a max.
    if (field.multiple === false && list.length > 1) {
      return fail("multiple", `${label} takes one file — remove the extra ones.`)
    }
    if (field.min !== undefined && list.length < field.min) {
      return fail("min", `${label} needs at least ${countOf(field.min, "file")}.`)
    }
    if (field.max !== undefined && list.length > field.max) {
      return fail("max", `${label} takes at most ${countOf(field.max, "file")}.`)
    }
    return null
  }

  if (type === "multiselect") {
    if (field.required && list.length === 0) return fail("required", `${label} is required.`)
    if (list.length === 0) return null
    if (!inOptions(field, list)) {
      return fail("options", `${label} must be one of its listed options.`)
    }
    if (field.min !== undefined && list.length < field.min) {
      return fail("min", `${label} needs at least ${countOf(field.min, "option")}.`)
    }
    if (field.max !== undefined && list.length > field.max) {
      return fail("max", `${label} takes at most ${countOf(field.max, "option")}.`)
    }
    return null
  }

  if (type === "checkbox") {
    // A required checkbox is a consent box: unticked is not an answer.
    if (field.required && !truthy(raw)) return fail("required", `${label} is required.`)
    return null
  }

  if (type === "select") {
    if (single.trim() === "") {
      return field.required ? fail("required", `${label} is required.`) : null
    }
    if (!inOptions(field, list)) {
      return fail("options", `${label} must be one of its listed options.`)
    }
    return null
  }

  if (type === "number" || type === "money") {
    if (single.trim() === "") {
      return field.required ? fail("required", `${label} is required.`) : null
    }
    const n = Number(single.trim())
    if (single.trim() === "" || Number.isNaN(n) || !Number.isFinite(n)) {
      return fail("number", `${label} must be a number.`)
    }
    if (field.min !== undefined && n < field.min) {
      return fail("min", `${label} must be at least ${formatBound(field.min)}.`)
    }
    if (field.max !== undefined && n > field.max) {
      return fail("max", `${label} must be at most ${formatBound(field.max)}.`)
    }
    return null
  }

  // text, textarea, date, month, and every OPEN type — all of which reach the
  // user as a text input, so all of which are checked as one.
  if (single.trim() === "") {
    return field.required ? fail("required", `${label} is required.`) : null
  }
  const length = runeLength(single)
  if (field.min !== undefined && length < field.min) {
    return fail("min", `${label} must be at least ${countOf(field.min, "character")}.`)
  }
  if (field.max !== undefined && length > field.max) {
    return fail("max", `${label} must be at most ${countOf(field.max, "character")}.`)
  }
  if (field.pattern) {
    const re = compilePattern(field.pattern)
    // A pattern that does not compile was refused on save; one that reaches
    // here came from a row that predates the validator, and refusing every
    // answer to it would be a field nobody can fill in.
    if (re && !re.test(single)) {
      return fail("pattern", `${label} is not in the expected format.`)
    }
  }
  return null
}

/** What a message calls the field: its label, or its name with the
 *  underscores opened out. Same rule as `fieldLabelText` in
 *  components/features/chat/asks/form-field.tsx — an error naming the field
 *  differently from the label above the input names a field nobody can find. */
export function askFieldLabel(field: AskFormField): string {
  const explicit = field.label?.trim()
  return explicit ? explicit : field.name.replace(/_/g, " ")
}

/** Anchored at both ends — the same thing an HTML `pattern=` attribute means,
 *  and the only reading that does not accept `xxCZ25788001xx` for
 *  `CZ[0-9]{8}`. The pattern was compiled by Go's RE2 on the way in, which is
 *  what makes running it here safe: RE2 has no backreferences and no
 *  lookaround, so nothing that reaches this point can backtrack pathologically. */
function compilePattern(pattern: string): RegExp | null {
  try {
    return new RegExp(`^(?:${pattern})$`)
  } catch {
    return null
  }
}

function inOptions(field: AskFormField, chosen: string[]): boolean {
  if (!field.options || field.options.length === 0) return true
  const allowed = new Set(field.options)
  return chosen.every((c) => allowed.has(c))
}

function truthy(value: unknown): boolean {
  if (typeof value === "boolean") return value
  return value === "true" || value === "yes"
}

/**
 * Flatten whatever the sheet holds into the strings the answer is made of —
 * the same coercion the renderer applies, so validation and rendering can
 * never disagree about how many answers a field has or how long one is.
 *
 * The cleaning rules (fold CR, strip control characters, trim the edges) are
 * restated here rather than imported because they live inside
 * lib/ask-template.ts, which is pinned to internal/askforms/render.go by a
 * golden fixture and must not grow exports for this. testdata/ask-field-types
 * .json pins the observable result on both sides instead.
 */
function answerList(value: unknown): string[] {
  if (value === null || value === undefined) return []
  if (typeof value === "string") {
    const cleaned = cleanAnswer(value)
    return cleaned === "" ? [] : [cleaned]
  }
  if (typeof value === "boolean") return value ? ["yes"] : []
  if (typeof value === "number") {
    const n = formatBound(value)
    return n === "" ? [] : [n]
  }
  if (Array.isArray(value)) return value.flatMap((item) => answerList(item))
  return []
}

function cleanAnswer(s: string): string {
  const folded = s.replace(/\r\n/g, "\n").replace(/\r/g, "\n")
  let stripped = ""
  for (const ch of folded) {
    if (ch === "\n") {
      stripped += ch
      continue
    }
    const code = ch.codePointAt(0) ?? 0
    if (code < 0x20 || code === 0x7f) continue
    stripped += ch
  }
  return stripped.replace(/^[ \t\n]+|[ \t\n]+$/g, "")
}

/** Characters, not UTF-16 code units. A cap that counts units keeps 1000 of
 *  2500 emoji and splits a surrogate pair; the renderer counts runes, so this
 *  does too. */
function runeLength(s: string): number {
  return Array.from(s).length
}

/** "3 files" / "1 file". Both halves of the feature build this sentence, and
 *  "1 files" in a toast is what makes a user distrust the rest of it. */
function countOf(n: number, noun: string): string {
  return n === 1 ? `1 ${noun}` : `${formatBound(n)} ${noun}s`
}

/** Plain decimal, no exponent — the form both languages agree on, and the
 *  same one lib/ask-template.ts uses for a substituted number. */
function formatBound(n: number): string {
  if (!Number.isFinite(n)) return ""
  if (Number.isInteger(n) && Math.abs(n) < 1e21) return n.toFixed(0)
  return String(n)
}
