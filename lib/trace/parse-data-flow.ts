import type { DataFlowEdge, TraceStep } from "./types"

// parseDataFlow — extract `{{ steps.X.output[.path] }}` references
// from every input field of every step in a DSL.
//
// The Go side validates and resolves these via a single regex
// (`internal/pipeline/dsl.go:templateRE` matching `{{ ... }}` and
// `checkTemplateRef` parsing the inner ref). We mirror the same
// shape on the FE — no need for the backend to ship resolved
// dependencies, since the DSL has all the info we need at hand.
//
// Returns a list of edges with the source step id, target step id,
// and the JSON path after `.output` (used as the edge label).

// Match `{{ steps.<id>.output[.path] }}`. The inner pattern allows
// whitespace around the inner expression, mirrors the Go regex
// (`\{\{\s*([^{}]+?)\s*\}\}`) but only for the step-ref shape we
// care about. Other refs (`{{ inputs.X }}`) are ignored — they
// don't create inter-step edges.
const STEP_REF_RE = /\{\{\s*steps\.([A-Za-z0-9_-]+)\.output(\.[^\s{}]+)?\s*\}\}/g

interface RefHit {
  fromStepId: string
  path: string // including leading "." or "" if just `.output`
}

function scan(value: string | undefined | null, hits: RefHit[]): void {
  if (!value) return
  STEP_REF_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = STEP_REF_RE.exec(value)) !== null) {
    hits.push({
      fromStepId: m[1],
      path: m[2] ?? "",
    })
  }
}

function scanRecord(rec: Record<string, unknown> | undefined, hits: RefHit[]): void {
  if (!rec) return
  for (const v of Object.values(rec)) {
    if (typeof v === "string") scan(v, hits)
    // Inputs values can also be nested; we don't recurse here
    // because the DSL only allows scalar string inputs at the
    // top level. Match server-side coverage.
  }
}

// scanStep — find every step ref inside a single step's inputs.
// Coverage matches `validateTemplatesInStep` on the Go side:
//   - agent_run: prompt
//   - http: url, body, headers values
//   - code: code body, env values
//   - wait: until (datetime), event_filter
//   - transform: input, expression
//   - call_pipeline: nested input values (when string)
function scanStep(step: TraceStep, hits: RefHit[]): void {
  switch (step.type) {
    case "agent_run":
      scan(step.prompt, hits)
      break
    case "http":
      scan(step.http?.url, hits)
      scan(step.http?.body, hits)
      scanRecord(step.http?.headers, hits)
      break
    case "code":
      scan(step.code?.code, hits)
      scanRecord(step.code?.env, hits)
      break
    case "wait":
      scan(step.wait?.until, hits)
      scan(step.wait?.approval_prompt, hits)
      break
    case "transform":
      scan(step.transform?.input, hits)
      scan(step.transform?.expression, hits)
      break
    case "call_pipeline":
      // inputs is Record<string, unknown> — only string values can
      // contain template refs. Match server-side coverage.
      if (step.inputs) {
        for (const v of Object.values(step.inputs)) {
          if (typeof v === "string") scan(v, hits)
        }
      }
      break
  }
}

// parseDataFlowEdges — top-level helper. Walks every step in a DSL
// and emits one DataFlowEdge per (source step → this step) pair.
//
// Dedup behavior: the same source can be referenced multiple times
// from one target (e.g. `{{ steps.fetch.output.body }}` AND
// `{{ steps.fetch.output.url }}`). We collapse by (from, to) and
// pick the most-specific path (longest) for the label, since the
// shorter ones are usually `.output` placeholders.
export function parseDataFlowEdges(steps: TraceStep[]): DataFlowEdge[] {
  const knownIds = new Set(steps.map((s) => s.id))
  const grouped = new Map<string, RefHit>()

  for (const step of steps) {
    const hits: RefHit[] = []
    scanStep(step, hits)
    for (const hit of hits) {
      // Skip refs to unknown step IDs — those would have been
      // rejected at save time by the Go validator. Defensive
      // anyway in case the DSL was edited between fetches.
      if (!knownIds.has(hit.fromStepId)) continue
      // Self-references are nonsensical and would create a self-loop
      // in the canvas. The Go validator rejects these too.
      if (hit.fromStepId === step.id) continue

      const key = `${hit.fromStepId}->${step.id}`
      const existing = grouped.get(key)
      if (!existing || hit.path.length > existing.path.length) {
        grouped.set(key, { fromStepId: hit.fromStepId, path: hit.path })
      }
    }
    void step.id // (intentional — `step` reference clarity above)
  }

  const edges: DataFlowEdge[] = []
  for (const [key, ref] of grouped) {
    const [, target] = key.split("->", 2)
    edges.push({ from: ref.fromStepId, to: target, path: ref.path })
  }
  return edges
}

// formatEdgeLabel — turn a raw `.body.url` path into a chip-friendly
// label. Empty path (just `.output`) shows as "output". Otherwise
// trim the leading dot.
export function formatEdgeLabel(path: string): string {
  if (!path) return "output"
  return path.replace(/^\./, "")
}
