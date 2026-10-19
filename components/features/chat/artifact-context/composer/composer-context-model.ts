import type { LinkedViewerTarget } from "../linked-viewer-targets"

export type EditMode = "create" | "update" | "reply_only"

export interface AttachedArtifact {
  /** Exact revision or explicit latest; attaching never changes access rights. */
  target: LinkedViewerTarget
  /** Only for items the sender may currently read. */
  title: string
}
/** Where the result lands and who really sees it; supplied by the server, not guessed. */
export type Audience =
  | { kind: "personal" }
  | { kind: "folder"; name: string; people: number }
  | { kind: "unknown" }

export interface ComposerArtifactContext {
  attachments: AttachedArtifact[]
  mode: EditMode | null
  /** Index into attachments, required when more than one is attached and mode is update. */
  updateTarget: number | null
  executor: string | null
  audience: Audience
}

export type SendBlock = "choose_mode" | "choose_target" | "no_executor" | "audience_unknown" | null

/** Nothing runs on an artifact without an explicit choice. Reply-only never needs a location. */
export function sendBlock(ctx: ComposerArtifactContext): SendBlock {
  if (ctx.attachments.length === 0 && ctx.mode === null) return null
  if (ctx.mode === null) return "choose_mode"
  if (ctx.mode === "reply_only") return null
  if (ctx.mode === "update" && ctx.attachments.length === 0) return "choose_mode"
  if (ctx.mode === "update" && ctx.attachments.length > 1 && (ctx.updateTarget === null || !ctx.attachments[ctx.updateTarget])) return "choose_target"
  if (!ctx.executor) return "no_executor"
  if (ctx.audience.kind === "unknown") return "audience_unknown"
  return null
}

export const BLOCK_TEXT: Record<Exclude<SendBlock, null>, string> = {
  choose_mode: "Choose what the agent should do with the attached item.",
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
