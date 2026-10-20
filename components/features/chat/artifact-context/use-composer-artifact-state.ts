"use client"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { fetchArtifactPreview } from "./client"
import type { ChatPrivacy } from "./chat-privacy"
import { BLOCK_TEXT, addAttachment, attachmentId, buildIterationIntent, detachAttachment, newSubmissionId, sendBlock, type AttachedArtifact, type ComposerArtifactContext, type DocumentIterationIntent, type EditMode } from "./composer/composer-context-model"
import type { LinkedViewerTarget } from "./linked-viewer-targets"
import { useArtifactContextEnabled } from "./use-artifact-context-enabled"

interface Input { workspaceId: string; chatId: string; executor: string | null; online: boolean; chatPrivacy: ChatPrivacy; serverConfirmedPrivate: boolean }

/** How long a handed-off send may stay unechoed before the strip says it is not confirmed. */
export const UNCONFIRMED_AFTER_MS = 8000

const EMPTY: ComposerArtifactContext = { executor: null, online: true, chatPrivacy: "checking", serverConfirmedPrivate: false, unconfirmed: false, attachments: [], mode: null, updateTargetId: null, createAudience: { kind: "unknown" }, createResultType: "Document" }
type Draft = ComposerArtifactContext

export type PrepareSend = { ok: true; metadata: Record<string, unknown> | undefined; intent: DocumentIterationIntent | null } | { ok: false; message: string }

/**
 * A send that is on the socket and not yet echoed. It is a frozen snapshot, kept apart from
 * the draft: editing the strip, or a late preview result, cannot change what a retry sends.
 */
interface Pending { text: string; intent: DocumentIterationIntent | null; draftRevision: number }

/**
 * Composer-side artifact context: attached items, the explicit mode, and the targeted-edit
 * intent that rides with the next send.
 *
 * Retry rule: a send made while the previous one is unconfirmed reuses its intent (same
 * submission_id, same page_id, rev and generation) only when the instruction text is
 * identical and the chosen target is the same page. A changed instruction or target is a
 * different request and gets a new submission_id.
 */
export function useComposerArtifactState({ workspaceId, chatId, executor, online, chatPrivacy, serverConfirmedPrivate }: Input) {
  const enabled = useArtifactContextEnabled()
  const [draft, setDraft] = useState<Draft>(EMPTY)
  const [unconfirmed, setUnconfirmed] = useState(false)
  const scope = `${workspaceId}:${chatId}`
  const scopeRef = useRef(scope)
  const pending = useRef<Pending | null>(null)
  const draftRevision = useRef(0)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const clearTimer = () => { if (timer.current) clearTimeout(timer.current); timer.current = null }
  const clearPending = useCallback(() => { clearTimer(); pending.current = null; setUnconfirmed(false) }, [])
  useEffect(() => { scopeRef.current = scope; clearPending(); setDraft(EMPTY) }, [scope, clearPending])
  useEffect(() => clearTimer, [])
  const context = useMemo<ComposerArtifactContext>(() => ({ ...draft, executor, online, chatPrivacy, serverConfirmedPrivate, unconfirmed }), [draft, executor, online, chatPrivacy, serverConfirmedPrivate, unconfirmed])
  const contextRef = useRef(context)
  contextRef.current = context
  // Edits change the draft only; a pending send keeps its own snapshot.
  const change = useCallback((update: (value: Draft) => Draft) => { draftRevision.current += 1; setDraft(update) }, [])

  const attach = useCallback((target: LinkedViewerTarget, title: string | null) => {
    if (!enabled) return
    const item: AttachedArtifact = { id: attachmentId(target), target, title, access: "readable", resultType: "Document", audience: { kind: "unknown" }, expected: null }
    change(value => addAttachment(value, item))
    const startedIn = scopeRef.current
    void fetchArtifactPreview(workspaceId, chatId, target.pageId).then(outcome => {
      if (scopeRef.current !== startedIn) return
      if (outcome.status === "absent") { change(value => detachAttachment(value, item.id)); return }
      if (outcome.status === "denied") { change(value => ({ ...value, attachments: value.attachments.map(entry => entry.id === item.id ? { ...entry, access: "lost" as const, title: null } : entry) })); return }
      if (outcome.status !== "ok") return
      const { create, update } = outcome.data
      change(value => ({
        ...value, createAudience: create.audience, createResultType: create.resultType,
        attachments: value.attachments.map(entry => entry.id === item.id && update ? { ...entry, audience: update.audience, resultType: update.resultType, expected: update.expected } : entry),
      }))
    }).catch(() => undefined)
  }, [enabled, workspaceId, chatId, change])

  const detach = useCallback((id: string) => change(value => detachAttachment(value, id)), [change])
  const setMode = useCallback((mode: EditMode) => change(value => ({ ...value, mode })), [change])
  const setUpdateTarget = useCallback((id: string) => change(value => ({ ...value, updateTargetId: id })), [change])

  /** Blocks the send, or adds the targeted-edit intent to the message metadata. Does nothing when no artifact context is in play. */
  const prepareSend = useCallback((text: string, metadata?: Record<string, unknown>, privacyAtSend?: ChatPrivacy): PrepareSend => {
    if (!enabled) return { ok: true, metadata, intent: null }
    // The privacy estimate is re-evaluated by the caller right now, not the value captured at render.
    const current = privacyAtSend ? { ...contextRef.current, chatPrivacy: privacyAtSend } : contextRef.current
    const block = sendBlock(current, text)
    if (block) return { ok: false, message: BLOCK_TEXT[block] }
    const fresh = buildIterationIntent(current, newSubmissionId())
    if (!fresh) return { ok: true, metadata, intent: null }
    const held = pending.current
    // Same instruction, same page: this is a retry of the unconfirmed request, so it is that request exactly.
    const intent = held?.intent && held.text === text && held.intent.page_id === fresh.page_id ? held.intent : fresh
    return { ok: true, metadata: { ...metadata, document_iteration_intent: intent }, intent }
  }, [enabled])

  /**
   * The frame is on the socket, which is not the server's acceptance: keep the sent request
   * (and the strip) until the message is echoed, and say so if it is not.
   */
  const markSent = useCallback((text: string, intent: DocumentIterationIntent | null) => {
    // A plain message with no artifact context ends any older pending send, so its "not confirmed" timer cannot fire for an unrelated message.
    if (!contextRef.current.attachments.length && !contextRef.current.mode) { clearPending(); return }
    clearTimer()
    pending.current = { text, intent, draftRevision: draftRevision.current }
    setUnconfirmed(false)
    timer.current = setTimeout(() => setUnconfirmed(true), UNCONFIRMED_AFTER_MS)
  }, [clearPending])

  /**
   * The server echoed one of our own persisted messages (useChat onOwnMessageSaved). The echo
   * must carry this request's submission id, or, when it carries none, be exactly the text
   * that was sent (the server stores and echoes content unchanged, with no trimming). A prefix, another tab's intent or an unrelated message never counts.
   */
  const confirmEcho = useCallback((content: string, metadata?: Record<string, unknown>) => {
    const held = pending.current
    if (!held) return
    const echoed = (metadata?.document_iteration_intent as { submission_id?: unknown } | undefined)?.submission_id
    const matches = echoed !== undefined ? held.intent !== null && echoed === held.intent.submission_id : content === held.text
    if (!matches) return
    const untouched = draftRevision.current === held.draftRevision
    clearPending()
    // The sender's newer choices are not thrown away by the echo of an older send.
    if (untouched) setDraft(EMPTY)
  }, [clearPending])

  return { enabled, context, attach, detach, setMode, setUpdateTarget, prepareSend, markSent, confirmEcho }
}
