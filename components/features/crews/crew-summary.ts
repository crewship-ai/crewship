/**
 * The crew header's one-line facts, in words a client reads (README §6
 * "copy says what it is"): no `TTL —h` when no TTL is set, "Restricted
 * network" rather than `network: restricted`, and a date that reads the same
 * in Prague and in Boston.
 */
import { formatMemory } from "./crew-canvas-tabs/types"

export interface CrewContainerFacts {
  runtime_image: string | null
  container_memory_mb: number
  container_cpus: number
  container_ttl_hours: number | null
  network_mode: string
}

export const DEFAULT_RUNTIME_IMAGE = "debian:trixie-slim"

export function networkModeLabel(mode: string): string {
  switch (mode) {
    case "restricted":
      return "Restricted network"
    case "free":
      return "Open network"
    case "none":
    case "isolated":
      return "No network"
    default:
      return `${mode} network`
  }
}

export function crewContainerSummary(crew: CrewContainerFacts): string {
  const parts = [
    crew.runtime_image ?? DEFAULT_RUNTIME_IMAGE,
    `${formatMemory(crew.container_memory_mb)} · ${crew.container_cpus} CPU`,
  ]
  if (crew.container_ttl_hours != null && crew.container_ttl_hours > 0) {
    parts.push(`stops after ${crew.container_ttl_hours}h idle`)
  }
  parts.push(networkModeLabel(crew.network_mode))
  return parts.join(" · ")
}

/** "3 Sep 2026" — day, short month, year, in the viewer's locale order. */
export function formatCrewDate(iso: string, locale?: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  return d.toLocaleDateString(locale, { day: "numeric", month: "short", year: "numeric" })
}
