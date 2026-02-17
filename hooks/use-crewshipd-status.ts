"use client"

import { useEffect, useRef, useState } from "react"

export type DaemonStatus = "connected" | "disconnected" | "checking"

const POLL_INTERVAL = 10_000

export function useCrewshipdStatus() {
  const [status, setStatus] = useState<DaemonStatus>("checking")
  const [uptime, setUptime] = useState<string | null>(null)
  const controllerRef = useRef<AbortController | null>(null)

  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | undefined

    async function check() {
      controllerRef.current?.abort()
      const controller = new AbortController()
      controllerRef.current = controller

      try {
        const res = await fetch("/api/v1/crewshipd", {
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

    check()
    timer = setInterval(check, POLL_INTERVAL)

    return () => {
      clearInterval(timer)
      controllerRef.current?.abort()
    }
  }, [])

  return { status, uptime }
}
