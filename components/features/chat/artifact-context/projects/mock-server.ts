import type { ArtifactComment, ArtifactFile, ArtifactProjectSnapshot, CraftLeaseStatus, CraftProposal } from "./types"

/**
 * In-memory artifact backend for component tests and Playwright (`page.route`).
 * It implements the contract's semantics rather than canned answers: restore
 * appends a version, writes are compare-and-set on base_version, comment
 * anchors are checked, and the agent can publish and answer comments. No
 * dependencies, so e2e code can import it directly.
 */

export interface ArtifactMockInitial {
  slug: string
  pageId?: string
  name?: string
  entrypoint?: string
  files: ArtifactFile[]
  message?: string
  canWrite?: boolean
  /** Share the first version immediately. */
  shared?: boolean
  /** What the availability route answers; true by default. */
  enabled?: boolean
  origin?: { chat_id: string; agent_id: string } | null
  /** Versions beyond this answer 413 artifact_history_quota. */
  maxVersions?: number
  /** Craft routes answer only when enabled; otherwise they 404 like a backend without them. */
  craft?: { enabled: boolean; lease?: CraftLeaseStatus; proposals?: CraftProposal[] }
}

interface StoredVersion { version: number; entrypoint: string; files: ArtifactFile[]; message?: string; actor: string; created_at: string; restored_from: number | null }

export interface ArtifactMockState {
  slug: string
  pageId: string
  name?: string
  canWrite: boolean
  enabled: boolean
  origin?: { chat_id: string; agent_id: string } | null
  maxVersions?: number
  sharedVersion: number
  versions: StoredVersion[]
  comments: ArtifactComment[]
  craftEnabled: boolean
  lease: CraftLeaseStatus
  proposals: CraftProposal[]
  /** Every handled request, as "METHOD /path", for assertions. */
  requests: string[]
}

export interface ArtifactMockServer {
  state: ArtifactMockState
  /** The Response for an artifact or Craft route, or null when the URL is not one (let the caller fall through). */
  handle(url: string, init?: RequestInit): Response | null
  snapshot(version?: number): ArtifactProjectSnapshot
  /** The agent publishes the next version and answers every pending or working comment with it. */
  agentPublish(files: ArtifactFile[], message?: string, reply?: string): number
  setLease(status: CraftLeaseStatus): void
  setProposals(proposals: CraftProposal[]): void
}

const FREE_LEASE: CraftLeaseStatus = { holder: null, mine: false, generation: 0, can_write: true, handoff_requested_by: null }
const YOU = { actor_id: "you", display_name: "You", kind: "human" as const }

// Regex literals keep this helper out of the frontend route registry scan, which reads quoted path strings.
const AVAILABILITY = /^\/api\/v1\/pages\/artifacts\/availability$/
const ARTIFACT = /^\/api\/v1\/pages\/([^/]+)\/artifact(?:\/(restore|share|comments))?$/
const CRAFT = /^\/api\/v1\/pages\/([^/]+)\/craft\/(availability|lease|lease\/request-handoff|proposals|proposals\/([^/]+)\/(accept|reject))$/

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })
const fail = (status: number, code: string, error: string) => json(status, { error, code })
const missing = () => new Response("404 page not found", { status: 404, headers: { "content-type": "text/plain" } })
const normalize = (text: string) => text.replace(/\s+/g, " ").trim()
const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")

function readBody(init?: RequestInit): Record<string, unknown> {
  if (typeof init?.body !== "string") return {}
  try {
    const parsed: unknown = JSON.parse(init.body)
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch { return {} }
}

/** Text of the element with this id: the content up to the matching close tag, tags stripped. Enough for test fixtures. */
function elementText(html: string, id: string): string | null {
  const open = new RegExp(`<([a-zA-Z][\\w-]*)\\b[^>]*\\bid=(?:"${escape(id)}"|'${escape(id)}'|${escape(id)}(?=[\\s>]))[^>]*>`, "g")
  const matches = [...html.matchAll(open)]
  if (matches.length !== 1) return null
  const [tag, name] = [matches[0], matches[0][1]]
  const start = tag.index! + tag[0].length
  const close = html.indexOf(`</${name}>`, start)
  return normalize(html.slice(start, close < 0 ? undefined : close).replace(/<[^>]*>/g, " "))
}

export function createArtifactMockServer(initial: ArtifactMockInitial): ArtifactMockServer {
  let clock = Date.parse("2026-10-27T09:00:00Z")
  const now = () => new Date(clock += 60_000).toISOString()
  const state: ArtifactMockState = {
    slug: initial.slug,
    pageId: initial.pageId ?? `page-${initial.slug}`,
    name: initial.name,
    canWrite: initial.canWrite ?? true,
    enabled: initial.enabled ?? true,
    origin: initial.origin,
    maxVersions: initial.maxVersions,
    sharedVersion: initial.shared ? 1 : 0,
    versions: [{ version: 1, entrypoint: initial.entrypoint ?? "index.html", files: initial.files, message: initial.message, actor: "agent", created_at: now(), restored_from: null }],
    comments: [],
    craftEnabled: initial.craft?.enabled ?? false,
    lease: initial.craft?.lease ?? FREE_LEASE,
    proposals: initial.craft?.proposals ?? [],
    requests: [],
  }
  const latest = () => state.versions[state.versions.length - 1]
  const find = (version: number) => state.versions.find(entry => entry.version === version)

  function append(entry: Omit<StoredVersion, "version" | "created_at">): StoredVersion | null {
    if (state.maxVersions !== undefined && state.versions.length >= state.maxVersions) return null
    const next = { ...entry, version: latest().version + 1, created_at: now() }
    state.versions.push(next)
    return next
  }

  function snapshot(version?: number): ArtifactProjectSnapshot {
    const shown = version === undefined ? latest() : find(version)
    if (!shown) throw new Error(`no version ${version}`)
    return {
      page_id: state.pageId,
      ...(state.name ? { name: state.name } : {}),
      slug: state.slug,
      ...(state.origin !== undefined ? { origin: state.origin } : {}),
      enabled: state.enabled,
      can_write: state.canWrite,
      latest_version: latest().version,
      shared_version: state.sharedVersion,
      manifest: { version: shown.version, entrypoint: shown.entrypoint, files: shown.files, created_at: shown.created_at, message: shown.message, restored_from: shown.restored_from },
      versions: state.canWrite ? [...state.versions].reverse().map(({ version: v, entrypoint, message, actor, created_at, restored_from }) => ({ version: v, entry_point: entrypoint, message, actor, created_at, restored_from })) : [],
      comments: state.comments.map(comment => ({ ...comment, replies: comment.replies?.map(reply => ({ ...reply })) })),
    }
  }

  /** Compare-and-set guard shared by every write. */
  function guard(body: Record<string, unknown>): Response | null {
    if (!state.canWrite) return fail(403, "forbidden", "You cannot change this artifact.")
    if (body.base_version !== latest().version) return fail(409, "artifact_version_conflict", `Version ${latest().version} is newer than your base.`)
    if (typeof body.version !== "number" || !find(body.version)) return fail(400, "invalid", "Unknown version.")
    return null
  }

  function artifact(method: string, action: string | undefined, query: URLSearchParams, body: Record<string, unknown>): Response {
    if (!state.enabled) return fail(404, "not_found", "Artifact projects are disabled.")
    if (!action && method === "GET") {
      const raw = query.get("version")
      if (raw === null) return json(200, snapshot())
      const version = Number(raw)
      return find(version) ? json(200, snapshot(version)) : fail(404, "not_found", "No such version.")
    }
    if (method !== "POST") return fail(405, "invalid", "Method not allowed.")
    if (action === "restore") {
      const refused = guard(body)
      if (refused) return refused
      const source = find(body.version as number)!
      const next = append({ entrypoint: source.entrypoint, files: source.files, message: `Restored version ${source.version}`, actor: "you", restored_from: source.version })
      return next ? json(200, { version: next.version, actor: next.actor }) : fail(413, "artifact_history_quota", "Version history is full.")
    }
    if (action === "share") {
      const refused = guard(body)
      if (refused) return refused
      state.sharedVersion = body.version as number
      return json(200, { shared_version: state.sharedVersion, url: `/p/${state.slug}` })
    }
    if (action === "comments") return comment(body)
    return missing()
  }

  function comment(body: Record<string, unknown>): Response {
    const { version, path, selector, quote, text, send_to_agent: sendToAgent } = body
    if (typeof version !== "number" || typeof path !== "string" || typeof selector !== "string" || typeof quote !== "string" || typeof text !== "string" || !text.trim()) return fail(400, "invalid", "Comment is incomplete.")
    const file = find(version)?.files.find(entry => entry.path === path)
    if (!file) return fail(400, "invalid", "Unknown file or version.")
    if (selector) {
      const id = /^#([A-Za-z_][\w-]*)$/.exec(selector)?.[1]
      const found = id ? elementText(file.content, id) : null
      if (found === null || found !== normalize(quote)) return fail(409, "artifact_anchor_mismatch", "The selected element changed.")
    }
    const stored: ArtifactComment = {
      id: `c${state.comments.length + 1}`, version, path, selector, quote, text, author: "You", created_at: now(),
      ...(sendToAgent === true ? { status: "pending" as const } : {}),
    }
    state.comments.push(stored)
    return json(201, stored)
  }

  function craft(method: string, route: string, proposalId: string | undefined, decision: string | undefined): Response {
    if (!state.craftEnabled) return missing()
    if (route === "availability" && method === "GET") return json(200, { enabled: true })
    if (route === "lease" && method === "GET") return json(200, state.lease)
    if (route === "lease" && method === "POST") {
      if (state.lease.holder && !state.lease.mine) return fail(409, "lease_held", `${state.lease.holder.display_name} is editing.`)
      state.lease = { ...state.lease, holder: YOU, mine: true, generation: state.lease.generation + 1, handoff_requested_by: null }
      return json(200, state.lease)
    }
    if (route === "lease/request-handoff" && method === "POST") {
      state.lease = { ...state.lease, handoff_requested_by: "You" }
      return json(200, state.lease)
    }
    if (route === "proposals" && method === "GET") return json(200, state.proposals)
    if (proposalId && decision && method === "POST") {
      const proposal = state.proposals.find(entry => entry.id === decodeURIComponent(proposalId))
      if (!proposal) return fail(404, "not_found", "No such proposal.")
      if (proposal.state !== "open") return fail(409, "artifact_version_conflict", "The proposal is no longer open.")
      proposal.state = decision === "accept" ? "accepted" : "rejected"
      return json(200, proposal)
    }
    return fail(405, "invalid", "Method not allowed.")
  }

  function handle(url: string, init?: RequestInit): Response | null {
    const parsed = new URL(url, "http://mock.local")
    const method = (init?.method ?? "GET").toUpperCase()
    const path = parsed.pathname
    if (AVAILABILITY.test(path)) {
      state.requests.push(`${method} ${path}`)
      return json(200, { enabled: state.enabled })
    }
    const art = ARTIFACT.exec(path)
    if (art) {
      state.requests.push(`${method} ${path}`)
      if (decodeURIComponent(art[1]) !== state.slug) return fail(404, "not_found", "No such artifact.")
      return artifact(method, art[2], parsed.searchParams, readBody(init))
    }
    const crafted = CRAFT.exec(path)
    if (crafted) {
      state.requests.push(`${method} ${path}`)
      if (decodeURIComponent(crafted[1]) !== state.slug) return missing()
      return craft(method, crafted[2], crafted[3], crafted[4])
    }
    return null
  }

  function agentPublish(files: ArtifactFile[], message?: string, reply?: string): number {
    const next = append({ entrypoint: latest().entrypoint, files, message, actor: "agent", restored_from: null })
    if (!next) throw new Error("history quota reached")
    for (const entry of state.comments) {
      if (entry.status !== "pending" && entry.status !== "working") continue
      entry.status = "answered"
      entry.answered_version = next.version
      entry.replies = [...(entry.replies ?? []), { text: reply ?? `Updated in version ${next.version}.`, version: next.version, author: "Agent", created_at: next.created_at }]
    }
    return next.version
  }

  return {
    state, handle, snapshot, agentPublish,
    setLease: status => { state.lease = status },
    setProposals: proposals => { state.proposals = proposals },
  }
}
