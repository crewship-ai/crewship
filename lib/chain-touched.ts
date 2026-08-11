import type { ChainSummary } from "@/hooks/use-chains"

/**
 * The one line that tells two runs of the same routine apart.
 *
 * The rail's first line is the routine, and on two runs of one routine it is
 * the same string twice — which is exactly what the workflow list looked like
 * when it shipped: two identical rows, nothing to choose between them. What
 * differs between runs is what each one REACHED: which issue it created or
 * moved, which agent it put to work.
 *
 * Issues first, because that is the noun a person came looking for; agents
 * second, because "who did it" is the follow-up question, not the first one.
 *
 * The counts are the server's UNCAPPED totals while the lists are capped at
 * five. Rendering only the returned five would make a chain that touched forty
 * issues read as one that touched five — a cut list that does not say it was
 * cut, which is the same class of quiet lie as a graph that stops without
 * declaring a gap.
 */
export function chainTouched(c: ChainSummary): string {
  const parts: string[] = []

  const issues = c.issues ?? []
  if (issues.length > 0) {
    // The identifier is what a human recognises and what the walk accepts as an
    // anchor. The id is the fallback: a freshly created issue can reach this
    // index before its identifier is readable, and rendering "undefined" is
    // worse than rendering something that at least resolves.
    const names = issues.map((i) => i.identifier || i.id)
    const more = c.issue_count - issues.length
    parts.push(more > 0 ? `${names.join(", ")} +${more}` : names.join(", "))
  } else if (c.issue_count > 0) {
    // A count with no list is reachable — the arrays are omitempty on the wire.
    // "3 issues" is still true and still narrows; silence is not.
    parts.push(`${c.issue_count} ${c.issue_count === 1 ? "issue" : "issues"}`)
  }

  const agents = c.agents ?? []
  if (agents.length > 0) {
    const names = agents.map((a) => {
      const name = a.slug || a.name || a.id
      // Only when it took more than one piece of work: "×1" on every row is
      // noise that makes the interesting "×3" harder to spot.
      return a.assignments > 1 ? `${name} ×${a.assignments}` : name
    })
    const more = c.agent_count - agents.length
    parts.push(more > 0 ? `${names.join(", ")} +${more}` : names.join(", "))
  } else if (c.agent_count > 0) {
    parts.push(`${c.agent_count} ${c.agent_count === 1 ? "agent" : "agents"}`)
  }

  return parts.join(" · ")
}
