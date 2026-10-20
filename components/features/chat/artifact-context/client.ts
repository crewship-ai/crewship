import { apiFetch } from "@/lib/api-fetch"
import type { CheckpointFetcher } from "./exact-revision"
import type { LinkedViewerTarget, ConversationLink } from "./linked-viewer-targets"
import type { Audience, ExpectedState } from "./composer/composer-context-model"
import type { LibraryItem, LibraryKind } from "./library/library-model"
import type { Freshness, WorkCardModel, WorkProblem, WorkRunState } from "./types"

/**
 * Typed client for the proposed endpoints (FE-REQ-F2-1, -2, -4, -5, -6). None
 * of them is served yet, so every call has a defined "unsupported" outcome and
 * nothing here throws into the UI.
 *
 * - "unsupported": the route is not served (404 without a JSON body, 405, 501).
 * - "absent": a JSON 404 on an item-level call. The server answers it for a
 *   private or unreadable item, so the item is dropped, never described.
 * - "denied": 401/403.
 * - "error": 503, network or a body that does not match the contract. Never read as a revoke.
 */
export type Fetched<T> =
  | { status: "ok"; data: T }
  | { status: "unsupported" }
  | { status: "absent" }
  | { status: "denied" }
  | { status: "error" }

const OBJECT_ID = /^[A-Za-z0-9_-]{1,128}$/
const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const text = (value: unknown, limit = 512): value is string => typeof value === "string" && value.trim().length > 0 && value.length <= limit

const chatPath = (chatId: string, rest: string, params: Record<string, string>) =>
  `/api/v1/chats/${encodeURIComponent(chatId)}/${rest}?${new URLSearchParams(params)}`

/** Item-level calls treat a JSON 404 as "absent"; list-level calls pass itemLevel false and read every 404 as a missing route. */
async function call<T>(url: string, parse: (body: unknown) => T | null, init: { signal?: AbortSignal; method?: string; itemLevel?: boolean } = {}): Promise<Fetched<T>> {
  try {
    const response = await apiFetch(url, { signal: init.signal, method: init.method ?? "GET", cache: "no-store" })
    if (response.status === 401 || response.status === 403) return { status: "denied" }
    const json = (response.headers.get("content-type") ?? "").includes("json")
    if (response.status === 404 && init.itemLevel && json) return { status: "absent" }
    if (response.status === 404 || response.status === 405 || response.status === 501) return { status: "unsupported" }
    if (!response.ok) return { status: "error" }
    const body = response.status === 204 ? null : await response.json().catch(() => undefined)
    const data = parse(body)
    return data === null ? { status: "error" } : { status: "ok", data }
  } catch (error) {
    if ((error as { name?: string })?.name === "AbortError") throw error
    return { status: "error" }
  }
}

/* ---- work cards (FE-REQ-F2-1) ---- */

const RUN_STATES: WorkRunState[] = ["queued", "running", "waiting", "proposal_ready", "applied", "conflicted", "failed", "canceled"]
const PROBLEMS: WorkProblem[] = ["revoked", "disconnected", "unknown_result", "conflict", "stopped", "missing_runtime", "broken_preview"]
const FRESHNESS: Freshness[] = ["current", "stale", "unknown"]

export interface WorkCardEntry {
  model: WorkCardModel
  /** What Open shows: the exact checkpoint when the card carries one, otherwise the latest. Null when nothing is saved. */
  target: LinkedViewerTarget | null
  title: string | null
}

export function parseWorkCards(body: unknown, workspaceId: string, currentUserId: string | null | undefined): WorkCardEntry[] | null {
  if (!object(body) || !Array.isArray(body.cards) || body.cards.length > 100) return null
  const entries: WorkCardEntry[] = []
  for (const raw of body.cards) {
    if (!object(raw) || !text(raw.id, 128) || !RUN_STATES.includes(raw.run_state as WorkRunState)) continue
    const problem = raw.problem === null || raw.problem === undefined ? null : PROBLEMS.includes(raw.problem as WorkProblem) ? raw.problem as WorkProblem : "unknown_result"
    const freshness = FRESHNESS.includes(raw.freshness as Freshness) ? raw.freshness as Freshness : "unknown"
    const saved = object(raw.saved) && Number.isSafeInteger(raw.saved.revision) && (raw.saved.revision as number) >= 0 ? raw.saved : null
    const readable = object(raw.access) && text(raw.access.title) && text(raw.access.type, 64)
    // Anything but a readable projection is restricted: no name, type or executor is ever shown for it.
    const access: WorkCardModel["access"] = readable ? { status: "readable", title: (raw.access as { title: string }).title, typeLabel: (raw.access as { type: string }).type } : { status: "restricted" }
    const can = object(raw.can) ? raw.can : {}
    const pageId = typeof raw.page_id === "string" && OBJECT_ID.test(raw.page_id) ? raw.page_id : null
    const checkpointId = saved && text(saved.checkpoint_id, 256) ? saved.checkpoint_id as string : null
    const target: LinkedViewerTarget | null = !readable || !saved || !pageId ? null
      : checkpointId ? { workspaceId, pageId, mode: "revision", revision: saved.revision as number, checkpointId }
        : { workspaceId, pageId, mode: "latest" }
    entries.push({
      title: readable ? (raw.access as { title: string }).title : null,
      target,
      model: {
        id: raw.id, runState: raw.run_state as WorkRunState, saved: saved ? { kind: "saved", revision: saved.revision as number } : { kind: "none" },
        freshness, problem, load: "ready", access,
        executor: text(raw.executor, 128) ? raw.executor : "Agent",
        requester: currentUserId && raw.on_behalf_of_user_id === currentUserId ? "you" : "collaborator",
        // No compare surface is mounted yet, so it is never offered.
        can: { cancel: can.cancel === true, retry: can.retry === true, compare: false, open: can.open === true },
      },
    })
  }
  return entries
}

export const fetchWorkCards = (workspaceId: string, chatId: string, currentUserId: string | null | undefined, signal?: AbortSignal) =>
  call(chatPath(chatId, "work-cards", { workspace_id: workspaceId }), body => parseWorkCards(body, workspaceId, currentUserId), { signal })

/** FE-REQ-F2-2: the server reuses the original id and verifies the outcome before any new run. */
export type WorkCardCommand = "cancel" | "retry" | "check-result"
export const postWorkCardCommand = (workspaceId: string, chatId: string, cardId: string, command: WorkCardCommand) =>
  call(chatPath(chatId, `work-cards/${encodeURIComponent(cardId)}/${command}`, { workspace_id: workspaceId }), () => true, { method: "POST", itemLevel: true })

/* ---- linked outputs (FE-REQ-F2-4) ---- */

export function parseLinks(body: unknown, workspaceId: string): ConversationLink[] | null {
  if (!object(body) || !Array.isArray(body.links) || body.links.length > 200) return null
  const links: ConversationLink[] = []
  for (const raw of body.links) {
    if (!object(raw) || raw.accessible !== true || typeof raw.page_id !== "string" || !OBJECT_ID.test(raw.page_id) || !text(raw.name)) continue
    const pinned = Number.isSafeInteger(raw.revision) && text(raw.checkpoint_id, 256)
    links.push({
      title: raw.name, accessible: true,
      target: pinned ? { workspaceId, pageId: raw.page_id, mode: "revision", revision: raw.revision as number, checkpointId: raw.checkpoint_id as string }
        : { workspaceId, pageId: raw.page_id, mode: "latest" },
    })
  }
  return links
}

export const fetchLinks = (workspaceId: string, chatId: string, signal?: AbortSignal) =>
  call(chatPath(chatId, "links", { workspace_id: workspaceId }), body => parseLinks(body, workspaceId), { signal })

/* ---- library (FE-REQ-F2-6) ---- */

const KINDS: LibraryKind[] = ["files", "artifacts", "tools", "work"]
const WIRE_KIND: Record<string, LibraryKind> = { file: "files", artifact: "artifacts", tool: "tools", work: "work" }

export interface LibraryRow { item: LibraryItem; target: LinkedViewerTarget | null }

export function parseLibrary(body: unknown, workspaceId: string): LibraryRow[] | null {
  if (!object(body) || !Array.isArray(body.items) || body.items.length > 500) return null
  const rows: LibraryRow[] = []
  for (const raw of body.items) {
    if (!object(raw) || !text(raw.link_id, 128)) continue
    const kind = typeof raw.kind === "string" ? WIRE_KIND[raw.kind] ?? (KINDS.includes(raw.kind as LibraryKind) ? raw.kind as LibraryKind : null) : null
    // A restricted row, or one without a name or kind, carries nothing but its id.
    if (raw.restricted === true || !kind || !text(raw.name)) { rows.push({ item: { status: "restricted", linkId: raw.link_id }, target: null }); continue }
    const pageId = typeof raw.page_id === "string" && OBJECT_ID.test(raw.page_id) ? raw.page_id : null
    const pinned = Number.isSafeInteger(raw.revision) && text(raw.checkpoint_id, 256)
    rows.push({
      item: { status: "readable", linkId: raw.link_id, kind, name: raw.name, revisionKey: `${pageId ?? raw.link_id}:${pinned ? raw.revision : "latest"}` },
      target: !pageId ? null : pinned ? { workspaceId, pageId, mode: "revision", revision: raw.revision as number, checkpointId: raw.checkpoint_id as string } : { workspaceId, pageId, mode: "latest" },
    })
  }
  return rows
}

export const fetchLibrary = (workspaceId: string, chatId: string, signal?: AbortSignal) =>
  call(chatPath(chatId, "library", { workspace_id: workspaceId }), body => parseLibrary(body, workspaceId), { signal })

/* ---- composer preview (FE-REQ-F2-5) ---- */

export interface ArtifactPreview {
  create: { audience: Audience; resultType: string }
  update: { audience: Audience; resultType: string; expected: ExpectedState | null } | null
}

function parseAudience(value: unknown): Audience {
  if (!object(value)) return { kind: "unknown" }
  if (value.kind === "personal") return { kind: "personal" }
  if (value.kind === "folder" && text(value.name, 256) && Number.isSafeInteger(value.people) && (value.people as number) >= 0) return { kind: "folder", name: value.name, people: value.people as number }
  return { kind: "unknown" }
}
function parseExpected(value: unknown): ExpectedState | null {
  return object(value) && Number.isSafeInteger(value.schema) && Number.isSafeInteger(value.rev) && Number.isSafeInteger(value.generation)
    ? { schema: value.schema as number, rev: value.rev as number, generation: value.generation as number } : null
}

export function parsePreview(body: unknown): ArtifactPreview | null {
  if (!object(body) || !object(body.create)) return null
  const update = object(body.update) ? { audience: parseAudience(body.update.audience), resultType: text(body.update.result_type, 64) ? body.update.result_type as string : "Document", expected: parseExpected(body.update.expected) } : null
  return { create: { audience: parseAudience(body.create.audience), resultType: text(body.create.result_type, 64) ? body.create.result_type as string : "Document" }, update }
}

export const fetchArtifactPreview = (workspaceId: string, chatId: string, pageId: string, signal?: AbortSignal) =>
  call(chatPath(chatId, "artifact-context/preview", { workspace_id: workspaceId, page_id: pageId }), parsePreview, { signal, itemLevel: true })

/* ---- exact revision (FE-REQ-F2-3-UPDATE) ---- */

/** Reads through the checkpoint reader only (the path already carries the workspace); the document endpoint ignores a revision query. */
export const checkpointFetcher: CheckpointFetcher = async (path, signal) => {
  const response = await apiFetch(path, { signal, cache: "no-store" })
  return { status: response.status, body: response.status === 200 ? await response.json().catch(() => null) : null }
}
