"use client"

import { useEffect, useRef } from "react"

import { apiFetch } from "@/lib/api-fetch"

/** How often an open restricted chat checks for messages it did not send. */
export const RESTRICTED_CHAT_POLL_MS = 10_000

/** Identifies a message list well enough to notice that it changed. */
export function messagesSignature(messages: readonly { id?: string }[]): string {
  return `${messages.length}:${messages[messages.length - 1]?.id ?? ""}`
}

interface Options {
  /** Poll only for a restricted chat (no WebSocket in that session). */
  enabled: boolean
  sessionId: string | null | undefined
  workspaceId: string | null | undefined
  /** Skip ticks while true, e.g. while this tab's own reply is streaming. */
  paused: boolean
  /** Signature of the history currently on screen, or null before the first
   *  load settles (nothing to compare against yet). */
  getKnownSignature: () => string | null
  /** The server holds messages the screen does not: reload history. */
  onChange: () => void
}

/**
 * Updates for a restricted chat without a WebSocket.
 *
 * A trusted chat learns about new messages from the session channel. A
 * restricted session has no socket (the allowlist refuses /ws-token), and the
 * restricted run streams only the reply to THIS tab's own message. A message
 * written by another participant of a shared restricted chat, or a reply
 * produced in another tab, would otherwise appear only on reload.
 *
 * So, while the chat is open and the tab is visible, this re-reads
 * `GET /api/v1/chats/{id}/messages` (allowlisted) every
 * {@link RESTRICTED_CHAT_POLL_MS} and asks for a history reload when the list
 * differs from the one on screen. It never polls while hidden, while paused or
 * with a request still in flight, checks once immediately when the tab becomes
 * visible again, stops on unmount, and stops for good on 404 (the chat is gone
 * or no longer readable — the screen's own error handling takes over).
 */
export function useRestrictedChatPoll({ enabled, sessionId, workspaceId, paused, getKnownSignature, onChange }: Options): void {
  const pausedRef = useRef(paused)
  const knownRef = useRef(getKnownSignature)
  const changeRef = useRef(onChange)
  useEffect(() => {
    pausedRef.current = paused
    knownRef.current = getKnownSignature
    changeRef.current = onChange
  })

  useEffect(() => {
    if (!enabled || !sessionId || !workspaceId || typeof document === "undefined") return
    const controller = new AbortController()
    let inFlight = false
    let stopped = false
    let timer: ReturnType<typeof setInterval> | undefined

    const stop = () => {
      stopped = true
      if (timer) clearInterval(timer)
      document.removeEventListener("visibilitychange", onVisible)
    }

    const tick = async () => {
      if (stopped || inFlight || pausedRef.current || document.visibilityState !== "visible") return
      const known = knownRef.current()
      if (known === null) return
      inFlight = true
      try {
        const res = await apiFetch(
          `/api/v1/chats/${encodeURIComponent(sessionId)}/messages?workspace_id=${encodeURIComponent(workspaceId)}`,
          { signal: controller.signal },
        )
        if (res.status === 404) { stop(); return }
        if (!res.ok) return
        const data = (await res.json()) as { messages?: { id?: string }[] } | null
        if (controller.signal.aborted || pausedRef.current) return
        if (messagesSignature(data?.messages ?? []) !== knownRef.current()) changeRef.current()
      } catch {
        // Transient: the next tick tries again.
      } finally {
        inFlight = false
      }
    }

    function onVisible() {
      if (document.visibilityState === "visible") void tick()
    }

    timer = setInterval(() => { void tick() }, RESTRICTED_CHAT_POLL_MS)
    document.addEventListener("visibilitychange", onVisible)
    return () => {
      controller.abort()
      stop()
    }
  }, [enabled, sessionId, workspaceId])
}
