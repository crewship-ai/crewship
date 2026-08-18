"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import {
  PromptInput,
  PromptInputTextarea,
  PromptInputFooter,
  PromptInputSubmit,
} from "@/components/ai-elements/prompt-input"
import { useComposerStore, type ComposerAttachment } from "@/stores/composer-store"
import { useIsMobile } from "@/hooks/use-mobile"
import {
  composeMessageWithAttachments,
  hasPendingUploads,
  sendableAttachments,
} from "@/lib/attachment-message"
import { useMessageSubmit } from "../hooks/use-message-submit"
import { MentionAutocomplete, type CrewMember } from "./mention-autocomplete"
import {
  AttachmentZone,
  AttachmentButton,
  CameraButton,
  EnsureChatSessionProvider,
} from "./attachment-zone"
import { AskFormSheet } from "../asks/ask-form-sheet"
import { ASK_SUBMISSION_METADATA_KEY, type AskSubmissionEnvelope } from "../asks/ask-envelope"
import { recordAskProvenance } from "../asks/ask-provenance"
import { isAttachmentField, type AskForm, type RenderAskTemplate } from "../asks/types"

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
  /** Make sure this session's `chats` row exists, and report whether it does.
   *  `false` means the message must not go out — see useMessageSubmit.
   *
   *  It gates UPLOADS too, through EnsureChatSessionProvider below: the
   *  attachments endpoint resolves the chat row before it takes a byte, so on a
   *  draft session (an agent whose first conversation this is) a file could not
   *  be attached until something had been sent. Attaching is an intent to
   *  converse, so it creates the row exactly as the first message does — one
   *  creation path, idempotent per session. */
  ensureSession: () => Promise<boolean>
  /** useChat's send. The second argument is metadata that rides WITH the
   *  message rather than inside it (the ask-form submission envelope today);
   *  `text` is identical whether or not it is present.
   *
   *  Declared with both parameters on purpose. TypeScript accepts a
   *  one-parameter function here either way, so a narrower prop type would
   *  typecheck a caller that silently drops the envelope — which is exactly
   *  the failure this feature is fixing, one layer up. */
  sendMessage: (text: string, metadata?: Record<string, unknown>) => void
  onSend?: (sessionId: string, text: string) => void
  /** Called after a message actually went out (size guard passed) — the
   *  parent bumps its pin-to-top nonce. Input/draft/attachment clearing is
   *  handled here, inside the composer. */
  onSent?: () => void
  /** Pre-populate the input on mount / when it changes. */
  initialInput?: string
  /** Group-chat members for @mention autocomplete (desktop only). */
  mentionMembers?: CrewMember[]
  /** The ask form the user opened from the chip rail, or null. The sheet is
   *  mounted HERE rather than in ChatPanel for two reasons that pull the same
   *  way: it has to grow directly above the composer inside the same column
   *  (PRD §5.2), and submitting it has to go down the composer's own
   *  `useMessageSubmit` path so the size guard, the still-uploading refusal
   *  and the attachment block apply to a form exactly as they do to something
   *  typed. Mounting it in the parent would have meant a second submit path. */
  askForm?: AskForm | null
  /** Fired when the sheet closes — cancelled, escaped, or sent. */
  onCloseAskForm?: () => void
  /** `lib/ask-template.ts`'s renderer, injected from ChatPanel. Optional
   *  because a composer with no ask forms — every caller that predates this —
   *  never needs one; the sheet only mounts when a form AND a renderer are
   *  both present, so a form can never be sent through a renderer that is not
   *  the product's. */
  renderAskTemplate?: RenderAskTemplate
}

/** Stable empty list for sessions with nothing attached. */
const NO_ATTACHMENTS: ComposerAttachment[] = []

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
  askForm,
  onCloseAskForm,
  renderAskTemplate,
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
  // This session's attachments, so the submit handler can name them in the
  // message. Subscribed (not read at submit time) so `submitDisabled` can
  // also see them — attaching a file must enable Send on an empty draft.
  // Falls back to a shared constant so an empty list keeps a stable identity
  // and does not re-create the submit callback on every render.
  const sessionAttachments = useComposerStore((s) => s.attachments[sessionId]) ?? NO_ATTACHMENTS

  const mentionTextareaRef = useRef<HTMLTextAreaElement>(null)
  const handleMentionPick = useCallback((member: CrewMember, atIndex: number) => {
    setInput((prev) => {
      const after = prev.slice(atIndex)
      const ws = after.search(/\s/)
      const end = ws === -1 ? prev.length : atIndex + ws
      return prev.slice(0, atIndex) + "@" + member.slug + " " + prev.slice(end)
    })
  }, [])

  // Set by handleSent, read by the ask sheet's submit. `useMessageSubmit`
  // returns nothing either way, and the sheet must NOT close on a send the
  // size guard refused — everything the user filled in has to still be there
  // for them to trim and retry, which is the same rule the draft follows.
  const sentRef = useRef(false)

  // Only fires when the message actually went out — a size-guard rejection
  // must leave the draft intact so the user can trim and resend.
  const handleSent = useCallback(() => {
    sentRef.current = true
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
    attachments: sessionAttachments,
    onSend,
    onSent: handleSent,
  })

  /** Submitting a form is submitting a message. Same hook, same guards, same
   *  attachment block — the only thing the form contributed is the text.
   *
   *  …and the envelope, which is the sheet's third argument and rides WITH the
   *  message as metadata rather than in it (asks/ask-envelope.ts). The text
   *  handed to `handleSubmit` is byte-for-byte what it was before the envelope
   *  existed, which is what keeps a form submission an ordinary message to
   *  every CLI adapter. From here it is the send path's business:
   *  useMessageSubmit sizes it into the frame guard and passes it to
   *  useChat's sendMessage, which puts it in the `send_message` payload and
   *  stamps it on the optimistic turn. */
  const handleAskSubmit = useCallback(
    async (form: AskForm, text: string, envelope: AskSubmissionEnvelope): Promise<boolean> => {
      sentRef.current = false
      // The legacy content-keyed provenance map, still recorded because the
      // turn-renderer falls back to it for anything that reaches it without an
      // envelope. Keyed by the content that will actually be on the turn,
      // which is the COMPOSED string (rendered template + attachment block),
      // not the template output on its own.
      recordAskProvenance(
        sessionId,
        composeMessageWithAttachments(text, sessionAttachments),
        form.label,
      )
      // `files` is PromptInput's own (unused) attachment channel — this
      // composer's attachments live in the store and are read by
      // useMessageSubmit, so the field is present and empty.
      await handleSubmit({
        text,
        files: [],
        metadata: { [ASK_SUBMISSION_METADATA_KEY]: envelope },
      })
      return sentRef.current
    },
    [handleSubmit, sessionId, sessionAttachments],
  )

  const noopCloseAskForm = useCallback(() => {}, [])
  const isMobile = useIsMobile()
  // A bottom sheet on a phone, and also below the chat page's 900px compact
  // breakpoint — that is the point at which the page hands this composer the
  // "mobile" variant, so the variant is the signal rather than a third
  // media query that could disagree with it.
  const askSheet = renderAskTemplate ? (
    <AskFormSheet
      form={askForm ?? null}
      renderTemplate={renderAskTemplate}
      agentId={agentId}
      sessionId={sessionId}
      compact={variant === "mobile" || isMobile}
      disabled={isStreaming || connectionStatus !== "connected"}
      onSubmit={handleAskSubmit}
      onClose={onCloseAskForm ?? noopCloseAskForm}
    />
  ) : null

  /**
   * Who draws the attachment chips right now.
   *
   * The sheet's `file` / `photo` slot mounts the same AttachmentZone this
   * composer does, over the same per-session list — so with a sheet open the
   * uploaded file was on screen twice, once under the field that asked for it
   * and once under the input. Both were telling the truth; one of them has to
   * stop.
   *
   * The sheet wins, because that is where the question is: the chip is the
   * field's answer, and removing it there is editing that answer. But only
   * when the sheet actually renders an upload control — a form with no
   * `file` / `photo` field has nowhere to show them, and the composer's
   * paperclip is then the only way anything got attached at all, so hiding
   * them there would hide the attachment completely.
   */
  const sheetOwnsChips =
    !!renderAskTemplate && !!askForm && askForm.fields.some(isAttachmentField)

  const chatStatus = isStreaming ? ("streaming" as const) : ("ready" as const)
  const placeholder = agentName ? `Message ${agentName}...` : "Send a message..."
  // An attachment is content: a photo with no caption is a message, and
  // leaving Send disabled for it is how one used to be silently dropped.
  //
  // But only an attachment that can actually go. Counting the raw list meant a
  // chip whose upload had been REFUSED enabled Send on an empty draft, and
  // pressing it did nothing whatsoever — useMessageSubmit has nothing to send,
  // so it returns, and the user is left pressing a live-looking button. An
  // upload still in flight does keep Send live on purpose: pressing it there
  // gets the "still uploading, wait" toast, which is an answer.
  const hasContent =
    !!input.trim() ||
    sendableAttachments(sessionAttachments).length > 0 ||
    hasPendingUploads(sessionAttachments)
  const submitDisabled = !isStreaming && (!hasContent || connectionStatus !== "connected")

  if (variant === "mobile") {
    // The mobile branch used to be a bare input: no attachments at all, so a
    // phone — the device that actually has a camera — was the one surface that
    // could not send a picture. It now wraps the same AttachmentZone the
    // desktop branch uses (which is also what renders the resulting chips, so
    // an upload is visible rather than a black hole) and puts a camera next to
    // the paperclip. Mention autocomplete stays desktop-only: it needs a
    // keyboard-driven caret, and group chat is not a phone surface yet.
    return (
      <EnsureChatSessionProvider ensureSession={ensureSession}>
      {askSheet}
      <div className="p-3 shrink-0">
        <AttachmentZone agentId={agentId} sessionId={sessionId} showChips={!sheetOwnsChips}>
          <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
            <PromptInputTextarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={placeholder}
              className="min-h-[44px]"
            />
            <PromptInputFooter className="justify-between p-2 gap-2">
              <div className="flex items-center gap-1">
                <CameraButton agentId={agentId} sessionId={sessionId} />
                <AttachmentButton agentId={agentId} sessionId={sessionId} />
              </div>
              <PromptInputSubmit disabled={submitDisabled} status={chatStatus} onStop={stopGeneration} />
            </PromptInputFooter>
          </PromptInput>
        </AttachmentZone>
      </div>
      </EnsureChatSessionProvider>
    )
  }

  return (
    <EnsureChatSessionProvider ensureSession={ensureSession}>
    {askSheet}
    <div className="mx-auto w-full max-w-3xl p-3 md:px-6 shrink-0">
      <AttachmentZone agentId={agentId} sessionId={sessionId} showChips={!sheetOwnsChips}>
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
    </EnsureChatSessionProvider>
  )
}
