"use client"

import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { z } from "zod"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"

const runSchema = z.object({ run_id: z.string().min(1), routine_id: z.string().min(1), status: z.string(), mode: z.string(), started_at: z.string(), ended_at: z.string().nullable() })
const resultSchema = runSchema.extend({ output: z.string().nullable(), step_outputs: z.record(z.string(), z.string()) })
const statuses: Record<string, string> = { queued: "Queued", running: "Running", completed: "Completed", failed: "Failed", cancelled: "Cancelled", interrupted: "Interrupted", waiting: "Waiting", dry_run: "Dry run" }
const statusLabel = (status: string) => statuses[status.toLowerCase()] ?? "Status unavailable"

/** Outputs need a separate explicit share; the parent removes this on hard loss. */
export function SharedRoutineResults({ workspaceId, userId, routineId }: { workspaceId: string; userId: string; routineId: string }) {
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState("")
  const base = `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-routine-library/${requireApiPathSegment(routineId)}/runs`
  const runs = useQuery({
    queryKey: ["shared-routine-runs", userId, workspaceId, routineId, offset], retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${base}?limit=25&offset=${offset}`, { signal })
      if ([401, 403, 404].includes(response.status)) return null
      if (!response.ok) throw new Error("Could not refresh run history. Showing the last successful check.")
      const items = z.array(runSchema).parse(await response.json())
      if (items.some(item => item.routine_id !== routineId)) throw new Error("Run history could not be confirmed.")
      return items
    },
  })
  const allowed = runs.data?.some(run => run.run_id === selected) ?? false
  const result = useQuery({
    queryKey: ["shared-routine-result", userId, workspaceId, routineId, selected], enabled: allowed, retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${base}/${requireApiPathSegment(selected)}`, { signal })
      if ([401, 403, 404].includes(response.status)) return null
      if (response.status === 413) throw new Error("This result exceeds the shared result size limit.")
      if (!response.ok) throw new Error("Could not refresh this result. Showing the last successful check.")
      const parsed = resultSchema.parse(await response.json())
      if (parsed.run_id !== selected || parsed.routine_id !== routineId) throw new Error("The selected result could not be confirmed.")
      return parsed
    },
  })
  return <section aria-label="Shared routine results" className="mt-4 space-y-3 border-t pt-4">
    <h3 className="font-medium">Runs and outputs</h3>
    <p className="text-sm text-muted-foreground">Explicitly shared routine history. Private conversation audiences still apply. Inputs are not shown directly; outputs may contain them. Transcripts are excluded.</p>
    {runs.isPending && <p role="status">Loading run history…</p>}
    {runs.isError && <p role="status">{runs.error.message}</p>}
    {runs.data === null && <p role="alert">Run history is no longer available. Ask your administrator for access.</p>}
    {runs.data && runs.data.length === 0 && <p>No visible runs on this page.</p>}
    {runs.data && <ul className="space-y-2">{runs.data.map(run => <li key={run.run_id}><Button variant="outline" className="h-auto min-h-9 whitespace-normal text-left coarse:min-h-[44px]" onClick={() => setSelected(run.run_id)} aria-pressed={run.run_id === selected}>{statusLabel(run.status)} · {run.started_at} · {run.run_id}</Button></li>)}</ul>}
    <div className="flex flex-wrap gap-2"><Button variant="outline" className="coarse:h-[44px]" disabled={runs.isFetching} onClick={() => { void runs.refetch(); if (allowed) void result.refetch() }}>Refresh runs</Button><Button variant="outline" className="coarse:h-[44px]" disabled={offset === 0 || runs.isFetching} onClick={() => { setSelected(""); setOffset(Math.max(0, offset - 25)) }}>Previous runs</Button><Button variant="outline" className="coarse:h-[44px]" disabled={!runs.data || runs.data.length < 25 || runs.isFetching} onClick={() => { setSelected(""); setOffset(offset + 25) }}>Next runs</Button></div>
    {allowed && result.isPending && <p role="status">Loading result…</p>}
    {allowed && result.isError && <p role="status">{result.error.message}</p>}
    {allowed && result.data === null && <p role="alert">This result is no longer available. Ask your administrator for access.</p>}
    {allowed && result.data && <div className="space-y-3" aria-label="Selected routine result"><p>{statusLabel(result.data.status)}</p><h4 className="text-sm font-medium">Final output</h4>{result.data.output === null ? <p>No final output recorded.</p> : <pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded bg-muted p-3 text-xs">{result.data.output}</pre>}<h4 className="text-sm font-medium">Step outputs</h4>{Object.entries(result.data.step_outputs).map(([step, output]) => <details key={step}><summary>{step}</summary><pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded bg-muted p-3 text-xs">{output}</pre></details>)}</div>}
  </section>
}
