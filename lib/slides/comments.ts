// Human comment protocol v1; independent from the canonical deck schema.
// Advisory permissions are snapshots, never mutation authority.
export interface SlideCommentAnchor {
  anchor_schema_version: 1
  kind: 'slide' | 'element'
  slide_id: string
  element_id?: string
  revision: number
}
export interface SlideCommentEvent {
  version: number
  kind: 'comment' | 'reply' | 'resolve' | 'reopen'
  body: string
  actor: string
  mutation_id: string
  created_at: string
}
export interface SlideCommentThread {
  id: string
  page_id: string
  anchor: SlideCommentAnchor
  version: number
  resolved: boolean
  status: 'attached' | 'detached' | 'restored'
  projection_revision: number
  last_missing_revision?: number
  restored_at_revision?: number
  created_at: string
  updated_at: string
  comment_count: number
  first_comment: SlideCommentEvent
}
export interface SlideCommentPermissions {
  can_comment: boolean
  can_resolve: boolean
}
export interface SlideCommentPage<T> extends SlideCommentPermissions {
  items: T[]
  next_cursor: string
  watermark: number
  has_more: boolean
}
export interface SlideCommentThreadResponse extends SlideCommentPermissions {
  thread: SlideCommentThread
}
export interface SlideCommentReceipt {
  mutation_id: string
  thread_id: string
  version: number
  duplicate: boolean
}
export interface SlideCommentCreate {
  page_id: string
  schema_version: 1
  base_rev: number
  mutation_id: string
  anchor: SlideCommentAnchor
  body: string
}
export interface SlideCommentReply {
  page_id: string
  schema_version: 1
  mutation_id: string
  if_version: number
  body: string
}
export interface SlideCommentResolve {
  page_id: string
  schema_version: 1
  mutation_id: string
  if_version: number
  resolved: boolean
}
