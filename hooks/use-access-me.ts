"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

export type AccessDecision = { state: "allowed" | "conditional" | "denied"; reason: string }
export type AccessMe = { actions: Record<string, AccessDecision> }

function parseAccessMe(value: unknown): AccessMe {
  if (!value || typeof value !== "object" || !Object.hasOwn(value, "actions")) throw new Error("Access response unavailable")
  const actions = (value as { actions: unknown }).actions
  if (!actions || typeof actions !== "object" || Array.isArray(actions)) throw new Error("Access response unavailable")
  const safe: Record<string, AccessDecision> = {}
  for (const [name, decision] of Object.entries(actions)) {
    if (!decision || typeof decision !== "object") throw new Error("Access response unavailable")
    const { state, reason } = decision as { state?: unknown; reason?: unknown }
    if ((state !== "allowed" && state !== "conditional" && state !== "denied") || typeof reason !== "string")
      throw new Error("Access response unavailable")
    safe[name] = { state, reason }
  }
  return { actions: safe }
}

/** No stale authorization label survives an error, refetch, or identity switch. */
export function useAccessMe(url?: string) {
  const [result, setResult] = useState<{ url: string; access: AccessMe } | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const controller = useRef<AbortController | null>(null)
  const refresh = useCallback(async () => {
    controller.current?.abort()
    if (!url) { setResult(null); setError(false); setLoading(false); return }
    const next = new AbortController()
    controller.current = next
    setResult(null)
    setError(false)
    setLoading(true)
    try {
      const response = await apiFetch(url, { signal: next.signal })
      if (!response.ok) throw new Error(`Access request ${response.status}`)
      const parsed = parseAccessMe(await response.json())
      if (!next.signal.aborted) setResult({ url, access: parsed })
    } catch {
      if (!next.signal.aborted) { setResult(null); setError(true) }
    } finally {
      if (!next.signal.aborted) setLoading(false)
    }
  }, [url])
  useEffect(() => { void refresh(); return () => controller.current?.abort() }, [refresh])
  return { access: result && result.url === url ? result.access : null, loading, error, refresh }
}
