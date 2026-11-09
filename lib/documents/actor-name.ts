/** A workspace member as the members roster returns it; only the fields used for a label. */
export interface DocumentActorMember {
  user: { id: string; full_name?: string | null; email?: string | null }
}

/**
 * Label for a document actor (`user:<id>`, `agent:<id>`): the member's name,
 * "You" for the viewer, or a description. Never the raw identifier.
 */
export function documentActorName(actor: string, { humanId, members }: { humanId?: string; members?: readonly DocumentActorMember[] }): string {
  const split = actor.indexOf(":")
  if (split <= 0) return "Unknown author"
  const kind = actor.slice(0, split), id = actor.slice(split + 1)
  if (kind === "agent") return "Agent"
  if (kind !== "user" && kind !== "human") return "Unknown author"
  if (kind === "user" && id && id === humanId) return "You"
  const member = kind === "user" ? members?.find(entry => entry.user.id === id) : undefined
  return member?.user.full_name || member?.user.email || "Workspace member"
}
