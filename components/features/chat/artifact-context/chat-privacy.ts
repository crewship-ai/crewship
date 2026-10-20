/** What the composer may assume about who can read this conversation. Anything not positively known is not private. */
export type ChatPrivacy = "private" | "checking" | "unverified" | "not_private"

/** The participants read for ONE session. The endpoint lists human participants and returns none for a private 1:1 chat. */
export interface ParticipantsLoad { scope: string; status: "ok" | "error"; userIds: string[] }

interface Input {
  /** Identity of the session being shown; participants of any other scope are ignored. */
  scope: string
  sessionKind: string
  /** A restricted group conversation. */
  sharedRestricted: boolean
  /** The execution-profile probe finished for this session. */
  probeSettled: boolean
  /** ...and answered (not left pending by a failure). */
  profileKnown: boolean
  participants: ParticipantsLoad | null
  currentUserId: string | null | undefined
}

/**
 * Private means: a direct chat, the profile probe answered for this session,
 * this session's participants read succeeded, and nobody but the current user
 * is in it. A pending read is "checking", a failed one "unverified"; neither
 * is ever treated as private.
 */
export function deriveChatPrivacy(input: Input): ChatPrivacy {
  if (input.sessionKind !== "direct" || input.sharedRestricted) return "not_private"
  if (!input.probeSettled) return "checking"
  if (!input.profileKnown || !input.currentUserId) return "unverified"
  const load = input.participants?.scope === input.scope ? input.participants : null
  if (!load) return "checking"
  if (load.status === "error") return "unverified"
  return load.userIds.every(id => id === input.currentUserId) ? "private" : "not_private"
}
