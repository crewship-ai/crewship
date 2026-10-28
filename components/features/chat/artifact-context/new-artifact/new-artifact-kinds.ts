import { AppWindow, FileText, Frame, type LucideIcon } from "lucide-react"

export type ArtifactKindId = "project" | "word" | "design"

export interface ArtifactKind {
  id: ArtifactKindId
  name: string
  description: string
  icon: LucideIcon
  starterPrompts: string[]
}

const PROJECT: ArtifactKind = {
  id: "project",
  name: "Project",
  description: "Screens that link together",
  icon: AppWindow,
  starterPrompts: ["A three-screen pricing site: plans, compare, FAQ", "A sign-up flow with a confirmation screen"],
}

/** Served by the existing private document creation. */
const WORD: ArtifactKind = {
  id: "word",
  name: "Word",
  description: "A document, one editor at a time",
  icon: FileText,
  starterPrompts: ["A partner brief from this chat", "Turn the weekly report into a two-page memo"],
}

/**
 * Kinds the composer may offer, given what the backend serves right now.
 * Kinds without a backend (dashboards, slides, docs, sheets, design systems)
 * are left out entirely rather than shown as planned or disabled tiles.
 */
export function availableArtifactKinds(caps: { projects: boolean; documents: boolean; designs?: boolean }): ArtifactKind[] {
  const kinds: ArtifactKind[] = []
  if (caps.projects) kinds.push(PROJECT)
  if (caps.documents) kinds.push(WORD)
  if (caps.designs) kinds.push({ id: "design", name: "Design", description: "A shared canvas with screens", icon: Frame, starterPrompts: ["Add a Cart screen and unify the colors on this Design"] })
  return kinds
}
