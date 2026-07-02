"use client"

import { useCallback } from "react"
import { toast } from "sonner"
import { encodedByteLength, WS_MAX_OUTBOUND_FRAME_BYTES } from "@/hooks/use-websocket"
import type { PromptInputMessage } from "@/components/ai-elements/prompt-input"

/** Result of a pre-send size check on a chat message. */
export interface MessageSizeCheck {
  ok: boolean
  sizeBytes: number
  limitBytes: number
  /** Human-readable error, only meaningful when `ok` is false. */
  message: string
}

function formatKB(bytes: number): string {
  return (bytes / 1024).toFixed(1)
}

/** Pre-flight size check for the outbound `send_message` WS frame.
 *
 *  Mirrors the exact wire envelope useChat's sendMessage / regenerateLastTurn
 *  / editAndResend hand to useWebSocket's send() (hooks/use-chat.ts): the
 *  `{session_id, content}` payload is JSON-stringified once, wrapped in the
 *  `{type, payload}` envelope, and the whole thing is JSON-stringified again
 *  by send() before it hits the wire. Sized in UTF-8 bytes — the unit the
 *  server's inbound frame cap actually enforces (wsMaxInboundFrameBytes,
 *  internal/ws/hub.go) — not JS string length, which undercounts every
 *  multi-byte character.
 *
 *  Without this guard, a paste over the server's 64 KiB frame cap doesn't
 *  get rejected gracefully: readPump treats the oversize frame as a read
 *  error and tears down the whole connection, silently dropping the
 *  message and every other in-flight subscription.
 */
export function checkChatMessageSize(sessionId: string, content: string): MessageSizeCheck {
  const frame = JSON.stringify({
    type: "send_message",
    payload: JSON.stringify({ session_id: sessionId, content }),
  })
  const sizeBytes = encodedByteLength(frame)
  const limitBytes = WS_MAX_OUTBOUND_FRAME_BYTES
  const ok = sizeBytes <= limitBytes
  return {
    ok,
    sizeBytes,
    limitBytes,
    message: ok
      ? ""
      : `Message is too large (${formatKB(sizeBytes)} KB, limit ${formatKB(limitBytes)} KB) — trim it or attach it as a file instead.`,
  }
}

export interface UseMessageSubmitOptions {
  sessionId: string
  isStreaming: boolean
  ensureSession: () => Promise<void>
  /** useWebSocket-backed send, exposed via useChat's sendMessage. */
  sendMessage: (text: string) => void
  onSend?: (sessionId: string, text: string) => void
  /** Called after a message actually goes out, so the caller can clear the
   *  input/draft. Deliberately NOT called when the size guard blocks the
   *  send — the user's draft must survive so they can trim and retry
   *  without retyping. */
  onSent: () => void
}

/** The composer's submit handler. Runs the size guard before anything else
 *  touches the network or clears the draft: a message that's too large is
 *  rejected locally with a clear, actionable error instead of silently
 *  killing the WebSocket connection. */
export function useMessageSubmit({
  sessionId,
  isStreaming,
  ensureSession,
  sendMessage,
  onSend,
  onSent,
}: UseMessageSubmitOptions) {
  return useCallback(
    async (message: PromptInputMessage) => {
      const text = message.text?.trim()
      if (!text || isStreaming) return

      const sizeCheck = checkChatMessageSize(sessionId, text)
      if (!sizeCheck.ok) {
        toast.error(sizeCheck.message)
        return
      }

      await ensureSession()
      sendMessage(text)
      onSend?.(sessionId, text)
      onSent()
    },
    [sessionId, isStreaming, ensureSession, sendMessage, onSend, onSent],
  )
}
