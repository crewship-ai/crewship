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

  // Sync raw map → state on a 500ms interval. The dirty short-circuit
  // skips work when no events arrived since the last tick, but it must
  // still run when stored entries can age out — otherwise an agent that
  // goes quiet leaves its snippet visible in the sidebar past the 30s
  // TTL until some other agent emits an event. Run whenever there's
  // anything to potentially expire, regardless of dirty state.
  useEffect(() => {
    const interval = setInterval(() => {
      if (!dirtyRef.current && rawRef.current.size === 0) return

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

      // Only push state when something actually changed (a new event
      // landed OR a stale entry was evicted). Avoids re-rendering every
      // 500ms with the same Map reference.
      if (dirtyRef.current || staleKeys.length > 0) {
        dirtyRef.current = false
        setActivities(next)
      }
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
