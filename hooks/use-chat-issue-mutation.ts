"use client"

import { useCallback, useRef, useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { randomUUIDv4 } from "@/lib/random-id"

export type CardWriteState = "idle" | "saving" | "saved" | "validation" | "conflict" | "unknown" | "rechecking"
type Intent = { path: string; method: "PATCH" | "POST"; body: string; key: string; operationId?: string }

/** An unknown transport outcome keeps its exact request and key until resolved. */
export function useChatIssueMutation(onChanged: () => void, onUnauthorized: () => void) {
  const pending = useRef<Intent | null>(null)
  const [state, setState] = useState<CardWriteState>("idle")
  const [message, setMessage] = useState("")
  const mutation = useMutation({
    mutationFn: async (intent: Intent) => {
      const response = await apiFetch(intent.path, {
        method: intent.method, headers: { "Content-Type": "application/json", "Idempotency-Key": intent.key }, body: intent.body,
      })
      intent.operationId = response.headers.get("Operation-ID") ?? undefined
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
      if (result === "saved") { pending.current = null; onChanged(); return true }
      if (result === "rechecking") { pending.current = null; onUnauthorized() }
      if (result === "validation") pending.current = null
      // A conflict needs an explicit fresh revision before starting a new intent.
      return false
    } catch {
      setState("unknown")
      return false
    }
  }, [mutation, onChanged, onUnauthorized])
  const submit = useCallback(async (path: string, method: Intent["method"], body: Record<string, unknown>) => {
    if (pending.current) return false
    const intent = { path, method, body: JSON.stringify(body), key: randomUUIDv4() }
    pending.current = intent
    return execute(intent)
  }, [execute])
  const retry = useCallback(async () => {
    const intent = pending.current
    if (!intent) return false
    // If headers arrived before the body failed, try the receipt first.
    if (intent.operationId) {
      try {
        const receipt = await apiFetch(`/api/v1/operations/${encodeURIComponent(intent.operationId)}?${new URLSearchParams(new URL(intent.path, "http://local").search).toString()}`)
        if (receipt.ok && (await receipt.json()).status === "completed") { pending.current = null; setState("saved"); onChanged(); return true }
        if ([401, 403].includes(receipt.status)) { setState("rechecking"); onUnauthorized(); return false }
      } catch { /* Retrying the same key is the authoritative recovery path. */ }
    }
    return execute(intent)
  }, [execute, onChanged, onUnauthorized])
  const resolveConflict = useCallback(() => { pending.current = null; setState("idle"); onChanged() }, [onChanged])
  return { state, message, submit, retry, resolveConflict }
}
