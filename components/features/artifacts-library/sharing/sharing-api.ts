"use client"

import { useMemo } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { apiErrorMessage } from "@/lib/api-error"
import { artifactsSharingMock } from "@/lib/feature-flags"
import type { LibraryItem } from "../library-items"

/*
 * Sharing for the Artifacts library, by the owner's decision of 28 October:
 * three levels, Read < Comment < Edit, workspace only, no expiry, no guests.
 * Test items are a server flag, archive never deletes, and every bulk change
 * is shown as a dry run before it is applied.
 *
 *   GET/PUT/DELETE /api/v1/pages/{slug}/grants    (level adds `comment`)
 *   GET            /api/v1/pages/{slug}/access    effective access and why
 *   GET/PATCH      /api/v1/page-folders/{slug}    `default_audience`
 *   GET/PUT/DELETE /api/v1/page-folders/{slug}/acl (entry adds `level`)
 *   POST           /api/v1/pages/bulk             archive|unarchive|move|share, dry_run
 *
 * Until the server serves the new routes, `artifactsSharingMock()` swaps in
 * an in-memory implementation of the same interface, so the screens are built
 * against the contract and not against the mock.
 */

export const SHARE_LEVELS = ["read", "comment", "write"] as const
export type ShareLevel = (typeof SHARE_LEVELS)[number]
export type ShareSubjectType = "user" | "crew" | "workspace"
export type AccessVia = "owner" | "grant" | "folder" | "role"

export const SHARE_LEVEL_LABEL: Record<ShareLevel, string> = { read: "Read", comment: "Comment", write: "Edit" }
export const SHARE_LEVEL_MEANING: Record<ShareLevel, string> = {
  read: "Opens the shared version.",
  comment: "Opens the shared version and leaves comments for the agent. Cannot change content, versions or sharing.",
  write: "Opens and edits, publishes versions and restores older ones.",
}
export const levelRank = (level: ShareLevel) => SHARE_LEVELS.indexOf(level)

export interface AccessEntry {
  subjectType: ShareSubjectType
  /** Empty for the workspace. */
  subjectId: string
  label: string
  level: ShareLevel
  via: AccessVia
  folderSlug?: string
  folderName?: string
}

export type DefaultAudience = "private" | "folder"
export interface FolderSettings {
  slug: string
  defaultAudience: DefaultAudience
  /** Who the folder itself shares with. */
  entries: AccessEntry[]
}

export type BulkAction = "archive" | "unarchive" | "move" | "share"
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
}
export interface BulkItemResult { slug: string; ok: boolean; reason?: string; gains: AccessEntry[]; loses: AccessEntry[] }
export interface BulkResult { dryRun: boolean; items: BulkItemResult[] }

export interface ShareInput { subjectType: ShareSubjectType; subjectId: string; label: string; level: ShareLevel }

/** Archive and test state the list needs before the page fields arrive. */
export interface LibraryState { archived: ReadonlySet<string>; test: ReadonlySet<string>; moved?: ReadonlyMap<string, { slug: string; name: string } | null> }

export interface SharingApi {
  mock: boolean
  access(item: LibraryItem): Promise<AccessEntry[]>
  share(item: LibraryItem, input: ShareInput): Promise<void>
  revoke(item: LibraryItem, subjectType: ShareSubjectType, subjectId: string): Promise<void>
  folderSettings(slug: string): Promise<FolderSettings>
  saveFolderSettings(slug: string, settings: Pick<FolderSettings, "defaultAudience" | "entries">): Promise<void>
  bulk(request: BulkRequest): Promise<BulkResult>
  libraryState(items: LibraryItem[]): LibraryState
}

export const BULK_LIMIT = 200
export const entryKey = (entry: Pick<AccessEntry, "subjectType" | "subjectId">) => `${entry.subjectType}:${entry.subjectId}`

// ── HTTP, exactly as agreed ────────────────────────────────────────────────

interface WireAccessEntry { subject_type?: string; subject_id?: string; label?: string; level?: string; via?: string; folder_slug?: string; folder_name?: string; can_write?: boolean }

/** An unknown `via` reads as a role: shown, never offered for removal. */
function toEntry(raw: WireAccessEntry, via: AccessVia = "role"): AccessEntry | null {
  const type = raw.subject_type
  if (type !== "user" && type !== "crew" && type !== "workspace") return null
  const level: ShareLevel = (SHARE_LEVELS as readonly string[]).includes(raw.level ?? "") ? raw.level as ShareLevel : raw.can_write ? "write" : "read"
  const known = raw.via === "owner" || raw.via === "grant" || raw.via === "folder" || raw.via === "role"
  return {
    subjectType: type, subjectId: type === "workspace" ? "" : raw.subject_id ?? "",
    label: type === "workspace" ? "Everyone in this workspace" : raw.label || raw.subject_id || "",
    level, via: known ? raw.via as AccessVia : via,
    ...(raw.folder_slug ? { folderSlug: raw.folder_slug } : {}), ...(raw.folder_name ? { folderName: raw.folder_name } : {}),
  }
}
const entries = (rows: unknown, via?: AccessVia) => Array.isArray(rows) ? rows.map(row => toEntry(row as WireAccessEntry, via)).filter((e): e is AccessEntry => e !== null) : []

/** The server's sentence with its status, so a 403 is told apart from a failure. */
export class SharingError extends Error {
  constructor(message: string, readonly status: number) { super(message) }
}

async function call(path: string, init?: RequestInit): Promise<unknown> {
  const res = await apiFetch(path, init)
  const text = await res.text()
  let body: unknown = null
  try { body = text ? JSON.parse(text) : null } catch { body = { detail: text } }
  if (!res.ok) throw new SharingError(apiErrorMessage(body, `Request failed (${res.status})`), res.status)
  return body
}
const JSON_INIT = (method: string, body: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })

export function httpSharingApi(workspaceId: string): SharingApi {
  const ws = `workspace_id=${encodeURIComponent(workspaceId)}`
  const page = (slug: string) => `/api/v1/pages/${encodeURIComponent(slug)}`
  const folder = (slug: string) => `/api/v1/page-folders/${encodeURIComponent(slug)}`
  return {
    mock: false,
    async access(item) {
      const body = await call(`${page(item.slug)}/access?${ws}`) as { entries?: unknown }
      return entries(body?.entries)
    },
    async share(item, input) {
      await call(`${page(item.slug)}/grants?${ws}`, JSON_INIT("PUT", { subject_type: input.subjectType, ...(input.subjectType === "workspace" ? {} : { subject_id: input.subjectId }), level: input.level }))
    },
    async revoke(item, subjectType, subjectId) {
      await call(`${page(item.slug)}/grants?${ws}`, JSON_INIT("DELETE", { subject_type: subjectType, ...(subjectType === "workspace" ? {} : { subject_id: subjectId }) }))
    },
    async folderSettings(slug) {
      const [meta, acl] = await Promise.all([call(`${folder(slug)}?${ws}`), call(`${folder(slug)}/acl?${ws}`)]) as [{ default_audience?: string } | null, { acl?: unknown } | null]
      return { slug, defaultAudience: meta?.default_audience === "private" ? "private" : "folder", entries: entries(acl?.acl, "folder") }
    },
    async saveFolderSettings(slug, settings) {
      await call(`${folder(slug)}?${ws}`, JSON_INIT("PATCH", { default_audience: settings.defaultAudience }))
      const current = entries(((await call(`${folder(slug)}/acl?${ws}`)) as { acl?: unknown } | null)?.acl, "folder")
      const wanted = new Map(settings.entries.map(e => [entryKey(e), e]))
      for (const entry of current) {
        if (wanted.has(entryKey(entry))) continue
        await call(`${folder(slug)}/acl/${entry.subjectType}${entry.subjectType === "workspace" ? "" : `/${encodeURIComponent(entry.subjectId)}`}?${ws}`, { method: "DELETE" })
      }
      for (const entry of settings.entries) {
        await call(`${folder(slug)}/acl?${ws}`, JSON_INIT("PUT", { subject_type: entry.subjectType, ...(entry.subjectType === "workspace" ? {} : { subject_id: entry.subjectId }), level: entry.level, can_write: entry.level === "write" }))
      }
    },
    async bulk(request) {
      const body = await call(`/api/v1/pages/bulk?${ws}`, JSON_INIT("POST", {
        action: request.action, slugs: request.items.map(item => item.slug), dry_run: request.dryRun,
        ...(request.action === "move" ? { folder: request.folder?.slug ?? null } : {}),
        ...(request.action === "share" ? { subject_type: request.subjectType, subject_id: request.subjectId, level: request.level } : {}),
      })) as { dry_run?: boolean; items?: { slug: string; ok?: boolean; reason?: string; gains?: unknown; loses?: unknown }[] }
      return {
        dryRun: typeof body?.dry_run === "boolean" ? body.dry_run : request.dryRun,
        items: (body?.items ?? []).map(row => ({ slug: row.slug, ok: row.ok === true, ...(row.reason ? { reason: row.reason } : {}), gains: entries(row.gains), loses: entries(row.loses) })),
      }
    },
    libraryState(items) {
      // The page fields `archived` and `origin` ride on the list once served; until then nothing is either.
      void items
      return { archived: new Set(), test: new Set() }
    },
  }
}

// ── The mock: same interface, held in this browser ─────────────────────────

/** What QA and CLI probes would mark with `origin: "test"`; the mock stands in for that flag. */
const TEST_NAME = /\b(?:test|tests|e2e|qa|probe|smoke|fixture|playwright)\b/i
const EVERYONE = "Everyone in this workspace"

interface MockStore {
  grants: Record<string, AccessEntry[]>
  folders: Record<string, { defaultAudience: DefaultAudience; entries: AccessEntry[] }>
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
        archived: new Set(store().archived),
        test: new Set(items.filter(item => TEST_NAME.test(item.name)).map(item => item.slug)),
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

export const sharingKeys = {
  all: (workspaceId: string) => ["artifact-sharing", workspaceId] as const,
  access: (workspaceId: string, slug: string) => ["artifact-sharing", workspaceId, "access", slug] as const,
  folder: (workspaceId: string, slug: string) => ["artifact-sharing", workspaceId, "folder", slug] as const,
  state: (workspaceId: string) => ["artifact-sharing", workspaceId, "state"] as const,
}

export function useSharingApi(workspaceId: string): SharingApi {
  return useMemo(() => artifactsSharingMock() ? mockSharingApi(workspaceId) : httpSharingApi(workspaceId), [workspaceId])
}

export function useAccess(workspaceId: string, item: LibraryItem | null) {
  const api = useSharingApi(workspaceId)
  return useQuery({ queryKey: sharingKeys.access(workspaceId, item?.slug ?? ""), queryFn: () => api.access(item!), enabled: Boolean(workspaceId && item), retry: false })
}

export function useFolderSettings(workspaceId: string, slug: string | null) {
  const api = useSharingApi(workspaceId)
  return useQuery({ queryKey: sharingKeys.folder(workspaceId, slug ?? ""), queryFn: () => api.folderSettings(slug!), enabled: Boolean(workspaceId && slug), retry: false })
}

/** Every write settles by re-reading sharing state and the page list. */
export function useSharingMutation<T, R>(workspaceId: string, run: (api: SharingApi, input: T) => Promise<R>) {
  const api = useSharingApi(workspaceId)
  const qc = useQueryClient()
  return useMutation<R, Error, T>({
    retry: false,
    mutationFn: input => run(api, input),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: sharingKeys.all(workspaceId) })
      if (!api.mock) void qc.invalidateQueries({ queryKey: ["pages", workspaceId] })
    },
  })
}
