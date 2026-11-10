import type { StatusTone } from "@/lib/format-status"
import { AGENT_LEVELS, levelRank, SHARE_LEVELS, type AccessEntry, type AccessLevel, type AgentLevel, type ShareLevel } from "./sharing-api"

export const LEVEL_TONE: Record<AccessLevel, StatusTone> = { write: "blue", propose: "purple", comment: "purple", read: "muted" }
/** Edit first, as the dialog and the Access tab list them. */
export const LEVELS_DESC: ShareLevel[] = [...SHARE_LEVELS].reverse()
/** An agent's levels, Edit first. */
export const AGENT_LEVELS_DESC: AgentLevel[] = [...AGENT_LEVELS].reverse()
/** Every group the Access tab can show, Edit first. */
export const ACCESS_LEVELS_DESC: AccessLevel[] = ["write", "propose", "comment", "read"]

export function accessVia(entry: AccessEntry): string {
  switch (entry.via) {
    case "owner": return "Owner"
    case "grant": return entry.inactive ? `Shared directly, not in effect: ${entry.inactive}` : "Shared directly"
    case "folder": return `Via folder ${entry.folderName ?? entry.folderSlug ?? ""}`.trim()
    case "role": return "Workspace role"
    case "crew": return entry.crew === "withheld" || !entry.crew ? "Through a crew" : `Member of crew ${entry.crew}`
  }
}

/**
 * Why a row cannot be changed here; null for a direct grant. An agent's direct
 * grant is changed by the artifact's owner only (only the Owner delegates Edit).
 */
export function lockedReason(entry: AccessEntry, ownedByMe = false): string | null {
  if (entry.subjectType === "agent") {
    if (entry.via !== "grant") return "An agent's access is set with crewship page grant"
    return ownedByMe ? null : "Only the owner changes an agent's access"
  }
  switch (entry.via) {
    case "owner": return "The owner always has Edit"
    case "folder": return "Change in folder settings"
    case "role": return "Set by their workspace role"
    case "crew": return "Set by crew membership"
    case "grant": return null
  }
}

export const byAccess = (a: AccessEntry, b: AccessEntry) =>
  Number(b.via === "owner") - Number(a.via === "owner") || levelRank(b.level) - levelRank(a.level) || a.label.localeCompare(b.label)
