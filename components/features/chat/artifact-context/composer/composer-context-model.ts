import type { ChatPrivacy } from "../chat-privacy"
import { targetKey, type LinkedViewerTarget } from "../linked-viewer-targets"

export type EditMode = "create" | "update" | "reply_only"

/** Where a result lands and who really sees it; supplied by the server per item, never guessed. */
export type Audience =
  | { kind: "personal" }
  | { kind: "folder"; name: string; people: number }
  | { kind: "unknown" }

export interface AttachedArtifact {
  /** Stable identity (targetKey): the same item at the same revision is one attachment. */
  id: string
  target: LinkedViewerTarget
  /** Null once the sender lost access: the old name must not be shown or announced. */
  title: string | null
  access: "readable" | "lost"
  resultType: string
  /** Where an update of this item lands. */
  audience: Audience
  /** Schema, revision and generation the server reported when the item was attached. Null until known. */
  expected: ExpectedState | null
}

export interface ExpectedState { schema: number; rev: number; generation: number }

export interface ComposerArtifactContext {
  attachments: AttachedArtifact[]
  mode: EditMode | null
  /** Id of the attachment to update; required when more than one is attached. */
  updateTargetId: string | null
  executor: string | null
  /** Where a newly created item lands. */
  createAudience: Audience
  createResultType: string
  online: boolean
  /** Targeted edits are accepted only in a private chat. Anything not positively verified is not private. */
  chatPrivacy: ChatPrivacy
  /** A send was handed to the socket and the server has not confirmed it; sending again retries the same request. */
  unconfirmed: boolean
}

export type SendBlock = "chat_checking" | "chat_unverified" | "chat_not_private" | "need_instruction" | "shared_folder" | "attachment_lost" | "offline" | "state_unknown" | "choose_mode" | "need_attachment" | "choose_target" | "no_executor" | "audience_unknown" | null

export const attachmentId = (target: LinkedViewerTarget) => targetKey(target)

export function addAttachment(ctx: ComposerArtifactContext, item: AttachedArtifact): ComposerArtifactContext {
  return ctx.attachments.some(existing => existing.id === item.id) ? ctx : { ...ctx, attachments: [...ctx.attachments, item] }
}
/** Detaching the chosen update target clears the choice; it never slides onto another item. */
export function detachAttachment(ctx: ComposerArtifactContext, id: string): ComposerArtifactContext {
  return { ...ctx, attachments: ctx.attachments.filter(item => item.id !== id), updateTargetId: ctx.updateTargetId === id ? null : ctx.updateTargetId }
}

/** The attachment an update would change, once the choice is unambiguous. */
export function updateTarget(ctx: ComposerArtifactContext): AttachedArtifact | null {
  if (ctx.attachments.length === 1) return ctx.attachments[0]
  return ctx.attachments.find(item => item.id === ctx.updateTargetId) ?? null
}

/** Nothing runs on an artifact without an explicit choice. */
export function sendBlock(ctx: ComposerArtifactContext, instruction?: string): SendBlock {
  if (ctx.attachments.length === 0 && ctx.mode === null) return null
  if (ctx.attachments.some(item => item.access === "lost")) return "attachment_lost"
  if (!ctx.online && ctx.mode !== null) return "offline"
  if (ctx.mode === null) return "choose_mode"
  if (ctx.mode === "reply_only") return null
  if (ctx.mode === "update") {
    if (ctx.chatPrivacy === "checking") return "chat_checking"
    if (ctx.chatPrivacy === "unverified") return "chat_unverified"
    if (ctx.chatPrivacy !== "private") return "chat_not_private"
    if (ctx.attachments.length === 0) return "need_attachment"
    const target = updateTarget(ctx)
    if (!target) return "choose_target"
    // Only the author's personal Document can be edited directly; a shared folder needs a proposal, which has no path yet.
    if (target.audience.kind === "folder") return "shared_folder"
    // The backend refuses an update with a blank instruction.
    if (instruction !== undefined && !instruction.trim()) return "need_instruction"
    if (!target.expected) return "state_unknown"
    if (!ctx.executor) return "no_executor"
    return target.audience.kind === "unknown" ? "audience_unknown" : null
  }
  if (!ctx.executor) return "no_executor"
  return ctx.createAudience.kind === "unknown" ? "audience_unknown" : null
}

export const BLOCK_TEXT: Record<Exclude<SendBlock, null>, string> = {
  chat_checking: "Checking chat visibility…",
  chat_unverified: "Could not verify this chat is private.",
  chat_not_private: "Editing an item needs a private chat, and this conversation is not one.",
  need_instruction: "Write what should change before sending an update.",
  shared_folder: "This item is in a shared folder. Editing it needs a proposal, which is not available yet, so nothing was sent.",
  attachment_lost: "You no longer have access to an attached item. Remove it to continue.",
  offline: "You are offline. Reconnect to send.",
  state_unknown: "The current version of the item to update is not confirmed yet. Wait or reattach it.",
  choose_mode: "Choose what the agent should do with the attached item.",
  need_attachment: "Attach the item you want to update.",
  choose_target: "Choose which attached item to update.",
  no_executor: "Choose who will do the work.",
  audience_unknown: "The audience could not be confirmed. Try again before sending.",
}

export function audienceText(audience: Audience, mode: EditMode | null): string {
  if (mode === "reply_only") return "No item is changed. Only the reply appears in this chat."
  if (audience.kind === "personal") return "Saved to My space. Only you can see it."
  if (audience.kind === "folder") return `Saved to ${audience.name}. ${audience.people} ${audience.people === 1 ? "person" : "people"} can see it.${mode === "update" ? " Editing an item in a shared folder is not available yet." : ""}`
  return "Audience unknown."
}

/** Updating a pinned revision applies to the latest one. */
export function pinnedUpdateWarning(ctx: ComposerArtifactContext): string | null {
  const target = ctx.mode === "update" ? updateTarget(ctx) : null
  return target?.target.mode === "revision" ? `You attached revision ${target.target.revision}. The update applies to the latest revision and may conflict with newer changes.` : null
}

/**
 * Targeted edit, exactly as the backend's prepared contract takes it as
 * `metadata.document_iteration_intent` on the websocket send_message
 * (feat/document-targeted-chat-iteration@88c2d91a2): five flat fields, the
 * immutable page id and the schema, revision and generation of the originally
 * readable Document. No principal, no mode and no titles travel with it.
 * Kept here, not in the shared document contracts, until the backend agrees it.
 */
export interface DocumentIterationIntent {
  submission_id: string
  page_id: string
  schema: 3
  rev: number
  generation: number
}

/** One id per send intent. Reuse it when retrying so the server can deduplicate. */
export const newSubmissionId = () => crypto.randomUUID()

/**
 * The intent for the next send, or null when the message carries none: only an
 * explicit update of one chosen, unblocked target with confirmed schema-3
 * state is a mandate. Create, reply-only and plain page context are not.
 */
export function buildIterationIntent(ctx: ComposerArtifactContext, submissionId: string): DocumentIterationIntent | null {
  if (ctx.mode !== "update" || sendBlock(ctx) || !submissionId) return null
  const target = updateTarget(ctx)
  if (!target?.expected || target.expected.schema !== 3) return null
  return { submission_id: submissionId, page_id: target.target.pageId, schema: 3, rev: target.expected.rev, generation: target.expected.generation }
}
