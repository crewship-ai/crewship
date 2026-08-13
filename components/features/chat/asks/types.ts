/**
 * The chat side of ask forms — types re-exported, plus the handful of view
 * helpers the rail and the sheet need.
 *
 * The definitions themselves live in `lib/ask-template.ts`, next to the
 * renderer that consumes them. They are re-exported here rather than
 * redeclared: a second `AskForm` in this directory would be a second answer to
 * "what is a form", and the first thing to drift would be the field list the
 * renderer substitutes from.
 */

import {
  parseAskForms as parseAskFormsColumn,
  type AskForm,
  type AskFormField,
  type AskValues,
} from "@/lib/ask-template"

export type { AskForm, AskFormField, AskValues }

/**
 * `{{field}}` substitution, as implemented once here and once in
 * `internal/askforms/render.go`, both pinned to `testdata/ask-templates.json`.
 *
 * The sheet takes it as a parameter rather than importing it. That keeps the
 * component testable against a function instead of a module path, and it keeps
 * the fact that there is exactly ONE renderer visible at the top of the
 * feature (chat-panel.tsx) instead of buried in a leaf.
 */
export type RenderAskTemplate = (form: AskForm, values: AskValues, chatId: string) => string

/** Field types whose control is an upload, not an input. */
export const ATTACHMENT_FIELD_TYPES = new Set(["file", "photo"])

export function isAttachmentField(field: { type: string }): boolean {
  return ATTACHMENT_FIELD_TYPES.has(field.type)
}

/** Chip labels cap at 48 characters and truncate with a native tooltip
 *  (PRD §5.1). Same number as `MAX_LABEL_RUNES`, which is what the server
 *  enforces on save; this is the render-side belt for a row that predates it. */
export const MAX_ASK_LABEL_LENGTH = 48

export function truncateAskLabel(label: string): string {
  return label.length > MAX_ASK_LABEL_LENGTH
    ? `${label.slice(0, MAX_ASK_LABEL_LENGTH - 1).trimEnd()}…`
    : label
}

/**
 * The forms a chat may actually offer.
 *
 * `parseAskForms` in lib/ask-template.ts is deliberately tolerant: it is also
 * the authoring UI's reader, where a half-written definition has to survive
 * long enough to be corrected. A CONVERSATION is the other case. A chip with
 * no label is unreadable, a form with no template sends an empty message, and
 * a form with no fields opens a sheet with nothing in it — a dead end the user
 * cannot get out of except by closing it (PRD §6).
 *
 * So the rail drops what it cannot render and shows the rest. Dropping is the
 * conservative direction: the worst case is a chip the user does not see,
 * against a chip that does something broken when tapped.
 */
export function usableAskForms(forms: AskForm[]): AskForm[] {
  const seen = new Set<string>()
  const out: AskForm[] = []
  for (const form of forms) {
    if (!form || typeof form !== "object") continue
    if (typeof form.id !== "string" || form.id === "") continue
    if (typeof form.label !== "string" || form.label.trim() === "") continue
    if (typeof form.template !== "string" || form.template.trim() === "") continue
    if (!Array.isArray(form.fields) || form.fields.length === 0) continue
    const fields = form.fields.filter(
      (f): f is AskFormField => !!f && typeof f === "object" && typeof f.name === "string" && f.name !== "",
    )
    if (fields.length === 0) continue
    if (seen.has(form.id)) continue
    seen.add(form.id)
    out.push({ ...form, label: form.label.trim(), fields })
  }
  return out
}

/** Read `agents.ask_forms` — a TEXT column holding a JSON array — into the
 *  forms a chat may offer. Never throws; anything unreadable is no forms,
 *  which is exactly the behaviour of an agent nobody has configured. */
export function askFormsFromColumn(raw: unknown): AskForm[] {
  if (raw == null) return []
  if (typeof raw !== "string") return []
  return usableAskForms(parseAskFormsColumn(raw).forms)
}

/** A field with no explicit type is a text field — the same fallback an
 *  unrecognised type gets, and for the same reason (asks/form-field.tsx). */
export function fieldType(field: { type?: string }): string {
  return typeof field.type === "string" && field.type !== "" ? field.type : "text"
}
