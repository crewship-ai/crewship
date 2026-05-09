import type { Node, Edge } from "@xyflow/react"
import { Graph as DagreGraph, layout as dagreLayout } from "@dagrejs/dagre"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type {
  PipelineDSL,
  StepStatus,
  TraceDataFlowEdgeData,
  TraceStep,
  TraceStepNodeData,
  TraceTriggerNodeData,
} from "./types"
import { formatEdgeLabel, parseDataFlowEdges } from "./parse-data-flow"
import type { HeatmapBucket } from "./percentile-heatmap"
import { summarizeValue } from "@/lib/format/summarize-value"

// buildTraceGraph — turns one (run, dsl) pair into ReactFlow nodes
// and edges for the trace canvas.
//
// The graph has three logical layers:
//   1. trigger    (1 synthetic node — the run's entry point)
//   2. steps      (N step nodes, one per DSL step)
//   3. data flow  (Phase 3 — edges parsed from {{ steps.X.output }})
//
// Phase 2 emits trigger + steps + sequencing edges (from step.needs).
// Phase 3 will append data-flow edges. Layout is dagre LR (left to
// right) so the chain reads like a flowchart.

const NODE_WIDTH = 200
const NODE_HEIGHT = 70
const TRIGGER_WIDTH = 180

interface BuildTraceGraphOptions {
  selectedStepId?: string | null
  // Workspace id — required when waitpointTokensByStepId is set so
  // the inline Approve/Deny buttons can call the workspace-scoped
  // decide endpoint.
  workspaceId?: string
  // Step ID → waitpoint token for steps with a pending waitpoint.
  // The step node renders inline Approve/Deny when its id matches.
  waitpointTokensByStepId?: ReadonlyMap<string, string>
  // Pre-computed heatmap buckets keyed by step id. The caller
  // computes this once via shadeNodes() and passes it in — keeping
  // the (cheap) percentile bucketing OUT of the hot rebuild path
  // means a stepMetrics change doesn't force a full dagre relayout.
  heatmapBuckets?: ReadonlyMap<string, HeatmapBucket>
}

export interface TraceGraphData {
  nodes: Node[]
  edges: Edge[]
}

// Resolve the runtime status of a step from the run's recorded state.
// The rules mirror what the existing RunStepTree uses:
//   - id present in step_outputs   → success
//   - id === current_step_id       → running (or waiting if the step
//                                     is a wait kind)
//   - id === failed_at_step        → failed
//   - run.status === "failed" with no failed_at_step → paint the
//     current step as failed if known, else the LAST step as failed.
//     Without this, runs that errored before any step (auth failures,
//     boot recovery sweeps, etc.) render all-pending — invisible.
//   - else                         → pending
//
// Skipped is reserved — Phase 2 doesn't infer it; if/when we wire
// `if:` conditions on steps, this is where it lights up.
function statusOf(
  run: PipelineRun,
  step: TraceStep,
  steps: TraceStep[],
): StepStatus {
  if (run.failed_at_step && run.failed_at_step === step.id) return "failed"
  if (run.step_outputs && step.id in run.step_outputs) return "success"
  if (run.current_step_id === step.id) {
    return step.type === "wait" ? "waiting" : "running"
  }
  // Catastrophic-failure fallback: status=failed but failed_at_step
  // is empty. Pin the failure to current_step_id when set, otherwise
  // to the last step in the chain so the user sees SOMETHING red.
  const isTerminalFailed =
    run.status === "failed" && !run.failed_at_step && step.id !== ""
  if (isTerminalFailed) {
    if (run.current_step_id && run.current_step_id === step.id) return "failed"
    if (!run.current_step_id && steps.length > 0 && steps[steps.length - 1].id === step.id) {
      return "failed"
    }
  }
  return "pending"
}

export function buildTraceGraph(
  run: PipelineRun,
  dsl: PipelineDSL | null,
  opts: BuildTraceGraphOptions = {},
): TraceGraphData {
  const steps = dsl?.steps ?? []
  // Fall back to outputs-only when DSL is missing — the run still has
  // step_outputs keys, which is enough to render success-state nodes.
  // Also include current_step_id and failed_at_step in the synthetic
  // chain: those steps may have produced no output (still running, or
  // failed before persisting), so a strictly-output-derived list
  // would hide the only non-success state on the canvas.
  let effectiveSteps: TraceStep[]
  if (steps.length > 0) {
    effectiveSteps = steps
  } else {
    const ids = new Set<string>(Object.keys(run.step_outputs ?? {}))
    if (run.current_step_id) ids.add(run.current_step_id)
    if (run.failed_at_step) ids.add(run.failed_at_step)
    // We don't know the kind without the DSL; default to agent_run
    // so the renderer still picks a reasonable icon + chrome.
    effectiveSteps = Array.from(ids).map((id) => ({ id, type: "agent_run" as const }))
  }

  // ---- Nodes ----
  const nodes: Node[] = []

  // Trigger node — always at the head of the trace.
  const triggerData: TraceTriggerNodeData = {
    triggeredVia: run.triggered_via,
    triggeredById: run.triggered_by_id,
    issueIdentifier: run.issue_identifier,
    pipelineName: run.pipeline_name,
  }
  nodes.push({
    id: "__trigger__",
    type: "traceTrigger",
    data: triggerData as unknown as Record<string, unknown>,
    position: { x: 0, y: 0 },
  })

  // One step node per DSL step.
  const heatmapBuckets = opts.heatmapBuckets
  for (const step of effectiveSteps) {
    const token = opts.waitpointTokensByStepId?.get(step.id)
    const waitpoint =
      token && opts.workspaceId
        ? { token, workspaceId: opts.workspaceId }
        : null
    const data: TraceStepNodeData = {
      step,
      status: statusOf(run, step, effectiveSteps),
      selected: opts.selectedStepId === step.id,
      waitpoint,
      heatmapBucket: heatmapBuckets?.get(step.id) ?? null,
    }
    nodes.push({
      id: step.id,
      type: "traceStep",
      data: data as unknown as Record<string, unknown>,
      position: { x: 0, y: 0 },
    })
  }

  // ---- Sequencing edges ----
  // Two cases:
  //   A) Step declares `needs: [...]` → edges from each predecessor
  //      to this step.
  //   B) No needs declared → infer linear chain from DSL order. The
  //      executor's default execution order is DSL order, so a chain
  //      with no explicit needs renders as step1 → step2 → step3.
  const edges: Edge[] = []
  const stepIndex = new Map(effectiveSteps.map((s, i) => [s.id, i]))

  // Track edges that already exist as data-flow edges so we don't
  // double-draw a sequencing edge between the same pair. When data
  // flows A → B that implies sequencing A → B; one edge with the
  // richer data-flow chrome is enough.
  const dataFlowEdges = parseDataFlowEdges(effectiveSteps)
  const dataFlowPairs = new Set(dataFlowEdges.map((e) => `${e.from}->${e.to}`))

  for (let i = 0; i < effectiveSteps.length; i++) {
    const step = effectiveSteps[i]
    const needs = step.needs ?? []

    if (needs.length === 0) {
      // Inferred linear chain — predecessor is either the previous
      // step or the trigger when this is the first step.
      const sourceId = i === 0 ? "__trigger__" : effectiveSteps[i - 1].id
      const pairKey = `${sourceId}->${step.id}`
      if (!dataFlowPairs.has(pairKey)) {
        edges.push(makeSequencingEdge(sourceId, step.id, run, effectiveSteps))
      }
    } else {
      for (const needId of needs) {
        if (!stepIndex.has(needId)) continue
        const pairKey = `${needId}->${step.id}`
        if (dataFlowPairs.has(pairKey)) continue
        edges.push(makeSequencingEdge(needId, step.id, run, effectiveSteps))
      }
    }
  }

  // ---- Data-flow edges ----
  // One per (source step, target step) pair where the target reads
  // `{{ steps.<source>.output[.path] }}` somewhere in its inputs.
  // Edge label = path; hover preview = the actual value that flowed
  // (resolved client-side from the upstream step's output).
  for (const dfe of dataFlowEdges) {
    const sourceStep = effectiveSteps.find((s) => s.id === dfe.from)
    const sourceStatus = sourceStep ? statusOf(run, sourceStep, effectiveSteps) : null
    const active = sourceStatus === "running" || sourceStatus === "waiting"
    const upstreamOutput = run.step_outputs?.[dfe.from]
    const data: TraceDataFlowEdgeData = {
      label: formatEdgeLabel(dfe.path),
      active,
      preview: previewValueAtPath(upstreamOutput, dfe.path),
    }
    edges.push({
      id: `data:${dfe.from}->${dfe.to}`,
      source: dfe.from,
      target: dfe.to,
      type: "traceDataFlow",
      data: data as unknown as Record<string, unknown>,
    })
  }

  // ---- Layout via dagre LR ----
  const g = new DagreGraph({ multigraph: false, compound: false })
  g.setGraph({ rankdir: "LR", nodesep: 30, ranksep: 70, marginx: 20, marginy: 20 })
  g.setDefaultEdgeLabel(() => ({}))

  for (const n of nodes) {
    const w = n.id === "__trigger__" ? TRIGGER_WIDTH : NODE_WIDTH
    g.setNode(n.id, { width: w, height: NODE_HEIGHT })
  }
  for (const e of edges) {
    g.setEdge(e.source, e.target)
  }
  dagreLayout(g)

  for (const n of nodes) {
    const pos = g.node(n.id)
    if (pos) {
      const w = n.id === "__trigger__" ? TRIGGER_WIDTH : NODE_WIDTH
      n.position = { x: pos.x - w / 2, y: pos.y - NODE_HEIGHT / 2 }
    }
  }

  return { nodes, edges }
}

// previewValueAtPath — given an upstream step's output and a JSON
// path string like ".body.url", drill into the value and return a
// truncated preview suitable for an edge hover popover. Falls back
// to a top-level summary when the upstream output is empty or the
// path doesn't resolve.
//
// We don't try to be a full JSONPath implementation — the executor
// itself only supports dotted paths into object properties (see
// `internal/pipeline/dsl.go:jsonPath`), so we mirror the same shape.
function previewValueAtPath(upstreamOutput: unknown, path: string): string | null {
  if (upstreamOutput === undefined || upstreamOutput === null) return null
  // Normalize: string outputs that LOOK like JSON get parsed once.
  let value: unknown = upstreamOutput
  if (typeof value === "string") {
    const t = value.trim()
    if (t.startsWith("{") || t.startsWith("[")) {
      try {
        value = JSON.parse(t)
      } catch {
        /* fall through — keep raw string */
      }
    }
  }

  // Walk the dotted path. When the path doesn't resolve (output
  // shape drifted from what the DSL expected, e.g. an HTTP response
  // changed schema), fall back to a summary of the root output —
  // returning null here would make the edge hover preview disappear
  // entirely, which reads as "nothing flowed" when actually the data
  // just doesn't match the requested path.
  const parts = path.replace(/^\./, "").split(".").filter(Boolean)
  let cur: unknown = value
  let resolved = true
  for (const seg of parts) {
    if (cur && typeof cur === "object") {
      cur = (cur as Record<string, unknown>)[seg]
    } else {
      resolved = false
      break
    }
  }
  return summarizeValue(resolved ? cur : value, { maxChars: 100, quoteStrings: true })
}


// makeSequencingEdge — gray control-flow edge between two steps.
// Animated when the source step is in a non-terminal state (i.e. data
// is "in flight" toward the target) so a running pipeline visibly
// breathes on the canvas.
function makeSequencingEdge(
  source: string,
  target: string,
  run: PipelineRun,
  steps: TraceStep[],
): Edge {
  const sourceStep = steps.find((s) => s.id === source)
  const sourceStatus = sourceStep ? statusOf(run, sourceStep, steps) : null
  const animated = source === "__trigger__"
    ? run.status === "running" || run.status === "queued"
    : sourceStatus === "running" || sourceStatus === "waiting"

  return {
    id: `seq:${source}->${target}`,
    source,
    target,
    type: "default",
    animated,
    style: {
      stroke: "rgba(148, 163, 184, 0.4)",
      strokeWidth: 1.5,
    },
  }
}
