import type { BreadcrumbItem } from "@/lib/store"
import { entityHref } from "@/lib/entity-links"

/**
 * The path back out of a conversation: Crews / <crew> / <agent>.
 *
 * Every crumb is a NAME and a place. The toolbar used to print the URL slug
 * ("riley", and "_crewship-setup-guide" for the onboarding Guide) because it
 * only had the pathname; the chat page has the roster and publishes this
 * through the store instead (docs/ux/audit-conversations.md P1-7).
 */
export interface BreadcrumbAgent {
  name: string
  slug: string
  crew?: { name: string; slug: string } | null
}

export function chatBreadcrumbs(agent: BreadcrumbAgent | null): BreadcrumbItem[] {
  if (!agent) return []
  const out: BreadcrumbItem[] = [{ label: "Crews", href: "/crews" }]
  // The onboarding Guide's crew and agent are hidden from /crews on purpose,
  // so a crumb pointing there would land on nothing. Its crumbs are labels.
  const hidden = isSetupSlug(agent.slug)
  if (agent.crew) out.push(hidden ? { label: agent.crew.name } : { label: agent.crew.name, href: entityHref({ kind: "crew", slug: agent.crew.slug }) })
  out.push(hidden ? { label: agent.name } : { label: agent.name, href: entityHref({ kind: "agent", slug: agent.slug }) })
  return out
}

/** The setup crew and its Guide use underscore-prefixed slugs (`_crewship-setup`). */
export function isSetupSlug(slug: string): boolean {
  return slug.startsWith("_")
}
