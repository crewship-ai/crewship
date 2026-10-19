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
}

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
}

export type SendBlock = "attachment_lost" | "offline" | "choose_mode" | "need_attachment" | "choose_target" | "no_executor" | "audience_unknown" | null

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
export function sendBlock(ctx: ComposerArtifactContext): SendBlock {
  if (ctx.attachments.length === 0 && ctx.mode === null) return null
  if (ctx.attachments.some(item => item.access === "lost")) return "attachment_lost"
  if (!ctx.online && ctx.mode !== null) return "offline"
  if (ctx.mode === null) return "choose_mode"
  if (ctx.mode === "reply_only") return null
  if (ctx.mode === "update") {
    if (ctx.attachments.length === 0) return "need_attachment"
    const target = updateTarget(ctx)
    if (!target) return "choose_target"
    if (!ctx.executor) return "no_executor"
    return target.audience.kind === "unknown" ? "audience_unknown" : null
  }
  if (!ctx.executor) return "no_executor"
  return ctx.createAudience.kind === "unknown" ? "audience_unknown" : null
}

export const BLOCK_TEXT: Record<Exclude<SendBlock, null>, string> = {
  attachment_lost: "You no longer have access to an attached item. Remove it to continue.",
  offline: "You are offline. Reconnect to send.",
  choose_mode: "Choose what the agent should do with the attached item.",
  need_attachment: "Attach the item you want to update.",
  choose_target: "Choose which attached item to update.",
  no_executor: "Choose who will do the work.",
  audience_unknown: "The audience could not be confirmed. Try again before sending.",
}

export function audienceText(audience: Audience, mode: EditMode | null): string {
  if (mode === "reply_only") return "No item is changed. Only the reply appears in this chat."
  if (audience.kind === "personal") return "Saved to My space. Only you can see it."
  if (audience.kind === "folder") return `Saved to ${audience.name}. ${audience.people} ${audience.people === 1 ? "person" : "people"} can see it. The agent prepares a proposal for you to accept.`
  return "Audience unknown."
}

/** Updating a pinned revision applies to the latest one. */
export function pinnedUpdateWarning(ctx: ComposerArtifactContext): string | null {
  const target = ctx.mode === "update" ? updateTarget(ctx) : null
  return target?.target.mode === "revision" ? `You attached revision ${target.target.revision}. The update applies to the latest revision and may conflict with newer changes.` : null
}
