"use client"

import { useCallback, useRef, useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { randomUUIDv4 } from "@/lib/random-id"

export type CardWriteState = "idle" | "saving" | "saved" | "validation" | "conflict" | "unknown" | "rechecking"
type Intent = { path: string; method: "PATCH" | "POST"; body: string; key: string; operationId?: string; uncertain?: boolean }

/** An unknown transport outcome keeps its exact request and key until resolved. */
export function useChatIssueMutation(onChanged: () => void, onUnauthorized: () => void, recoveryScope?: string) {
  const storageKey = recoveryScope ? `chat-issue-intent:${recoveryScope}` : null
  const [restored] = useState<Intent | null>(() => {
    if (!storageKey) return null
    try { const value = JSON.parse(sessionStorage.getItem(storageKey) ?? "null"); return value?.key && value?.path && value?.body && ["PATCH", "POST"].includes(value.method) ? { ...value, uncertain: true } : null } catch { return null }
  })
  const pending = useRef<Intent | null>(restored)
  const [savedMethod, setSavedMethod] = useState<Intent["method"]>()
  const persist = useCallback((intent: Intent | null) => {
    if (!storageKey) return
    // Refuse a new write if durable recovery cannot be recorded.
    if (intent) sessionStorage.setItem(storageKey, JSON.stringify(intent))
    else { try { sessionStorage.removeItem(storageKey) } catch { /* A retained key conservatively blocks a fresh operation. */ } }
  }, [storageKey])
  const [state, setState] = useState<CardWriteState>(restored ? "unknown" : "idle")
  const [message, setMessage] = useState("")
  const mutation = useMutation({
    mutationFn: async (intent: Intent) => {
      const response = await apiFetch(intent.path, {
        method: intent.method, headers: { "Content-Type": "application/json", "Idempotency-Key": intent.key }, body: intent.body,
      })
      intent.operationId = response.headers.get("Operation-ID") ?? undefined
      persist(intent)
      if (response.ok) return "saved" as const
      const body = await response.json().catch(() => null)
      if ([401, 403, 404].includes(response.status)) return "rechecking" as const
      if (response.status === 409) return "conflict" as const
      if (response.status === 400 || response.status === 422) {
        setMessage(body?.detail ?? body?.error ?? "Check your changes and try again.")
        return "validation" as const
      }
      throw new Error("Unknown outcome")
    },
    retry: false,
  })
  const execute = useCallback(async (intent: Intent) => {
    if (mutation.isPending) return false
    setState("saving"); setMessage("")
    try {
      const result = await mutation.mutateAsync(intent)
      setState(result)
      if (result === "saved") { setSavedMethod(intent.method); pending.current = null; persist(null); onChanged(); return true }
      if (result === "rechecking") {
        // A denied retry says nothing about the earlier, unacknowledged write.
        // Keep that intent locked until the same key can resolve its outcome.
        if (intent.uncertain) setState("unknown")
        else { pending.current = null; persist(null) }
        onUnauthorized()
      }
      if (result === "validation") { pending.current = null; persist(null) }
      // A conflict needs an explicit fresh revision before starting a new intent.
      return false
    } catch {
      intent.uncertain = true
      setState("unknown")
      return false
    }
  }, [mutation, onChanged, onUnauthorized, persist])
  const submit = useCallback(async (path: string, method: Intent["method"], body: Record<string, unknown>) => {
    if (pending.current) return false
    if (storageKey) {
      try { const existing = JSON.parse(sessionStorage.getItem(storageKey) ?? "null"); if (existing?.key) { pending.current = { ...existing, uncertain: true }; setState("unknown"); return false } } catch { setMessage("Recovery storage is unavailable. No request was sent."); setState("validation"); return false }
    }
    const intent = { path, method, body: JSON.stringify(body), key: randomUUIDv4() }
    try { persist(intent) } catch { setMessage("Recovery storage is unavailable. No request was sent."); setState("validation"); return false }
    pending.current = intent
    return execute(intent)
  }, [execute, persist, storageKey])
  const retry = useCallback(async () => {
    const intent = pending.current
    if (!intent) return false
    // If headers arrived before the body failed, try the receipt first.
    if (intent.operationId) {
      try {
        const receipt = await apiFetch(`/api/v1/operations/${encodeURIComponent(intent.operationId)}?${new URLSearchParams(new URL(intent.path, "http://local").search).toString()}`)
        if (receipt.ok && (await receipt.json()).status === "completed") { pending.current = null; persist(null); setSavedMethod(intent.method); setState("saved"); onChanged(); return true }
        if ([401, 403].includes(receipt.status)) { setState("unknown"); onUnauthorized(); return false }
      } catch { /* Retrying the same key is the authoritative recovery path. */ }
    }
    return execute(intent)
  }, [execute, onChanged, onUnauthorized, persist])
  const resolveConflict = useCallback(() => { pending.current = null; persist(null); setState("idle"); onChanged() }, [onChanged, persist])
  return { state, savedMethod, message, submit, retry, resolveConflict }
}
