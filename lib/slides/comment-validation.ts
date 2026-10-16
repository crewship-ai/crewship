import type { SlideCommentAnchor, SlideCommentEvent, SlideCommentPage, SlideCommentThread } from "./comments"

const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value)
const integer = (value: unknown, minimum = 1): value is number => Number.isSafeInteger(value) && Number(value) >= minimum
const id = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value)
const text = (value: unknown, max: number): value is string => typeof value === "string" && new TextEncoder().encode(value).length <= max && !/[\x00-\x08\x0B-\x1F\x7F]/.test(value) && !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value)
export const validCommentBody = (value: unknown): value is string => text(value, 10000) && value.trim().length > 0
export function validCommentAnchor(value: unknown): value is SlideCommentAnchor {
  if (!record(value) || value.anchor_schema_version !== 1 || !id(value.slide_id) || !integer(value.revision)) return false
  return value.kind === "slide" ? value.element_id === undefined : value.kind === "element" && id(value.element_id)
}
export function validCommentEvent(value: unknown): value is SlideCommentEvent {
  if (!record(value) || !integer(value.version) || !id(value.mutation_id) || !text(value.actor, 512) || !text(value.created_at, 128)) return false
  return value.kind === "comment" ? value.version === 1 && validCommentBody(value.body) : value.kind === "reply" ? Number(value.version) > 1 && validCommentBody(value.body) : (value.kind === "resolve" || value.kind === "reopen") && Number(value.version) > 1 && value.body === ""
}
export function validCommentThread(value: unknown, pageId: string): value is SlideCommentThread {
  if (!record(value) || !id(value.id) || value.page_id !== pageId || !validCommentAnchor(value.anchor) || !integer(value.version) || typeof value.resolved !== "boolean" || !integer(value.projection_revision) || !integer(value.comment_count) || !validCommentEvent(value.first_comment)) return false
  if (value.first_comment.kind !== "comment" || value.first_comment.version !== 1 || value.first_comment.mutation_id !== value.id || value.anchor.revision > value.projection_revision) return false
  if (!text(value.created_at, 128) || !text(value.updated_at, 128) || (value.last_missing_revision !== undefined && !integer(value.last_missing_revision)) || (value.restored_at_revision !== undefined && !integer(value.restored_at_revision))) return false
  if (value.status === "attached") return value.last_missing_revision === undefined && value.restored_at_revision === undefined
  if (!integer(value.last_missing_revision) || value.last_missing_revision < value.anchor.revision || value.last_missing_revision > value.projection_revision) return false
  return value.status === "detached" ? value.restored_at_revision === undefined : value.status === "restored" && integer(value.restored_at_revision) && value.restored_at_revision > value.last_missing_revision && value.restored_at_revision <= value.projection_revision
}
export function validCommentPage<T>(value: unknown, validate: (item: unknown) => item is T): value is SlideCommentPage<T> {
  return record(value) && Array.isArray(value.items) && value.items.length <= 100 && value.items.every(validate) && typeof value.can_comment === "boolean" && typeof value.can_resolve === "boolean" && integer(value.watermark, 0) && text(value.next_cursor, 1024) && typeof value.has_more === "boolean" && value.has_more === (value.next_cursor.length > 0)
}
