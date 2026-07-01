// Build the HTTP request for a routine (pipeline) action button.
//
// A routine has two actions: Run and Dry run. Both operate on a SAVED
// pipeline, addressed by slug:
//   POST /api/v1/workspaces/{ws}/pipelines/{slug}/run
//   POST /api/v1/workspaces/{ws}/pipelines/{slug}/dry_run
//
// `run` is the real execution; `dry_run` is the safe static preview (walk
// the DSL, render templates, compute the would_execute plan + the declared
// manifest — no agent invocations, no side effects). The old `test_run`
// action was dropped: it ran agents for real (side effects) yet proved
// nothing, so `run` (real) + `dry_run` (safe preview) cover the need.

export type PipelineAction = "run" | "dry_run"

/** The subset of a routine this module needs. */
export interface RoutineActionContext {
  slug: string
  definition: Record<string, unknown>
}

export interface PipelineActionRequest {
  url: string
  body: Record<string, unknown>
}

export function buildPipelineActionRequest(
  workspaceId: string,
  slug: string,
  action: PipelineAction,
  _routine: RoutineActionContext,
): PipelineActionRequest {
  return {
    url: `/api/v1/workspaces/${workspaceId}/pipelines/${slug}/${action}`,
    body: { inputs: {} },
  }
}
