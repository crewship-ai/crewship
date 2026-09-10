import { isTerminalRoutineRun } from "./routine-run-status"

export interface ComparisonCase {
  id: string
  inputs: Record<string, unknown>
  expected_output?: string
}
export interface ComparisonVersion {
  version: number
  definition_hash: string
  is_head?: boolean
}
export interface ComparisonSide {
  version: ComparisonVersion
  tier: string
}
export interface ComparisonResult {
  run_id: string
  status: string
  output: string
  cost_usd: number
  duration_ms: number
  pipeline_version: number
  definition_hash: string
}
export interface ComparisonJob {
  case: ComparisonCase
  side: "A" | "B"
  config: ComparisonSide
  key: string
  result?: ComparisonResult
  run_id?: string
}

function object(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value)
}
export function parseComparisonCases(text: string): ComparisonCase[] {
  const parsed: unknown = JSON.parse(text)
  if (!Array.isArray(parsed) || parsed.length < 1 || parsed.length > 20)
    throw new Error("Use a dataset of 1–20 cases.")
  const ids = new Set<string>()
  return parsed.map((row) => {
    if (
      !object(row) ||
      typeof row.id !== "string" ||
      !row.id.trim() ||
      ids.has(row.id) ||
      !object(row.inputs)
    )
      throw new Error("Each case needs a unique id and an inputs object.")
    if (row.expected_output !== undefined && typeof row.expected_output !== "string")
      throw new Error("expected_output must be text when supplied.")
    ids.add(row.id)
    return {
      id: row.id,
      inputs: row.inputs,
      ...(typeof row.expected_output === "string" ? { expected_output: row.expected_output } : {}),
    }
  })
}
export function comparisonVerdict(job: ComparisonJob): string {
  const result = job.result
  if (!result && !job.run_id) return "Not started"
  if (!result || !isTerminalRoutineRun(result.status)) return "Pending"
  if (result.status.toLowerCase() === "dry_run") return "Recipe checked · no live result"
  if (result.status.toLowerCase() !== "completed") return "Run failed"
  if (job.case.expected_output === undefined) return "Completed · quality ungraded"
  return result.output === job.case.expected_output ? "Exact match" : "Output differs"
}
export function readComparisonResult(
  raw: unknown,
  job: ComparisonJob,
  workspaceId: string,
): ComparisonResult {
  if (!object(raw) || raw.id !== job.run_id || raw.workspace_id !== workspaceId)
    throw new Error("The result does not belong to the requested run.")
  if (
    raw.definition_hash !== job.config.version.definition_hash ||
    raw.pipeline_version !== job.config.version.version
  )
    throw new Error("The recorded recipe does not match the selected archive. Comparison stopped.")
  if (
    typeof raw.status !== "string" ||
    typeof raw.output !== "string" ||
    typeof raw.cost_usd !== "number" ||
    typeof raw.duration_ms !== "number" ||
    !Number.isFinite(raw.cost_usd) ||
    raw.cost_usd < 0 ||
    !Number.isFinite(raw.duration_ms) ||
    raw.duration_ms < 0
  )
    throw new Error("The recorded result is incomplete.")
  return {
    run_id: raw.id as string,
    status: raw.status,
    output: raw.output,
    cost_usd: raw.cost_usd,
    duration_ms: raw.duration_ms,
    pipeline_version: raw.pipeline_version as number,
    definition_hash: raw.definition_hash as string,
  }
}
