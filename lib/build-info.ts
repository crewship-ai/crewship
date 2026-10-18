export interface Build {
  current: string
  commit?: string
  dirty?: boolean
  latest?: string | null
  newer?: boolean
}

const EDITIONS: Record<string, string> = { community: "Community Edition", team: "Team Edition", enterprise: "Enterprise Edition" }

/** "v0.9.2", or "dev · d43bfd1" for a build without a release tag. */
export function buildLabel(b: Build): string {
  if (b.current !== "dev" || !b.commit) return b.current
  return `dev · ${b.commit.slice(0, 7)}${b.dirty ? "+" : ""}`
}

export function editionName(edition: string | undefined): string {
  if (!edition) return "Crewship"
  return EDITIONS[edition] ?? `${edition.charAt(0).toUpperCase()}${edition.slice(1)} Edition`
}
