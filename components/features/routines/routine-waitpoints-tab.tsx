"use client"

import { useEffect, useState } from "react"
import { CheckCircle2, XCircle, Clock, MessageSquare } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { RoutineListSkeleton } from "./routine-skeletons"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// RoutineWaitpointsTab — HITL approval inbox for pending wait-step
// approvals. Workspace-wide (waitpoints aren't per-routine on the
// list endpoint), but we show a hint when the slug is set so users
// understand the scope. Approve/reject wakes the parked run goroutine.

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
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines/waitpoints`)
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

  // Pending list changes when a wait-step starts or another user
  // decides one. Backend doesn't emit dedicated WS events for that
  // yet, so we hook into pipeline.run.* as a proxy: any run change
  // is a fair refresh trigger for an inbox of pending waits.
  useRealtimeEvent("pipeline.run.started", fetchPending)
  useRealtimeEvent("pipeline.run.completed", fetchPending)
  useRealtimeEvent("pipeline.run.failed", fetchPending)

  const decide = async (token: string, approved: boolean) => {
    setDecidingToken(token)
    try {
      const res = await fetch(
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

  if (loading && pending.length === 0) return <RoutineListSkeleton rows={2} />

  return (
    <div className="space-y-2">
      {error && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-400">
          {error}
        </div>
      )}

      <p className="text-[10px] text-muted-foreground">
        Pending approvals are workspace-wide. Filter by routine: {slug}
      </p>

      {pending.length === 0 ? (
        <div className="rounded-md border border-dashed border-border/60 p-6 text-center">
          <Clock className="mx-auto mb-2 h-6 w-6 text-muted-foreground/50" />
          <p className="text-xs text-muted-foreground">
            No pending waitpoints. Routines that include `wait` steps with `kind: approval` create
            pending entries here while parked.
          </p>
        </div>
      ) : (
        <ol className="space-y-2">
          {pending.map((w) => {
            const expiresMs = new Date(w.timeout_at).getTime() - Date.now()
            const expiresHrs = Math.max(0, Math.round(expiresMs / 3600e3))
            return (
              <li
                key={w.token}
                className={cn(
                  "rounded-md border border-white/[0.06] bg-card/40 p-2.5 text-[11px]",
                  decidingToken === w.token && "opacity-50",
                )}
              >
                <div className="mb-1 flex items-center gap-2">
                  <Badge variant="outline" className="text-[9px] capitalize">{w.kind}</Badge>
                  <span className="font-mono text-[10px] text-muted-foreground">
                    step {w.step_id}
                  </span>
                  <span className="ml-auto text-[10px] text-muted-foreground">
                    {expiresHrs}h to timeout
                  </span>
                </div>
                {w.prompt && (
                  <div className="mb-2 rounded bg-muted/30 px-2 py-1.5">
                    <div className="flex items-baseline gap-1.5">
                      <MessageSquare className="h-2.5 w-2.5 shrink-0 text-muted-foreground" />
                      <p className="text-foreground/90 whitespace-pre-wrap">{w.prompt}</p>
                    </div>
                  </div>
                )}
                <div className="mb-2">
                  <textarea
                    placeholder="Decision comment (optional, sent to the parked run as the waitpoint payload)…"
                    value={comments[w.token] ?? ""}
                    onChange={(e) => setComments((prev) => ({ ...prev, [w.token]: e.target.value }))}
                    className="h-14 w-full resize-none rounded-md border border-white/10 bg-background p-1.5 text-[11px]"
                  />
                </div>
                <div className="flex items-center justify-between">
                  <span className="font-mono text-[9px] text-muted-foreground">
                    Run {w.pipeline_run_id.slice(0, 12)}…
                  </span>
                  <div className="flex gap-1.5">
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => decide(w.token, false)}
                      disabled={decidingToken !== null}
                      className="h-6 gap-1 px-2 text-[10px]"
                    >
                      <XCircle className="h-2.5 w-2.5" />
                      Reject
                    </Button>
                    <Button
                      size="sm"
                      onClick={() => decide(w.token, true)}
                      disabled={decidingToken !== null}
                      className="h-6 gap-1 px-2 text-[10px]"
                    >
                      <CheckCircle2 className="h-2.5 w-2.5" />
                      Approve
                    </Button>
                  </div>
                </div>
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}
