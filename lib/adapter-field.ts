/**
 * Reading adapter-supplied JSON.
 *
 * Anything an adapter forwards — CLI init metadata, tool payloads, per-message
 * metadata — arrives verbatim and is typed by hope: a `Record<string, unknown>`
 * that TypeScript's `as` will happily narrow to whatever the reader wanted. A
 * key that holds a string in the CLI that shipped last month can hold an object
 * in the one that ships tomorrow, and the reader finds out by calling a string
 * method on it during render.
 *
 * That is a whole class of crash, not one bug, so the coercion lives here
 * rather than in the component that hit it first: every new adapter field goes
 * through `fieldText` and costs a missing row instead of a thrown render.
 */

/** Read one adapter-supplied field as display text.
 *
 *  A value that is not already legible as a scalar reads as ABSENT, so the
 *  caller falls back the same way it does for a key that was never sent — that
 *  is the point. Never returns an empty string: an empty field and a missing
 *  field render identically, and `undefined` is the one that `??` chains and
 *  `&&` guards already handle.
 *
 *  Deliberately does NOT stringify objects and arrays. A caller that wants to
 *  show a structured value has to decide how (`describeMetaValue` in the turn
 *  renderer is one such decision); silently emitting `[object Object]` or a
 *  JSON blob into a label is worse than showing nothing. */
export function fieldText(value: unknown): string | undefined {
  if (typeof value === "string") return value.trim() || undefined
  if (typeof value === "number" || typeof value === "boolean") return String(value)
  return undefined
}
