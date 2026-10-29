"use client"

import { useEffect, useMemo, useRef } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { apiErrorMessage } from "@/lib/api-error"
import { artifactsSharingMock } from "@/lib/feature-flags"
import { useSessionSafe } from "@/hooks/use-auth"
import type { LibraryItem } from "../library-items"

/*
 * Sharing for the Artifacts library, by the owner's decision of 28 October:
 * three levels, Read < Comment < Edit, workspace only, no expiry, no guests.
 * Test items are a server flag, archive never deletes, and every bulk change
 * is shown as a dry run before it is applied.
 *
 * The HTTP client speaks the routes #4444 serves, and only those:
 *
 *   GET            /api/v1/pages/{slug}/access        subjects[].sources: every path, with its permissions
 *   GET/PUT/DELETE /api/v1/pages/{slug}/grants        one row per (subject, level); DELETE by query
 *   GET/PUT        /api/v1/page-folders/{slug}/acl    can_write: Read or Edit, no Comment, no default audience
 *   DELETE         /api/v1/page-folders/{slug}/acl/{type}/{id}   the workspace spelled `workspace/workspace`
 *   POST           /api/v1/pages/{slug}/archive|unarchive        dry_run → review_token → confirm
 *   POST           /api/v1/page-folders/{slug}/pages             the same review, with before/after access
 *   DELETE         /api/v1/page-folders/{slug}/pages/{page}      to Unfiled; the server does not preview it
 *
 * There is no `/pages/bulk`: a bulk action is that page's own reviewed route,
 * once per item. What the server does not offer (a folder's default audience,
 * Comment on a folder, an atomic folder save, a preview of leaving a folder)
 * is reported through `capabilities`, and the screens say so instead of
 * sending writes the server would refuse or ignore.
 *
 * `artifactsSharingMock()` swaps in an in-memory implementation of the same
 * interface, which still shows the agreed 19:40 extras for the demo.
 */

export const SHARE_LEVELS = ["read", "comment", "write"] as const
export type ShareLevel = (typeof SHARE_LEVELS)[number]
export type ShareSubjectType = "user" | "crew" | "workspace"
/** Agents appear in access lists (their grants are made with the CLI), never in the Share picker. */
export type AccessSubjectType = ShareSubjectType | "agent"
export type AccessVia = "owner" | "grant" | "folder" | "role" | "crew"

export const SHARE_LEVEL_LABEL: Record<ShareLevel, string> = { read: "Read", comment: "Comment", write: "Edit" }
export const SHARE_LEVEL_MEANING: Record<ShareLevel, string> = {
  read: "Opens the shared version.",
  comment: "Opens the shared version and leaves comments for the agent. Cannot change content, versions or sharing.",
  write: "Opens and edits, publishes versions and restores older ones.",
}
export const levelRank = (level: ShareLevel) => SHARE_LEVELS.indexOf(level)

export interface AccessEntry {
  subjectType: AccessSubjectType
  /** Empty for the workspace. */
  subjectId: string
  label: string
  level: ShareLevel
  via: AccessVia
  folderSlug?: string
  folderName?: string
  /** The crew whose membership opens it (via `crew`). */
  crew?: string
  /** A grant the server keeps but does not honour today, and why. */
  inactive?: string
}

export type DefaultAudience = "private" | "folder"
export interface FolderSettings {
  slug: string
  /** Null where the server has no default audience for new artifacts. */
  defaultAudience: DefaultAudience | null
  /** Who the folder itself shares with. */
  entries: AccessEntry[]
}

export type BulkAction = "archive" | "unarchive" | "move" | "share"
/** What a confirmation needs from its preview; opaque to the screens. */
export interface BulkReview { token?: string; pagesVersion?: number; aclVersion?: number; from?: string }
export interface BulkRequest {
  action: BulkAction
  items: LibraryItem[]
  /** Move target; null is Unfiled. */
  folder?: { slug: string; name: string } | null
  subjectType?: ShareSubjectType
  subjectId?: string
  subjectLabel?: string
  level?: ShareLevel
  dryRun: boolean
  /** The preview rows being confirmed; an item without one is not applied. */
  reviewed?: BulkItemResult[]
}
export interface BulkItemResult {
  slug: string
  ok: boolean
  reason?: string
  gains: AccessEntry[]
  loses: AccessEntry[]
  /** What the preview could not show, said plainly. */
  note?: string
  review?: BulkReview
}
export interface BulkResult { dryRun: boolean; items: BulkItemResult[] }

export interface ShareInput { subjectType: ShareSubjectType | "agent"; subjectId: string; label: string; level: ShareLevel }

/** Archive and test state the list needs before the page fields arrive. */
export interface LibraryState { archived: ReadonlySet<string>; test: ReadonlySet<string>; moved?: ReadonlyMap<string, { slug: string; name: string } | null> }

/** What this implementation can do; the screens hide the rest. */
export interface SharingCapabilities {
  folderDefaultAudience: boolean
  /** The levels a folder entry can carry. */
  folderLevels: readonly ShareLevel[]
  /** One request for the whole folder; otherwise a failure can leave part saved. */
  atomicFolderSave: boolean
}

export interface SharingApi {
  mock: boolean
  capabilities: SharingCapabilities
  access(item: LibraryItem, signal?: AbortSignal): Promise<AccessEntry[]>
  share(item: LibraryItem, input: ShareInput): Promise<void>
  revoke(item: LibraryItem, subjectType: AccessSubjectType, subjectId: string): Promise<void>
  folderSettings(slug: string, signal?: AbortSignal): Promise<FolderSettings>
  saveFolderSettings(slug: string, settings: Pick<FolderSettings, "defaultAudience" | "entries">): Promise<void>
  bulk(request: BulkRequest): Promise<BulkResult>
  libraryState(items: LibraryItem[]): LibraryState
}

export const BULK_LIMIT = 200
export const entryKey = (entry: Pick<AccessEntry, "subjectType" | "subjectId">) => `${entry.subjectType}:${entry.subjectId}`
const EVERYONE = "Everyone in this workspace"
const WITHHELD = "A crew you cannot see"

// ── HTTP, as #4444 serves it ───────────────────────────────────────────────

/** The server's sentence with its status, so a 403 is told apart from a failure. */
export class SharingError extends Error {
  constructor(message: string, readonly status: number) { super(message) }
}

async function send(path: string, init?: RequestInit): Promise<unknown> {
  const res = await apiFetch(path, init)
  const text = await res.text()
  let body: unknown = null
  try { body = text ? JSON.parse(text) : null } catch { body = { detail: text } }
  if (!res.ok) throw new SharingError(apiErrorMessage(body, `Request failed (${res.status})`), res.status)
  return body
}
const JSON_INIT = (method: string, body: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
const unreadable = (what: string) => new SharingError(`The server's ${what} could not be read; nothing is shown rather than a guess.`, 502)
const isRecord = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const str = (value: unknown) => typeof value === "string" ? value : ""

interface WirePermissions { read?: boolean; comment?: boolean; edit?: boolean }
/** Edit, Comment and Read are the human ladder; produce is not on it. */
function permissionLevel(p: unknown): ShareLevel | null {
  if (!isRecord(p)) return null
  const perms = p as WirePermissions
  return perms.edit ? "write" : perms.comment ? "comment" : perms.read ? "read" : null
}
const HUMAN_LEVEL = (level: string): level is ShareLevel => (SHARE_LEVELS as readonly string[]).includes(level)
const SUBJECT_TYPES = ["user", "crew", "workspace", "agent"] as const
const subjectType = (value: unknown): AccessSubjectType | null => (SUBJECT_TYPES as readonly unknown[]).includes(value) ? value as AccessSubjectType : null

interface WireGrant { subject_type: AccessSubjectType; subject: string; subject_id: string; level: ShareLevel; live: boolean; inert_reason?: string }

/** The direct grants: the only rows Remove and a level change act on (Codex 1, 20:16). */
function grantRows(body: unknown): WireGrant[] {
  if (!isRecord(body) || !Array.isArray(body.grants)) throw unreadable("grant list")
  return body.grants.flatMap(raw => {
    if (!isRecord(raw)) return []
    const type = subjectType(raw.subject_type)
    const level = str(raw.level)
    if (!type || !HUMAN_LEVEL(level)) return []
    return [{ subject_type: type, subject: str(raw.subject), subject_id: type === "workspace" ? "" : str(raw.subject_id), level, live: raw.live !== false, ...(str(raw.inert_reason) ? { inert_reason: str(raw.inert_reason) } : {}) }]
  })
}

/** One row per path that is not a grant: owner, role, owning crew, folder. */
function accessRows(subjects: unknown[], folder: LibraryItem["folder"]): { rows: AccessEntry[]; labels: Map<string, string> } {
  const rows: AccessEntry[] = []
  const labels = new Map<string, string>()
  for (const raw of subjects) {
    if (!isRecord(raw)) continue
    const type = subjectType(raw.subject_type)
    const id = str(raw.subject_id)
    if (!type || type === "workspace") continue
    const withheld = id === "withheld"
    const label = withheld ? WITHHELD : str(raw.label) || id
    labels.set(`${type}:${id}`, label)
    if (type === "agent" || !Array.isArray(raw.sources)) continue
    for (const source of raw.sources) {
      if (!isRecord(source)) continue
      const path = str(source.path)
      const level = permissionLevel(source.permissions)
      if (!level || path.startsWith("grant:")) continue
      const base = { subjectType: type, subjectId: id, label, level }
      if (path === "owner") rows.push({ ...base, via: "owner" })
      else if (path.startsWith("folder:")) {
        const slug = path.slice("folder:".length)
        rows.push({ ...base, via: "folder", folderSlug: slug, ...(folder?.slug === slug ? { folderName: folder.name } : {}) })
      } else if (path.startsWith("crew:") || path.startsWith("panel_crew:")) rows.push({ ...base, via: "crew", crew: path.slice(path.indexOf(":") + 1) })
      else rows.push({ ...base, via: "role" })
    }
  }
  return { rows, labels }
}

/** One row per subject that holds a grant: its strongest live level, or the inert one and why. */
function directGrants(grants: WireGrant[], labels: Map<string, string>): AccessEntry[] {
  const best = new Map<string, WireGrant>()
  const better = (a: WireGrant, b: WireGrant) => a.live !== b.live ? a.live : levelRank(a.level) > levelRank(b.level)
  for (const g of grants) {
    const key = `${g.subject_type}:${g.subject_id}`
    const held = best.get(key)
    if (!held || better(g, held)) best.set(key, g)
  }
  return [...best.entries()].map(([key, g]) => ({
    subjectType: g.subject_type, subjectId: g.subject_id, level: g.level, via: "grant" as const,
    label: g.subject_type === "workspace" ? EVERYONE : labels.get(key) ?? (g.subject || g.subject_id),
    ...(g.live ? {} : { inactive: g.inert_reason || "Not in effect" }),
  }))
}

/** Runs one request per item, a few at a time, in the items' order. */
async function each<T, R>(list: T[], run: (value: T) => Promise<R>, width = 4): Promise<R[]> {
  const out = new Array<R>(list.length)
  let next = 0
  await Promise.all(Array.from({ length: Math.min(width, list.length) }, async () => {
    while (next < list.length) { const i = next++; out[i] = await run(list[i]) }
  }))
  return out
}
const reasonOf = (error: unknown) => error instanceof Error && error.message ? error.message : "Not allowed."
const refuse = (slug: string, reason: string): BulkItemResult => ({ slug, ok: false, reason, gains: [], loses: [] })

export const HTTP_CAPABILITIES: SharingCapabilities = { folderDefaultAudience: false, folderLevels: ["read", "write"], atomicFolderSave: false }

/**
 * Checked before every request of one operation. It throws once the
 * operation is retired (another session, scope or an unmounted caller), so
 * an ACL change started by one person never sends its next request as anyone else.
 */
export type RequestGuard = () => void

export function httpSharingApi(workspaceId: string, guard?: RequestGuard, operation?: AbortSignal): SharingApi {
  // The operation's signal rides every request, so apiFetch's own retry after
  // a token refresh is cancelled too once the operation is retired.
  const call = (path: string, init?: RequestInit) => { guard?.(); return send(path, { ...init, signal: init?.signal ?? operation }) }
  const ws = `workspace_id=${encodeURIComponent(workspaceId)}`
  const page = (slug: string) => `/api/v1/pages/${encodeURIComponent(slug)}`
  const folder = (slug: string) => `/api/v1/page-folders/${encodeURIComponent(slug)}`
  const grantQuery = (type: AccessSubjectType, id: string, level?: ShareLevel) =>
    `${ws}&subject_type=${type}${type === "workspace" ? "" : `&subject=${encodeURIComponent(id)}`}${level ? `&level=${level}` : ""}`
  const readGrants = async (slug: string, signal?: AbortSignal) => grantRows(await call(`${page(slug)}/grants?${ws}`, { signal }))
  const heldLevels = (grants: WireGrant[], type: AccessSubjectType, id: string) =>
    grants.filter(g => g.subject_type === type && (type === "workspace" || g.subject_id === id)).map(g => g.level)

  async function access(item: LibraryItem, signal?: AbortSignal): Promise<AccessEntry[]> {
    const subjects: unknown[] = []
    const readSubjects = async () => {
      let cursor = ""
      for (let pages = 0; pages < 20; pages++) {
        const body = await call(`${page(item.slug)}/access?${ws}&limit=1000${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`, { signal })
        if (!isRecord(body) || !Array.isArray(body.subjects)) throw unreadable("access list")
        subjects.push(...body.subjects)
        cursor = str(body.next_cursor)
        if (!cursor) return
      }
      throw unreadable("access list (too many pages)")
    }
    const [, grants] = await Promise.all([readSubjects(), readGrants(item.slug, signal)])
    const { rows, labels } = accessRows(subjects, item.folder)
    return [...rows, ...directGrants(grants, labels)]
  }

  async function readFolder(slug: string, signal?: AbortSignal): Promise<FolderSettings> {
    const body = await call(`${folder(slug)}/acl?${ws}`, { signal })
    if (!isRecord(body) || !Array.isArray(body.acl)) throw unreadable("folder permissions")
    const entries = body.acl.flatMap((raw): AccessEntry[] => {
      if (!isRecord(raw)) return []
      const type = subjectType(raw.subject_type)
      if (!type || type === "agent") return []
      const id = type === "workspace" ? "" : str(raw.subject_id)
      return [{ subjectType: type, subjectId: id, label: type === "workspace" ? EVERYONE : str(raw.label) || id, level: raw.can_write === true ? "write" : "read", via: "folder" }]
    })
    return { slug, defaultAudience: null, entries }
  }

  const put = (slug: string, type: AccessSubjectType, id: string, level: ShareLevel) =>
    call(`${page(slug)}/grants?${ws}`, JSON_INIT("PUT", { subject_type: type, ...(type === "workspace" ? {} : { subject: id }), level }))
  const remove = (slug: string, type: AccessSubjectType, id: string, level: ShareLevel) =>
    call(`${page(slug)}/grants?${grantQuery(type, id, level)}`, { method: "DELETE" })

  return {
    mock: false,
    capabilities: HTTP_CAPABILITIES,
    access,
    async share(item, input) {
      // A grant row is keyed by level, so a change is: add the new level, then take the others away.
      const held = heldLevels(await readGrants(item.slug), input.subjectType, input.subjectId)
      await put(item.slug, input.subjectType, input.subjectId, input.level)
      for (const level of held) {
        if (level === input.level) continue
        try { await remove(item.slug, input.subjectType, input.subjectId, level) } catch (error) {
          throw new SharingError(`${SHARE_LEVEL_LABEL[input.level]} was added, but ${input.label} still has ${SHARE_LEVEL_LABEL[level]}: ${reasonOf(error)}`, error instanceof SharingError ? error.status : 500)
        }
      }
    },
    async revoke(item, type, id) {
      // Each human level by name: an agent's produce grant on the same subject is not this dialog's.
      for (const level of new Set(heldLevels(await readGrants(item.slug), type, id))) await remove(item.slug, type, id, level)
    },
    folderSettings: readFolder,
    async saveFolderSettings(slug, settings) {
      if (settings.defaultAudience !== null) throw new SharingError("This server has no default audience for new artifacts in a folder.", 400)
      if (settings.entries.some(e => e.level === "comment")) throw new SharingError("A folder shares Read or Edit; Comment is set on each artifact.", 400)
      const current = (await readFolder(slug)).entries
      const wanted = new Map(settings.entries.map(e => [entryKey(e), e]))
      const was = new Map(current.map(e => [entryKey(e), e]))
      let saved = 0
      try {
        for (const entry of current) {
          if (wanted.has(entryKey(entry))) continue
          await call(`${folder(slug)}/acl/${entry.subjectType}/${entry.subjectType === "workspace" ? "workspace" : encodeURIComponent(entry.subjectId)}?${ws}`, { method: "DELETE" })
          saved++
        }
        for (const entry of settings.entries) {
          if (was.get(entryKey(entry))?.level === entry.level) continue
          await call(`${folder(slug)}/acl?${ws}`, JSON_INIT("PUT", { subject_type: entry.subjectType, ...(entry.subjectType === "workspace" ? {} : { subject_id: entry.subjectId }), can_write: entry.level === "write" }))
          saved++
        }
      } catch (error) {
        if (!saved) throw error
        throw new SharingError(`${saved} ${saved === 1 ? "change was" : "changes were"} saved before this failed: ${reasonOf(error).replace(/\.?$/, ".")} Reopen the settings to see what the folder has now.`, error instanceof SharingError ? error.status : 500)
      }
    },
    async bulk(request) {
      if (request.items.length > BULK_LIMIT) throw new SharingError(`At most ${BULK_LIMIT} artifacts at once.`, 400)
      const reviewed = new Map((request.reviewed ?? []).filter(r => r.ok).map(r => [r.slug, r.review ?? {}]))
      const needsPreview = (slug: string) => refuse(slug, "Not previewed; preview again before confirming.")
      const version = (item: LibraryItem) => typeof item.pagesVersion === "number" ? item.pagesVersion : null
      const stale = (slug: string) => refuse(slug, "The list is out of date; refresh it and try again.")

      switch (request.action) {
        case "archive":
        case "unarchive": {
          const archiving = request.action === "archive"
          const items = await each(request.items, async (item): Promise<BulkItemResult> => {
            const path = `${page(item.slug)}/${request.action}?${ws}`
            try {
              if (request.dryRun) {
                const pagesVersion = version(item)
                if (pagesVersion === null) return stale(item.slug)
                const body = await call(path, JSON_INIT("POST", { dry_run: true, pages_version: pagesVersion }))
                if (!isRecord(body) || !isRecord(body.before) || typeof body.before.archived !== "boolean") throw unreadable("archive preview")
                if (body.before.archived === archiving) return refuse(item.slug, archiving ? "Already archived" : "Not archived")
                return { slug: item.slug, ok: true, gains: [], loses: [], review: { token: str(body.review_token), pagesVersion } }
              }
              const review = reviewed.get(item.slug)
              if (!review?.token || review.pagesVersion === undefined) return needsPreview(item.slug)
              await call(path, JSON_INIT("POST", { confirm: true, review_token: review.token, pages_version: review.pagesVersion }))
              return { slug: item.slug, ok: true, gains: [], loses: [] }
            } catch (error) { return refuse(item.slug, reasonOf(error)) }
          })
          return { dryRun: request.dryRun, items }
        }
        case "move": {
          const target = request.folder ?? null
          if (!target) {
            const items = await each(request.items, async (item): Promise<BulkItemResult> => {
              const from = item.folder?.slug
              if (!from) return refuse(item.slug, "Already there")
              if (request.dryRun) {
                const pagesVersion = version(item)
                if (pagesVersion === null) return stale(item.slug)
                return { slug: item.slug, ok: true, gains: [], loses: [], note: `Leaves ${item.folder?.name ?? from}; whoever the folder gave access loses it. The server has not previewed this move.`, review: { pagesVersion, from } }
              }
              const review = reviewed.get(item.slug)
              if (review?.pagesVersion === undefined || !review.from) return needsPreview(item.slug)
              try {
                await call(`${folder(review.from)}/pages/${encodeURIComponent(item.slug)}?${ws}`, JSON_INIT("DELETE", { pages_version: review.pagesVersion }))
                return { slug: item.slug, ok: true, gains: [], loses: [] }
              } catch (error) { return refuse(item.slug, reasonOf(error)) }
            })
            return { dryRun: request.dryRun, items }
          }
          let aclVersion: number | null = null
          if (request.dryRun) {
            const meta = await call(`${folder(target.slug)}?${ws}`)
            if (!isRecord(meta) || typeof meta.acl_version !== "number") throw unreadable("folder")
            aclVersion = meta.acl_version
          }
          const items = await each(request.items, async (item): Promise<BulkItemResult> => {
            if (item.folder?.slug === target.slug) return refuse(item.slug, "Already there")
            const path = `${folder(target.slug)}/pages?${ws}`
            try {
              if (request.dryRun) {
                const pagesVersion = version(item)
                if (pagesVersion === null || aclVersion === null) return stale(item.slug)
                const body = await call(path, JSON_INIT("POST", { page: item.slug, pages_version: pagesVersion, acl_version: aclVersion, dry_run: true }))
                if (!isRecord(body) || !Array.isArray(body.changes)) throw unreadable("move preview")
                const withheld = (isRecord(body.before) && body.before.withheld === true) || (isRecord(body.after) && body.after.withheld === true)
                const { gains, loses } = impact(body.changes)
                return {
                  slug: item.slug, ok: true, gains, loses,
                  ...(withheld ? { note: "You cannot see who this folder shares with, so the change in access is not shown." } : {}),
                  review: { token: str(body.review_token), pagesVersion, aclVersion },
                }
              }
              const review = reviewed.get(item.slug)
              if (!review?.token || review.pagesVersion === undefined || review.aclVersion === undefined) return needsPreview(item.slug)
              await call(path, JSON_INIT("POST", { page: item.slug, pages_version: review.pagesVersion, acl_version: review.aclVersion, confirm: true, review_token: review.token }))
              return { slug: item.slug, ok: true, gains: [], loses: [] }
            } catch (error) { return refuse(item.slug, reasonOf(error)) }
          })
          return { dryRun: request.dryRun, items }
        }
        case "share": {
          const type = request.subjectType ?? "workspace"
          const id = type === "workspace" ? "" : request.subjectId ?? ""
          const level = request.level ?? "read"
          const entry: AccessEntry = { subjectType: type, subjectId: id, label: request.subjectLabel ?? (type === "workspace" ? EVERYONE : id), level, via: "grant" }
          const items = await each(request.items, async (item): Promise<BulkItemResult> => {
            try {
              if (request.dryRun) {
                // The real access list is the dry run: a 403 is "you cannot share it", a stronger path is "already has it".
                const held = (await access(item)).filter(e => entryKey(e) === entryKey(entry) && !e.inactive)
                if (held.some(e => levelRank(e.level) >= levelRank(level))) return refuse(item.slug, "Already has this access")
                return { slug: item.slug, ok: true, gains: [entry], loses: [] }
              }
              if (!reviewed.has(item.slug)) return needsPreview(item.slug)
              await put(item.slug, type, id, level)
              return { slug: item.slug, ok: true, gains: [entry], loses: [] }
            } catch (error) { return refuse(item.slug, reasonOf(error)) }
          })
          return { dryRun: request.dryRun, items }
        }
      }
    },
    libraryState(items) {
      // The server's own flags (`archived`, `created_by_test`), carried on each item; never the name.
      return { archived: new Set(items.filter(i => i.archived).map(i => i.slug)), test: new Set(items.filter(i => i.test).map(i => i.slug)) }
    },
  }
}

/** A reviewed move's `changes[]` as what each subject gains or loses on the human ladder. */
function impact(changes: unknown[]): { gains: AccessEntry[]; loses: AccessEntry[] } {
  const gains: AccessEntry[] = []
  const loses: AccessEntry[] = []
  for (const raw of changes) {
    if (!isRecord(raw)) continue
    const type = subjectType(raw.subject_type)
    if (!type) continue
    const id = str(raw.subject_id)
    const label = id === "withheld" ? WITHHELD : type === "workspace" ? EVERYONE : str(raw.label) || id
    const before = permissionLevel(raw.before)
    const after = permissionLevel(raw.after)
    const rank = (level: ShareLevel | null) => level ? levelRank(level) : -1
    const base = { subjectType: type, subjectId: id, label, via: "folder" as const }
    if (rank(after) > rank(before)) gains.push({ ...base, level: after! })
    else if (rank(after) < rank(before)) loses.push({ ...base, level: before! })
  }
  return { gains, loses }
}

// ── The mock: same interface, held in this browser ─────────────────────────

/** What QA and CLI probes would mark with `origin: "test"`; the mock stands in for that flag. */
const TEST_NAME = /\b(?:test|tests|e2e|qa|probe|smoke|fixture|playwright)\b/i

interface MockStore {
  grants: Record<string, AccessEntry[]>
  folders: Record<string, { defaultAudience: DefaultAudience | null; entries: AccessEntry[] }>
  archived: string[]
  moved: Record<string, { slug: string; name: string } | null>
}

const stores = new Map<string, MockStore>()
const storageKey = (workspaceId: string) => `artifacts-sharing-mock:${workspaceId}`

function storeFor(workspaceId: string): MockStore {
  let store = stores.get(workspaceId)
  if (store) return store
  store = { grants: {}, folders: {}, archived: [], moved: {} }
  try {
    const saved = window.localStorage.getItem(storageKey(workspaceId))
    if (saved) store = { ...store, ...JSON.parse(saved) as Partial<MockStore> }
  } catch { /* private window or blocked storage: start empty */ }
  stores.set(workspaceId, store)
  return store
}
function persist(workspaceId: string) {
  try { window.localStorage.setItem(storageKey(workspaceId), JSON.stringify(storeFor(workspaceId))) } catch { /* see storeFor */ }
}

/** Test seam: forget every mock workspace. */
export function resetSharingMock() { stores.clear() }

const pause = () => new Promise(resolve => setTimeout(resolve, 120))

export function mockSharingApi(workspaceId: string): SharingApi {
  const store = () => storeFor(workspaceId)
  const folderOf = (item: LibraryItem) => item.slug in store().moved ? store().moved[item.slug] : item.folder ? { slug: item.folder.slug, name: item.folder.name } : null
  const folderEntries = (target: { slug: string; name: string } | null): AccessEntry[] =>
    target ? (store().folders[target.slug]?.entries ?? []).map(e => ({ ...e, via: "folder" as const, folderSlug: target.slug, folderName: target.name })) : []
  const seeded = (item: LibraryItem): AccessEntry[] => {
    const grants = store().grants
    if (!grants[item.slug]) grants[item.slug] = item.private ? [] : [{ subjectType: "workspace", subjectId: "", label: EVERYONE, level: "read", via: "grant" }]
    return grants[item.slug]
  }
  /** Every path to the item, a direct grant beside a folder's: the Access list says each one. */
  const paths = (item: LibraryItem, target = folderOf(item)): AccessEntry[] => [
    { subjectType: "user", subjectId: "owner", label: item.ownerLabel && item.ownerLabel !== "You" ? item.ownerLabel : "You", level: "write", via: "owner" },
    ...seeded(item), ...folderEntries(target),
  ]
  /** The strongest level per subject, which is what a move gains or loses. */
  const effective = (item: LibraryItem, target = folderOf(item)): AccessEntry[] => {
    const best = new Map<string, AccessEntry>()
    for (const entry of paths(item, target)) {
      const held = best.get(entryKey(entry))
      if (!held || levelRank(entry.level) > levelRank(held.level)) best.set(entryKey(entry), entry)
    }
    return [...best.values()]
  }
  const diff = (before: AccessEntry[], after: AccessEntry[]) => {
    const was = new Map(before.map(e => [entryKey(e), e]))
    const now = new Map(after.map(e => [entryKey(e), e]))
    return {
      gains: after.filter(e => !was.has(entryKey(e)) || levelRank(e.level) > levelRank(was.get(entryKey(e))!.level)),
      loses: before.filter(e => !now.has(entryKey(e)) || levelRank(e.level) > levelRank(now.get(entryKey(e))!.level)),
    }
  }
  return {
    mock: true,
    capabilities: { folderDefaultAudience: true, folderLevels: SHARE_LEVELS, atomicFolderSave: true },
    async access(item) { await pause(); return paths(item) },
    async share(item, input) {
      await pause()
      const list = seeded(item).filter(e => entryKey(e) !== entryKey(input))
      store().grants[item.slug] = [...list, { ...input, subjectId: input.subjectType === "workspace" ? "" : input.subjectId, via: "grant" }]
      persist(workspaceId)
    },
    async revoke(item, subjectType, subjectId) {
      await pause()
      store().grants[item.slug] = seeded(item).filter(e => entryKey(e) !== entryKey({ subjectType, subjectId }))
      persist(workspaceId)
    },
    async folderSettings(slug) {
      await pause()
      const saved = store().folders[slug]
      return { slug, defaultAudience: saved?.defaultAudience ?? "folder", entries: saved?.entries ?? [] }
    },
    async saveFolderSettings(slug, settings) {
      await pause()
      store().folders[slug] = { defaultAudience: settings.defaultAudience, entries: settings.entries.map(e => ({ ...e, via: "folder" })) }
      persist(workspaceId)
    },
    async bulk(request) {
      await pause()
      if (request.items.length > BULK_LIMIT) throw new Error(`At most ${BULK_LIMIT} artifacts at once.`)
      const archived = new Set(store().archived)
      const items: BulkItemResult[] = request.items.map(item => {
        const refuse = (reason: string): BulkItemResult => ({ slug: item.slug, ok: false, reason, gains: [], loses: [] })
        const owned = item.ownerLabel === "You"
        switch (request.action) {
          case "archive":
            if (archived.has(item.slug)) return refuse("Already archived")
            if (!owned) return refuse("Only its owner can archive it")
            return { slug: item.slug, ok: true, gains: [], loses: [] }
          case "unarchive":
            return archived.has(item.slug) ? { slug: item.slug, ok: true, gains: [], loses: [] } : refuse("Not archived")
          case "move": {
            const target = request.folder ?? null
            if ((folderOf(item)?.slug ?? null) === (target?.slug ?? null)) return refuse("Already there")
            return { slug: item.slug, ok: true, ...diff(effective(item), effective(item, target)) }
          }
          case "share": {
            if (!owned) return refuse("Only its owner can share it")
            const entry: AccessEntry = { subjectType: request.subjectType ?? "workspace", subjectId: request.subjectId ?? "", label: request.subjectLabel ?? EVERYONE, level: request.level ?? "read", via: "grant" }
            const before = effective(item)
            const held = before.find(e => entryKey(e) === entryKey(entry))
            if (held && levelRank(held.level) >= levelRank(entry.level)) return refuse("Already has this access")
            return { slug: item.slug, ok: true, gains: [entry], loses: [] }
          }
        }
      })
      if (!request.dryRun) {
        for (const result of items) {
          if (!result.ok) continue
          const item = request.items.find(i => i.slug === result.slug)!
          if (request.action === "archive") archived.add(item.slug)
          if (request.action === "unarchive") archived.delete(item.slug)
          if (request.action === "move") store().moved[item.slug] = request.folder ?? null
          if (request.action === "share") {
            const entry = result.gains[0]
            store().grants[item.slug] = [...seeded(item).filter(e => entryKey(e) !== entryKey(entry)), entry]
          }
        }
        store().archived = [...archived]
        persist(workspaceId)
      }
      return { dryRun: request.dryRun, items }
    },
    libraryState(items) {
      return {
        archived: new Set([...store().archived, ...items.filter(item => item.archived).map(item => item.slug)]),
        // The server's flag first; the name stands in for it only in the demo.
        test: new Set(items.filter(item => item.test || TEST_NAME.test(item.name)).map(item => item.slug)),
        moved: new Map(Object.entries(store().moved)),
      }
    },
  }
}

/** Where a mock move put an item, so the list shows the folder it now sits in. */
export function mockFolderOverride(workspaceId: string, slug: string): { slug: string; name: string } | null | undefined {
  const moved = storeFor(workspaceId).moved
  return slug in moved ? moved[slug] : undefined
}

// ── Hooks ──────────────────────────────────────────────────────────────────

/*
 * Sharing state is private, so every read, displayed result and completion
 * is bound to the signed-in human as well as the workspace and item: a new
 * session or scope gets its own cache entry at once (the old one is never
 * shown), a superseded read is aborted, and a write that finishes after the
 * human changed reports that instead of acting on the new session's screen.
 */

export const sharingKeys = {
  all: (workspaceId: string) => ["artifact-sharing", workspaceId] as const,
  access: (workspaceId: string, slug: string, humanId = "") => ["artifact-sharing", workspaceId, "access", slug, humanId] as const,
  folder: (workspaceId: string, slug: string, humanId = "") => ["artifact-sharing", workspaceId, "folder", slug, humanId] as const,
  state: (workspaceId: string) => ["artifact-sharing", workspaceId, "state"] as const,
}

/** The signed-in human, and a ref that always holds the current one. */
export function useSharingHuman(): { humanId: string | null; current: { readonly current: string | null }; generation: { readonly current: number } } {
  const session = useSessionSafe()
  const humanId = session.status === "authenticated" ? session.data?.user.id ?? null : null
  const current = useRef(humanId)
  current.current = humanId
  // Bumped in render on every change of human, so A → B → A is a new generation too.
  const generation = useRef({ humanId, value: 0 })
  if (generation.current.humanId !== humanId) generation.current = { humanId, value: generation.current.value + 1 }
  return { humanId, current, generation: { get current() { return generation.current.value } } }
}

/** The API for one operation: its requests stop at the first one the guard refuses. */
export function guardedSharingApi(workspaceId: string, guard: RequestGuard, operation?: AbortSignal): SharingApi {
  return artifactsSharingMock() ? mockSharingApi(workspaceId) : httpSharingApi(workspaceId, guard, operation)
}

export function useSharingApi(workspaceId: string): SharingApi {
  return useMemo(() => artifactsSharingMock() ? mockSharingApi(workspaceId) : httpSharingApi(workspaceId), [workspaceId])
}

/** The server is asked only for a known human; the in-browser mock needs none. */
const canRead = (api: SharingApi, humanId: string | null) => api.mock || Boolean(humanId)

export function useAccess(workspaceId: string, item: LibraryItem | null) {
  const api = useSharingApi(workspaceId)
  const { humanId } = useSharingHuman()
  return useQuery({
    queryKey: sharingKeys.access(workspaceId, item?.slug ?? "", humanId ?? ""),
    queryFn: ({ signal }) => api.access(item!, signal),
    enabled: Boolean(workspaceId && item) && canRead(api, humanId),
    gcTime: 0, retry: false,
  })
}

export function useFolderSettings(workspaceId: string, slug: string | null) {
  const api = useSharingApi(workspaceId)
  const { humanId } = useSharingHuman()
  return useQuery({
    queryKey: sharingKeys.folder(workspaceId, slug ?? "", humanId ?? ""),
    queryFn: ({ signal }) => api.folderSettings(slug!, signal),
    enabled: Boolean(workspaceId && slug) && canRead(api, humanId),
    gcTime: 0, retry: false,
  })
}

export const SESSION_CHANGED = "Your session changed before this finished. Open it again to see where things stand."

/** Every write settles by re-reading sharing state and the page list. */
export function useSharingMutation<T, R>(workspaceId: string, run: (api: SharingApi, input: T) => Promise<R>, scope = "") {
  const api = useSharingApi(workspaceId)
  const { humanId, generation } = useSharingHuman()
  // Operations in flight. A new human, workspace or scope, or an unmounted
  // caller, aborts them: the guard refuses their next request and the abort
  // cancels the one in flight, including apiFetch's retry after a refresh.
  const running = useRef(new Set<AbortController>())
  useEffect(() => {
    const operations = running.current
    return () => { for (const operation of operations) operation.abort(); operations.clear() }
  }, [humanId, workspaceId, scope])
  // A caller that unmounts in the same tick as mutate() is gone before the
  // mutation starts: its operation is born retired and sends nothing (#4460).
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const qc = useQueryClient()
  return useMutation<R, Error, T>({
    retry: false,
    mutationFn: async input => {
      const started = generation.current
      const operation = new AbortController()
      if (!mounted.current) operation.abort()
      running.current.add(operation)
      const guard = () => { if (operation.signal.aborted || generation.current !== started) throw new SharingError(SESSION_CHANGED, 401) }
      try {
        const result = await run(api.mock ? api : guardedSharingApi(workspaceId, guard, operation.signal), input)
        guard()
        return result
      } catch (error) {
        guard()
        throw error
      } finally { running.current.delete(operation) }
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: sharingKeys.all(workspaceId) })
      if (!api.mock) void qc.invalidateQueries({ queryKey: ["pages", workspaceId] })
    },
  })
}
