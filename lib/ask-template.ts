/**
 * Ask-form template rendering — the composer's half of a renderer that
 * exists twice.
 *
 * The other half is internal/askforms/render.go, for the server and
 * `crewship agent ask-preview`. Both are tested against ONE golden fixture,
 * testdata/ask-templates.json, read by lib/__tests__/ask-template.test.ts and
 * by internal/askforms/render_test.go. That is not belt-and-braces: two
 * implementations that can silently disagree about what the user is sending
 * is the defect class the fixture exists to prevent
 * (docs/prd/agent-ask-packs-and-document-intake.md §7, and the reasoning in
 * docs/prd/documentation-contract-testing.md).
 *
 * Why twice at all: the preview the user approves has to render without a
 * round trip — a preview that needs the network is a preview nobody opens —
 * and the CLI has to render the same message for anyone testing a template
 * without a browser.
 *
 * The grammar is {{field}} substitution and nothing else. No conditionals, no
 * loops, no expressions. The output is a user message, and a template
 * language is a program whose bugs get sent to somebody's agent.
 *
 * ─── The one piece of magic ─────────────────────────────────────────────
 *
 * An empty optional value drops the WHOLE LINE it sits on, static label and
 * all, as long as no other placeholder on that line produced anything. So
 *
 *     Supplier: {{supplier}}
 *     Category: {{category}}
 *
 * with no category sends "Supplier: Vodafone" — not that plus a dangling
 * "Category:". It is the only rule an author has to be told about, which is
 * why the config tab says it in words next to the editor.
 *
 * Everything a definition could get wrong — a placeholder naming no field, a
 * form with no fields, a select with no options — was refused when the form
 * was SAVED (PRD §7 rule 1, internal/askforms/forms.go). Rendering therefore
 * cannot fail and has no error path: the user must never meet a broken
 * template, and an error mid-send is exactly that.
 */

/** One input in a form. Extends the `SlashFormField` shape the slash palette
 *  already uses, rather than inventing a second one.
 *
 *  `type` is intentionally a plain string. PRD §6.1 names text, textarea,
 *  number, money, date, month, select, multiselect, checkbox, file and photo,
 *  but an unrecognised type falls back to a text input exactly as
 *  components/features/chat/composer/slash-action-modal.tsx documents — and
 *  that fallback is what lets the server add a field type without a
 *  coordinated frontend release. */
export interface AskFormField {
  name: string
  label: string
  label_cs?: string
  type: string
  required?: boolean
  placeholder?: string
  help?: string
  default?: unknown
  options?: string[]
  /** Offered next to a `money` amount. A money field named `amount` answers
   *  to TWO placeholders: `{{amount}}` for the number and
   *  `{{amount_currency}}` for the currency. Deriving the second name from
   *  the first is what stops two money fields fighting over one
   *  `{{currency}}`. */
  currency?: string[]
  multiple?: boolean
  min?: number
  max?: number
  pattern?: string
}

/** One questionnaire: a chip that opens a sheet, plus the template its
 *  answers are rendered into. Submitting sends an ordinary user message. */
export interface AskForm {
  id: string
  label: string
  label_cs?: string
  icon?: string
  template: string
  /** `none` | `optional` | `required`. */
  attachment?: string
  fields: AskFormField[]
}

/** The answers, keyed by field name. A money field named `amount` takes its
 *  currency under `amount_currency`; a file or photo field takes the list of
 *  file names (or the paths the upload response already returned). */
export type AskValues = Record<string, string | string[] | number | boolean | null | undefined>

// The caps. Enforced SERVER-side (internal/askforms) — these exist so the UI
// can show a count and so the two renderers cannot drift on the two that
// change what gets sent. testdata/ask-templates.json carries the same numbers
// and both test suites assert against it.
export const MAX_FORMS = 4
export const MAX_FIELDS_PER_FORM = 6
export const MAX_LABEL_RUNES = 48
export const MAX_TEMPLATE_RUNES = 2000
export const MAX_VALUE_RUNES = 2000
export const MAX_MESSAGE_RUNES = 32000

/** Deliberately lax about what sits between the braces. A strict name pattern
 *  would simply not match `{{ not a name }}`, which would then survive into
 *  the message as literal braces — the one thing the user must never see.
 *  Matching it here lets the server refuse it on save and lets this renderer
 *  drop it. */
const PLACEHOLDER = /\{\{([^{}]*)\}\}/g

/** Leading/trailing space, tab and newline — and only those. `String.trim()`
 *  and Go's `strings.TrimSpace` disagree about a handful of Unicode spaces
 *  and about U+FEFF, and "the two renderers disagree" is the one outcome this
 *  module exists to prevent. */
const EDGE_WHITESPACE = /^[ \t\n]+|[ \t\n]+$/g

/** The second name a money field answers to. */
export function currencyPlaceholder(fieldName: string): string {
  return `${fieldName}_currency`
}

/**
 * Render one form plus one set of answers into the message that will be sent.
 *
 * `chatId` is the chat the attachments were uploaded into: file and photo
 * values render as the agent-visible path `attachments/<chatId>/<name>`, the
 * form fixed by PRD §7.4 and already used by lib/attachment-message.ts.
 */
export function renderAskTemplate(form: AskForm, values: AskValues, chatId: string): string {
  const byName = new Map<string, AskFormField>()
  for (const field of form.fields ?? []) {
    byName.set(field.name, field)
    if (field.type === "money") {
      const cur = currencyPlaceholder(field.name)
      byName.set(cur, { name: cur, label: cur, type: "text" })
    }
  }

  const kept: string[] = []
  for (const line of normalizeTemplate(form.template ?? "").split("\n")) {
    // A fresh matcher per line: PLACEHOLDER is global and therefore stateful.
    const spans = [...line.matchAll(PLACEHOLDER)]
    if (spans.length === 0) {
      // Static text is never dropped, including when it is blank — the blank
      // lines in a template are the author's paragraph breaks.
      kept.push(line)
      continue
    }

    const rendered = spans.map((m) => renderValue(byName, m[1].trim(), values, chatId))
    if (rendered.every((v) => v === "")) {
      // The magic: every placeholder on this line came back empty, so the
      // line had no dynamic content of its own and goes with them.
      continue
    }

    let out = ""
    let last = 0
    spans.forEach((m, i) => {
      const start = m.index ?? 0
      out += line.slice(last, start) + rendered[i]
      last = start + m[0].length
    })
    out += line.slice(last)
    kept.push(out)
  }

  // Cap first, then trim: the cap is a hard guarantee about what leaves here,
  // and trimming after it keeps the result stable wherever the cut landed.
  return trimEdges(truncateRunes(kept.join("\n"), MAX_MESSAGE_RUNES))
}

/** Pick a form out of an agent's list. */
export function findAskForm(forms: AskForm[], formId: string): AskForm | undefined {
  return forms.find((f) => f.id === formId)
}

function renderValue(
  byName: Map<string, AskFormField>,
  name: string,
  values: AskValues,
  chatId: string,
): string {
  const field = byName.get(name)
  // Unreachable through the API — an unknown placeholder is refused on save.
  // Defined anyway: a definition that predates the validator, or one edited
  // straight in the database, must degrade to an empty value and a dropped
  // line, never to visible braces.
  if (!field) return ""

  const parts = coerceValue(values[name])
  if (parts.length === 0) return ""

  const joined =
    field.type === "file" || field.type === "photo"
      ? // One path per line, unquoted — the same choice
        // lib/attachment-message.ts made and for the same reason: spaces,
        // quotes and brackets are common in filenames and the line break is
        // the only delimiter none of them can forge. The "- " bullet that
        // module adds belongs to its own block, which supplies its own
        // lead-in sentence; inside a template the author writes the lead-in.
        parts.map((p) => attachmentPath(chatId, p)).join("\n")
      : parts.join(", ")

  return truncateRunes(joined, MAX_VALUE_RUNES)
}

/** Flatten whatever the caller passed into the non-empty strings it is made
 *  of. Strings are what the composer sends; numbers and booleans are accepted
 *  so a CLI `--var` and a hand-written body need not quote everything. */
function coerceValue(value: unknown): string[] {
  if (value === null || value === undefined) return []
  if (typeof value === "string") {
    const cleaned = cleanValue(value)
    return cleaned === "" ? [] : [cleaned]
  }
  if (typeof value === "boolean") {
    // A ticked box reads "yes"; an unticked one is empty, so its line drops
    // like any other empty optional value. Rendering "no" would put a
    // negative claim in the user's message that they never typed.
    return value ? ["yes"] : []
  }
  if (typeof value === "number") {
    const n = formatNumber(value)
    return n === "" ? [] : [n]
  }
  if (Array.isArray(value)) return value.flatMap((item) => coerceValue(item))
  return []
}

/** The agent-visible path (PRD §7.4). The agent's working directory IS its
 *  output directory, so the RELATIVE path opens with no further guessing —
 *  the reasoning is written out in lib/attachment-message.ts.
 *
 *  A value that already carries the prefix passes through: the upload
 *  response hands the composer `attachments/<chatId>/<file>` directly, and
 *  prefixing that twice names a file that does not exist. */
function attachmentPath(chatId: string, name: string): string {
  if (name.startsWith("attachments/")) return name
  return chatId ? `attachments/${chatId}/${name}` : `attachments/${name}`
}

function cleanValue(s: string): string {
  return trimEdges(stripControl(foldNewlines(s)))
}

function normalizeTemplate(t: string): string {
  return stripControl(foldNewlines(t))
}

/** CR is folded rather than dropped, so a value pasted from Windows keeps its
 *  line breaks instead of losing them. */
function foldNewlines(s: string): string {
  return s.replace(/\r\n/g, "\n").replace(/\r/g, "\n")
}

/** C0 control characters and DEL go; the newline stays. A newline is content
 *  in a textarea and the separator in a file list, while every other control
 *  character survives no round trip a user can see — and a stray one would
 *  split a line the author never wrote. Same rule, same reason, as
 *  lib/attachment-message.ts applies to a path.
 *
 *  Written as a code-point walk rather than a character class so it is
 *  visibly the same three conditions as strings.Map in
 *  internal/askforms/render.go. Two regexes in two dialects claiming to mean
 *  the same set is how the halves drift. */
function stripControl(s: string): string {
  let out = ""
  for (const ch of s) {
    if (ch === "\n") {
      out += ch
      continue
    }
    const code = ch.codePointAt(0) ?? 0
    if (code < 0x20 || code === 0x7f) continue
    out += ch
  }
  return out
}

function trimEdges(s: string): string {
  return s.replace(EDGE_WHITESPACE, "")
}

/** Runes, not UTF-16 code units. `s.slice(0, 2000)` on 2500 emoji keeps 1000
 *  of them and splits a surrogate pair; Go counting bytes keeps 500. The
 *  shared fixture pins exactly that case. */
function truncateRunes(s: string, max: number): string {
  if (s.length <= max) return s
  const runes = Array.from(s)
  return runes.length <= max ? s : runes.slice(0, max).join("")
}

/** Plain decimal, no exponent — the form both languages can agree on. Values
 *  big or small enough for JavaScript to switch to exponent notation are
 *  outside what a money or number field collects; send those as strings. */
function formatNumber(n: number): string {
  if (!Number.isFinite(n)) return ""
  if (Number.isInteger(n) && Math.abs(n) < 1e21) return n.toFixed(0)
  return String(n)
}

/** What the config tab needs to show a count, and what the composer needs to
 *  read the column: a tolerant parse that never throws.
 *
 *  Tolerant on purpose — the server is the enforcement (internal/askforms),
 *  and a UI that refuses to render because a byte is off leaves the author
 *  with no way back. `error` is a message to show; `forms` is whatever could
 *  be salvaged, which for malformed JSON is nothing. */
export function parseAskForms(raw?: string | null): { forms: AskForm[]; error?: string } {
  const text = (raw ?? "").trim()
  if (text === "") return { forms: [] }
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch (err) {
    return { forms: [], error: err instanceof Error ? err.message : "not valid JSON" }
  }
  if (!Array.isArray(parsed)) {
    return { forms: [], error: "ask forms must be a JSON array of form definitions" }
  }
  const forms = parsed.filter((f): f is AskForm => !!f && typeof f === "object")
  if (forms.length !== parsed.length) {
    return { forms, error: "every entry must be a form definition" }
  }
  return { forms }
}

/** Counts for the authoring UI. The UI shows them; it does not enforce them —
 *  a field that silently will not submit is worse than a specific error from
 *  the server. */
export function summarizeAskForms(raw?: string | null): {
  forms: number
  fields: number
  tooManyForms: boolean
  overFullForms: string[]
  error?: string
} {
  const { forms, error } = parseAskForms(raw)
  const fields = forms.reduce((n, f) => n + (Array.isArray(f.fields) ? f.fields.length : 0), 0)
  return {
    forms: forms.length,
    fields,
    tooManyForms: forms.length > MAX_FORMS,
    overFullForms: forms
      .filter((f) => (Array.isArray(f.fields) ? f.fields.length : 0) > MAX_FIELDS_PER_FORM)
      .map((f) => f.id || "(no id)"),
    error,
  }
}
