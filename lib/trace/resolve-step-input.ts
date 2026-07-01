import type { TraceStep } from "./types"

// resolveStepInput — derive a step's resolved input from the DSL +
// upstream step outputs + run inputs. Pure, never throws.
//
// The pipeline runtime resolves `{{ steps.X.output[.path] }}` and
// `{{ inputs.Y[.path] }}` server-side against the keeper credential
// store and the live run; the FE only ever sees the templated DSL.
// For the Input tab we do a best-effort *display-only* resolution
// against the data we already have on the client (run.step_outputs +
// run.inputs) so the user sees what actually flowed into the step
// instead of the raw `{{ … }}` placeholders.
//
// Security note: we resolve against run.step_outputs / run.inputs only
// — never against secret refs (`{{ secrets.X }}`), which stay literal.
// This mirrors the data-flow parser invariant in parse-data-flow.ts.

export interface ResolveContext {
  // Run-level inputs (RunDetailResponse.inputs).
  inputs?: Record<string, unknown> | null
  // Per-step outputs (run.step_outputs), keyed by step id.
  stepOutputs?: Record<string, unknown> | null
}

export interface ResolvedInputEntry {
  // Dotted field key — e.g. "prompt", "http.url", "headers.Authorization".
  key: string
  // Resolved value. Strings have `{{ … }}` refs substituted; objects /
  // arrays are deep-resolved (string leaves substituted) and passed
  // through so the renderer can hand them to a JSON viewer.
  value: unknown
  // True when the declared value carried at least one `{{ … }}`
  // template — drives a "resolved from upstream" affordance.
  hasRefs: boolean
}

// Match any `{{ … }}` template. Mirrors the Go templateRE
// (`\{\{\s*([^{}]+?)\s*\}\}`); we only resolve steps.*/inputs.* refs and
// leave everything else (e.g. `{{ secrets.X }}`) untouched.
const TEMPLATE_RE = /\{\{\s*([^{}]+?)\s*\}\}/g

interface RefResult {
  found: boolean
  value: unknown
}

// walkPath — descend dotted `a.b.c` segments through nested objects /
// arrays. Returns {found:false} the moment a segment misses so an
// unresolved ref renders as its literal `{{ … }}` rather than "null".
function walkPath(root: unknown, segments: string[]): RefResult {
  let cur: unknown = root
  for (const seg of segments) {
    if (cur === null || cur === undefined) return { found: false, value: undefined }
    if (Array.isArray(cur)) {
      const idx = Number(seg)
      if (!Number.isInteger(idx) || idx < 0 || idx >= cur.length) {
        return { found: false, value: undefined }
      }
      cur = cur[idx]
      continue
    }
    if (typeof cur === "object") {
      const rec = cur as Record<string, unknown>
      if (!(seg in rec)) return { found: false, value: undefined }
      cur = rec[seg]
      continue
    }
    // Scalar reached before path exhausted — can't descend further.
    return { found: false, value: undefined }
  }
  return { found: true, value: cur }
}

// resolveRef — resolve one inner expression (without the `{{ }}`).
//   steps.<id>.output[.path]  → ctx.stepOutputs[id] then walk path
//   inputs.<name>[.path]      → ctx.inputs[name] then walk path
// Anything else → not found (left literal by the caller).
function resolveRef(expr: string, ctx: ResolveContext): RefResult {
  const parts = expr.split(".").map((p) => p.trim()).filter(Boolean)
  if (parts.length < 2) return { found: false, value: undefined }

  if (parts[0] === "steps") {
    // steps.<id>.output[.path…]
    if (parts.length < 3 || parts[2] !== "output") {
      return { found: false, value: undefined }
    }
    const stepId = parts[1]
    const root = ctx.stepOutputs?.[stepId]
    if (root === undefined) return { found: false, value: undefined }
    return walkPath(root, parts.slice(3))
  }

  if (parts[0] === "inputs") {
    const name = parts[1]
    const root = ctx.inputs?.[name]
    if (root === undefined) return { found: false, value: undefined }
    return walkPath(root, parts.slice(2))
  }

  return { found: false, value: undefined }
}

function stringifyResolved(value: unknown): string {
  if (value === null) return "null"
  if (typeof value === "string") return value
  if (typeof value === "object") {
    try {
      return JSON.stringify(value)
    } catch {
      return String(value)
    }
  }
  return String(value)
}

interface SubstituteResult {
  value: unknown
  hasRefs: boolean
}

// substitute — resolve every `{{ … }}` in a string against ctx.
//
// Special case: a string that is *exactly* a single template resolving
// to a non-string (object/array/number) returns that raw value, so the
// renderer can show structured JSON instead of a `[object Object]`
// stringification. Mixed strings ("Hello {{ inputs.name }}") get
// per-occurrence substitution; unresolved refs stay literal.
function substitute(str: string, ctx: ResolveContext): SubstituteResult {
  TEMPLATE_RE.lastIndex = 0
  const trimmed = str.trim()
  const single = trimmed.match(/^\{\{\s*([^{}]+?)\s*\}\}$/)
  if (single) {
    const ref = resolveRef(single[1], ctx)
    // Found → return the raw resolved value (object/array stays
    // structured so the renderer shows JSON, not "[object Object]").
    // Unresolved → keep the literal so the user still sees the binding
    // it would have pulled from.
    return ref.found
      ? { value: ref.value, hasRefs: true }
      : { value: str, hasRefs: true }
  }

  let hasRefs = false
  const out = str.replace(TEMPLATE_RE, (whole, inner: string) => {
    hasRefs = true
    const ref = resolveRef(inner.trim(), ctx)
    return ref.found ? stringifyResolved(ref.value) : whole
  })
  return { value: out, hasRefs }
}

// resolveValue — deep-resolve a declared value (string / array / object).
function resolveValue(value: unknown, ctx: ResolveContext): SubstituteResult {
  if (typeof value === "string") return substitute(value, ctx)
  if (Array.isArray(value)) {
    let hasRefs = false
    const out = value.map((v) => {
      const r = resolveValue(v, ctx)
      if (r.hasRefs) hasRefs = true
      return r.value
    })
    return { value: out, hasRefs }
  }
  if (value && typeof value === "object") {
    let hasRefs = false
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      const r = resolveValue(v, ctx)
      if (r.hasRefs) hasRefs = true
      out[k] = r.value
    }
    return { value: out, hasRefs }
  }
  return { value, hasRefs: false }
}

// Declared (key, rawValue) pairs per step type. Mirrors
// scanStep/validateTemplatesInStep coverage. Empty / nullish values are
// dropped by the caller so the panel never shows blank rows.
function declaredEntries(step: TraceStep): Array<[string, unknown]> {
  switch (step.type) {
    case "agent_run":
      return [["prompt", step.prompt]]
    case "http":
      return [
        ["http.method", step.http?.method],
        ["http.url", step.http?.url],
        ["http.headers", step.http?.headers],
        ["http.body", step.http?.body],
      ]
    case "transform":
      return [
        ["transform.input", step.transform?.input],
        ["transform.expression", step.transform?.expression],
      ]
    case "code":
      return [
        ["code.runtime", step.code?.runtime],
        ["code.code", step.code?.code],
        ["code.env", step.code?.env],
      ]
    case "wait":
      return [
        ["wait.kind", step.wait?.kind],
        ["wait.until", step.wait?.until],
        ["wait.approval_prompt", step.wait?.approval_prompt],
      ]
    case "call_pipeline": {
      const entries: Array<[string, unknown]> = [["pipeline_slug", step.pipeline_slug]]
      if (step.inputs) {
        for (const [k, v] of Object.entries(step.inputs)) {
          entries.push([`inputs.${k}`, v])
        }
      }
      return entries
    }
    default:
      return []
  }
}

function isEmpty(value: unknown): boolean {
  if (value === undefined || value === null) return true
  if (typeof value === "string") return value.trim() === ""
  if (Array.isArray(value)) return value.length === 0
  if (typeof value === "object") return Object.keys(value).length === 0
  return false
}

// resolveStepInput — top-level helper. Returns the step's declared
// inputs with `{{ … }}` refs resolved against the run's step outputs +
// inputs. Never throws: a malformed step or output map yields [].
export function resolveStepInput(
  step: TraceStep,
  ctx: ResolveContext = {},
): ResolvedInputEntry[] {
  try {
    const out: ResolvedInputEntry[] = []
    for (const [key, raw] of declaredEntries(step)) {
      if (isEmpty(raw)) continue
      const { value, hasRefs } = resolveValue(raw, ctx)
      out.push({ key, value, hasRefs })
    }
    return out
  } catch {
    return []
  }
}
