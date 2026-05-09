// Frontend trace model — the shape consumed by /activity canvas and
// side panel. Mirrors the Go pipeline DSL just enough to render a
// readable execution chain; we never need the full server-side step
// type (validation, retry config, etc.) on the FE.

import type { PipelineRun } from "@/hooks/use-pipeline-runs"

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
