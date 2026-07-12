"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import {
  PromptInput,
  PromptInputTextarea,
  PromptInputFooter,
  PromptInputSubmit,
} from "@/components/ai-elements/prompt-input"
import { useComposerStore } from "@/stores/composer-store"
import { useMessageSubmit } from "../hooks/use-message-submit"
import { MentionAutocomplete, type CrewMember } from "./mention-autocomplete"
import { AttachmentZone, AttachmentButton } from "./attachment-zone"

interface ChatComposerProps {
  agentId: string
  sessionId: string
  agentName?: string
  /** "desktop" wraps the input in AttachmentZone + mention autocomplete +
   *  attachment button; "mobile" renders the bare input, matching the two
   *  historical branches of ChatPanel. */
  variant: "mobile" | "desktop"
  isStreaming: boolean
  connectionStatus: string
  stopGeneration: () => void
  ensureSession: () => Promise<void>
  sendMessage: (text: string) => void
  onSend?: (sessionId: string, text: string) => void
  /** Called after a message actually went out (size guard passed) — the
   *  parent bumps its pin-to-top nonce. Input/draft/attachment clearing is
   *  handled here, inside the composer. */
  onSent?: () => void
  /** Pre-populate the input on mount / when it changes. */
  initialInput?: string
  /** Group-chat members for @mention autocomplete (desktop only). */
  mentionMembers?: CrewMember[]
}

/**
 * The chat input, extracted from ChatPanel so per-keystroke state updates
 * re-render ONLY this component — previously the `input` state lived in the
 * same component that maps every conversation turn inside an
 * AnimatePresence, so typing re-reconciled the entire message list
 * (O(turns) per keystroke on the app's hottest interactive path).
 */
export function ChatComposer({
  agentId,
  sessionId,
  agentName,
  variant,
  isStreaming,
  connectionStatus,
  stopGeneration,
  ensureSession,
  sendMessage,
  onSend,
  onSent,
  initialInput,
  mentionMembers,
}: ChatComposerProps) {
  const [input, setInput] = useState(initialInput ?? "")

  // Pre-populate input when a new session is started with a prefill value.
  useEffect(() => {
    if (initialInput) setInput(initialInput)
  }, [initialInput])

  // Narrow selectors: this component only ever calls the two clear actions;
  // subscribing to the whole store would re-render the composer on every
  // draft/attachment write for ANY session.
  const clearDraft = useComposerStore((s) => s.clearDraft)
  const clearAttachments = useComposerStore((s) => s.clearAttachments)

  const mentionTextareaRef = useRef<HTMLTextAreaElement>(null)
  const handleMentionPick = useCallback((member: CrewMember, atIndex: number) => {
    setInput((prev) => {
      const after = prev.slice(atIndex)
      const ws = after.search(/\s/)
      const end = ws === -1 ? prev.length : atIndex + ws
      return prev.slice(0, atIndex) + "@" + member.slug + " " + prev.slice(end)
    })
  }, [])

  // Only fires when the message actually went out — a size-guard rejection
  // must leave the draft intact so the user can trim and resend.
  const handleSent = useCallback(() => {
    setInput("")
    clearDraft(sessionId)
    clearAttachments(sessionId)
    onSent?.()
  }, [clearDraft, clearAttachments, sessionId, onSent])

  const handleSubmit = useMessageSubmit({
    sessionId,
    isStreaming,
    ensureSession,
    sendMessage,
    onSend,
    onSent: handleSent,
  })

  const chatStatus = isStreaming ? ("streaming" as const) : ("ready" as const)
  const placeholder = agentName ? `Message ${agentName}...` : "Send a message..."
  const submitDisabled = !isStreaming && (!input.trim() || connectionStatus !== "connected")

  if (variant === "mobile") {
    return (
      <div className="p-3 shrink-0">
        <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
          <PromptInputTextarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder={placeholder}
            className="min-h-[44px]"
          />
          <PromptInputFooter className="justify-end p-2">
            <PromptInputSubmit disabled={submitDisabled} status={chatStatus} onStop={stopGeneration} />
          </PromptInputFooter>
        </PromptInput>
      </div>
    )
  }

  return (
    <div className="mx-auto w-full max-w-3xl p-3 md:px-6 shrink-0">
      <AttachmentZone agentId={agentId} sessionId={sessionId}>
        <MentionAutocomplete
          text={input}
          textareaRef={mentionTextareaRef}
          members={mentionMembers ?? []}
          onPick={handleMentionPick}
        />
        <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
          <PromptInputTextarea
            ref={mentionTextareaRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder={placeholder}
            className="min-h-[44px]"
          />
          <PromptInputFooter className="justify-between p-2 gap-2">
            <div className="flex items-center gap-1">
              <AttachmentButton agentId={agentId} sessionId={sessionId} />
            </div>
            <PromptInputSubmit disabled={submitDisabled} status={chatStatus} onStop={stopGeneration} />
          </PromptInputFooter>
        </PromptInput>
      </AttachmentZone>
    </div>
  )
}
