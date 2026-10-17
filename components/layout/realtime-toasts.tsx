"use client"

import { useCallback, useRef } from "react"
import { usePathname, useRouter } from "next/navigation"
import { toast } from "@/lib/toast"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { useWorkspace } from "@/hooks/use-workspace"

function asString(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined
}

/** "@scout" → "Scout": a toast names the agent the way a person would. */
function agentName(slug: string | undefined): string {
  if (!slug) return "An agent"
  const s = slug.replace(/^@/, "")
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/**
 * Realtime toasts (Harbor 2 G16 / v67 feedback rules): a failure stays until
 * it is closed; the same failure again folds into one toast with "×N" instead
 * of stacking; an escalation is an Inbox matter — on /inbox it is already on
 * screen, so it does not toast there. A mission start is visible in Activity
 * and no longer interrupts.
 */
export function RealtimeToasts() {
  const { workspaceId } = useWorkspace()
  const pathname = usePathname()
  const router = useRouter()
  const onInbox = pathname === "/inbox" || (pathname?.startsWith("/inbox/") ?? false)
  const onInboxRef = useRef(onInbox)
  onInboxRef.current = onInbox
  // Repeat counts per toast id, so "Scout couldn't finish the run" ×3 is one toast.
  const repeats = useRef(new Map<string, number>())
  const counted = (id: string, title: string) => {
    const n = (repeats.current.get(id) ?? 0) + 1
    repeats.current.set(id, n)
    return n > 1 ? `${title} ×${n}` : title
  }
  const onDismiss = (id: string) => () => { repeats.current.delete(id) }

  // The current workspace was deleted out from under this tab (#890). The
  // deleting tab redirects itself via onDelete; this catches every OTHER
  // connected tab/user still sitting in it. Guard by id — the provider only
  // subscribes to the current workspace channel, but stay defensive. Toast
  // first, then hard-redirect to "/" (mirrors settings-layout's onDelete) so
  // the app re-resolves to a live workspace instead of hammering dead routes.
  useRealtimeEvent(
    "workspace.deleted",
    useCallback((event) => {
      const deletedId = asString(event.payload.id)
      if (!workspaceId || deletedId !== workspaceId) return
      toast.error("This workspace was deleted", {
        description: "Redirecting you out…",
        duration: 3000,
      })
      window.setTimeout(() => { window.location.href = "/" }, 1200)
    }, [workspaceId]),
  )

  useRealtimeEvent(
    "escalation.created",
    useCallback((event) => {
      // Already on screen in the Inbox; the bell's dot carries it elsewhere too.
      if (onInboxRef.current) return
      const agent = agentName(asString(event.payload.agent_slug))
      toast.info(`${agent} needs your decision`, {
        description: "It is waiting in your Inbox.",
        action: { label: "Open", onClick: () => router.push("/inbox") },
      })
    }, [router]),
  )

  useRealtimeEvent(
    "run.failed",
    useCallback((event) => {
      // Backend uses the same `run.failed` event for both FAILED and
      // CANCELLED runs. CANCELLED happens when the user navigates away
      // mid-stream (the WS drops, the orchestrator stops the agent) —
      // that's an expected user action, not a failure to surface.
      if (asString(event.payload.status) === "CANCELLED") return
      const slug = asString(event.payload.agent_slug) ?? asString(event.payload.agent_id)
      const id = `run-failed:${slug ?? "agent"}`
      // No duration: a failure stays until it is closed (lib/toast).
      toast.error(counted(id, `${agentName(slug)} couldn’t finish the run`), {
        id,
        description: asString(event.payload.error) ?? "The run stopped with an error.",
        onDismiss: onDismiss(id),
      })
    }, []),
  )

  useRealtimeEvent(
    "run.completed",
    useCallback((event) => {
      const agent = asString(event.payload.agent_slug) ?? "Agent"
      toast.success(`Run completed: @${agent}`, {
        duration: 4000,
      })
    }, []),
  )

  // Routine (pipeline) failures get the same workspace-wide surfacing as
  // agent run.failed above — previously they were only visible inside the
  // /routines and /activity views.
  useRealtimeEvent(
    "pipeline.run.failed",
    useCallback((event) => {
      if (asString(event.payload.status) === "CANCELLED") return
      const routine = asString(event.payload.pipeline_slug) ?? "routine"
      const id = `routine-failed:${routine}`
      toast.error(counted(id, `Routine ${routine} couldn’t finish`), {
        id,
        description:
          asString(event.payload.error_message) ??
          "The routine run stopped with an error.",
        onDismiss: onDismiss(id),
      })
    }, []),
  )

  useRealtimeEvent(
    "mission.updated",
    useCallback((event) => {
      // A mission start shows in Activity; it does not need to interrupt.
      if (event.payload.status === "COMPLETED") {
        toast.success("Mission completed", {
          description: asString(event.payload.title) ?? "A mission has been completed successfully.",
          duration: 6000,
        })
      } else if (event.payload.status === "FAILED") {
        toast.error("Mission failed", {
          description: asString(event.payload.title) ?? "A mission has failed.",
        })
      }
    }, []),
  )

  useRealtimeEvent(
    "escalation.resolved",
    useCallback(() => {
      toast.success("Escalation resolved", { duration: 4000 })
    }, []),
  )

  useRealtimeEvent(
    "agent.created",
    useCallback((event) => {
      const name = asString(event.payload.name) ?? "Agent"
      toast.info(`New agent: @${name}`, { duration: 4000 })
    }, []),
  )

  useRealtimeEvent(
    "crew.created",
    useCallback((event) => {
      const name = asString(event.payload.name) ?? "Crew"
      toast.info(`New crew: ${name}`, { duration: 4000 })
    }, []),
  )

  return null
}
