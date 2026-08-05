// Reading a git link.
//
// Everything the issue detail needs to turn one row of `mission_code_links`
// (internal/api/issue_code_links.go) into something a reader can scan, kept
// pure and out of the JSX for the same reason `issue-facts.ts` is:
//
//   - The state → tone mapping is a DECISION, not a format. "Merged and closed
//     must not look the same" is a claim a test can hold; the same `switch`
//     inline in a component is a claim nothing checks.
//   - Half of this module is a security boundary. A pull-request title, its
//     author and its branch names are written by whoever opened the request —
//     on a public repository, by anyone — and the URL is the one field that
//     becomes executable if it is ever allowed to carry a `javascript:`
//     scheme. Those rules belong somewhere they can be enumerated, not
//     scattered across attribute positions.
//
// The corresponding agent-facing read path fences the same strings in an
// `<untrusted>` block (internal/api/issues_internal.go). The browser's fence
// is different: React escapes text, so the rule here is that these strings are
// only ever rendered AS TEXT — never as markdown, never as HTML, never as a
// URL that is not re-checked.

import type { IssueCodeLink } from "@/lib/types/mission"

/**
 * The Pill tones this module hands out — a subset of `DetailTone` in
 * components/ui/detail, mirrored rather than imported so a pure logic module
 * does not depend on a "use client" component. Assignment to `Pill`'s prop is
 * what type-checks the two are still in step.
 */
export type CodeLinkTone = "default" | "success" | "destructive" | "purple" | "warn"

/** Which glyph the card draws. Names, not components — this module has no JSX. */
export type CodeLinkStateIcon = "open" | "draft" | "merged" | "closed" | "unknown"

export interface CodeLinkStateBadge {
  label: string
  tone: CodeLinkTone
  icon: CodeLinkStateIcon
}

/**
 * The longest badge label, in characters.
 *
 * The card draws these in a FIXED-WIDTH column so the states line down the
 * left edge and the eye can run them — which means the column's width is
 * sized to this number, and a longer label would push a pill out of its column
 * and into the title beside it. A test pins it, so adding a fifth state that
 * does not fit fails here rather than in a screenshot.
 */
export const MAX_STATE_LABEL = 7

/**
 * The four states, four ways.
 *
 * A merged pull request, a closed one, an open draft and an open
 * ready-to-review one are four different facts, and the state is the thing a
 * reader is scanning for — so it is drawn before the title and each state gets
 * its own tone. `purple` for merged follows both forges' own convention;
 * `default` (muted) for a draft says "open, but not asking for anything yet"
 * without spending a colour on it.
 *
 * The label is written here, never echoed from the wire. `state` is normalised
 * server-side to one of four values, so an unrecognised one means the row
 * predates the normalisation or something else wrote it — and either way,
 * printing it back would turn the badge into a text channel somebody else
 * controls.
 */
export function codeLinkStateBadge(state: string | null | undefined): CodeLinkStateBadge {
  switch ((state ?? "").trim().toUpperCase()) {
    case "OPEN":
      return { label: "Open", tone: "success", icon: "open" }
    case "DRAFT":
      return { label: "Draft", tone: "default", icon: "draft" }
    case "MERGED":
      return { label: "Merged", tone: "purple", icon: "merged" }
    case "CLOSED":
      return { label: "Closed", tone: "destructive", icon: "closed" }
    default:
      return { label: "Unknown", tone: "default", icon: "unknown" }
  }
}

/** `acme/thing#7` — Crewship's own parsed values, so it is safe to show mono. */
export function codeLinkRef(link: Pick<IssueCodeLink, "owner" | "repo" | "number">): string {
  return `${link.owner}/${link.repo}#${link.number}`
}

/** What the provider calls the thing. GitLab users do not have pull requests. */
export function codeLinkNoun(provider: string): string {
  return provider === "GITLAB" ? "merge request" : "pull request"
}

/**
 * `feat/widget → main`, or null.
 *
 * Both halves or neither: half an arrow reads as a rendering bug rather than a
 * missing field. Branch names come out of provider JSON like the title does —
 * the caller renders this as text.
 */
export function codeLinkBranches(
  link: Pick<IssueCodeLink, "source_branch" | "target_branch">,
): string | null {
  const from = link.source_branch?.trim()
  const to = link.target_branch?.trim()
  if (!from || !to) return null
  return `${from} → ${to}`
}

/**
 * The stored URL, if it is safe to put in an `href`.
 *
 * The server reconstructs this string from parsed parts and only ever selects
 * between the `http` and `https` literals (internal/gitlink/parse.go), so
 * nothing reaching this function today can fail it. That is exactly why it is
 * worth having: the guard is cheap, and it is what keeps "the URL column is
 * trustworthy" true the day something other than `gitlink.Parse` writes a row.
 *
 * `new URL` rather than a prefix test — a prefix test is defeated by
 * `java\tscript:`, which the URL parser normalises away before reporting the
 * protocol.
 */
export function safeExternalHref(url: string | null | undefined): string | undefined {
  if (!url) return undefined
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return undefined
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined
  return url
}

/**
 * Why this row is showing stale data, or null.
 *
 * A failed refresh keeps the state it already had and records the reason
 * (internal/api/issue_code_links.go → noteSyncError), so the card can say
 * "merged, last checked Tuesday, refreshing is failing because the token was
 * revoked" instead of silently presenting week-old truth as current.
 */
export function codeLinkStaleReason(link: Pick<IssueCodeLink, "last_sync_error">): string | null {
  const reason = link.last_sync_error?.trim()
  return reason ? reason : null
}

/** Longest problem detail rendered before it is cut. */
const MAX_PROBLEM_DETAIL = 400

/**
 * The sentence to show when a write fails.
 *
 * These handlers answer RFC 7807, and the `detail` was written for the moment
 * it appears: the 412 names the credential to add AND the account label to put
 * on it, which is the entire fix. Collapsing that into "Failed to attach link"
 * sends the reader to a dead end with no next step — so the server's sentence
 * wins, and the caller's fallback is only for a response that carries none.
 *
 * Rendered as text by the caller. The bound is on length, not on content: a
 * detail interpolates the host and URL the user pasted, and an unbounded
 * string would let a long paste push the popover off the screen.
 */
export function codeLinkProblemMessage(body: unknown, fallback: string): string {
  const detail = (body as { detail?: unknown } | null)?.detail
  if (typeof detail !== "string") return fallback
  const trimmed = detail.trim()
  if (!trimmed) return fallback
  return trimmed.length > MAX_PROBLEM_DETAIL
    ? `${trimmed.slice(0, MAX_PROBLEM_DETAIL - 1)}…`
    : trimmed
}
