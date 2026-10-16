"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { DecisionRefSchema, E2CommandSchemas, type DecisionRef } from "@/lib/chat-e2-cards"
import type { DecisionSubmitResult } from "@/components/features/chat/decision-entity-card"

export type DecisionAnswerIntent = { answer: ReturnType<typeof E2CommandSchemas.decision_answer.parse>; key: string; uncertain?: boolean }
/** Call in a viewer/source-keyed child; restored intents are never auto-sent. */
export function useChatDecisionAnswer({ workspaceId, userId, chatId, reference, onChanged, onUnauthorized }: {
  workspaceId: string; userId: string; chatId: string; reference: DecisionRef; onChanged: () => void; onUnauthorized: () => void
}) {
  const source = reference.decision.source
  const storageKey = `chat-decision:${JSON.stringify([workspaceId, userId, chatId, source.type, source.id])}`
  const [recovery] = useState<DecisionAnswerIntent | null>(() => {
    try {
      const stored = JSON.parse(sessionStorage.getItem(storageKey) ?? "null")
      if (!stored || typeof stored.key !== "string" || !stored.key) return null
      const answer = E2CommandSchemas.decision_answer.parse(stored.answer)
      return answer.source_id === source.id ? { answer, key: stored.key, uncertain: true } : null
    } catch { return null }
  })
  const [message, setMessage] = useState("")
  const pending = useRef(recovery)
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const mutation = useMutation({ retry: false, mutationFn: async (intent: DecisionAnswerIntent): Promise<DecisionSubmitResult> => {
    const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/decisions/${source.type}/${encodeURIComponent(source.id)}/answer?${new URLSearchParams({ workspace_id: workspaceId })}`, {
      method: "POST", headers: { "Content-Type": "application/json", "Idempotency-Key": intent.key }, body: JSON.stringify(intent.answer),
    })
    if (response.ok) {
      const receipt = DecisionRefSchema.parse(await response.json())
      if (receipt.decision.source.id !== source.id || receipt.decision.source.type !== source.type || receipt.decision.status === "pending") throw new Error("Invalid source receipt")
      return "saved"
    }
    if ([401, 403, 404].includes(response.status)) { if (mounted.current) onUnauthorized(); return intent.uncertain ? "unknown" : "refused" }
    if (response.status === 409) return "conflict"
    if ([400, 422].includes(response.status)) {
      const body = await response.json().catch(() => null)
      setMessage(typeof body?.error === "string" ? body.error : "Review the fields and try again.")
      return "invalid"
    }
    throw new Error("Unknown outcome")
  } })
  const answer = useCallback(async (body: DecisionAnswerIntent["answer"], key: string): Promise<DecisionSubmitResult> => {
    setMessage("")
    const captured: DecisionAnswerIntent = { answer: body, key }
    if (!mounted.current) return "refused"
    const previous = pending.current
    if (previous && (previous.key !== key || JSON.stringify(previous.answer) !== JSON.stringify(body))) return "conflict"
    const intent = previous ?? captured
    try {
      const stored = JSON.parse(sessionStorage.getItem(storageKey) ?? "null")
      if (stored && (stored.key !== key || JSON.stringify(stored.answer) !== JSON.stringify(body))) return "conflict"
    } catch { setMessage("Your browser could not save the reply. No response was sent."); return "invalid" }
    try { sessionStorage.setItem(storageKey, JSON.stringify(intent)) } catch { setMessage("Your browser could not save the reply. No response was sent."); return "invalid" }
    pending.current = intent
    try {
      const result = await mutation.mutateAsync(intent)
      if (result === "saved" || result === "invalid" && !intent.uncertain || result === "refused") { pending.current = null; sessionStorage.removeItem(storageKey) }
      if (result === "saved" && mounted.current) onChanged()
      if (result === "invalid" && intent.uncertain) return "unknown"
      return result
    } catch {
      intent.uncertain = true
      if (mounted.current) { try { sessionStorage.setItem(storageKey, JSON.stringify(intent)) } catch { /* Keep the original durable key conservatively. */ } }
      return "unknown"
    }
  }, [mutation, onChanged, storageKey])
  const reviewConflict = useCallback(() => {
    pending.current = null
    try { sessionStorage.removeItem(storageKey) } catch { /* A retained intent blocks a later fresh mount conservatively. */ }
  }, [storageKey])
  return { answer, recovery, reviewConflict, message }
}
