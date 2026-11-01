"use client"

import { ARTIFACT_CREW_NOT_ALLOWED, ARTIFACT_CREW_NOT_ALLOWED_MESSAGE } from "@/lib/artifact-refusal"
import { useCallback, useEffect, useId, useRef, useState } from "react"
import { AlertTriangle, FilePlus2, X } from "lucide-react"
import { NewArtifactMenu } from "../artifact-context/new-artifact/new-artifact-menu"
import { availableArtifactKinds } from "../artifact-context/new-artifact/new-artifact-kinds"
import { chatArtifactProjects } from "@/lib/feature-flags"
import { Button } from "@/components/ui/button"
import { Alert, AlertDescription } from "@/components/ui/alert"

import {
  PromptInput,
  PromptInputTextarea,
  PromptInputFooter,
  PromptInputSubmit,
} from "@/components/ai-elements/prompt-input"
import { useComposerStore, type ComposerAttachment } from "@/stores/composer-store"
import { noteUnsentDraft } from "@/lib/signed-out"
import { useAttachmentScope } from "@/hooks/use-attachment-scope"
import { useSessionSafe } from "@/hooks/use-auth"
import { useIsMobile } from "@/hooks/use-mobile"
import {
  composeMessageWithAttachments,
  hasPendingUploads,
  sendableAttachments,
  unattachedAttachments,
} from "@/lib/attachment-message"
import { useMessageSubmit, type SubmittedMessage } from "../hooks/use-message-submit"
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
  /** Confirmed support in this private chat with no selected Page. */
  documentCreationEnabled?: boolean
  /** Server-confirmed support for explicit, private artifact creation. */
  artifactCreationEnabled?: boolean
  /** A send the server refused for a typed reason. A new `id` restores the last
   *  typed message and shows the specific reason instead of a generic failure. */
  sendRefusal?: { id: number; code: string } | null
  /** "desktop" wraps the input in AttachmentZone + mention autocomplete +
   *  attachment button; "mobile" renders the bare input, matching the two
   *  historical branches of ChatPanel. */
  variant: "mobile" | "desktop"
  isStreaming: boolean
  connectionStatus: string
  /** A draft session with no row and no profile yet (#2898): its first send
   *  creates the row through ensureSession and waits for the connection, so
   *  Send is offered although the socket is not open. */
  draftSendable?: boolean
  stopGeneration: () => void
  /** Stop was sent and the server has not confirmed it yet. */
  stopping?: boolean
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
  /** Row-only variant for uploads. The send path's `ensureSession` also waits
   *  for the connection and refuses once the person has left this chat, which is
   *  wrong for an upload that finishes in the background. Defaults to
   *  `ensureSession`. */
  ensureSessionForUpload?: () => Promise<boolean>
  /** useChat's send. The second argument is metadata that rides WITH the
   *  message rather than inside it (the ask-form submission envelope today);
   *  `text` is identical whether or not it is present.
   *
   *  Declared with both parameters on purpose. TypeScript accepts a
   *  one-parameter function here either way, so a narrower prop type would
   *  typecheck a caller that silently drops the envelope — which is exactly
   *  the failure this feature is fixing, one layer up. */
  sendMessage: (text: string, metadata?: Record<string, unknown>) => void | boolean | Promise<boolean>
  onSend?: (sessionId: string, text: string) => void
  /** Called after a message actually went out (size guard passed) — the
   *  parent bumps its pin-to-top nonce. Input/draft/attachment clearing is
   *  handled here, inside the composer. */
  onSent?: () => void
  /** Pre-populate the input on mount / when it changes. */
  initialInput?: string
  /** Explicit user-selected reference; append visibly and never submit automatically. */
  contextRequest?: { id: number; text: string }
  onContextApplied?: () => void
  /** Known participants for @mention autocomplete on desktop and mobile. */
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

/**
 * Harbor composer: an 18px card on the page ground with a brand focus ring.
 * The border and radius live on the input group the prompt input renders,
 * so the form reaches it by its data-slot instead of drawing a second box.
 */
/** At most two names, then a count: a long list would bury the instruction. */
function summarizeNames(names: string[]): string {
  if (names.length <= 2) return names.join(", ")
  return `${names.slice(0, 2).join(", ")} and ${names.length - 2} more`
}

/** How long a wait must last before it is announced (a blink is not). */
const WAIT_ANNOUNCE_DELAY_MS = 800

const COMPOSER_SHELL = [
  "[&_[data-slot=input-group]]:rounded-2xl [&_[data-slot=input-group]]:border-border [&_[data-slot=input-group]]:bg-card [&_[data-slot=input-group]]:shadow-none dark:[&_[data-slot=input-group]]:bg-card",
  "[&_[data-slot=input-group]]:transition-[border-color,box-shadow]",
  "[&_[data-slot=input-group]:focus-within]:border-primary/60 [&_[data-slot=input-group]:focus-within]:ring-[3px] [&_[data-slot=input-group]:focus-within]:ring-primary/20",
].join(" ")

export function ChatComposer({
  agentId,
  sessionId,
  agentName,
  documentCreationEnabled = false,
  artifactCreationEnabled = false,
  sendRefusal = null,
  variant,
  isStreaming,
  connectionStatus,
  draftSendable = false,
  stopGeneration,
  stopping = false,
  ensureSession,
  ensureSessionForUpload,
  sendMessage,
  onSend,
  onSent,
  initialInput,
  contextRequest,
  onContextApplied,
  mentionMembers,
  askForm,
  onCloseAskForm,
  renderAskTemplate,
}: ChatComposerProps) {
  const session = useSessionSafe()
  const userId = session.data?.user?.id
  // Never adopt legacy session-only drafts: another person may have written
  // them in this browser. Anonymous/embedded composers keep text in memory.
  const draftKey = userId ? JSON.stringify(["user", userId, sessionId]) : null
  const inputScope = draftKey ?? JSON.stringify(["anonymous", sessionId])
  const creationScope = JSON.stringify([userId, agentId, sessionId])
  const canCreateDocument = documentCreationEnabled && Boolean(userId)
  const canCreateArtifact = artifactCreationEnabled && Boolean(userId)
  const artifactScope = creationScope + ":artifact"
  const [creationSelection, setCreationSelection] = useState<string | null>(null)
  const createDocument = canCreateDocument && creationSelection === creationScope
  const createArtifact = canCreateArtifact && creationSelection === artifactScope
  const creationPacket = useRef<{ scope: string; content: string; submissionId: string } | null>(null)
  // Consent is ephemeral and belongs to one identity and conversation. It is
  // neither a persisted composer draft nor a preference for future messages.
  useEffect(() => {
    setCreationSelection(null)
    creationPacket.current = null
  }, [creationScope, canCreateDocument, canCreateArtifact])
  const lastSent = useRef<{ scope: string; text: string } | null>(null)
  const handledRefusal = useRef(sendRefusal?.id ?? 0)
  const [refusalNotice, setRefusalNotice] = useState<string | null>(null)
  const restoreInput = () => {
    const saved = draftKey ? useComposerStore.getState().drafts[draftKey] : undefined
    return saved?.trim() ? saved : initialInput ?? ""
  }
  const [inputState, setInputState] = useState(() => ({ scope: inputScope, text: restoreInput() }))
  // Resolve the new identity immediately rather than showing the previous
  // person's text for a render while an effect catches up.
  const input = inputState.scope === inputScope ? inputState.text : restoreInput()
  const setInput = useCallback((text: string) => setInputState({ scope: inputScope, text }), [inputScope])
  const setDraft = useComposerStore((s) => s.setDraft)
  const updateInput = useCallback((text: string) => {
    setInput(text)
    if (draftKey) setDraft(draftKey, text)
  }, [draftKey, setDraft, setInput])
  // A typed refusal arrives after the optimistic clear: hand the message back
  // (unless the person has already started something new) and say why.
  useEffect(() => {
    if (!sendRefusal || sendRefusal.id === handledRefusal.current) return
    handledRefusal.current = sendRefusal.id
    if (sendRefusal.code !== ARTIFACT_CREW_NOT_ALLOWED) return
    const sent = lastSent.current
    if (sent && sent.scope === inputScope && !input.trim()) updateInput(sent.text)
    setCreationSelection(null)
    creationPacket.current = null
    setRefusalNotice(ARTIFACT_CREW_NOT_ALLOWED_MESSAGE)
  }, [sendRefusal, inputScope, input, updateInput])
  // If the session ends while this is unsent, the signed-out screen says the
  // message to this agent is kept (#3787, lib/signed-out.ts). The draft itself
  // stays where it is — persisted per user and session above.
  useEffect(() => {
    if (draftKey) noteUnsentDraft(draftKey, agentName, input)
  }, [draftKey, agentName, input])

  const appliedContext = useRef<string | null>(null)
  useEffect(() => {
    if (!contextRequest) return
    const key = JSON.stringify([inputScope, contextRequest.id])
    if (appliedContext.current === key) return
    appliedContext.current = key
    updateInput(input.trim() ? `${input}\n\n${contextRequest.text}` : contextRequest.text)
    onContextApplied?.()
  }, [contextRequest, inputScope, input, updateInput, onContextApplied])

  // A handoff may arrive after the composer mounts. Preserve anything the
  // person already typed, including a restored draft for this identity.
  useEffect(() => {
    if (!initialInput) return
    const saved = draftKey ? useComposerStore.getState().drafts[draftKey] : undefined
    if (saved?.trim()) return
    setInputState((current) => current.scope === inputScope && current.text.trim()
      ? current
      : { scope: inputScope, text: initialInput })
  }, [initialInput, draftKey, inputScope])

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
  const { key: attachmentKey, waitingForSignIn, waitingReason } = useAttachmentScope(sessionId)
  const sessionAttachments = useComposerStore((s) => s.attachments[attachmentKey]) ?? NO_ATTACHMENTS

  const mentionTextareaRef = useRef<HTMLTextAreaElement>(null)
  const handleMentionPick = useCallback((member: CrewMember, atIndex: number) => {
    const after = input.slice(atIndex)
    const ws = after.search(/\s/)
    const end = ws === -1 ? input.length : atIndex + ws
    updateInput(input.slice(0, atIndex) + "@" + member.slug + " " + input.slice(end))
    requestAnimationFrame(() => {
      const caret = atIndex + member.slug.length + 2
      mentionTextareaRef.current?.focus()
      mentionTextareaRef.current?.setSelectionRange(caret, caret)
    })
  }, [input, updateInput])

  // Set by handleSent, read by both submits. `useMessageSubmit` returns nothing
  // either way, and neither caller may act as though a message went out when it
  // did not: the sheet must NOT close on a send the size guard refused —
  // everything the user filled in has to still be there for them to trim and
  // retry — and the typed path must not clear the box for the same reason.
  const sentRef = useRef(false)

  const unattached = unattachedAttachments(sessionAttachments)
  const sendBlocked = !isStreaming && (waitingForSignIn || unattached.length > 0)
  const blockReasonId = `${useId()}-send-block`
  const shown = unattached.slice(0, 2)
  const reasons = shown.filter((a) => a.failureLabel).map((a) => `${a.name}: ${a.failureLabel}`)
  const blockReasonText = !sendBlocked
    ? ""
    : waitingForSignIn
      ? waitingReason === "workspace"
        ? "Loading this chat… Send is off until it is ready."
        : "Waiting for sign-in… Send is off until your session is confirmed."
      : `Send is off: ${summarizeNames(unattached.map((a) => a.name))} ${
          unattached.length === 1 ? "is" : "are"
        } not attached${reasons.length ? ` (${reasons.join("; ")})` : ""}. Use Retry or Remove on ${
          unattached.length === 1 ? "it" : "each"
        }, then send.`
  const [announcement, setAnnouncement] = useState("")
  const announceTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const blockTextRef = useRef(blockReasonText)
  blockTextRef.current = blockReasonText
  const cancelAnnounce = useCallback(() => {
    if (announceTimer.current) clearTimeout(announceTimer.current)
    announceTimer.current = null
  }, [])
  // On a blocked attempt (click or Enter): clear first so a repeat is heard
  // again, then set. The text is read when the timer fires, and nothing is
  // announced if the block ended in between.
  const announceBlocked = useCallback(() => {
    cancelAnnounce()
    setAnnouncement("")
    announceTimer.current = setTimeout(() => {
      announceTimer.current = null
      setAnnouncement(blockTextRef.current)
    }, 50)
  }, [cancelAnnounce])
  // Announce when a block STARTS after mount (an upload failed, or a retry
  // failed again); not on mount when the store already holds failed chips, so
  // opening a conversation stays quiet. The region is cleared when it ends.
  const wasBlocked = useRef(sendBlocked)
  useEffect(() => {
    // A wait for sign-in or a workspace load is announced from its own effect
    // below, after a delay, so a blink is not announced at all.
    if (sendBlocked && !wasBlocked.current && !waitingForSignIn) setAnnouncement(blockReasonText)
    if (!sendBlocked) {
      cancelAnnounce()
      setAnnouncement("")
    }
    wasBlocked.current = sendBlocked
  }, [sendBlocked, blockReasonText, cancelAnnounce, waitingForSignIn])
  // Announce a wait once, and only if it lasts: ~1s of continuous waiting.
  const waitText = waitingForSignIn ? blockReasonText : ""
  useEffect(() => {
    if (!waitText) return
    const t = setTimeout(() => setAnnouncement(waitText), WAIT_ANNOUNCE_DELAY_MS)
    return () => {
      clearTimeout(t)
      setAnnouncement("")
    }
  }, [waitText])
  useEffect(() => cancelAnnounce, [cancelAnnounce])

  // Only fires when the message actually went out — a size-guard rejection
  // must leave the draft intact so the user can trim and resend.
  //
  // What it does NOT do is clear the textarea. This runs for BOTH submits, and
  // the attachments are the only thing both of them consumed: the composer's
  // per-session list is appended to whichever message went, typed or rendered.
  // The typed text belongs to the typed path alone — see handleTypedSubmit.
  const handleSent = useCallback(() => {
    sentRef.current = true
    clearAttachments(attachmentKey)
    onSent?.()
  }, [clearAttachments, attachmentKey, onSent])

  const handleSubmit = useMessageSubmit({
    sessionId,
    isStreaming,
    ensureSession,
    sendMessage,
    attachments: sessionAttachments,
    onSend,
    onSent: handleSent,
  })

  /** The textarea's own submit — and the only thing allowed to empty it.
   *
   *  The ask sheet reaches `handleSubmit` too, and it renders ABOVE this
   *  composer inside the same column rather than over it (asks/ask-form-sheet
   *  .tsx), so the textarea stays live and visible the whole time a form is
   *  being filled in. A note typed there is not part of what the form sends —
   *  `handleAskSubmit` passes the rendered template as the text and nothing
   *  else — so clearing on the form's behalf deleted a message the user had
   *  never sent, with no undo and nothing on screen to say it had gone.
   *
   *  `sentRef` is the same "did it actually go out" signal the sheet reads: a
   *  refused send (over the frame cap, still uploading, no session row) must
   *  leave the draft exactly where it was. */
  const handleTypedSubmit = useCallback(
    async (message: SubmittedMessage) => {
      sentRef.current = false
      // Enter in the textarea lands here; answer it instead of returning silently.
      // Rejecting makes PromptInput skip its own clearing of the text; a
      // plain return would be treated as a completed send and empty the box.
      if (sendBlocked) {
        announceBlocked()
        throw new Error("send blocked: attachment not attached")
      }
      let outgoing = message
      if (createDocument || createArtifact) {
        const content = composeMessageWithAttachments(message.text, sessionAttachments)
        if (creationPacket.current?.scope !== creationScope || creationPacket.current.content !== content) {
          creationPacket.current = { scope: creationScope, content, submissionId: crypto.randomUUID() }
        }
        const intent = createArtifact
          ? { artifact_project_intent: { submission_id: creationPacket.current.submissionId } }
          : { document_creation_intent: { submission_id: creationPacket.current.submissionId, drafting: true } }
        outgoing = { ...message, metadata: { ...message.metadata, ...intent } }
      }
      await handleSubmit(outgoing)
      if (!sentRef.current) return
      lastSent.current = { scope: inputScope, text: message.text }
      setRefusalNotice(null)
      setCreationSelection(null)
      creationPacket.current = null
      setInput("")
      if (draftKey) clearDraft(draftKey)
    },
    [handleSubmit, clearDraft, draftKey, inputScope, setInput, createDocument, createArtifact, creationScope, sessionAttachments, sendBlocked, announceBlocked],
  )

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
      if (waitingForSignIn) {
        announceBlocked()
        return false
      }
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
    [handleSubmit, sessionId, sessionAttachments, waitingForSignIn, announceBlocked],
  )

  const noopCloseAskForm = useCallback(() => {}, [])
  const isMobile = useIsMobile()
  // A bottom sheet on a phone, and also below the chat page's 900px compact
  // breakpoint — that is the point at which the page hands this composer the
  // "mobile" variant, so the variant is the signal rather than a third
  // media query that could disagree with it.
  const canSend = connectionStatus === "connected" || draftSendable
  const askSheet = renderAskTemplate ? (
    <AskFormSheet
      form={askForm ?? null}
      renderTemplate={renderAskTemplate}
      agentId={agentId}
      sessionId={sessionId}
      compact={variant === "mobile" || isMobile}
      disabled={isStreaming || !canSend}
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
  // Owner decision: a failed or cancelled attachment keeps Send off until the
  // user retries or removes it. The reason is on screen (not only a toast).
  // Send uses aria-disabled rather than disabled: it stays focusable, so the
  // reason linked by aria-describedby reaches keyboard users, and an attempt
  // (click or Enter) is answered through an always-mounted live region.
  // While waiting for sign-in the button is aria-disabled, not disabled, so it
  // stays focusable and can say why (the text box is empty for an unknown
  // identity, which would otherwise disable it silently).
  // Same when the only content is a failed or cancelled chip with no caption:
  // `sendBlocked` keeps the button aria-disabled and focusable, with the reason.
  const submitDisabled = !isStreaming && !sendBlocked && (!hasContent || !canSend)
  const blockReason = (
    <>
      {sendBlocked && (
        <Alert id={blockReasonId} role="note" variant={waitingForSignIn ? "default" : "destructive"} className="mt-2 py-2">
          <AlertTriangle aria-hidden="true" />
          <AlertDescription>
            <p>{blockReasonText}</p>
          </AlertDescription>
        </Alert>
      )}
      <div role="status" aria-live="polite" className="sr-only">
        {announcement}
      </div>
    </>
  )
  const submitBlockProps = sendBlocked
    ? {
        "aria-disabled": true as const,
        // Button styles only `disabled:`; mirror it for aria-disabled and
        // neutralise the default variant's hover lift and colour.
        className:
          "aria-disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:shadow-none aria-disabled:hover:translate-y-0 aria-disabled:hover:bg-primary-strong aria-disabled:active:translate-y-0",
        "aria-describedby": blockReasonId,
        onClick: (e: React.MouseEvent) => {
          e.preventDefault()
          announceBlocked()
        },
      }
    : {}
  const refusalAlert = refusalNotice ? <p role="alert" className="px-1 pt-1 text-xs text-destructive">{refusalNotice}</p> : null
  const artifactCreationControl = canCreateArtifact ? (
    <>
      <Button
        type="button"
        variant={createArtifact ? "secondary" : "ghost"}
        size="sm"
        aria-label="Create artifact project"
        aria-pressed={createArtifact}
        disabled={isStreaming || !canSend}
        className="coarse:min-h-[44px]"
        onClick={() => {
          setCreationSelection(createArtifact ? null : artifactScope)
          creationPacket.current = null
        }}
      >Create artifact project</Button>
      {createArtifact && <span role="status" className="text-xs text-muted-foreground">Private project · Sharing requires your approval</span>}
    </>
  ) : null
  // "+" › New artifact lists only kinds with a backend. Picking one sets the
  // same explicit creation intent the toggle buttons set, so a starter never
  // reaches an agent without consent to create it.
  const newArtifactKinds = chatArtifactProjects() ? availableArtifactKinds({ projects: canCreateArtifact, documents: canCreateDocument }) : []
  const newArtifactMenu = <NewArtifactMenu kinds={newArtifactKinds} disabled={isStreaming || !canSend} onPick={(kind, prompt) => {
    if (kind === "word") { setCreationSelection(creationScope); creationPacket.current = null }
    if (kind === "project") { setCreationSelection(artifactScope); creationPacket.current = null }
    // The starter goes in front; text the person already typed stays.
    updateInput(input.trim() ? `${prompt}\n\n${input}` : prompt)
  }} />
  // With the menu, the picked kind shows as one removable chip instead of two
  // text toggles that crowd the toolbar off a phone screen.
  const pickedKind = createArtifact ? newArtifactKinds.find(k => k.id === "project") : createDocument ? newArtifactKinds.find(k => k.id === "word") : undefined
  const pickedKindChip = pickedKind ? (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <span className="inline-flex shrink-0 items-center gap-1 rounded-full border bg-secondary py-0.5 pl-2 pr-0.5 text-xs font-medium">
        <pickedKind.icon aria-hidden className="size-3" />New {pickedKind.name}
        <Button type="button" size="icon" variant="ghost" aria-label={`Remove New ${pickedKind.name}`} className="size-5 rounded-full coarse:size-[44px]"
          onClick={() => { setCreationSelection(null); creationPacket.current = null }}><X aria-hidden className="size-3" /></Button>
      </span>
      <span role="status" className="truncate text-xs text-muted-foreground">{createArtifact ? "Private project · Sharing requires your approval" : "My space · You and workspace OWNER (audited access)"}</span>
    </span>
  ) : null
  const creationControl = canCreateDocument ? (
    <>
    <Button
      type="button"
      variant={createDocument ? "secondary" : "ghost"}
      size="sm"
      aria-label="Create document"
      aria-pressed={createDocument}
      title="Create document / Vytvořit dokument"
      disabled={isStreaming || !canSend}
      className="coarse:min-h-[44px]"
      onClick={() => {
        setCreationSelection(createDocument ? null : creationScope)
        creationPacket.current = null
      }}
    >
      <FilePlus2 className="size-4" aria-hidden="true" />
      Create document
    </Button>
    {createDocument && <span role="status" className="text-xs text-muted-foreground">My space · You and workspace OWNER (audited access)</span>}
    </>
  ) : null
  const creationControls = newArtifactKinds.length > 0 ? <>{newArtifactMenu}{pickedKindChip}</> : <>{creationControl}{artifactCreationControl}</>

  if (variant === "mobile") {
    // The mobile branch used to be a bare input: no attachments at all, so a
    // phone — the device that actually has a camera — was the one surface that
    // could not send a picture. It now wraps the same AttachmentZone the
    // desktop branch uses (which is also what renders the resulting chips, so
    // an upload is visible rather than a black hole) and puts a camera next to
    // the paperclip. The same mention picker also supports touch selection.
    return (
      <EnsureChatSessionProvider ensureSession={ensureSessionForUpload ?? ensureSession}>
      {askSheet}
      <div className="p-3 shrink-0">
        <AttachmentZone agentId={agentId} sessionId={sessionId} showChips={!sheetOwnsChips}>
          <MentionAutocomplete
            text={input}
            textareaRef={mentionTextareaRef}
            members={mentionMembers ?? []}
            onPick={handleMentionPick}
          />
          <PromptInput className={COMPOSER_SHELL} onSubmit={handleTypedSubmit}>
            <PromptInputTextarea
              ref={mentionTextareaRef}
              value={input}
              onChange={(e) => updateInput(e.target.value)}
              placeholder={placeholder}
              className="min-h-[44px]"
            />
            <PromptInputFooter className="justify-between p-2 gap-2">
              <div className="flex min-w-0 items-center gap-1">
                <CameraButton agentId={agentId} sessionId={sessionId} />
                <AttachmentButton agentId={agentId} sessionId={sessionId} />
                {creationControls}
              </div>
              <PromptInputSubmit disabled={submitDisabled} {...submitBlockProps} status={chatStatus} stopping={stopping} onStop={stopGeneration} />
            </PromptInputFooter>
          </PromptInput>
        </AttachmentZone>
        {blockReason}
{refusalAlert}
      </div>
      </EnsureChatSessionProvider>
    )
  }

  return (
    <EnsureChatSessionProvider ensureSession={ensureSessionForUpload ?? ensureSession}>
    {askSheet}
    <div className="mx-auto w-full max-w-3xl p-3 md:px-6 shrink-0">
      <AttachmentZone agentId={agentId} sessionId={sessionId} showChips={!sheetOwnsChips}>
        <MentionAutocomplete
          text={input}
          textareaRef={mentionTextareaRef}
          members={mentionMembers ?? []}
          onPick={handleMentionPick}
        />
        <PromptInput className={COMPOSER_SHELL} onSubmit={handleTypedSubmit}>
          <PromptInputTextarea
            ref={mentionTextareaRef}
            value={input}
            onChange={(e) => updateInput(e.target.value)}
            placeholder={placeholder}
            className="min-h-[44px]"
          />
          <PromptInputFooter className="justify-between p-2 gap-2">
            <div className="flex min-w-0 items-center gap-1">
              <AttachmentButton agentId={agentId} sessionId={sessionId} />
              {creationControls}
            </div>
            <PromptInputSubmit disabled={submitDisabled} {...submitBlockProps} status={chatStatus} stopping={stopping} onStop={stopGeneration} />
          </PromptInputFooter>
        </PromptInput>
      </AttachmentZone>
      {blockReason}
{refusalAlert}
    </div>
    </EnsureChatSessionProvider>
  )
}
