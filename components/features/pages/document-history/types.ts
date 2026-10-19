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

export interface DocumentHistoryProps {
  pageId: string
  scopeKey: string
  list: HistoryRead<DocumentCheckpointList>
  selectedId?: string
  snapshot: HistoryRead<DocumentCheckpointSnapshot>
  onSelect: (checkpointId: string) => void
  onClose: () => void
}
