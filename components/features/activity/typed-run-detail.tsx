"use client"

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { Bot, RotateCcw } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { RoutineRunDetail } from "@/components/features/routines/routine-run-detail"
import { RunActivityTimeline } from "@/components/features/activity/run-activity-timeline"
import { RunEvidencePanel } from "@/components/features/activity/run-evidence-panel"

interface AgentRunRecord {
  id: string
  kind: "agent" | "pipeline"
  status: string
  started_at?: string | null
  finished_at?: string | null
  trigger_type?: string
  agent_name?: string | null
  agent_slug?: string | null
  mission_identifier?: string | null
  exit_code?: number | null
  error_message?: string | null
}

/** Type-check the existing run API before choosing an engine-specific detail. */
export function TypedRunDetail({ workspaceId, runId }: { workspaceId: string; runId: string }) {
  const [run, setRun] = useState<AgentRunRecord | null>(null)
  const [state, setState] = useState<"loading" | "agent" | "pipeline" | "routine-fallback" | "missing" | "error">("loading")
  const [code, setCode] = useState<number | null>(null)
  const [revision, setRevision] = useState(0)
  const retry = useCallback(() => setRevision((n) => n + 1), [])

  useEffect(() => {
    const controller = new AbortController()
    setRun(null)
    setCode(null)
    setState("loading")
    void apiFetch(`/api/v1/runs/${encodeURIComponent(runId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
      .then(async (response) => {
        if (controller.signal.aborted) return
        if (response.status === 404) {
          // Some older routine rows have no journal aggregate. Probe only the
          // workspace-scoped routine detail; 404 from both is genuinely unknown.
          const routine = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}`, { signal: controller.signal })
          if (controller.signal.aborted) return
          if (routine.ok) setState("routine-fallback")
          else if (routine.status === 404) setState("missing")
          else { setCode(routine.status); setState("error") }
          return
        }
        if (!response.ok) { setCode(response.status); setState("error"); return }
        const body = await response.json() as AgentRunRecord
        if (controller.signal.aborted) return
        if (body.id !== runId || (body.kind !== "agent" && body.kind !== "pipeline")) {
          setState("error")
          return
        }
        setRun(body)
        setState(body.kind)
      })
      .catch(() => { if (!controller.signal.aborted) setState("error") })
    return () => controller.abort()
  }, [workspaceId, runId, revision])

  if (state === "loading") return <p role="status" className="p-6 text-sm">Loading run…</p>
  if (state === "error") return <div role="alert" className="p-6 text-sm">Could not load this run{code ? ` (${code})` : ""}. <Button type="button" variant="outline" size="sm" onClick={retry}><RotateCcw className="mr-1 h-3 w-3" />Retry</Button></div>
  if (state === "missing") return <div className="space-y-4 p-6">
    <p role="status" className="text-sm">No execution record is available for this run ID. A missing execution record does not prove that the agent never started.</p>
    <RunActivityTimeline workspaceId={workspaceId} params={{ run_id: runId }} title="Run activity" hideWhenEmpty={false} showControls card />
  </div>
  if (state === "pipeline" || state === "routine-fallback") return <RoutineRunDetail workspaceId={workspaceId} runId={runId} />
  if (!run) return null

  return <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4">
    <div className="flex flex-wrap items-center gap-2">
      <Bot className="h-5 w-5" />
      <h1 className="text-lg font-medium">Agent run</h1>
      <span className="font-mono text-xs text-muted-foreground">{run.id}</span>
    </div>
    <DetailCard title="Recorded process" icon={Bot}>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
        <dt>Status</dt><dd>{run.status}</dd>
        <dt>Agent</dt><dd>{run.agent_slug ? <Link href={`/crews?agent=${encodeURIComponent(run.agent_slug)}`} className="text-primary hover:underline">{run.agent_name || run.agent_slug}</Link> : "Not recorded"}</dd>
        <dt>Issue</dt><dd>{run.mission_identifier ? <Link href={`/issues?issue=${encodeURIComponent(run.mission_identifier)}`} className="text-primary hover:underline">{run.mission_identifier}</Link> : "Not recorded"}</dd>
        <dt>Exit code</dt><dd>{run.exit_code ?? "Not recorded"}</dd>
      </dl>
      <p className="mt-2 text-xs text-muted-foreground">Process status and exit code do not confirm the issue's outcome or external effects.</p>
      {run.error_message && <details className="mt-2 text-xs"><summary className="cursor-pointer">Recorded error</summary><p className="mt-1 break-words whitespace-pre-wrap">{run.error_message}</p></details>}
    </DetailCard>
    <RunEvidencePanel key={runId} workspaceId={workspaceId} run={{ kind: "agent", runId: run.id, status: run.status, startedAt: run.started_at ?? undefined, endedAt: run.finished_at ?? undefined, trigger: run.trigger_type }} />
    <RunActivityTimeline workspaceId={workspaceId} params={{ run_id: runId }} title="Run activity" hideWhenEmpty={false} showControls card />
  </div>
}
