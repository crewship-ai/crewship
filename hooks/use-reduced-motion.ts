"use client"

import { useEffect, useState } from "react"

/**
 * React hook for `prefers-reduced-motion`. Reactive — updates if the OS
 * setting changes mid-session. SSR-safe (returns false during the first
 * paint, then matches the user preference).
 */
export function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(false)

  useEffect(() => {
    if (typeof window === "undefined") return
    const mq = window.matchMedia("(prefers-reduced-motion: reduce)")
    setReduced(mq.matches)
    const onChange = (e: MediaQueryListEvent) => setReduced(e.matches)
    mq.addEventListener("change", onChange)
    return () => mq.removeEventListener("change", onChange)
  }, [])

  return reduced
}
