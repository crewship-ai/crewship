/**
 * Client-side estimate of who can read this conversation. It is a BLOCKER ONLY, kept as defence in depth: it can
 * stop an update the server might otherwise allow, and it can never enable one. Only the server-confirmed signal
 * (`serverConfirmedPrivate`, from a field that does not exist yet) can do that. Anything not positively known is not private.
 */
export type ChatPrivacy = "private" | "checking" | "unverified" | "not_private"

/** The participants read for ONE session. The endpoint lists human participants and returns none for a private 1:1 chat. */
export interface ParticipantsLoad { scope: string; status: "ok" | "error"; userIds: string[]; /** When the read finished (ms since epoch). */ at: number }

/** A participants read older than this no longer counts: membership can change. */
export const PARTICIPANTS_MAX_AGE_MS = 30_000

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
  /** Evaluate staleness at this time (ms since epoch). Pass Date.now() right before a send. */
  now?: number
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
  if (input.now !== undefined && input.now - load.at > PARTICIPANTS_MAX_AGE_MS) return "checking"
  return load.userIds.every(id => id === input.currentUserId) ? "private" : "not_private"
}
