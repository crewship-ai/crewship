// Client half of persisted agent avatars (#1297).
//
// An agent's face is generated from (avatar_seed, avatar_style) by DiceBear
// on every render, which makes it a function of the installed library
// version: a dependency bump repaints the whole roster. The server can store
// a render and serve it back verbatim, but it cannot *produce* one — the
// generator is JavaScript-only. So the browser fills that gap, here.
//
// Two jobs:
//   - resolveStoredAvatarSrc: decide whether an <img> can actually load the
//     stored render, or whether the caller should generate from the seed.
//   - queueAvatarBackfill: hand the server a render for an agent that has
//     none yet.
//
// The backfill runs off ordinary page views, so everything below exists to
// keep it from becoming a nuisance: it fires at most once per agent per
// session, is capped per page load, and gives up entirely the moment the
// server says the caller may not write.

import { getAgentAvatarSVG } from "@/lib/agent-avatar"
import { apiFetch } from "@/lib/api-fetch"
import { getAuthMode, withServerBase } from "@/lib/server-base"

/**
 * Per-page-load ceiling on backfill uploads.
 *
 * A large workspace can paint hundreds of avatars in one roster render.
 * Without a cap, the first visit after this ships would fire one write per
 * agent — a self-inflicted thundering herd for what is only an optimisation.
 * With it, coverage converges over a handful of visits instead, which is
 * fine: an agent that is never viewed is also never repainted in front of
 * anyone.
 */
const BACKFILL_BUDGET_PER_LOAD = 25

/** Agents already attempted this session (regardless of outcome). */
const attempted = new Set<string>()

let spent = 0

/**
 * Set once the server refuses a write on authorisation grounds. A VIEWER has
 * no edit rights on any agent, so the second 403 would tell us nothing the
 * first didn't — and a roster's worth of them is just log noise on the
 * server and wasted requests on the client.
 */
let forbidden = false

/** The per-load upload ceiling. Exported so tests don't hard-code it. */
export function avatarBackfillBudget(): number {
  return BACKFILL_BUDGET_PER_LOAD
}

/**
 * Resolve the <img> src for an agent's stored avatar, or null when the
 * caller should fall back to generating from the seed.
 *
 * Returns null in bearer mode (the desktop shell): an <img> request carries
 * no Authorization header and cookies are omitted there, so the stored URL
 * would 401 and render as a broken image. Generating from the seed is the
 * pre-persistence behaviour — never worse than today, just not better.
 */
export function resolveStoredAvatarSrc(avatarUrl: string | null | undefined): string | null {
  if (!avatarUrl) return null
  if (getAuthMode() === "bearer") return null
  // Remote-server mode points the dashboard at a different origin than the
  // page it was served from; a bare relative path would resolve against the
  // wrong host.
  return withServerBase(avatarUrl)
}

/**
 * Store a render for an agent that has none, so its face survives the next
 * generator upgrade.
 *
 * Safe to call from a render effect for every visible agent: it self-limits
 * (see the module comment) and never rejects — a failed backfill just means
 * the agent keeps generating from its seed, exactly as it does today.
 */
export async function queueAvatarBackfill(
  agentId: string,
  seed: string,
  style: string | null | undefined,
): Promise<void> {
  if (!agentId || forbidden) return
  if (attempted.has(agentId)) return
  if (spent >= BACKFILL_BUDGET_PER_LOAD) return

  // Null means the style's collection is still loading and getAgentAvatarSVG
  // would otherwise have handed back a placeholder disc. Storing that would
  // freeze the wrong picture — and since the server stores write-once, there
  // is no second chance. Skip without marking the agent attempted so a later
  // render, once the import lands, can still fill it in.
  const svg = getAgentAvatarSVG(seed, style)
  if (!svg) return

  attempted.add(agentId)
  spent++

  try {
    const res = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/avatar`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ svg }),
    })
    if (res.status === 403) {
      // Not our agent to write to, and almost certainly none of them are.
      forbidden = true
    }
    // 409 means someone else stored one first. That is the race working as
    // designed and says nothing about the next agent, so it is deliberately
    // not treated like a 403.
  } catch {
    // Offline, aborted navigation, server restart. The avatar still renders
    // from its seed; a later session retries.
  }
}

/** Test-only: clear the session guards between cases. */
export function _resetAvatarBackfillForTest(): void {
  attempted.clear()
  spent = 0
  forbidden = false
}
