"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

/** Connection status of the crewshipd backend engine. */
export type EngineStatus = "connected" | "disconnected" | "checking"

const POLL_INTERVAL = 10_000

/**
 * Poll the crewshipd health endpoint every 10 seconds and report connection status + uptime.
 * Used by the toolbar to show engine connectivity.
 */
export function useEngineStatus(workspaceId: string | null) {
  const [status, setStatus] = useState<EngineStatus>("checking")
  const [uptime, setUptime] = useState<string | null>(null)
  const controllerRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined

    async function check() {
      controllerRef.current?.abort()
      const controller = new AbortController()
      controllerRef.current = controller

      try {
        // apiFetch — without it, an expired session 401s every 10s
        // and the toolbar shows "Offline" while never redirecting to
        // /login. apiFetch refreshes the access cookie or surfaces
        // session-expired so the AuthProvider can hard-redirect.
        const res = await apiFetch(`/api/v1/crewshipd?workspace_id=${encodeURIComponent(workspaceId!)}`, {
          signal: controller.signal,
          cache: "no-store",
        })
        if (res.ok) {
          const data: { status?: string; uptime?: string } = await res.json()
          setStatus("connected")
          setUptime(data.uptime ?? null)
        } else {
          setStatus("disconnected")
          setUptime(null)
        }
      } catch {
        if (!controller.signal.aborted) {
          setStatus("disconnected")
          setUptime(null)
        }
      }
    }

    // Self-scheduling timeout with ±15% jitter rather than a fixed
    // setInterval: several open dashboards would otherwise poll
    // /crewshipd on the same 10s tick and spike the backend. check()
    // already aborts any in-flight request, so polls never overlap.
    const schedule = () => {
      const delay = POLL_INTERVAL * (0.85 + Math.random() * 0.3)
      timer = setTimeout(async () => {
        await check()
        if (!cancelled) schedule()
      }, delay)
    }

    check()
    schedule()

    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
      controllerRef.current?.abort()
    }
  }, [workspaceId])

  return { status, uptime }
}
