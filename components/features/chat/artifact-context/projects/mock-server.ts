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

const FREE_LEASE: CraftLeaseStatus = { holder: null, mine: false, generation: null, can_write: true, requests: [] }
const YOU = { actor_id: "user:you", display_name: "You", kind: "human" as const }
const SESSION_TOKEN = "mock-craft-session"

// Regex literals keep this helper out of the frontend route registry scan, which reads quoted path strings.
const AVAILABILITY = /^\/api\/v1\/pages\/artifacts\/availability$/
const ARTIFACT = /^\/api\/v1\/pages\/([^/]+)\/artifact(?:\/(restore|share|comments))?$/
const CRAFT = /^\/api\/v1\/pages\/([^/]+)\/craft\/(availability|sessions|lease|lease\/request-handoff|proposals|proposals\/([^/]+)\/(accept|reject))$/

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })
const fail = (status: number, code: string, error: string) => json(status, { error, code })
const missing = () => new Response("404 page not found", { status: 404, headers: { "content-type": "text/plain" } })

function readBody(init?: RequestInit): Record<string, unknown> {
  if (typeof init?.body !== "string") return {}
  try {
    const parsed: unknown = JSON.parse(init.body)
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch { return {} }
}

// Go's unicode.IsSpace, which the backend's strings.Fields splits on.
const fields = (text: string) => text.split(/[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+/).filter(Boolean).join(" ")

/**
 * The backend's anchor text for the element with this id: its descendant text
 * nodes joined with nothing, <script> and <style> skipped, then strings.Fields
 * joined by " ". The id must be unique in the document.
 */
function elementText(html: string, id: string): string | null {
  const doc = new DOMParser().parseFromString(html, "text/html")
  const matches = [...doc.querySelectorAll("[id]")].filter(el => el.id === id)
  if (matches.length !== 1) return null
  let text = ""
  const walk = (node: Node) => {
    for (const child of node.childNodes) {
      if (child.nodeType === Node.TEXT_NODE) text += child.nodeValue ?? ""
      else if (child.nodeType === Node.ELEMENT_NODE && !/^(script|style)$/i.test((child as Element).tagName)) walk(child)
    }
  }
  walk(matches[0])
  return fields(text)
}

/** Backend rule: the quote equals the element text, or is exactly 500 UTF-8 bytes and a prefix of it. */
function quoteMatches(found: string, quote: string): boolean {
  return quote === found || (new TextEncoder().encode(quote).length === 500 && found.startsWith(quote))
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
      if (found === null || !quoteMatches(found, quote)) return fail(409, "artifact_anchor_mismatch", "The selected element changed.")
    }
    const stored: ArtifactComment = {
      id: `c${state.comments.length + 1}`, version, path, selector, quote, text, author: "You", created_at: now(),
      ...(sendToAgent === true ? { status: "pending" as const } : {}),
    }
    state.comments.push(stored)
    return json(201, stored)
  }

  // The #4374 wire shape: pending requests travel as handoff_requested_by.
  const wireLease = ({ requests, ...lease }: CraftLeaseStatus) => ({ ...lease, handoff_requested_by: requests })

  function craft(method: string, route: string, proposalId: string | undefined, decision: string | undefined, admission: string | null): Response {
    if (!state.craftEnabled) return missing()
    if (route === "availability" && method === "GET") return json(200, { enabled: true })
    if (route === "sessions" && method === "POST") return json(201, { session_token: SESSION_TOKEN, session_id: "mock-session", application_id: "mock-app", page_id: `page-${state.slug}` })
    if (route === "lease" || route === "lease/request-handoff") {
      if (admission !== SESSION_TOKEN) return fail(403, "forbidden", "native Craft access denied")
      if (route === "lease" && method === "GET") return json(200, wireLease(state.lease))
      if (route === "lease/request-handoff" && method === "POST") {
        if (!state.lease.holder || state.lease.generation === null) return fail(409, "conflict", "native editor lease changed")
        const request = { request_id: `r${state.lease.requests.length + 1}`, ...YOU }
        state.lease = { ...state.lease, requests: [...state.lease.requests.filter(entry => entry.actor_id !== YOU.actor_id), request] }
        return json(201, { request_id: request.request_id, generation: state.lease.generation })
      }
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
      return craft(method, crafted[2], crafted[3], crafted[4], new Headers(init?.headers).get("X-Craft-Session"))
    }
    return null
  }

  function agentPublish(files: ArtifactFile[], message?: string, reply?: string): number {
    const next = append({ entrypoint: latest().entrypoint, files, message, actor: "agent:cmuxw95k60027db59c6f2:msg_1793118136200864661_c3b6aa1c9005c313", restored_from: null })
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
