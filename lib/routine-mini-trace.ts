// routine-mini-trace — pure, framework-free projection of HOW a single
// routine run flowed, rendered compactly inside the routine "Last Run"
// card. NOT the heavy interactive React Flow canvas (that lives in
// /activity); this is a read-only strip: trigger → step nodes, each
// carrying the agent's "mini calls" (its sub-spans) so a human can see
// at a glance what the run actually invoked.
//
// It reuses buildFlowNodes (label/icon/kind derivation) and the trace
// sub-span normalizers so the icons + brand logos read identically to
// the full trace. Everything here is DEFENSIVE — a malformed run or DSL
// must degrade gracefully, never throw. Unit tested in
// lib/__tests__/routine-mini-trace.test.ts.

import {
  buildFlowNodes,
  type FlowIconKey,
  type FlowNodeKind,
  type BrandIconKey,
} from "@/lib/routine-flow"
import { mapSubSpans, pickModel } from "@/lib/trace/sub-spans"
import type { SubSpanKind, SubSpanStatus } from "@/lib/trace/types"

// The slice of a run the mini-trace needs. Mirrors the GetRun wire shape
// (PipelineRun) but kept minimal + all-optional so a list-row record (no
// step_outputs / sub_spans) and a full run detail both satisfy it.
export interface MiniRun {
  status?: string
  current_step_id?: string
  failed_at_step?: string
  step_outputs?: Record<string, unknown> | null
  // Raw wire sub_spans map keyed by step id; mapSubSpans normalizes it.
  sub_spans?: Record<string, unknown> | null
}

export type MiniStepStatus = "success" | "failed" | "running" | "pending" | "none"

// One "mini call" = a single agent tool invocation inside a step (a bash
// command, a file write, an MCP tool, …). Flattened from a SubSpan into
// just what the card renders.
export interface MiniCall {
  kind: SubSpanKind
  name: string
  // Concrete tool (e.g. "ansible") — drives the brand logo when known.
  tool?: string
  durationMs?: number
  status: SubSpanStatus
  artifactPath?: string
  host?: string
}

export interface MiniTraceNode {
  id: string
  kind: FlowNodeKind
  label: string
  detail?: string
  iconKey: FlowIconKey
  brandIconKey?: BrandIconKey
  // Runtime status of this node for THIS run (the trigger is always
  // "success" — it fired). Step status derives from step_outputs /
  // current_step_id / failed_at_step / run.status.
  status: MiniStepStatus
  // Model that ran the step (from the first sub-span carrying one).
  model?: string
  // The agent's tool calls inside this step, ordered by seq. Empty for
  // non-agent steps and for older runs that captured none.
  calls: MiniCall[]
}

// deriveStatus computes one step's status for a run. Mirrors the rules in
// lib/trace/build-trace-graph statusOf, plus a "completed run with no
// per-step output → success" fallback so a finished single-agent routine
// doesn't render its only step as forever-pending.
function deriveStatus(
  run: MiniRun | null,
  stepId: string,
  index: number,
  failedIndex: number,
  isLast: boolean,
): MiniStepStatus {
  if (!run) return "none"
  const outputs = run.step_outputs
  if (outputs && typeof outputs === "object" && stepId in outputs) return "success"
  if (run.failed_at_step && run.failed_at_step === stepId) return "failed"
  if (run.current_step_id && run.current_step_id === stepId) return "running"

  // Catastrophic failure with no failed_at_step: pin the last step red.
  if (run.status === "failed" && !run.failed_at_step && !run.current_step_id && isLast) {
    return "failed"
  }

  // A known failure point lets us infer the steps around it: everything
  // before it ran (success), everything after never started (pending).
  if (failedIndex >= 0) {
    return index < failedIndex ? "success" : "pending"
  }

  // The run finished cleanly but recorded no granular output for this
  // step — treat as success rather than a misleading "pending".
  if (run.status === "completed") return "success"

  return "pending"
}

/**
 * buildMiniTrace projects a (dsl, run) pair into the compact node strip the
 * Last Run card renders: a trigger node followed by one node per DSL step,
 * each carrying its status, model, and the agent's mini-calls (sub-spans).
 * The data-flow output bookend and manifest resource nodes are intentionally
 * omitted — this is a "how did it flow + what did it call" view, not the full
 * blast-radius diagram. Pure + never throws.
 */
export function buildMiniTrace(dsl: unknown, run: MiniRun | null): MiniTraceNode[] {
  // Reuse the canonical label/icon/kind derivation. Passing no manifest
  // yields exactly: trigger → [step nodes] → output. We drop the output
  // bookend; the card is about steps + calls, not terminal I/O.
  const flow = buildFlowNodes(dsl, null).filter((n) => n.kind !== "out")

  const stepIds = flow.filter((n) => n.kind !== "trigger").map((n) => n.id)
  const failedIndex =
    run?.failed_at_step != null ? stepIds.indexOf(run.failed_at_step) : -1

  const subSpans =
    run?.sub_spans && typeof run.sub_spans === "object" && !Array.isArray(run.sub_spans)
      ? run.sub_spans
      : null

  let stepCursor = 0
  return flow.map((node) => {
    if (node.kind === "trigger") {
      return { ...node, status: "success" as MiniStepStatus, calls: [] }
    }
    const index = stepCursor++
    const spans = mapSubSpans(subSpans?.[node.id])
    const calls: MiniCall[] = spans.map((s) => ({
      kind: s.kind,
      name: s.name,
      tool: s.attributes.tool,
      durationMs: s.durationMs,
      status: s.status,
      artifactPath: s.attributes.artifact_path,
      host: s.attributes.host,
    }))
    return {
      ...node,
      status: deriveStatus(run, node.id, index, failedIndex, index === stepIds.length - 1),
      model: pickModel(spans) ?? undefined,
      calls,
    }
  })
}
