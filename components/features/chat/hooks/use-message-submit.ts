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
 *  `metadata` is part of that payload when a message carries any — today, an
 *  ask-form submission envelope, which is unbounded in principle (a long-answer
 *  field, a form with many fields, a list of upload paths). It MUST be measured:
 *  the guard's whole job is to know what the server will be asked to accept, and
 *  a guard that sized the text alone would pass a frame the socket then dies on.
 *  Omitted from the sized payload when absent, exactly as sendMessage omits it,
 *  so a plain message is measured against the same bytes as before.
 *
 *  Without this guard, a paste over the server's 64 KiB frame cap doesn't
 *  get rejected gracefully: readPump treats the oversize frame as a read
 *  error and tears down the whole connection, silently dropping the
 *  message and every other in-flight subscription.
 */
export function checkChatMessageSize(
  sessionId: string,
  content: string,
  metadata?: Record<string, unknown>,
): MessageSizeCheck {
  const frame = JSON.stringify({
    type: "send_message",
    payload: JSON.stringify({
      session_id: sessionId,
      content,
      ...(metadata ? { metadata } : {}),
    }),
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

/** What this hook's submit handler accepts.
 *
 *  A superset of `PromptInputMessage`, because the handler is BOTH the
 *  `<PromptInput onSubmit>` callback and the path the ask-form sheet submits
 *  through, and only the second one has anything extra to say. The extra field
 *  goes on the message rather than into a second parameter for a blunt reason:
 *  PromptInput already occupies the second argument with the form's
 *  `FormEvent`, so a `(message, metadata)` signature would silently receive a
 *  DOM event as the metadata on every typed message.
 *
 *  PromptInput never sets `metadata`, so a typed message arrives with it
 *  undefined and everything downstream behaves as it did before it existed.
 */
export type SubmittedMessage = PromptInputMessage & {
  /** Structured data to carry WITH the message, not inside it — the ask-form
   *  submission envelope (asks/ask-envelope.ts), keyed by
   *  `ASK_SUBMISSION_METADATA_KEY`. `content` is unaffected by its presence. */
  metadata?: Record<string, unknown>
}

export interface UseMessageSubmitOptions {
  sessionId: string
  isStreaming: boolean
  /** Makes sure the session's `chats` row exists before anything is sent into
   *  it, and reports whether it does. `false` is a hard stop: the WS channel
   *  authorizer refuses a `send_message` for a session with no row
   *  (internal/ws/channel_auth.go), so sending anyway would drop the user's
   *  message on the floor while the UI acted as though it had been saved. */
  ensureSession: () => Promise<boolean>
  /** useWebSocket-backed send, exposed via useChat's sendMessage. Receives
   *  the COMPOSED content — the user's text plus the attachment block — and,
   *  when the submission carried one, the metadata that rides beside it. */
  sendMessage: (text: string, metadata?: Record<string, unknown>) => void
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
    async (message: SubmittedMessage) => {
      if (isStreaming) return
      const text = message.text?.trim() ?? ""
      const items = attachments ?? []
      const metadata = message.metadata

      // An upload still in flight has no path yet. Sending now would produce
      // a message that names some of the files the user attached and stays
      // silent about the rest — the same defect, one file narrower.
      if (hasPendingUploads(items)) {
        toast.error("Still uploading — wait for the attachment to finish, then send.")
        return
      }

      // An attachment with no caption is a real message. Requiring text here
      // is how a photo sent from a phone used to disappear without a trace.
      if (!text && sendableAttachments(items).length === 0) {
        // There IS something in the composer, it just cannot go: every
        // attachment failed to upload. Returning silently is what made a
        // refused upload look like a working one — the user presses Send on a
        // composer that visibly holds a file and nothing happens at all.
        if (items.length > 0) {
          toast.error("Nothing to send — no attachment uploaded", {
            description:
              "Retry the failed attachment, or remove it and write a message instead.",
          })
        }
        return
      }

      const content = composeMessageWithAttachments(text, items)

      const sizeCheck = checkChatMessageSize(sessionId, content, metadata)
      if (!sizeCheck.ok) {
        toast.error(sizeCheck.message)
        return
      }

      // The row has to exist before the message can go anywhere. When it
      // could not be created, nothing else runs: no send, no `onSend` (which
      // would put a phantom row in the sidebar and fire an auto-title PATCH at
      // a chat that isn't there), and no `onSent` — so the draft and the
      // attachments survive for a retry. The caller has already told the user.
      if (!(await ensureSession())) return
      // Called with ONE argument when there is nothing to carry, not with a
      // trailing `undefined`. A plain composer message, a suggestion chip and
      // `?prompt=` all come through here, and "the send path is untouched for
      // them" has to mean the call itself too — `arguments.length` is
      // observable, to a spy in a test and to any wrapper a caller passes in.
      if (metadata) {
        sendMessage(content, metadata)
      } else {
        sendMessage(content)
      }
      onSend?.(sessionId, text)
      onSent()
    },
    [sessionId, isStreaming, ensureSession, sendMessage, attachments, onSend, onSent],
  )
}
