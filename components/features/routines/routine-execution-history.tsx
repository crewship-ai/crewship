"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { formatRoutineTime } from "@/lib/routine-time"
import { Button } from "@/components/ui/button"

interface Execution {
  id: string; parent_execution_id: string; step_id: string; execution_path: string
  attempt: number; kind: string; status: string; agent_slug: string; model: string
  started_at: string; ended_at: string; error: string; output_bytes: number
}
export function RoutineExecutionHistory({ workspaceId, runId, active }: { workspaceId: string; runId: string; active: boolean }) {
  const [rows, setRows] = useState<Execution[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [next, setNext] = useState<string | null>(null)
  const [error, setError] = useState(false)
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<string | null>(null)
  const [output, setOutput] = useState<string | null>(null)
  const [outputError, setOutputError] = useState(false)
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}/executions`
  const refresh = useCallback(async (signal: AbortSignal) => {
    try {
      const res = await apiFetch(base + (cursor ? `?after=${cursor}` : ""), { signal })
      if (!res.ok) throw new Error("load executions")
      const data = await res.json()
      if (signal.aborted) return
      setRows(data.rows); setNext(data.next_cursor); setError(false)
    } catch { if (!signal.aborted) setError(true) }
    finally { if (!signal.aborted) setLoading(false) }
  }, [base, cursor])
  useEffect(() => {
    const controller = new AbortController()
    void refresh(controller.signal)
    const timer = active ? setInterval(() => void refresh(controller.signal), 3000) : null
    return () => { controller.abort(); if (timer) clearInterval(timer) }
  }, [refresh, active])
  useEffect(() => {
    const controller = new AbortController()
    setOutput(null); setOutputError(false)
    if (selected) void (async () => {
      try {
        const res = await apiFetch(`${base}?execution_id=${encodeURIComponent(selected)}`, { signal: controller.signal })
        if (!res.ok) throw new Error("output unavailable")
        const data = await res.json()
        if (!controller.signal.aborted) setOutput(data.output)
      } catch { if (!controller.signal.aborted) setOutputError(true) }
    })()
    return () => controller.abort()
  }, [base, selected])
  return <section className="space-y-3 rounded-xl border p-4">
    <h2 className="text-sm font-medium">Recorded executions and attempts</h2>
    {error ? <p role="alert" className="text-sm text-destructive">Execution history could not be loaded. <button onClick={() => void refresh(new AbortController().signal)}>Retry</button></p> : loading ? <p className="text-sm text-muted-foreground">Loading attempts…</p> : !rows.length ? <p className="text-sm text-muted-foreground">{active ? "No execution recorded yet." : "Detailed attempt history is unavailable for this run."}</p> : rows.map(row => <div key={row.id} className="rounded-lg border p-3" style={{ marginLeft: `${Math.min(5, row.execution_path.split("/").length - 2) * 12}px` }}>
      <button className="flex w-full flex-wrap items-center justify-between gap-2 text-left text-sm" onClick={() => setSelected(selected === row.id ? null : row.id)} aria-expanded={selected === row.id}>
        <span>{row.kind === "agent_attempt" ? "Agent invocation" : row.step_id} · Attempt {row.attempt}</span><span className="text-xs text-muted-foreground">{row.status}</span>
      </button>
      <p className="mt-1 break-all text-xs text-muted-foreground">{row.execution_path} · {formatRoutineTime(row.started_at)}{row.agent_slug && ` · ${row.agent_slug}`}{row.model && ` · requested ${row.model}`}</p>
      {row.error && <p className="mt-2 whitespace-pre-wrap text-xs text-destructive">{row.error}</p>}
      {selected === row.id && <pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">{outputError ? "This output is unavailable." : output === null ? "Loading output…" : output || "No output recorded."}</pre>}
    </div>)}
    <div className="flex gap-2">{cursor && <Button size="sm" variant="outline" onClick={() => setCursor(null)}>First executions</Button>}{next && <Button size="sm" variant="outline" onClick={() => setCursor(next)}>Next executions</Button>}</div>
  </section>
}
