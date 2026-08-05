// One-line summary of what a step is invoked with, for the hover card.
//
// Lives in lib rather than inside the card because it is the third
// place that has to know all ten step kinds — after the StepKind union
// and the node subtitle — and the first two both drifted from
// internal/pipeline/types.go before anyone noticed. A kind missing here
// renders a hover card with a header, a gap and "Click for full detail":
// a box that costs a hover and says nothing.

import { summarizeValue } from "@/lib/format/summarize-value"
import type { TraceStep } from "./types"

/** Most important field for the kind, or null if the step carries none. */
export function describeStepInput(step: TraceStep): string | null {
  switch (step.type) {
    case "http": {
      const method = (step.http?.method ?? "GET").toUpperCase()
      const url = step.http?.url
      return url ? `${method} ${url}` : method
    }
    case "agent_run":
      if (step.prompt) return summarizeValue(step.prompt, { maxChars: 120 })
      if (step.agent_slug) return `agent: ${step.agent_slug}`
      return null
    case "transform":
      if (step.transform?.expression) return step.transform.expression
      if (step.transform?.input) return `from ${step.transform.input}`
      return null
    case "code":
      return step.code?.runtime ? `${step.code.runtime} script` : "code"
    case "script":
      return step.script?.path ?? null
    case "query":
      return step.query?.source ? `query ${step.query.source}` : null
    case "foreach":
      return step.foreach?.items ? `for each ${step.foreach.items}` : null
    case "notify": {
      const to = step.notify?.to
      const title = step.notify?.title
      if (to && title) return `→ ${to}: ${summarizeValue(title, { maxChars: 60 })}`
      if (to) return `→ ${to}`
      return title ? summarizeValue(title, { maxChars: 90 }) : null
    }
    case "wait": {
      const kind = step.wait?.kind ?? "approval"
      if (step.wait?.approval_prompt) return `${kind} — ${step.wait.approval_prompt}`
      return kind
    }
    case "call_pipeline":
      return step.pipeline_slug ? `→ ${step.pipeline_slug}` : "sub-routine"
    default:
      return null
  }
}

/**
 * Whether the stats block has anything to render.
 *
 * The old guard asked whether the fields were `!== undefined`, but
 * buildTraceGraph hands through `null` for a step with no recorded
 * metrics — which is every step when the canvas is drawing a
 * definition rather than a run. `null !== undefined` is true, so the
 * card rendered an empty <dl> with padding: a blank band of nothing.
 *
 * Ask what will actually be drawn instead: a duration or cost above
 * zero. A step that ran instantly and cost nothing has no stats worth a
 * row either.
 */
export function hasHoverStats(payload: {
  durationMs?: number | null
  costUsd?: number | null
}): boolean {
  const dur = payload.durationMs
  const cost = payload.costUsd
  return (typeof dur === "number" && dur > 0) || (typeof cost === "number" && cost > 0)
}
