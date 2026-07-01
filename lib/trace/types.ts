// Frontend trace model — the shape consumed by /activity canvas and
// side panel. Mirrors the Go pipeline DSL just enough to render a
// readable execution chain; we never need the full server-side step
// type (validation, retry config, etc.) on the FE.
//
// React Flow node/edge data interfaces also live here so the lib
// layer doesn't import from components/ — that flips the dependency
// arrow and makes lib/trace unusable from anywhere else.

import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { HeatmapBucket } from "./percentile-heatmap"

export type StepKind =
  | "agent_run"
  | "call_pipeline"
  | "http"
  | "code"
  | "wait"
  | "transform"

export type StepStatus =
  | "pending"
  | "running"
  | "waiting"
  | "success"
  | "failed"
  | "skipped"

// Trimmed DSL step shape — only the fields the trace view renders.
// The full Go struct has dozens more (retry, validation, outcomes…)
// that don't surface in the graph.
//
// Security note: http.headers/body and code.env values can carry
// templated credential refs (`{{ inputs.token }}`, `{{ secrets.X }}`)
// at the DSL level. The pipeline runtime resolves these against the
// keeper credential store server-side; the FE only ever sees the
// templated form. Anyone editing this file should keep that
// invariant — never persist or log resolved values on the client.
export interface TraceStep {
  id: string
  type: StepKind
  needs?: string[]
  // type-specific snippets, all optional. Keep them flat instead of
  // discriminated unions so the renderer can do `step.http?.url` etc.
  // without exhaustive switches.
  agent_slug?: string
  prompt?: string
  http?: {
    method?: string
    url?: string
    body?: string
    headers?: Record<string, string>
  }
  transform?: {
    input?: string
    expression?: string
  }
  code?: {
    runtime?: string
    code?: string
    env?: Record<string, string>
  }
  wait?: {
    kind?: string
    approval_prompt?: string
    until?: string
  }
  pipeline_slug?: string
  inputs?: Record<string, unknown>
}

export interface PipelineDSL {
  steps?: TraceStep[]
}

// ── Sub-spans — the agent's INTERNAL activity inside one step ─────────
//
// `GetRun` returns `sub_spans`: a map keyed by step id, each value an
// array of the tool calls the agent made while executing that step
// (the bash commands it ran, files it wrote, MCP tools it invoked,
// thinking blocks, …), ordered by `seq`. A run with none returns `{}`.
//
// This is the drill-down layer beneath the step-level flow: the canvas
// shows step1 → step2 → step3; expanding a step reveals what the agent
// actually DID inside it. We keep the public shape camelCased and
// normalize the wire (`started_at`, `duration_ms`) in mapSubSpans so
// every renderer consumes one stable type.
export type SubSpanKind =
  | "bash"
  | "write"
  | "read"
  | "edit"
  | "mcp_tool"
  | "http"
  | "tool"
  | "think"

export type SubSpanStatus = "ok" | "error" | "running"

export interface SubSpanAttributes {
  // Concrete tool the action ran (e.g. "ansible", "terraform") — drives
  // the brand logo when it resolves to a known Simple Icon.
  tool?: string
  // Model that produced this span (e.g. "opus-4-8") — surfaced as a
  // badge on the owning step node and in the detail panel.
  model?: string
  // Path of a file the action wrote/read — clickable in the panel.
  artifact_path?: string
  // Network host the action reached (egress target).
  host?: string
}

export interface SubSpan {
  kind: SubSpanKind
  name: string
  detail?: string
  // ISO timestamp the span started — used to position its bar in the
  // detail-panel waterfall. May be absent on malformed rows.
  startedAt?: string
  durationMs?: number
  status: SubSpanStatus
  attributes: SubSpanAttributes
}

// Edges parsed from `{{ steps.X.output[.path] }}` references in any
// step input field. Frontend mirrors the regex used by the Go
// renderer in internal/pipeline/dsl.go:Render() — no need to ship
// resolved values from the backend.
export interface DataFlowEdge {
  from: string // source step id
  to: string // dependent step id
  // Reference path after `.output` — e.g. ".body.url" for
  // `{{ steps.fetch.output.body.url }}`. Used as the edge label.
  path: string
}

// One row in the run timeline rail. PipelineRun from hooks already
// has every field we need; this is a re-export alias so callers can
// import a single type.
export type RunRow = PipelineRun

// ── React Flow node + edge data shapes ─────────────────────────────
//
// Lifted out of components/ so lib/trace/build-trace-graph.ts (and
// any other lib helpers that build canvas data) never has to import
// back into components/. The graph builder is the source of truth for
// what fields a node carries; the components just render them.

export interface TraceStepNodeData {
  step: TraceStep
  status: StepStatus
  selected: boolean
  // When set, the node renders inline Approve/Deny buttons that call
  // the workspace-scoped /pipelines/waitpoints/{token}/approve
  // endpoint. Same handler the inbox uses; lifted to a shared lib so
  // both surfaces stay in sync.
  waitpoint?: {
    token: string
    workspaceId: string
  } | null
  // Heatmap shading — discrete percentile bucket, mapped to a
  // Tailwind border class by the node renderer. Keeping the bucket
  // (not a hex color) here keeps theme/color decisions in CSS where
  // they belong.
  heatmapBucket?: HeatmapBucket | null
  // Hover-card payload — duration/cost from journal events + a
  // truncated output snippet. None of these are required to render
  // the node itself; they're peek-only data we pre-resolve in the
  // graph builder so the hover renderer stays dumb.
  durationMs?: number | null
  costUsd?: number | null
  outputSnippet?: string | null
  errorMessage?: string | null
  // Drill-down layer — the agent's internal tool calls inside this
  // step, mapped from `run.sub_spans[step.id]`. Empty array when the
  // step recorded none (the common case for non-agent steps). Drives
  // the "▾ N actions" affordance on the node + the detail-panel
  // waterfall when the step is selected.
  subSpans?: SubSpan[]
  // Model that ran this step (e.g. "opus-4-8"), derived from the first
  // sub-span carrying `attributes.model`. Rendered as a node badge.
  model?: string | null
  [key: string]: unknown
}

export interface TraceTriggerNodeData {
  triggeredVia: string
  triggeredById?: string
  issueIdentifier?: string
  pipelineName?: string
  [key: string]: unknown
}

export interface TraceDataFlowEdgeData {
  label?: string
  // Truncated string preview of the value that flowed.
  // null = no value yet (source step hasn't run).
  preview?: string | null
  active?: boolean
  [key: string]: unknown
}
