"use client"

// Client half of persisted agent avatars (#1297).
//
// An agent's face is generated from (avatar_seed, avatar_style) by DiceBear
// on every render, which makes it a function of the installed library
// version: a dependency bump repaints the whole roster. The server can store
// a render and serve it back verbatim, but it cannot *produce* one — the
// generator is JavaScript-only. So the browser fills that gap, here.
//
// Three jobs:
//   - resolveStoredAvatarSrc: decide whether an <img> can actually load the
//     stored render, or whether the caller should generate from the seed.
//   - storeAgentAvatar: hand the server a render for one agent, called by
//     the surfaces that create or edit an agent.
//   - backfillAgentAvatars: the explicit action that stores renders for
//     existing agents that have none.
//
// What is deliberately NOT here any more is a write on view (#2876). Renders
// used to be backfilled off ordinary page views: an avatar without a stored
// render queued a PUT for whoever happened to look at it. That made a read
// cause a write, produced refused writes for every role that cannot edit
// agents, and decided an agent's frozen face by who looked first. Writes now
// happen only where a person chose to change something.

import { getAgentAvatarSVG, preloadAvatarStyle, DEFAULT_AVATAR_STYLE, AVATAR_STYLES } from "@/lib/agent-avatar"
import { apiFetch } from "@/lib/api-fetch"
import { readTotalCount } from "@/hooks/use-paged-list"
import { getAuthMode, withServerBase } from "@/lib/server-base"

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

/** The agent fields a render is drawn from — the shape the agents API returns. */
export interface AvatarSubject {
  id: string
  slug: string
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  crew?: { avatar_style?: string | null } | null
}

/**
 * What one store attempt came to.
 *
 *   stored  — the server now holds this render.
 *   present — it already held one (no request, or a 409 from a race).
 *   refused — the caller may not edit this agent, or it is gone (403/404).
 *   failed  — anything else: no workspace, generator unavailable, 4xx/5xx,
 *             network. The agent keeps generating from its seed.
 */
export type AvatarStoreOutcome = "stored" | "present" | "refused" | "failed"

/**
 * The (seed, style) the dashboard draws this agent with: its own seed or its
 * slug, its own style or its crew's. Storing anything else would freeze a
 * face nobody has seen.
 */
export function avatarInputs(agent: AvatarSubject, crewStyle?: string | null): { seed: string; style: string } {
  const wanted = agent.avatar_style || agent.crew?.avatar_style || crewStyle || null
  return {
    seed: agent.avatar_seed || agent.slug,
    style: wanted && AVATAR_STYLES[wanted] ? wanted : DEFAULT_AVATAR_STYLE,
  }
}

/**
 * Store the render of an agent that has none. Called right after a create or
 * an avatar-affecting edit, by the person who made it — never from a render.
 *
 * Never rejects: a render is an optimisation, so a failure here must not turn
 * a successful save into an error. The outcome says what happened for
 * callers that report it (the explicit backfill does).
 */
export async function storeAgentAvatar(
  agent: AvatarSubject,
  workspaceId: string | null | undefined,
  crewStyle?: string | null,
): Promise<AvatarStoreOutcome> {
  if (agent.avatar_url) return "present"
  // The PUT sits behind wsCtx and is refused without a workspace (#2196).
  if (!agent.id || !workspaceId) return "failed"
  try {
    const { seed, style } = avatarInputs(agent, crewStyle)
    // Wait for the collection rather than skipping it: getAgentAvatarSVG
    // returns null while a lazy style is still loading, and storing is
    // write-once, so a placeholder can never be stored.
    await preloadAvatarStyle(style)
    const svg = getAgentAvatarSVG(seed, style)
    if (!svg) return "failed"
    const res = await apiFetch(
      `/api/v1/agents/${encodeURIComponent(agent.id)}/avatar` +
        `?workspace_id=${encodeURIComponent(workspaceId)}`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ svg }),
      },
    )
    if (res.ok) return "stored"
    if (res.status === 409) return "present"
    if (res.status === 403 || res.status === 404) return "refused"
    return "failed"
  } catch {
    return "failed"
  }
}

export interface AvatarBackfillResult {
  /** Agents looked at. */
  total: number
  stored: number
  present: number
  refused: number
  failed: number
}

/**
 * The explicit backfill: store a render for every agent in the workspace (or
 * one crew) that has none. Sequential on purpose — this is a one-off action
 * somebody clicked, and one write at a time keeps it gentle on a large
 * workspace.
 *
 * Throws only when the agent list itself cannot be read; per-agent failures
 * are counted, not thrown.
 */
export async function backfillAgentAvatars(
  workspaceId: string,
  options: { crewId?: string; signal?: AbortSignal } = {},
): Promise<AvatarBackfillResult> {
  const result: AvatarBackfillResult = { total: 0, stored: 0, present: 0, refused: 0, failed: 0 }
  let offset = 0
  for (;;) {
    const params = new URLSearchParams({ workspace_id: workspaceId, limit: "500", offset: String(offset) })
    if (options.crewId) params.set("crew_id", options.crewId)
    const res = await apiFetch(`/api/v1/agents?${params}`, { signal: options.signal })
    if (!res.ok) throw new Error(`Agents could not be listed (${res.status})`)
    const rows: AvatarSubject[] = await res.json()
    if (!Array.isArray(rows)) throw new Error("Agent list returned an invalid response")
    for (const agent of rows) {
      if (options.signal?.aborted) return result
      result.total++
      result[await storeAgentAvatar(agent, workspaceId)]++
    }
    if (!rows.length) return result
    offset += rows.length
    const total = readTotalCount(res.headers)
    if (total == null || offset >= total) return result
  }
}
