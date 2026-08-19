/**
 * A routine's inputs, as a form.
 *
 * Two surfaces need this and they arrive from different directions:
 *
 *   · the slash palette gets a `form_schema` the server already
 *     translated (internal/api/slash_routine_catalog.go), and needs the
 *     RETURN leg — form strings back into the typed `inputs` map the
 *     routine runs on;
 *   · the routines detail panel holds the routine's raw definition, and
 *     needs both legs — the same InputSpec→field translation the server
 *     does, then the same conversion back.
 *
 * So the translation lives here once, in both directions, rather than
 * twice in two components. The out-leg is a deliberate mirror of the Go
 * function; `__tests__/routine-inputs.test.ts` pins the pairs the server
 * test pins, so the two cannot drift silently.
 *
 * Why a conversion at all: a form field holds a string, but a routine's
 * inputs reach a `code` step with their ORIGINAL types (the CEL runner
 * exposes them as the `inputs` map so expressions can do typed
 * arithmetic). A routine declaring `{"name":"limit","type":"integer"}`
 * and comparing `inputs.limit > 20` FAILS the run outright when 42
 * arrives as the string "42" — verified against a live routine, not
 * assumed. Nothing rejects it earlier: run-time input validation does
 * not exist, so `type` is honoured by whatever consumes the value and by
 * nothing before it. That is what makes this conversion load-bearing
 * rather than tidy.
 */

import type { SlashFormField } from "@/hooks/use-slash-commands"

/** Marks a catalog entry as "run this routine". Mirrors
 *  slashRoutineIDPrefix in internal/api/slash_routine_catalog.go. */
export const SLASH_ROUTINE_ID_PREFIX = "routine.run:"

/** One declared input, as it appears in a routine's definition JSON. */
export interface RoutineInputSpec {
  name: string
  /** JSON data type: string | integer | number | boolean | array | object. */
  type?: string
  required?: boolean
  default?: unknown
  description?: string
}

/**
 * The routine slug inside a catalog id, or null for any other id.
 *
 * The single place the client decides "this entry runs a routine" — by
 * reading the prefix the server put there, never by guessing from a name
 * a routine author chose. Note `/routine` (the platform command that
 * SCHEDULES one) is not this: the two differ by a character that a
 * `startsWith("routine")` would have run together.
 */
export function routineSlugFromSlashId(id: string): string | null {
  if (!id.startsWith(SLASH_ROUTINE_ID_PREFIX)) return null
  const slug = id.slice(SLASH_ROUTINE_ID_PREFIX.length)
  return slug === "" ? null : slug
}

/**
 * The word a user types, derived from the catalog id.
 *
 * The platform catalog types as it is named ("issue" → /issue). A
 * per-routine entry is offered under its bare slug, so
 * `routine.run:msn-etn-podklady` shows as `/msn-etn-podklady` — what a
 * person can be told to type and will remember.
 */
export function slashCommandName(id: string): string {
  return routineSlugFromSlashId(id) ?? id
}

/**
 * The widget that collects a routine input of this data type. Mirrors
 * slashWidgetForInputType in internal/api/slash_routine_catalog.go.
 */
export function widgetForInputType(inputType: string | undefined): string {
  switch (inputType) {
    case "string":
      return "text"
    case "integer":
    case "number":
      return "number"
    case "boolean":
      return "boolean"
    case "array":
    case "object":
      return "textarea"
    default:
      // Undeclared, or a type from a newer DSL than this build. A text
      // box the server can validate beats rendering nothing.
      return "text"
  }
}

/**
 * A declared default, as the string a form field can hold.
 *
 * Mirrors formatInputDefault in internal/api/slash_routine_catalog.go,
 * including the part that matters: a number renders without acquiring a
 * decimal point or an exponent it never had, so 42 stays "42" and
 * 1000000 does not become "1e+06".
 */
export function formatInputDefault(value: unknown): string {
  if (value === null || value === undefined) return ""
  if (typeof value === "string") return value
  if (typeof value === "boolean") return value ? "true" : "false"
  if (typeof value === "number") {
    // Number.prototype.toString already gives the shortest round-tripping
    // form for every value in the range a definition can hold, EXCEPT
    // that it switches to exponent notation at 1e21 — well past any
    // plausible routine input, and the textarea path would carry such a
    // value anyway.
    return Number.isFinite(value) ? String(value) : ""
  }
  try {
    return JSON.stringify(value) ?? ""
  } catch {
    return ""
  }
}

/**
 * A routine's declared inputs as form fields — the same translation the
 * server performs for the slash catalog, for the surface that holds the
 * definition itself rather than a catalog entry.
 */
export function slashFieldsFromRoutineInputs(
  inputs: RoutineInputSpec[] | undefined,
): SlashFormField[] {
  if (!inputs?.length) return []
  return inputs
    // An input with no name cannot be keyed by a value, so it is not a
    // field — the same drop the server makes.
    .filter((i) => typeof i?.name === "string" && i.name !== "")
    .map((i) => ({
      name: i.name,
      type: widgetForInputType(i.type),
      required: Boolean(i.required),
      default: formatInputDefault(i.default),
      value_type: i.type ?? "",
      help: i.description,
    }))
}

/** Reading a routine's `inputs` out of its definition JSON, defensively:
 *  the definition is a `Record<string, unknown>` on this side and may be
 *  anything at all. */
export function routineInputSpecs(
  definition: Record<string, unknown> | undefined | null,
): RoutineInputSpec[] {
  const raw = definition?.inputs
  if (!Array.isArray(raw)) return []
  return raw.filter(
    (i): i is RoutineInputSpec =>
      typeof i === "object" && i !== null && typeof (i as RoutineInputSpec).name === "string",
  )
}

/** Thrown when a typed value cannot be restored from what the user
 *  typed. Carries the field name so the caller can point at the box. */
export class RoutineInputError extends Error {
  readonly field: string
  constructor(field: string, message: string) {
    super(`${field}: ${message}`)
    this.name = "RoutineInputError"
    this.field = field
  }
}

/**
 * One form string, parsed into the JSON type the routine declared.
 *
 * An unknown or absent value_type yields the string unchanged: a catalog
 * entry from a server newer than this build, and every field in the
 * static slash catalog, is a string and always was.
 */
export function coerceRoutineInput(
  valueType: string | undefined,
  raw: string,
  field = "value",
): unknown {
  switch (valueType) {
    case "integer": {
      // Number() accepts "4.5" and " 42 " and ""; an integer input is
      // narrower than that, and nothing downstream will say so — a
      // fractional value simply flows into the routine as declared-int.
      if (!/^[+-]?\d+$/.test(raw.trim())) {
        throw new RoutineInputError(field, `"${raw}" is not a whole number`)
      }
      return Number(raw.trim())
    }
    case "number": {
      const n = Number(raw.trim())
      if (raw.trim() === "" || !Number.isFinite(n)) {
        throw new RoutineInputError(field, `"${raw}" is not a number`)
      }
      return n
    }
    case "boolean": {
      // This list and parseSlashBool in internal/cli/slash_server.go are
      // the same list, and have to stay that way: a user is told one
      // command, and `/pack dry=yes` must not work in chat and error in
      // the repl.
      //
      // "" is what an UNTICKED checkbox emits (FormField's encoding), and
      // it means false. A checkbox has no third state to express "leave
      // this one to the routine's default" with, so reading its empty
      // string as "unset" would be reading it as something the control
      // cannot say.
      const v = raw.trim().toLowerCase()
      if (["true", "1", "yes", "on"].includes(v)) return true
      if (["false", "0", "no", "off", ""].includes(v)) return false
      throw new RoutineInputError(field, `"${raw}" is not true or false`)
    }
    case "array":
    case "object": {
      let parsed: unknown
      try {
        parsed = JSON.parse(raw)
      } catch {
        throw new RoutineInputError(field, `"${raw}" is not valid JSON`)
      }
      // JSON.parse accepts any document, so a `42` typed into a field
      // declared as an object parses fine and then reaches the routine
      // as a number, where it fails — or worse, quietly does not.
      // Check the shape here, where the message can name the box the
      // user is looking at.
      if (valueType === "array" && !Array.isArray(parsed)) {
        throw new RoutineInputError(field, `"${raw}" is valid JSON but not an array — try ["a","b"]`)
      }
      if (
        valueType === "object" &&
        (typeof parsed !== "object" || parsed === null || Array.isArray(parsed))
      ) {
        throw new RoutineInputError(
          field,
          `"${raw}" is valid JSON but not an object — try {"key":"value"}`,
        )
      }
      return parsed
    }
    default:
      return raw
  }
}

/**
 * Whether a required field has been left unanswered.
 *
 * A blank-string check is the obvious implementation and it is wrong for
 * exactly one type. A checkbox emits `""` when unticked, so
 * `{"name":"confirm","type":"boolean","required":true}` would render a
 * box that reports "required" until it is TICKED — leaving no way at all
 * to submit the answer `false`, which is half of what a boolean is for.
 *
 * A boolean is therefore never missing: both of its states are answers.
 * (The CLI never had this bug — its required check is presence-based —
 * so this is also what keeps the two clients agreeing about the same
 * routine.)
 */
export function isMissingRequired(field: SlashFormField, value: string | undefined): boolean {
  if (!field.required) return false
  if (field.value_type === "boolean") return false
  return !value?.trim()
}

/**
 * The form's values as the `inputs` map the run endpoint expects.
 *
 * Two rules beyond the type mapping:
 *
 *   · An empty value is OMITTED, never sent as "". The routine's own
 *     default then applies server-side, which is the only place that
 *     knows what it is — for msn-etn-podklady an empty `obdobi` means
 *     "the previous month", and sending "" would replace that with a
 *     blank period.
 *
 *     A BOOLEAN is the exception and always sends. Its control is a
 *     checkbox with exactly two states, so its empty string is an
 *     unticked box — a deliberate false — and not the "I left this one
 *     alone" that an empty text box is. Omitting it would let a
 *     `default: true` overrule somebody who had just unticked it.
 *   · A value for something the schema doesn't declare goes through as a
 *     string. The user supplied it deliberately, and the server
 *     rejecting an undeclared input says so better than a silent
 *     client-side drop.
 *
 * Throws RoutineInputError on the first value that cannot be restored,
 * so the caller can keep the form open and point at the field.
 */
export function routineInputsFromValues(
  fields: SlashFormField[] | undefined,
  values: Record<string, string>,
): Record<string, unknown> {
  const byName = new Map((fields ?? []).map((f) => [f.name, f]))
  const out: Record<string, unknown> = {}
  for (const [name, raw] of Object.entries(values)) {
    const field = byName.get(name)
    if (raw === "" && field?.value_type !== "boolean") continue
    out[name] = field ? coerceRoutineInput(field.value_type, raw, name) : raw
  }
  return out
}
