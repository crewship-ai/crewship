import type { LucideIcon } from "lucide-react"
import { ARTIFACT_KINDS, NO_CAPABILITIES, type ArtifactCapabilities } from "./artifact-kinds"

/** The kinds the chat composer can start: it carries an intent for exactly these two. */
export type ArtifactKindId = "project" | "word"

export interface ArtifactKind {
  id: ArtifactKindId
  name: string
  description: string
  icon: LucideIcon
  starterPrompts: string[]
}

const CHAT_KINDS: readonly ArtifactKindId[] = ["project", "word"]

/**
 * Kinds the composer may offer, given what the backend serves right now, read
 * from the shared registry. Kinds the composer has no creation intent for
 * (dashboards, sheets, pages, slides) are left out entirely rather than shown
 * as planned or disabled tiles.
 */
export function availableArtifactKinds(caps: { projects: boolean; documents: boolean }): ArtifactKind[] {
  // The composer already checked that a signed-in person may create.
  const full: ArtifactCapabilities = { ...NO_CAPABILITIES, ...caps, pages: true }
  return ARTIFACT_KINDS
    .filter(k => (CHAT_KINDS as readonly string[]).includes(k.key) && k.live(full))
    .map(k => ({ id: k.key as ArtifactKindId, name: k.label, description: k.description, icon: k.icon, starterPrompts: k.starterPrompts }))
}
