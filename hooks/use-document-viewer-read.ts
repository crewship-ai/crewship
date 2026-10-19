"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { documentSchema } from "@/lib/documents/schema"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { DocumentViewerRead, ViewerTarget } from "@/components/features/chat/artifact-viewer/types"

export interface DocumentViewerReadInput {
  workspaceId: string
  chatId: string
  currentUserId: string | null | undefined
  /** Caller-generated auth/open epoch. Never pass a credential. */
  activationKey: string
  target: ViewerTarget | null
  enabled: boolean
}
export interface SavedDocumentBody { schema: 2 | 3; rev: number; content: unknown }
type Read = DocumentViewerRead<SavedDocumentBody>
type FailureStatus = "denied" | "unavailable" | "error"
class ReadFailure extends Error {
  constructor(readonly status: FailureStatus) { super("Document preview unavailable") }
}
const nonempty = (value: unknown, limit = 128): value is string => typeof value === "string" && value.trim().length > 0 && value.length <= limit
const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)

async function readJSON(url: string, signal: AbortSignal): Promise<unknown> {
  const response = await apiFetch(url, { signal, cache: "no-store" })
  if (!response.ok) throw new ReadFailure([401,403,404].includes(response.status) ? "denied" : response.status === 503 ? "unavailable" : "error")
  try { return await response.json() } catch { throw new ReadFailure("unavailable") }
}

/** Resolves current saved content only; never opens a CRDT/editor or writes. */
export function useDocumentViewerRead({ workspaceId, chatId, currentUserId, activationKey, target, enabled }: DocumentViewerReadInput) {
  const [generation, setGeneration] = useState(0)
  const refresh = useCallback(() => setGeneration(value => value + 1), [])
  useRealtimeEventSafe("page.updated", refresh)
  useRealtimeEventSafe("page.deleted", refresh)
  useRealtimeEventSafe("realtime.reconnected", refresh)
  const pageId = target?.pageId
  const available = enabled === true && nonempty(workspaceId) && nonempty(chatId) && nonempty(currentUserId) && nonempty(activationKey, 1024)
    && target?.workspaceId === workspaceId && nonempty(pageId) && target.type === "document" && target.mode === "latest"
  const scopeKey = JSON.stringify([currentUserId, workspaceId, chatId, activationKey, target?.workspaceId, pageId, target?.type, target?.mode, enabled === true, generation])
  const [settled, setSettled] = useState<{ scopeKey: string; read: Read }>()

  useEffect(() => {
    let active = true
    const controller = new AbortController()
    const current = () => active && !controller.signal.aborted
    // Also discard settled state on close/disable, so reopening cannot reuse it.
    setSettled({ scopeKey, read: { status: available ? "loading" : "unavailable" } })
    if (available && pageId) {
      const path = `/api/v1/pages/${encodeURIComponent("@" + pageId)}`
      const query = new URLSearchParams({ workspace_id: workspaceId })
      void (async () => {
        try {
          const metadata = await readJSON(`${path}?${query}`, controller.signal)
          if (!current()) return
          if (!object(metadata) || metadata.id !== pageId || metadata.has_document !== true || !nonempty(metadata.name, 512)) throw new ReadFailure("unavailable")
          const value = await readJSON(`${path}/document?${query}`, controller.signal)
          if (!current()) return
          if (!object(value) || value.page_id !== pageId || (value.schema !== 2 && value.schema !== 3)
            || !Number.isSafeInteger(value.rev) || (value.rev as number) < 0) throw new ReadFailure("unavailable")
          try {
            documentSchema.validateJSON(value.content, value.schema)
            const schema = documentSchema.schemaFor(value.schema)
            const node = schema.nodeFromJSON(value.content)
            node.check()
            if (node.type !== schema.topNodeType) throw new Error("Document root required")
          } catch { throw new ReadFailure("unavailable") }
          if (!current()) return
          setSettled({ scopeKey, read: {
            status: "ready", scopeKey, target: { workspaceId, pageId, type: "document", mode: "latest" },
            title: metadata.name, content: { schema: value.schema, rev: value.rev as number, content: value.content },
          } })
        } catch (error) {
          if (current()) setSettled({ scopeKey, read: { status: error instanceof ReadFailure ? error.status : "error" } })
        }
      })()
    }
    return () => { active = false; controller.abort() }
  }, [scopeKey, available, pageId, workspaceId])

  // Mask during render, before effect cleanup/reset runs on an identity change.
  const read: Read = !available ? { status: "unavailable" }
    : settled?.scopeKey === scopeKey ? settled.read : { status: "loading" }
  return { scopeKey, read }
}
