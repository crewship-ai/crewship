"use client"

import { useEffect, useState } from "react"
import { CheckCircle2, XCircle, Clock, MessageSquare } from "lucide-react"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { RoutineListSkeleton } from "./routine-skeletons"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { Card, EmptyState, Pill } from "./_shared"

// RoutineWaitpointsTab — HITL approval inbox for pending wait-step
// approvals. Workspace-wide (waitpoints aren't per-routine on the
// list endpoint), but we show a hint when the slug is set so users
// understand the scope.

interface PendingWaitpoint {
  token: string
  pipeline_run_id: string
  step_id: string
  kind: string
  prompt: string
  invoking_crew_id?: string
  timeout_at: string
  created_at: string
}

interface Props {
  workspaceId: string
  slug: string
}

export function RoutineWaitpointsTab({ workspaceId, slug }: Props) {
  const [pending, setPending] = useState<PendingWaitpoint[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [decidingToken, setDecidingToken] = useState<string | null>(null)
  const [comments, setComments] = useState<Record<string, string>>({})

  const fetchPending = async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/waitpoints`)
      if (!res.ok) {
        if (res.status === 503) {
          setPending([])
          setError("Waitpoint store not wired on this server")
          return
        }
        throw new Error(`fetch waitpoints: ${res.status}`)
      }
      const data: PendingWaitpoint[] = await res.json()
      setPending(Array.isArray(data) ? data : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchPending()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  useRealtimeEvent("pipeline.run.started", fetchPending)
  useRealtimeEvent("pipeline.run.completed", fetchPending)
  useRealtimeEvent("pipeline.run.failed", fetchPending)

  const decide = async (token: string, approved: boolean) => {
    setDecidingToken(token)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/waitpoints/${token}/approve`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ approved, comment: comments[token] ?? "" }),
        },
      )
      if (!res.ok) {
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      toast.success(approved ? "Approved" : "Rejected")
      fetchPending()
    } catch (e) {
      toast.error("Decision failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setDecidingToken(null)
    }
  }

  if (loading && pending.length === 0) {
    return (
      <Card title="Pending approvals" subtitle="loading…">
        <div className="p-4">
          <RoutineListSkeleton rows={2} />
        </div>
      </Card>
    )
  }

  return (
    <div className="space-y-4">
      {error && (
        <Card tone="amber">
          <div className="px-4 py-3 text-sm text-amber-300">{error}</div>
        </Card>
      )}

      {pending.length === 0 ? (
        <Card title="Wait points">
          <EmptyState
            icon={Clock}
            title="No pending approvals"
            description="Routines that include `wait` steps with `kind: approval` create pending entries here while parked. The inbox is workspace-wide; this tab shows everything currently waiting."
          />
        </Card>
      ) : (
        <Card
          title="Pending approvals"
          subtitle={`${pending.length} waiting · workspace-wide · context ${slug}`}
        >
          <ol className="divide-y divide-border/40">
            {pending.map((w) => {
              const expiresMs = new Date(w.timeout_at).getTime() - Date.now()
              const expiresHrs = Math.max(0, Math.round(expiresMs / 3600e3))
              const isUrgent = expiresHrs < 2
              const isDeciding = decidingToken === w.token
              return (
                <li
                  key={w.token}
                  className={cn("space-y-3 px-4 py-4", isDeciding && "opacity-50")}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Pill tone="amber" className="capitalize">
                      <Clock className="h-3 w-3" />
                      {w.kind}
                    </Pill>
                    <span className="font-mono text-[12px] text-muted-foreground">step {w.step_id}</span>
                    <span className="ml-auto text-[11px] text-muted-foreground">
                      Timeout in <span className={cn("text-foreground/85", isUrgent && "text-rose-400 font-semibold")}>{expiresHrs}h</span>
                    </span>
                  </div>

                  {w.prompt && (
                    <div className="rounded-md border border-border/60 bg-white/[0.02] px-3 py-2.5">
                      <div className="flex items-start gap-2">
                        <MessageSquare className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                        <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-foreground/90">
                          {w.prompt}
                        </p>
                      </div>
                    </div>
                  )}

                  <div>
                    <textarea
                      placeholder="Decision comment (optional, sent to the parked run as the waitpoint payload)…"
                      value={comments[w.token] ?? ""}
                      onChange={(e) => setComments((prev) => ({ ...prev, [w.token]: e.target.value }))}
                      className="h-16 w-full resize-none rounded-md border border-white/[0.1] bg-background p-2.5 text-[13px] leading-relaxed placeholder:text-muted-foreground-soft"
                    />
                  </div>

                  <div className="flex items-center justify-between">
                    <span className="font-mono text-[11px] text-muted-foreground">
                      Run {w.pipeline_run_id.slice(0, 16)}…
                    </span>
                    <div className="flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => decide(w.token, false)}
                        disabled={decidingToken !== null}
                        className="h-8 gap-1.5 px-3 text-xs"
                      >
                        <XCircle className="h-3 w-3" />
                        Reject
                      </Button>
                      <Button
                        size="sm"
                        onClick={() => decide(w.token, true)}
                        disabled={decidingToken !== null}
                        className="h-8 gap-1.5 px-3 text-xs"
                      >
                        <CheckCircle2 className="h-3 w-3" />
                        Approve
                      </Button>
                    </div>
                  </div>
                </li>
              )
            })}
          </ol>
        </Card>
      )}
    </div>
  )
}
