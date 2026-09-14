export interface CapturedRoutineFixture {
  source: {
    run_id: string
    workspace_id: string
    definition_hash: string
    pipeline_version?: number
    status: string
  }
  inputs: Record<string, unknown>
  step_outputs: Record<string, string>
}

function object(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value)
}

/** Never turn a failed output read or missing archive into an empty fixture. */
export function capturedRoutineFixture(
  raw: unknown,
  workspaceId: string,
  runId: string,
): CapturedRoutineFixture {
  if (!object(raw) || raw.id !== runId || raw.workspace_id !== workspaceId)
    throw new Error("The response does not belong to the requested run and workspace.")
  if (
    raw.definition_status !== "available" ||
    !object(raw.definition) ||
    typeof raw.definition_hash !== "string" ||
    !raw.definition_hash
  )
    throw new Error("The run's archived recipe is unavailable. Captured data was not imported.")
  if (
    raw.step_outputs_available !== true ||
    !object(raw.step_outputs) ||
    Object.values(raw.step_outputs).some((value) => typeof value !== "string")
  )
    throw new Error("The run's outputs are unavailable. Captured data was not imported.")
  if (raw.inputs !== null && !object(raw.inputs))
    throw new Error("The run's historical inputs are unavailable.")
  return {
    source: {
      run_id: runId,
      workspace_id: workspaceId,
      definition_hash: raw.definition_hash,
      ...(typeof raw.pipeline_version === "number"
        ? { pipeline_version: raw.pipeline_version }
        : {}),
      status: typeof raw.status === "string" ? raw.status : "unknown",
    },
    inputs: raw.inputs ?? {},
    step_outputs: raw.step_outputs as Record<string, string>,
  }
}
