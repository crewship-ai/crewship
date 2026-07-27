"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

/**
 * Connection status of the crewshipd backend engine.
 *
 *   checking     — first poll for this workspace hasn't resolved yet;
 *                  no opinion formed either way.
 *   connected    — most recent poll answered with a 2xx.
 *   degraded     — a single real failure, OR a 429 throttle response.
 *                  A deploy restart is ~one poll long, so ONE bad poll
 *                  must not read as "the engine is gone" — this holds
 *                  the previous good state (uptime included) and lets
 *                  the toolbar say something like "Reconnecting"
 *                  instead of lying "Online" or panicking "Offline".
 *   disconnected — TWO consecutive real (non-429) failures. This is the
 *                  genuine "engine is down" signal the red pill/"Offline"
 *                  copy should be reserved for.
 *
 * A 429 is deliberately NOT a "real failure": several open dashboards can
 * poll /crewshipd on the same 10s tick and trip a rate limit that says
 * nothing about engine health. It never counts toward the disconnect
 * threshold and never downgrades an already-connected status — see
 * `check()` below.
 */
export type EngineStatus = "connected" | "degraded" | "disconnected" | "checking"

const POLL_INTERVAL = 10_000

/**
 * Poll the crewshipd health endpoint every 10 seconds and report connection status + uptime.
 * Used by the toolbar to show engine connectivity.
 */
export function useEngineStatus(workspaceId: string | null) {
  const [status, setStatus] = useState<EngineStatus>("checking")
  const [uptime, setUptime] = useState<string | null>(null)
  const controllerRef = useRef<AbortController | null>(null)
  // Mirrors `status` for reads inside check()/schedule(), which are
  // defined once per effect run and would otherwise close over the
  // "checking" value from the render that started polling — React state
  // updates don't rewrite an already-created closure. Every status write
  // goes through setEngineStatus() below so this never drifts from what
  // the hook actually returns.
  const statusRef = useRef<EngineStatus>("checking")

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    // Real (non-429) failures seen back-to-back. Plain ref, not state:
    // it's bookkeeping for the NEXT poll's decision, not something the
    // toolbar renders, so bumping it must not itself trigger a render.
    // Reset per effect run so switching workspaces starts the streak
    // fresh rather than inheriting another workspace's failure count.
    let consecutiveFailures = 0

    function setEngineStatus(next: EngineStatus) {
      statusRef.current = next
      setStatus(next)
    }

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
        // The component (or this effect, on a workspaceId change) may
        // have gone away while the request was in flight. Bail before
        // touching state — matches the abort guard in the catch branch
        // below and keeps an unmount from ever setState-ing.
        if (cancelled) return

        if (res.ok) {
          const data: { status?: string; uptime?: string } = await res.json()
          if (cancelled) return
          consecutiveFailures = 0
          setEngineStatus("connected")
          setUptime(data.uptime ?? null)
        } else if (res.status === 429) {
          // Throttled, not unhealthy — see the type doc above. Only
          // move off "checking" (so a workspace whose very first poll
          // ever gets throttled doesn't spin forever); an already
          // connected/degraded/disconnected status is left exactly as
          // it was, which is the "keep the last known good state"
          // behaviour this case exists for.
          if (statusRef.current === "checking") setEngineStatus("degraded")
        } else {
          registerFailure()
        }
      } catch {
        if (!controller.signal.aborted && !cancelled) {
          registerFailure()
        }
      }
    }

    function registerFailure() {
      consecutiveFailures += 1
      if (consecutiveFailures >= 2) {
        // Second real failure in a row: no longer "probably a restart",
        // this is an outage. Only now clear uptime and go red.
        setEngineStatus("disconnected")
        setUptime(null)
      } else {
        // First failure: hold the previous uptime and report degraded
        // rather than jumping straight to disconnected.
        setEngineStatus("degraded")
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
