"use client"

import { useCallback } from "react"
import { toast } from "sonner"
import { encodedByteLength, WS_MAX_OUTBOUND_FRAME_BYTES } from "@/hooks/use-websocket"
import type { PromptInputMessage } from "@/components/ai-elements/prompt-input"
import {
  composeMessageWithAttachments,
  hasPendingUploads,
  sendableAttachments,
  type OutgoingAttachment,
} from "@/lib/attachment-message"

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
  /** useWebSocket-backed send, exposed via useChat's sendMessage. Receives
   *  the COMPOSED content — the user's text plus the attachment block. */
  sendMessage: (text: string) => void
  /** This session's composer attachments, in the order the user added them.
   *  Passed in rather than read from the store here so the hook stays a pure
   *  function of its inputs and the store stays the composer's business. */
  attachments?: OutgoingAttachment[]
  /** Receives the user's OWN text, deliberately not the composed content:
   *  its consumer is session auto-titling, and a title derived from the
   *  appended attachment block would name every such session after the
   *  block. Attachment-only sends still title correctly — the titler reads
   *  the attachment names straight from the store. */
  onSend?: (sessionId: string, text: string) => void
  /** Called after a message actually goes out, so the caller can clear the
   *  input/draft. Deliberately NOT called when the size guard blocks the
   *  send — the user's draft must survive so they can trim and retry
   *  without retyping. */
  onSent: () => void
}

/** The composer's submit handler.
 *
 *  Composes first, then guards. The message the user sends is their text plus
 *  a block naming every attachment they uploaded (lib/attachment-message.ts):
 *  the upload endpoint puts the file in the agent's container, and until this
 *  ran, nothing ever told the agent it was there — the file was uploaded,
 *  stored, and never mentioned.
 *
 *  The size guard therefore measures the COMPOSED content, not the user's
 *  text. The appended block is part of the frame the server has to accept,
 *  and an over-cap frame is not rejected gracefully: readPump treats it as a
 *  read error and tears the whole connection down. Sizing the text alone
 *  would leave a draft that fits and a message that does not.
 *
 *  Nothing is cleared on a refusal — `onSent` is the only thing that clears
 *  the draft or the attachments, and it fires only after the send is away. */
export function useMessageSubmit({
  sessionId,
  isStreaming,
  ensureSession,
  sendMessage,
  attachments,
  onSend,
  onSent,
}: UseMessageSubmitOptions) {
  return useCallback(
    async (message: PromptInputMessage) => {
      if (isStreaming) return
      const text = message.text?.trim() ?? ""
      const items = attachments ?? []

      // An upload still in flight has no path yet. Sending now would produce
      // a message that names some of the files the user attached and stays
      // silent about the rest — the same defect, one file narrower.
      if (hasPendingUploads(items)) {
        toast.error("Still uploading — wait for the attachment to finish, then send.")
        return
      }

      // An attachment with no caption is a real message. Requiring text here
      // is how a photo sent from a phone used to disappear without a trace.
      if (!text && sendableAttachments(items).length === 0) return

      const content = composeMessageWithAttachments(text, items)

      const sizeCheck = checkChatMessageSize(sessionId, content)
      if (!sizeCheck.ok) {
        toast.error(sizeCheck.message)
        return
      }

      await ensureSession()
      sendMessage(content)
      onSend?.(sessionId, text)
      onSent()
    },
    [sessionId, isStreaming, ensureSession, sendMessage, attachments, onSend, onSent],
  )
}
