"use client"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { fetchArtifactPreview } from "./client"
import { BLOCK_TEXT, addAttachment, attachmentId, buildIterationIntent, detachAttachment, newSubmissionId, sendBlock, type AttachedArtifact, type ComposerArtifactContext, type EditMode } from "./composer/composer-context-model"
import type { LinkedViewerTarget } from "./linked-viewer-targets"
import { useArtifactContextEnabled } from "./use-artifact-context-enabled"

interface Input { workspaceId: string; chatId: string; executor: string | null; online: boolean; privateChat: boolean }

const EMPTY: ComposerArtifactContext = { executor: null, online: true, privateChat: false, attachments: [], mode: null, updateTargetId: null, createAudience: { kind: "unknown" }, createResultType: "Document" }
type Draft = ComposerArtifactContext

export type PrepareSend = { ok: true; metadata: Record<string, unknown> | undefined } | { ok: false; message: string }

/** Composer-side artifact context: attached items, the explicit mode, and the submission that rides with the next send. */
export function useComposerArtifactState({ workspaceId, chatId, executor, online, privateChat }: Input) {
  const enabled = useArtifactContextEnabled()
  const [draft, setDraft] = useState<Draft>(EMPTY)
  const scope = `${workspaceId}:${chatId}`
  const scopeRef = useRef(scope)
  const submissionId = useRef<string | null>(null)
  /** Texts handed to the socket and not yet echoed back by the server. */
  const unconfirmed = useRef<string[]>([])
  useEffect(() => { scopeRef.current = scope; submissionId.current = null; unconfirmed.current = []; setDraft(EMPTY) }, [scope])
  const context = useMemo<ComposerArtifactContext>(() => ({ ...draft, executor, online, privateChat }), [draft, executor, online, privateChat])
  const contextRef = useRef(context)
  contextRef.current = context
  // Any change to what would be sent is a new send intent.
  const change = useCallback((update: (value: Draft) => Draft) => { submissionId.current = null; unconfirmed.current = []; setDraft(update) }, [])

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

  /** Blocks the send, or adds the stable submission to the message metadata. Does nothing when no artifact context is in play. */
  const prepareSend = useCallback((text: string, metadata?: Record<string, unknown>): PrepareSend => {
    if (!enabled) return { ok: true, metadata }
    const current = contextRef.current
    const block = sendBlock(current, text)
    if (block) return { ok: false, message: BLOCK_TEXT[block] }
    submissionId.current ??= newSubmissionId()
    const intent = buildIterationIntent(current, submissionId.current)
    return { ok: true, metadata: intent ? { ...metadata, document_iteration_intent: intent } : metadata }
  }, [enabled])
  /**
   * The frame is on the socket, which is not the server's acceptance: keep the
   * attached context and the same submission id until the message is echoed
   * (confirmEcho), so a send the server never saw is retried as the same intent.
   */
  const markSent = useCallback((text: string) => {
    if (contextRef.current.attachments.length || contextRef.current.mode) unconfirmed.current = [...unconfirmed.current, text.trim()]
  }, [])
  /** The server echoed one of our own persisted messages (useChat onOwnMessageSaved). Only then is the context spent. */
  const confirmEcho = useCallback((content: string, metadata?: Record<string, unknown>) => {
    if (!unconfirmed.current.length) return
    const intent = metadata?.document_iteration_intent as { submission_id?: unknown } | undefined
    const bySubmission = Boolean(intent && submissionId.current && intent.submission_id === submissionId.current)
    const byContent = unconfirmed.current.some(text => text && content.startsWith(text))
    if (!bySubmission && !byContent) return
    submissionId.current = null
    unconfirmed.current = []
    setDraft(EMPTY)
  }, [])

  return { enabled, context, attach, detach, setMode, setUpdateTarget, prepareSend, markSent, confirmEcho }
}
