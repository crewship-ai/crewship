/**
 * The links a crew always carries (README §5): its chat, agents, routines,
 * issues, pages, credentials, spend, journal. The crew header used to offer
 * one button — Files — and the Open issues card went to /issues unscoped.
 * Built here, through entityHref, so no screen spells a route by hand.
 */
import { entityHref } from "@/lib/entity-links"
import { isLeadRole } from "@/lib/agent-role"

export interface CrewLinkAgent {
  slug: string
  agent_role: string
  name: string
}

export interface CrewLink {
  id: "chat" | "issues" | "routines" | "pages" | "journal" | "spend" | "credentials"
  label: string
  href: string
  /** Mono count after the label, when the crew has one to show. */
  count?: string
  /** Why the link is what it is — the lead's name behind "Chat", say. */
  title?: string
}

/** The agent a person talks to about the crew: its lead, else its first agent. */
export function crewSpokesperson(agents: CrewLinkAgent[]): CrewLinkAgent | null {
  return agents.find((a) => isLeadRole(a.agent_role)) ?? agents[0] ?? null
}

export function crewHeaderLinks({
  crew,
  agents,
  counts = {},
}: {
  crew: { id: string; slug: string }
  agents: CrewLinkAgent[]
  counts?: { issues?: number | null; routines?: number | null; credentials?: number | null }
}): CrewLink[] {
  const spokesperson = crewSpokesperson(agents)
  const n = (v: number | null | undefined) => (v == null ? undefined : String(v))
  const links: CrewLink[] = []
  if (spokesperson) {
    links.push({
      id: "chat",
      label: "Chat",
      href: entityHref({ kind: "chat", agentSlug: spokesperson.slug }),
      title: `Chat with ${spokesperson.name}`,
    })
  }
  links.push(
    { id: "issues", label: "Issues", href: entityHref({ kind: "issues", crewSlug: crew.slug }), count: n(counts.issues) },
    { id: "routines", label: "Routines", href: entityHref({ kind: "routines", crewSlug: crew.slug }), count: n(counts.routines) },
    { id: "pages", label: "Pages", href: entityHref({ kind: "pages", crewSlug: crew.slug }) },
    { id: "journal", label: "Journal", href: entityHref({ kind: "journal", crewSlug: crew.slug }) },
    { id: "spend", label: "Spend", href: entityHref({ kind: "spend", crewId: crew.id }) },
    { id: "credentials", label: "Credentials", href: entityHref({ kind: "credentials", crewSlug: crew.slug }), count: n(counts.credentials) },
  )
  return links
}
