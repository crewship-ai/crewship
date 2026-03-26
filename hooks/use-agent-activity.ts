"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

/**
 * Subscribes to agent.log WebSocket events and maintains a map of
 * the latest activity snippet per agent slug.
 *
 * Throttled to max 2 updates/second to avoid React re-render storms
 * (agent.log fires on every LLM token chunk).
 */
export function useAgentActivity(): Map<string, string> {
  const [activities, setActivities] = useState<Map<string, string>>(new Map())
  const rawRef = useRef<Map<string, { text: string; updatedAt: number }>>(new Map())
  const dirtyRef = useRef(false)

  // Sync raw map → state on a 500ms interval
  useEffect(() => {
    const interval = setInterval(() => {
      if (!dirtyRef.current) return
      dirtyRef.current = false

      const now = Date.now()
      const STALE_MS = 30_000 // Clear snippets older than 30s

      const next = new Map<string, string>()
      const staleKeys: string[] = []
      for (const [slug, entry] of rawRef.current) {
        if (now - entry.updatedAt < STALE_MS) {
          next.set(slug, entry.text)
        } else {
          staleKeys.push(slug)
        }
      }
      for (const key of staleKeys) rawRef.current.delete(key)
      setActivities(next)
    }, 500)

    return () => clearInterval(interval)
  }, [])

  const handleLog = useCallback((event: { payload: Record<string, unknown> }) => {
    // Backend sends "agent" field (not "agent_slug") containing the slug
    const agentSlug = (event.payload.agent ?? event.payload.agent_slug) as string | undefined
    const content = event.payload.content as string | undefined
    if (!agentSlug || !content) return

    // Truncate to 80 chars
    const snippet = content.length > 80 ? content.slice(0, 77) + "..." : content

    rawRef.current.set(agentSlug, { text: snippet, updatedAt: Date.now() })
    dirtyRef.current = true
  }, [])

  useRealtimeEvent("agent.log", handleLog)

  return activities
}
