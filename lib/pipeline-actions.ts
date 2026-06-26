// Build the HTTP request for a routine (pipeline) action button.
//
// Run and Dry run operate on a SAVED pipeline, addressed by slug:
//   POST /api/v1/workspaces/{ws}/pipelines/{slug}/run
//   POST /api/v1/workspaces/{ws}/pipelines/{slug}/dry_run
//
// Test run is different: the backend registers it WITHOUT a slug because it
// executes a draft DSL passed inline in the body (the save-gate flow), not a
// saved pipeline:
//   POST /api/v1/workspaces/{ws}/pipelines/test_run
//   body: { definition, author_crew_id, sample_inputs }
//
// The old frontend sent all three to /pipelines/{slug}/{action} with an
// `{ inputs: {} }` body, so Test run hit an unregistered route → 404. This
// helper centralises the per-action shape so the bug can't silently return.

export type PipelineAction = "run" | "test_run" | "dry_run"

/** The subset of a routine this module needs. */
export interface RoutineActionContext {
  slug: string
  definition: Record<string, unknown>
  author_crew_id?: string
}

export interface PipelineActionRequest {
  url: string
  body: Record<string, unknown>
}

/**
 * Whether Test run is possible for this routine. The backend requires a
 * non-empty author_crew_id; without one the button can only 400, so callers
 * should disable it.
 */
export function canTestRun(routine: RoutineActionContext | null | undefined): boolean {
  return !!routine?.author_crew_id
}

export function buildPipelineActionRequest(
  workspaceId: string,
  slug: string,
  action: PipelineAction,
  routine: RoutineActionContext,
): PipelineActionRequest {
  if (action === "test_run") {
    return {
      url: `/api/v1/workspaces/${workspaceId}/pipelines/test_run`,
      body: {
        definition: routine.definition,
        author_crew_id: routine.author_crew_id ?? "",
        sample_inputs: {},
      },
    }
  }
  return {
    url: `/api/v1/workspaces/${workspaceId}/pipelines/${slug}/${action}`,
    body: { inputs: {} },
  }
}
