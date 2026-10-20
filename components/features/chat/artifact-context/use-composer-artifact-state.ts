"use client"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { fetchArtifactPreview } from "./client"
import { BLOCK_TEXT, addAttachment, attachmentId, buildIterationIntent, detachAttachment, newSubmissionId, sendBlock, type AttachedArtifact, type ComposerArtifactContext, type EditMode } from "./composer/composer-context-model"
import type { LinkedViewerTarget } from "./linked-viewer-targets"
import { useArtifactContextEnabled } from "./use-artifact-context-enabled"

interface Input { workspaceId: string; chatId: string; executor: string | null; online: boolean }

const EMPTY: ComposerArtifactContext = { executor: null, online: true, attachments: [], mode: null, updateTargetId: null, createAudience: { kind: "unknown" }, createResultType: "Document" }
type Draft = ComposerArtifactContext

export type PrepareSend = { ok: true; metadata: Record<string, unknown> | undefined } | { ok: false; message: string }

/** Composer-side artifact context: attached items, the explicit mode, and the submission that rides with the next send. */
export function useComposerArtifactState({ workspaceId, chatId, executor, online }: Input) {
  const enabled = useArtifactContextEnabled()
  const [draft, setDraft] = useState<Draft>(EMPTY)
  const scope = `${workspaceId}:${chatId}`
  const scopeRef = useRef(scope)
  const submissionId = useRef<string | null>(null)
  useEffect(() => { scopeRef.current = scope; submissionId.current = null; setDraft(EMPTY) }, [scope])
  const context = useMemo<ComposerArtifactContext>(() => ({ ...draft, executor, online }), [draft, executor, online])
  const contextRef = useRef(context)
  contextRef.current = context
  // Any change to what would be sent is a new send intent.
  const change = useCallback((update: (value: Draft) => Draft) => { submissionId.current = null; setDraft(update) }, [])

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
  const prepareSend = useCallback((metadata?: Record<string, unknown>): PrepareSend => {
    if (!enabled) return { ok: true, metadata }
    const current = contextRef.current
    const block = sendBlock(current)
    if (block) return { ok: false, message: BLOCK_TEXT[block] }
    submissionId.current ??= newSubmissionId()
    const intent = buildIterationIntent(current, submissionId.current)
    return { ok: true, metadata: intent ? { ...metadata, document_iteration_intent: intent } : metadata }
  }, [enabled])
  /** Called once a message with an intent went out; the next send is a new intent. */
  const sent = useCallback(() => { if (contextRef.current.attachments.length || contextRef.current.mode) { submissionId.current = null; setDraft(EMPTY) } }, [])

  return { enabled, context, attach, detach, setMode, setUpdateTarget, prepareSend, sent }
}
