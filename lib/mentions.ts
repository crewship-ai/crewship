/**
 * `@mention` of an agent, as it is stored.
 *
 * ─────────────────────────────────────────────────────────────────────────
 *  THE WIRE FORMAT — this is the decision the backend has to live with.
 * ─────────────────────────────────────────────────────────────────────────
 *
 *   [@<slug>](crewship:agent/<agentId>)
 *
 * e.g.  `please pick this up [@robin](crewship:agent/cmt20ikph011ab4683c02)`
 *
 * It is a plain Markdown inline link. That one choice buys four properties
 * the alternatives do not:
 *
 *  1. **Unambiguously parseable.** The agent id lives in the link
 *     destination behind a private scheme nobody else emits, so a mention is
 *     `link.url.startsWith("crewship:agent/")` — one predicate, on a node the
 *     Markdown parser already produced. No hand-rolled scanner, and — because
 *     the parser produced it — a mention inside a code fence or a code span
 *     is *not* a link node and is therefore not a mention. That falls out for
 *     free; a regex over the raw body gets it wrong and turns documentation
 *     of the syntax into a live trigger.
 *
 *  2. **Degrades to readable text.** Nothing parses it? You read
 *     `[@robin](crewship:agent/cmt20…)` — the handle is right there, in
 *     front. Any generic Markdown renderer shows a link labelled `@robin`.
 *     Compare `<@cmt20ikph011ab4683c02>` (Slack's shape), which degrades to
 *     an opaque id and tells a human nothing.
 *
 *  3. **Addressed by id, never by name.** The only thing read out of a
 *     comment body is the agent id. The chip's name and avatar come from the
 *     workspace roster, resolved at render time. So no wording a user can
 *     type makes a chip *claim* anything: write
 *     `[@head-of-security](crewship:agent/<robin's id>)` and you get a chip
 *     reading "Robin", with Robin's face. This is the forgery defence, and it
 *     is why the label is decorative and the id is not.
 *
 *  4. **An id that does not resolve is not a mention.** It renders as plain
 *     muted text, never as a chip. So `[@ceo](crewship:agent/agt_made_up)`
 *     buys exactly as much as typing `@ceo` did: nothing.
 *
 * The honest limit: a body is text, so of course a human can *type* a
 * well-formed token for a real agent. That is not forgery — that is a
 * mention, which is the feature. What cannot be done is make a mention say
 * something the roster does not, or address an agent the reader's workspace
 * cannot see. Authorization ("may this actor make this agent work?") is the
 * backend's call at trigger time, and no wire format can stand in for it.
 *
 * ── What the backend has to implement to meet this ──────────────────────
 *
 *   - On comment write, parse the body with the equivalent of
 *     {@link extractMentionAgentIds} (the Go regexp is in the doc guide),
 *     resolve each id **within the comment's workspace**, and drop ids that
 *     do not resolve — a mention of a foreign-workspace agent is not a
 *     mention, it is a probe.
 *   - Persist the resolved set (a `mission_comment_mentions` join, or a
 *     column) rather than re-parsing on read: the body is user input and
 *     re-parsing on every read is a second chance to get it wrong.
 *   - Emit a `mentioned` activity per resolved mention, with `details` set to
 *     the agent id (the renderer also accepts a full token or
 *     `{"agent_id":"…"}` — see `mentionTargetFromActivityDetails`).
 *   - Only then dispatch the trigger, under the same authorization the
 *     equivalent "assign this agent" action would take.
 */

/** The private URL scheme a mention's destination must start with. */
export const MENTION_URL_SCHEME = "crewship:agent/"

/**
 * Ids we are willing to write into, or read out of, a comment body.
 *
 * CUIDs are `[a-z0-9]+`; this is deliberately a little wider (so a seeded or
 * prefixed id still round-trips) and deliberately not wide enough to hold a
 * slash, a dot, a space or a bracket. That is what stops a destination from
 * ever being a path (`../../etc/passwd`), a second token, or a URL to
 * somewhere else.
 */
const AGENT_ID_RE = /^[A-Za-z0-9_-]{1,64}$/

/** Characters a written label may contain. Everything else is dropped. */
const LABEL_ALLOWED_RE = /[^a-z0-9._-]+/g

/** Reading is liberal in the label, strict in the destination. */
const MENTION_TOKEN_RE = /\[@[^\]\n]{0,80}\]\(crewship:agent\/([A-Za-z0-9_-]{1,64})\)/g

/** Characters that may sit directly before the `@` that opens the picker. */
const HANDLE_CHAR_RE = /[a-zA-Z0-9._-]/

/**
 * An agent, as much of one as a mention needs. Structurally satisfied by
 * `AgentSummary` from the agents endpoint.
 */
export interface MentionAgent {
  id: string
  name: string
  slug: string
  /** DiceBear seed; falls back to the slug, then the name. */
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  role_title?: string | null
}

/** Agents by id — what the renderer resolves a mention against. */
export type MentionDirectory = ReadonlyMap<string, MentionAgent>

export function mentionDirectory(agents: readonly MentionAgent[]): MentionDirectory {
  return new Map(agents.map((a) => [a.id, a]))
}

/**
 * The label written into a token. Not identity — decoration. It is reduced
 * to the slug alphabet so it can never carry a `]` or a `)` and split one
 * token into two.
 */
export function normalizeMentionLabel(slug: string): string {
  const cleaned = slug.toLowerCase().replace(LABEL_ALLOWED_RE, "").slice(0, 64)
  return cleaned || "agent"
}

/** Write the token for an agent. Throws on an id this format cannot express. */
export function mentionToken(agent: Pick<MentionAgent, "id" | "slug" | "name">): string {
  if (!AGENT_ID_RE.test(agent.id)) {
    throw new Error(`mentionToken: agent id is not expressible in a mention: ${agent.id}`)
  }
  const label = normalizeMentionLabel(agent.slug || agent.name)
  return `[@${label}](${MENTION_URL_SCHEME}${agent.id})`
}

/**
 * The agent id behind a link destination, or null if this link is not a
 * mention. Case-sensitive on the scheme on purpose: `CREWSHIP:AGENT/` is not
 * a thing we write, so accepting it only widens what has to be reasoned about.
 */
export function parseMentionUrl(url: string | null | undefined): string | null {
  if (!url || !url.startsWith(MENTION_URL_SCHEME)) return null
  const id = url.slice(MENTION_URL_SCHEME.length)
  return AGENT_ID_RE.test(id) ? id : null
}

/** Every agent id mentioned in a body, de-duplicated, in first-seen order. */
export function extractMentionAgentIds(body: string): string[] {
  const seen = new Set<string>()
  const re = new RegExp(MENTION_TOKEN_RE.source, "g")
  let m: RegExpExecArray | null
  while ((m = re.exec(body)) !== null) seen.add(m[1])
  return [...seen]
}

/** An open `@…` at the caret: where it starts, and what has been typed. */
export interface MentionQuery {
  /** Index of the `@`. */
  start: number
  /** Lower-cased text between the `@` and the caret. */
  query: string
}

/**
 * Is the caret inside an `@handle` the picker should be open for?
 *
 * Only at a word boundary, so `pavel@unify.cz` is an email address and not a
 * half-typed mention of `unify.cz`.
 */
export function findMentionQuery(text: string, caret: number): MentionQuery | null {
  if (caret <= 0 || caret > text.length) return null
  let i = caret - 1
  while (i >= 0 && HANDLE_CHAR_RE.test(text[i])) i--
  if (i < 0 || text[i] !== "@") return null
  if (i > 0 && /\S/.test(text[i - 1])) return null
  return { start: i, query: text.slice(i + 1, caret).toLowerCase() }
}

/** Swap the typed `@handle` for the real token, and park the caret after it. */
export function applyMention(
  text: string,
  start: number,
  caret: number,
  agent: Pick<MentionAgent, "id" | "slug" | "name">,
): { text: string; caret: number } {
  const token = `${mentionToken(agent)} `
  const next = text.slice(0, start) + token + text.slice(caret)
  return { text: next, caret: start + token.length }
}

/**
 * The agent a `mentioned` activity points at.
 *
 * The activity kind does not exist server-side yet (see the guide), so this
 * accepts every shape the backend might plausibly put in `details` — a bare
 * id, a whole token, or a JSON object — rather than betting on one and
 * rendering nothing when the bet is wrong.
 */
export function mentionTargetFromActivityDetails(details: string | null): string | null {
  if (!details) return null
  const trimmed = details.trim()
  if (AGENT_ID_RE.test(trimmed)) return trimmed
  const [fromToken] = extractMentionAgentIds(trimmed)
  if (fromToken) return fromToken
  if (trimmed.startsWith("{")) {
    try {
      const parsed: unknown = JSON.parse(trimmed)
      if (parsed && typeof parsed === "object") {
        const id = (parsed as Record<string, unknown>).agent_id
        if (typeof id === "string" && AGENT_ID_RE.test(id)) return id
      }
    } catch {
      /* details is not JSON — nothing to read out of it. */
    }
  }
  return null
}

/** Activity `action` values the history row renders as "X mentioned @Y". */
const MENTION_ACTIONS = new Set(["mentioned", "mention", "agent_mentioned", "comment_mentioned"])

export function isMentionActivity(action: string): boolean {
  return MENTION_ACTIONS.has(action)
}
