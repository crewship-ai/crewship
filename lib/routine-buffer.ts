// Is what is in the editor a routine, and if so, what?
//
// One function, used by BOTH the live validity indicator and Save.
// They used to be two: the indicator parsed in the editor's active
// format while Save called an older JSON-only validator. So the header
// said "syntax ok" over a perfectly good YAML document and Save
// answered
//
//   Unexpected token 'c', "credential"... is not valid JSON
//
// Two functions doing the same job is how they came to disagree, and
// the disagreement is invisible until someone presses the button.

import { parseDsl, type DslFormat } from "./routine-dsl-format"

export type RoutineBufferResult =
  | { ok: true; parsed: Record<string, unknown> }
  | { ok: false; message: string }

/**
 * Parse and shape-check an editor buffer.
 *
 * The shape check is the same one the server applies at the door: a
 * routine is an object with a `name` and a non-empty `steps` array.
 * Everything past that — step kinds, needs, credentials — is the
 * server's to judge, and duplicating its rules here would mean two
 * validators to keep in step.
 */
export function parseRoutineBuffer(text: string, format: DslFormat): RoutineBufferResult {
  const r = parseDsl(text, format)
  if (!r.ok) {
    return { ok: false, message: r.line ? `${r.message} (line ${r.line})` : r.message }
  }
  const v = r.value
  if (typeof v.name !== "string" || v.name.trim() === "") {
    return { ok: false, message: "missing or empty `name` field" }
  }
  if (!Array.isArray(v.steps) || v.steps.length === 0) {
    return { ok: false, message: "missing or empty `steps` array" }
  }
  return { ok: true, parsed: v }
}
