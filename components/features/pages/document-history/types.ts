/** Confirmed pins, not an exhaustive log of every document revision. */
export interface DocumentCheckpoint {
  id: string
  rev: number
  schema: number
  actor: string
  created_at: string
}
export interface DocumentCheckpointList {
  checkpoints: DocumentCheckpoint[]
}
/** Public read projection: no Yjs bytes, lease, epoch or original run authority. */
export interface DocumentCheckpointSnapshot {
  page_id: string
  id: string
  rev: number
  schema: number
  content: unknown
}
/** A ready response belongs to exactly one current human/workspace/Page/auth scope. */
export type HistoryRead<T> =
  | { status: "idle" | "loading" | "denied" | "error" | "unavailable" }
  | { status: "ready"; scopeKey: string; data: T }

import type { DocumentHistoryActions } from "@/lib/documents/history-actions"

/** Optional compare and revert tools; without them history is a read-only browser (plus restore). */
export interface DocumentHistoryReview {
  /** Confirmed head content and revision; compare and revert are relative to it. */
  currentContent: unknown
  headRev: number
  hasLocalDraft: boolean
  /** Only for people who can write; undefined hides Revert. */
  actions?: DocumentHistoryActions
  onDownloadDraft: () => void
  onActionDone: (rev: number) => void
}

export interface DocumentHistoryProps {
  pageId: string
  scopeKey: string
  list: HistoryRead<DocumentCheckpointList>
  selectedId?: string
  snapshot: HistoryRead<DocumentCheckpointSnapshot>
  onSelect: (checkpointId: string) => void
  onClose: () => void
  review?: DocumentHistoryReview
  restore?: {
    allowed: boolean
    baseKey?: string
    status: "idle" | "pending" | "uncertain" | "error" | "denied"
    onConfirm: () => void
    onRetry: () => void
  }
}
