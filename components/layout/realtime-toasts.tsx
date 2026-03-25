"use client"

import { useCallback } from "react"
import { toast } from "sonner"
import { useRealtimeEvent } from "@/hooks/use-realtime"

export function RealtimeToasts() {
  useRealtimeEvent(
    "escalation.created",
    useCallback((event) => {
      const agent = event.payload.agent_slug ?? "Agent"
      toast.warning(`Escalation from @${agent}`, {
        description: "An agent needs human input to proceed.",
        duration: 8000,
      })
    }, []),
  )

  useRealtimeEvent(
    "run.failed",
    useCallback((event) => {
      const agent = event.payload.agent_slug ?? event.payload.agent_id ?? "Agent"
      toast.error(`Run failed: @${agent}`, {
        description: event.payload.error ?? "The agent run encountered an error.",
        duration: 8000,
      })
    }, []),
  )

  useRealtimeEvent(
    "run.completed",
    useCallback((event) => {
      const agent = event.payload.agent_slug ?? "Agent"
      toast.success(`Run completed: @${agent}`, {
        duration: 4000,
      })
    }, []),
  )

  useRealtimeEvent(
    "mission.updated",
    useCallback((event) => {
      if (event.payload.status === "STARTED") {
        toast.info("Mission started", {
          description: event.payload.title ?? "A new mission is being orchestrated.",
          duration: 4000,
        })
      } else if (event.payload.status === "COMPLETED") {
        toast.success("Mission completed", {
          description: event.payload.title ?? "A mission has been completed successfully.",
          duration: 6000,
        })
      } else if (event.payload.status === "FAILED") {
        toast.error("Mission failed", {
          description: event.payload.title ?? "A mission has failed.",
          duration: 8000,
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
      const name = event.payload.name ?? "Agent"
      toast.info(`New agent: @${name}`, { duration: 4000 })
    }, []),
  )

  useRealtimeEvent(
    "crew.created",
    useCallback((event) => {
      const name = event.payload.name ?? "Crew"
      toast.info(`New crew: ${name}`, { duration: 4000 })
    }, []),
  )

  return null
}
