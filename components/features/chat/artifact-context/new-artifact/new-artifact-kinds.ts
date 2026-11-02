import type { LucideIcon } from "lucide-react"
import { ACCENT, type Accent } from "@/lib/concept-accents"
import { ARTIFACT_KINDS, NO_CAPABILITIES, type ArtifactCapabilities } from "./artifact-kinds"

/** The kinds the chat composer can start: it carries an explicit intent for these kinds. */
export type ArtifactKindId = "project" | "word" | "design"

export interface ArtifactKind {
  id: ArtifactKindId
  name: string
  description: string
  icon: LucideIcon
  /** The kind's colour classes from the registry (glyph and tinted square). */
  accent: Accent
  starterPrompts: string[]
}

const CHAT_KINDS: readonly ArtifactKindId[] = ["project", "word", "design"]

/**
 * Kinds the composer may offer, given what the backend serves right now, read
 * from the shared registry. Kinds the composer has no creation intent for
 * (dashboards, sheets, pages, slides) are left out entirely rather than shown
 * as planned or disabled tiles.
 */
export function availableArtifactKinds(caps: { projects: boolean; documents: boolean; designs?: boolean }): ArtifactKind[] {
  // The chat panel already checked that this person may create Pages: Project
  // through the server's agent-scoped can_create, Word through page creation access.
  const full: ArtifactCapabilities = { ...NO_CAPABILITIES, ...caps, pages: true }
  return ARTIFACT_KINDS
    .filter(k => (CHAT_KINDS as readonly string[]).includes(k.key) && k.live(full))
    .map(k => ({ id: k.key as ArtifactKindId, name: k.label, description: k.description, icon: k.icon, accent: ACCENT[k.accent], starterPrompts: k.starterPrompts }))
}
