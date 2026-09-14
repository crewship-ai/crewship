"use client"

/**
 * Page folders — the data half of collections analysis F1 (#2527).
 *
 * A folder is a named group of pages owned by one crew, with an icon from the
 * crew icon set and an optional colour. One level, a page in at most one
 * folder, and every page outside one is "Unfiled". Nothing here decides who
 * may see a page: the server's list already contains only what the caller
 * reaches, and a folder's `page_count` is that same reach counted, so the
 * rail can never draw a number the person cannot open.
 *
 * Two things are deliberate about the writes:
 *
 *  1. **A move carries two versions and the server refuses a stale one.**
 *     `pages_version` from the page row and `acl_version` from the target
 *     folder both ride on the request. A 409 comes back typed —
 *     `FolderFenceError` — naming which of the two moved and carrying the
 *     fresh values, the same shape `PublishFenceError` gives the review
 *     screen. The UI re-reads both and asks again; it never retries on its
 *     own, because the person consented to a move of THIS page into THIS
 *     folder as they were, not as they are now — and with folder permissions
 *     (#2533) "as they were" includes who the page becomes visible to.
 *
 *  2. **Every write invalidates both lists, on settle.** A folder change
 *     moves a page between rail sections and changes a header count, and the
 *     page list is what says where each page is. A 409 invalidates too: the
 *     whole point of the refusal is that what is on screen is behind.
 *
 *  3. **The ACL is read by whoever asks, and the server says no.** A folder's
 *     full permission list is for managers of the owning crew and admins;
 *     everyone else gets a 403 with a sentence, and `useFolderAcl` keeps the
 *     403 apart from a broken request so a surface can render the marker
 *     instead of the table. Nothing here guesses who is a manager.
 *
 * Same conventions as `use-pages.ts`: React Query, `apiFetch`,
 * `[resource, workspaceId, params?]` keys, realtime invalidation rather than
 * polling, tolerant on read.
 */

import { useCallback, useMemo } from "react"
import { useMutation, useQuery, useQueryClient, type UseMutationResult } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { apiErrorMessage } from "@/lib/api-error"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import {
  PagesRequestError,
  pagesKeys,
  type PageFolderLike,
  type WirePage,
} from "@/hooks/use-pages"
import {
  EVERYONE_LABEL,
  toFolderShared,
  type FolderAclEntry,
  type FolderAclSubjectType,
  type FolderShared,
} from "@/lib/pages/folder-sharing"

// ── The wire ───────────────────────────────────────────────────────────────

export interface WirePageFolder {
  id?: string | null
  slug?: string | null
  name?: string | null
  /** A `CREW_ICONS` name, or null for the plain folder glyph. */
  icon?: string | null
  /** A `GRADIENT_PALETTES` id or a hex, or null for no dot. */
  color?: string | null
  /** `crew/<slug>` — a folder is always owned by a crew. */
  owner?: string | null
  owner_crew_name?: string | null
  /** Pages in this folder that the CALLER reaches. */
  page_count?: number | null
  /** Bumped by every permission change on the folder; a move sends it back. */
  acl_version?: number | null
  /** Who the folder is shared with, without names (#2533). */
  shared?: string | null
  created_at?: string | null
  updated_at?: string | null
}

/** A folder as the rail and the dialogs consume it. */
export interface PageFolderView extends PageFolderLike {
  id: string
  ownerRef: string | null
  ownerLabel: string | null
  aclVersion: number | null
  shared: FolderShared
  createdAt: Date | null
  updatedAt: Date | null
}

function trimmed(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : null
}

function toDate(value: unknown): Date | null {
  const s = trimmed(value)
  if (!s) return null
  const d = new Date(s)
  return Number.isFinite(d.getTime()) ? d : null
}

function finiteOrNull(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

/** One folder. Null for a record without a slug — there is nothing to key it on. */
export function toPageFolderView(raw: WirePageFolder): PageFolderView | null {
  const slug = trimmed(raw.slug)
  if (!slug) return null
  const ownerRef = trimmed(raw.owner)
  const cut = ownerRef ? ownerRef.indexOf("/") : -1
  return {
    id: trimmed(raw.id) ?? slug,
    slug,
    name: trimmed(raw.name) ?? slug,
    icon: trimmed(raw.icon),
    color: trimmed(raw.color),
    ownerRef,
    ownerLabel:
      trimmed(raw.owner_crew_name) ?? (ownerRef ? (cut >= 0 ? ownerRef.slice(cut + 1) || ownerRef : ownerRef) : null),
    pageCount: Math.max(0, Math.trunc(finiteOrNull(raw.page_count) ?? 0)),
    aclVersion: finiteOrNull(raw.acl_version),
    shared: toFolderShared(raw.shared),
    createdAt: toDate(raw.created_at),
    updatedAt: toDate(raw.updated_at),
  }
}

/** `{folders: [...]}` is the contract; a bare array is read too. */
export function normalizeFolderList(body: unknown): WirePageFolder[] {
  if (Array.isArray(body)) return body as WirePageFolder[]
  if (body && typeof body === "object") {
    const rec = body as Record<string, unknown>
    for (const key of ["folders", "rows", "items", "data"]) {
      if (Array.isArray(rec[key])) return rec[key] as WirePageFolder[]
    }
  }
  return []
}

/** The create and update routes answer with the folder, bare or wrapped. */
export function normalizeFolder(body: unknown): WirePageFolder | null {
  if (!body || typeof body !== "object") return null
  const rec = body as Record<string, unknown>
  if (typeof rec.slug === "string") return rec as WirePageFolder
  const inner = rec.folder
  if (inner && typeof inner === "object") return inner as WirePageFolder
  return null
}

// ── The 409 ────────────────────────────────────────────────────────────────

export type FolderConflictKind = "pages_version" | "acl_version"

/** The body of a 409 from a move: which fence tripped, and the fresh values.
 *  `acl` rides along only when the caller may read it (spec §3/8, §3/10);
 *  `page` names the page a BATCH move stopped at. */
export interface FolderConflictWire {
  readonly error: string
  readonly conflict?: FolderConflictKind
  readonly pages_version?: number
  readonly acl_version?: number
  readonly acl?: FolderAclEntry[]
  readonly page?: string
}

/**
 * A move refused because a version moved, carried as a typed value rather
 * than flattened into a message. The dialog names what changed and re-reads
 * it; it cannot parse that back out of English.
 */
export class FolderFenceError extends Error {
  readonly conflict: FolderConflictWire
  constructor(conflict: FolderConflictWire) {
    super(conflict.error)
    this.name = "FolderFenceError"
    this.conflict = conflict
  }
}

/** Narrow an unknown mutation error to the typed 409 body, or null. */
export function folderConflictOf(error: unknown): FolderConflictWire | null {
  return error instanceof FolderFenceError ? error.conflict : null
}

/**
 * A batch move refused for ONE page — nothing was written (the batch is all
 * or nothing), and the server names the page it stopped at. Kept apart from
 * a plain refusal so the dialog can put the sentence beside that page.
 */
export class FolderBatchRefusal extends Error {
  readonly status: number
  readonly page: string | null
  constructor(status: number, message: string, page: string | null) {
    super(message)
    this.name = "FolderBatchRefusal"
    this.status = status
    this.page = page
  }
}

/** The page a batch refusal or a batch 409 names, or null. */
export function refusedPageOf(error: unknown): string | null {
  if (error instanceof FolderBatchRefusal) return error.page
  if (error instanceof FolderFenceError) return error.conflict.page ?? null
  return null
}

export const FOLDER_CONFLICT_SENTENCE = "The folder or the page changed; try again."

export function normalizeFolderConflict(body: unknown): FolderConflictWire {
  const raw = (body ?? {}) as Partial<FolderConflictWire>
  const conflict = raw.conflict === "pages_version" || raw.conflict === "acl_version" ? raw.conflict : undefined
  const pagesVersion = finiteOrNull(raw.pages_version)
  const aclVersion = finiteOrNull(raw.acl_version)
  const acl = Array.isArray(raw.acl) ? normalizeFolderAcl({ acl: raw.acl }).entries : null
  const page = trimmed(raw.page)
  return {
    error: trimmed(raw.error) ?? FOLDER_CONFLICT_SENTENCE,
    ...(conflict ? { conflict } : {}),
    ...(pagesVersion !== null ? { pages_version: pagesVersion } : {}),
    ...(aclVersion !== null ? { acl_version: aclVersion } : {}),
    ...(acl !== null ? { acl } : {}),
    ...(page !== null ? { page } : {}),
  }
}

// ── The ACL wire (#2533) ───────────────────────────────────────────────────

export interface WireFolderAclEntry {
  subject_type?: string | null
  subject_id?: string | null
  label?: string | null
  can_read?: boolean | null
  can_write?: boolean | null
  set_by?: string | null
  set_at?: string | null
}

export interface WireFolderAcl {
  acl?: WireFolderAclEntry[] | null
  acl_version?: number | null
}

export interface FolderAcl {
  entries: FolderAclEntry[]
  aclVersion: number | null
}

export function toFolderAclEntry(raw: WireFolderAclEntry): FolderAclEntry | null {
  const type = trimmed(raw.subject_type)
  if (type !== "user" && type !== "crew" && type !== "workspace") return null
  const subjectId = type === "workspace" ? "" : (trimmed(raw.subject_id) ?? "")
  if (type !== "workspace" && subjectId === "") return null
  return {
    subjectType: type,
    subjectId,
    label: type === "workspace" ? EVERYONE_LABEL : (trimmed(raw.label) ?? subjectId),
    // Strictly `=== true`: a right this build could not read is not granted.
    canWrite: raw.can_write === true,
    setBy: trimmed(raw.set_by),
    setAt: trimmed(raw.set_at),
  }
}

/** `{acl: [...], acl_version}` is the contract; a bare array is read too. */
export function normalizeFolderAcl(body: unknown): FolderAcl {
  const rows = Array.isArray(body)
    ? (body as WireFolderAclEntry[])
    : body && typeof body === "object" && Array.isArray((body as WireFolderAcl).acl)
      ? ((body as WireFolderAcl).acl as WireFolderAclEntry[])
      : []
  const version = body && typeof body === "object" && !Array.isArray(body) ? finiteOrNull((body as WireFolderAcl).acl_version) : null
  return {
    entries: rows.map(toFolderAclEntry).filter((e): e is FolderAclEntry => e !== null),
    aclVersion: version,
  }
}

// ── Keys and routes ────────────────────────────────────────────────────────

/** `[resource, workspaceId, params?]` — CONTRIBUTING.md. */
export const pageFoldersKeys = {
  all: (workspaceId: string) => ["page-folders", workspaceId] as const,
  list: (workspaceId: string) => ["page-folders", workspaceId, { view: "list" }] as const,
  acl: (workspaceId: string, slug: string) => ["page-folders", workspaceId, { view: "acl", slug }] as const,
  /** Under the PAGES resource: it is a fact about a page, and a move changes it. */
  accessMe: (workspaceId: string, slug: string) => ["pages", workspaceId, { view: "access-me", slug }] as const,
}

function ws(workspaceId: string): string {
  return `?${new URLSearchParams({ workspace_id: workspaceId }).toString()}`
}

const FOLDERS = "/api/v1/page-folders"

function folderRoute(workspaceId: string, slug: string): string {
  return `${FOLDERS}/${encodeURIComponent(slug)}${ws(workspaceId)}`
}

function folderPagesRoute(workspaceId: string, slug: string, page?: string): string {
  const tail = page === undefined ? "" : `/${encodeURIComponent(page)}`
  return `${FOLDERS}/${encodeURIComponent(slug)}/pages${tail}${ws(workspaceId)}`
}

function folderPagesBatchRoute(workspaceId: string, slug: string): string {
  return `${FOLDERS}/${encodeURIComponent(slug)}/pages:batch${ws(workspaceId)}`
}

function folderAclRoute(workspaceId: string, slug: string): string {
  return `${FOLDERS}/${encodeURIComponent(slug)}/acl${ws(workspaceId)}`
}

/**
 * `DELETE …/acl/{subject_type}/{subject_id}`. The workspace subject has no
 * id — its `subject_id` is the empty string in the table — and an empty path
 * segment is not a segment, so the type alone addresses it.
 */
function folderAclEntryRoute(workspaceId: string, slug: string, subjectType: FolderAclSubjectType, subjectId: string): string {
  // The server's route is `/acl/{subject_type}/{subject_id}` and a path
  // segment cannot be empty, so the workspace row is addressed as
  // `/acl/workspace/workspace` (PR #2535).
  const tail = subjectType === "workspace" ? "/workspace" : `/${encodeURIComponent(subjectId)}`
  return `${FOLDERS}/${encodeURIComponent(slug)}/acl/${subjectType}${tail}${ws(workspaceId)}`
}

function pageAccessMeRoute(workspaceId: string, slug: string): string {
  return `/api/v1/pages/${encodeURIComponent(slug)}/access/me${ws(workspaceId)}`
}

async function bodyOf(res: Response): Promise<unknown> {
  const text = await res.text().catch(() => "")
  if (!text) return null
  try {
    return JSON.parse(text) as unknown
  } catch {
    return null
  }
}

/**
 * The one place a folder write's response is judged. A 409 becomes the typed
 * fence; every other failure keeps its status and says the server's own
 * words, so a 403 can render at the control with the sentence naming who may.
 */
async function judge(res: Response, what: string): Promise<unknown> {
  const body = await bodyOf(res)
  if (res.status === 409) throw new FolderFenceError(normalizeFolderConflict(body))
  if (!res.ok) throw new PagesRequestError(res.status, apiErrorMessage(body, `${what}: ${res.status}`))
  return body
}

// ── Reading ────────────────────────────────────────────────────────────────

export interface UsePageFoldersResult {
  folders: PageFolderView[]
  loading: boolean
  error: string | null
  /**
   * False when the server answered the folder route with a 404 — an
   * installation that predates folders. The rail then groups by owner and
   * offers no folder control, rather than drawing one empty "Unfiled".
   */
  supported: boolean
  refresh: () => void
  /** Re-read both lists and wait for them — what a 409 asks for. */
  reread: () => Promise<void>
}

/** Every folder the caller may read, with the count of pages they reach in it. */
export function usePageFolders(workspaceId: string | null | undefined, enabled = true): UsePageFoldersResult {
  const qc = useQueryClient()
  const query = useQuery({
    queryKey: pageFoldersKeys.list(workspaceId ?? ""),
    queryFn: async ({ signal }) => {
      const res = await apiFetch(`${FOLDERS}${ws(workspaceId!)}`, { signal })
      if (res.status === 404) return { supported: false, folders: [] as WirePageFolder[] }
      const body = await judge(res, "folders")
      return { supported: true, folders: normalizeFolderList(body) }
    },
    enabled: Boolean(workspaceId) && enabled,
    retry: false,
  })

  const invalidate = useCallback(() => {
    if (!workspaceId) return
    void qc.invalidateQueries({ queryKey: pageFoldersKeys.all(workspaceId) })
    void qc.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
  }, [qc, workspaceId])

  // A folder event carries only a slug; the page list is what says where each
  // page now is, so both are re-read. `page.updated` too: a rename shows in
  // the rail, and a page's own `folder` rides on that row.
  useRealtimeEventSafe("page.folder.updated", invalidate)
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)

  const folders = useMemo(
    () =>
      (query.data?.folders ?? [])
        .map(toPageFolderView)
        .filter((f): f is PageFolderView => f !== null)
        .sort((a, b) => a.name.localeCompare(b.name)),
    [query.data],
  )

  const reread = useCallback(async () => {
    if (!workspaceId) return
    await Promise.all([
      qc.refetchQueries({ queryKey: pageFoldersKeys.all(workspaceId) }),
      qc.refetchQueries({ queryKey: pagesKeys.all(workspaceId) }),
    ])
  }, [qc, workspaceId])

  return {
    folders,
    loading: query.isPending && Boolean(workspaceId) && enabled,
    error: query.error ? (query.error as Error).message : null,
    supported: query.data ? query.data.supported : true,
    refresh: invalidate,
    reread,
  }
}

/** A crew as the New-folder owner picker needs it. */
export interface FolderOwnerChoice {
  id: string
  slug: string
  name: string
}

/**
 * The crews a folder may be owned by. The list is every crew in the
 * workspace, not the ones the caller is MANAGER+ in: the crews route says
 * nothing about the caller's standing in each, and a per-crew roster read is
 * a request per option. The server's 403 renders at the control instead,
 * with its sentence — refusal at the control, never hidden.
 */
export function useFolderOwnerChoices(workspaceId: string | null | undefined, enabled = true) {
  return useQuery({
    // Shared with the backup dialog's picker, which reads the same shape off
    // the same route; two keys would be two fetches for one list.
    queryKey: ["crews-lite", workspaceId ?? undefined],
    queryFn: async ({ signal }) => {
      const res = await apiFetch(`/api/v1/crews${ws(workspaceId!)}`, { signal })
      const body = await judge(res, "crews")
      const rows = Array.isArray(body)
        ? body
        : body && typeof body === "object" && Array.isArray((body as { data?: unknown }).data)
          ? ((body as { data: unknown[] }).data ?? [])
          : []
      return rows
        .map((r) => {
          const rec = (r ?? {}) as Record<string, unknown>
          const slug = trimmed(rec.slug)
          if (!slug) return null
          return { id: trimmed(rec.id) ?? slug, slug, name: trimmed(rec.name) ?? slug }
        })
        .filter((c): c is FolderOwnerChoice => c !== null)
        .sort((a, b) => a.name.localeCompare(b.name))
    },
    enabled: Boolean(workspaceId) && enabled,
    staleTime: 60_000,
    retry: false,
  })
}

// ── Writing ────────────────────────────────────────────────────────────────

export interface CreateFolderInput {
  name: string
  icon?: string | null
  color?: string | null
  /** `crew/<slug>`. */
  owner: string
}

export interface UpdateFolderInput {
  slug: string
  name?: string
  icon?: string | null
  color?: string | null
}

export interface AddPageInput {
  folder: string
  page: string
  pagesVersion: number | null
  aclVersion: number | null
}

export interface AddPagesInput {
  folder: string
  pages: Array<{ page: string; pagesVersion: number | null }>
  aclVersion: number | null
}

export interface RemovePageInput {
  folder: string
  page: string
  pagesVersion: number | null
}

export interface PageFolderMutations {
  create: UseMutationResult<PageFolderView | null, Error, CreateFolderInput>
  update: UseMutationResult<PageFolderView | null, Error, UpdateFolderInput>
  /** 204 on success; a 409 says the folder is not empty, in the server's words. */
  remove: UseMutationResult<void, Error, string>
  /** Files a page into a folder — a move, when it was in another. */
  addPage: UseMutationResult<WirePage | null, Error, AddPageInput>
  /** Files several pages at once — all or nothing; a refusal names the page. */
  addPages: UseMutationResult<WirePage[], Error, AddPagesInput>
  removePage: UseMutationResult<void, Error, RemovePageInput>
}

const JSON_HEADERS = { "Content-Type": "application/json" }

export function usePageFolderMutations(workspaceId: string | null | undefined): PageFolderMutations {
  const qc = useQueryClient()
  const settle = useCallback(() => {
    if (!workspaceId) return
    void qc.invalidateQueries({ queryKey: pageFoldersKeys.all(workspaceId) })
    void qc.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
  }, [qc, workspaceId])

  const create = useMutation<PageFolderView | null, Error, CreateFolderInput>({
    retry: false,
    mutationFn: async (input) => {
      const res = await apiFetch(`${FOLDERS}${ws(workspaceId!)}`, {
        method: "POST",
        headers: JSON_HEADERS,
        body: JSON.stringify({
          name: input.name,
          owner: input.owner,
          ...(input.icon !== undefined ? { icon: input.icon } : {}),
          ...(input.color !== undefined ? { color: input.color } : {}),
        }),
      })
      const body = await judge(res, "create folder")
      const raw = normalizeFolder(body)
      return raw ? toPageFolderView(raw) : null
    },
    onSettled: settle,
  })

  const update = useMutation<PageFolderView | null, Error, UpdateFolderInput>({
    retry: false,
    mutationFn: async ({ slug, ...patch }) => {
      const body: Record<string, unknown> = {}
      if (patch.name !== undefined) body.name = patch.name
      if (patch.icon !== undefined) body.icon = patch.icon
      if (patch.color !== undefined) body.color = patch.color
      const res = await apiFetch(folderRoute(workspaceId!, slug), {
        method: "PATCH",
        headers: JSON_HEADERS,
        body: JSON.stringify(body),
      })
      const answered = await judge(res, "update folder")
      const raw = normalizeFolder(answered)
      return raw ? toPageFolderView(raw) : null
    },
    onSettled: settle,
  })

  const remove = useMutation<void, Error, string>({
    retry: false,
    mutationFn: async (slug) => {
      const res = await apiFetch(folderRoute(workspaceId!, slug), { method: "DELETE" })
      await judge(res, "delete folder")
    },
    onSettled: settle,
  })

  const addPage = useMutation<WirePage | null, Error, AddPageInput>({
    retry: false,
    mutationFn: async (input) => {
      const res = await apiFetch(folderPagesRoute(workspaceId!, input.folder), {
        method: "POST",
        headers: JSON_HEADERS,
        body: JSON.stringify({
          page: input.page,
          pages_version: input.pagesVersion,
          acl_version: input.aclVersion,
        }),
      })
      const body = await judge(res, "move page")
      const rec = body && typeof body === "object" ? (body as { page?: unknown }).page : null
      return rec && typeof rec === "object" ? (rec as WirePage) : null
    },
    onSettled: settle,
  })

  const addPages = useMutation<WirePage[], Error, AddPagesInput>({
    retry: false,
    mutationFn: async (input) => {
      const res = await apiFetch(folderPagesBatchRoute(workspaceId!, input.folder), {
        method: "POST",
        headers: JSON_HEADERS,
        body: JSON.stringify({
          pages: input.pages.map((p) => ({ page: p.page, pages_version: p.pagesVersion })),
          acl_version: input.aclVersion,
        }),
      })
      const body = await bodyOf(res)
      if (res.status === 409) throw new FolderFenceError(normalizeFolderConflict(body))
      if (!res.ok) {
        const page = body && typeof body === "object" ? trimmed((body as { page?: unknown }).page) : null
        throw new FolderBatchRefusal(res.status, apiErrorMessage(body, `move pages: ${res.status}`), page)
      }
      const rows = body && typeof body === "object" ? (body as { pages?: unknown }).pages : null
      return Array.isArray(rows) ? (rows.filter((r) => r && typeof r === "object") as WirePage[]) : []
    },
    onSettled: settle,
  })

  const removePage = useMutation<void, Error, RemovePageInput>({
    retry: false,
    mutationFn: async (input) => {
      const res = await apiFetch(folderPagesRoute(workspaceId!, input.folder, input.page), {
        method: "DELETE",
        headers: JSON_HEADERS,
        body: JSON.stringify({ pages_version: input.pagesVersion }),
      })
      await judge(res, "remove page from folder")
    },
    onSettled: settle,
  })

  return { create, update, remove, addPage, addPages, removePage }
}

// ── The ACL (#2533) ────────────────────────────────────────────────────────

export interface UseFolderAclResult extends FolderAcl {
  loading: boolean
  /**
   * The server's sentence for a 403 — this reader may not read the ACL. Not
   * an error: it is the answer that decides which surface to draw, the table
   * or the marker.
   */
  refusal: string | null
  /** Any other failure. */
  error: string | null
  /** True once the server has said yes — the reader manages this folder. */
  manages: boolean
  /** Re-read and wait — what a 409 asks for. */
  reread: () => Promise<void>
}

/** The full permission list of one folder, for whoever the server lets read it. */
export function useFolderAcl(
  workspaceId: string | null | undefined,
  slug: string | null | undefined,
  enabled = true,
): UseFolderAclResult {
  const qc = useQueryClient()
  const on = Boolean(workspaceId) && Boolean(slug) && enabled
  const query = useQuery({
    queryKey: pageFoldersKeys.acl(workspaceId ?? "", slug ?? ""),
    queryFn: async ({ signal }) => {
      const res = await apiFetch(folderAclRoute(workspaceId!, slug!), { signal })
      return normalizeFolderAcl(await judge(res, "folder permissions"))
    },
    enabled: on,
    retry: false,
  })
  const err = query.error as Error | null
  const forbidden = err instanceof PagesRequestError && err.status === 403
  const reread = useCallback(async () => {
    if (!workspaceId || !slug) return
    await qc.refetchQueries({ queryKey: pageFoldersKeys.acl(workspaceId, slug) })
  }, [qc, workspaceId, slug])
  return {
    entries: query.data?.entries ?? [],
    aclVersion: query.data?.aclVersion ?? null,
    loading: query.isPending && on,
    refusal: forbidden ? err.message : null,
    error: err && !forbidden ? err.message : null,
    manages: query.data !== undefined,
    reread,
  }
}

export interface SetAclEntryInput {
  subjectType: FolderAclSubjectType
  /** A reference the server resolves — an email, a crew slug; omitted for `workspace`. */
  subjectId?: string
  canWrite: boolean
}

export interface RemoveAclEntryInput {
  subjectType: FolderAclSubjectType
  subjectId: string
}

export interface FolderAclMutations {
  /** PUT — add an entry, or change whether it may edit. Always carries `r`. */
  set: UseMutationResult<FolderAclEntry | null, Error, SetAclEntryInput>
  /** DELETE — 204. */
  unset: UseMutationResult<void, Error, RemoveAclEntryInput>
}

/**
 * Writes to one folder's ACL. Both settle by invalidating the ACL AND the
 * folder list: an entry changes the row's `shared` marker and bumps its
 * `acl_version`, which the next move has to send.
 */
export function useFolderAclMutations(workspaceId: string | null | undefined, slug: string | null | undefined): FolderAclMutations {
  const qc = useQueryClient()
  const settle = useCallback(() => {
    if (!workspaceId || !slug) return
    void qc.invalidateQueries({ queryKey: pageFoldersKeys.all(workspaceId) })
    void qc.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
  }, [qc, workspaceId, slug])

  const set = useMutation<FolderAclEntry | null, Error, SetAclEntryInput>({
    retry: false,
    mutationFn: async (input) => {
      const res = await apiFetch(folderAclRoute(workspaceId!, slug!), {
        method: "PUT",
        headers: JSON_HEADERS,
        body: JSON.stringify({
          subject_type: input.subjectType,
          ...(input.subjectType !== "workspace" && input.subjectId !== undefined ? { subject_id: input.subjectId } : {}),
          can_write: input.canWrite,
        }),
      })
      const body = await judge(res, "share folder")
      // PUT answers the whole ACL document with the written row under
      // `entry`, not the row alone; reading the envelope as a row yielded
      // null. The dialog refetches after settling, so nothing drew from it,
      // but a caller that does gets the row it wrote.
      const entry = body && typeof body === "object" ? (body as { entry?: WireFolderAclEntry | null }).entry : null
      return entry && typeof entry === "object" ? toFolderAclEntry(entry) : null
    },
    onSettled: settle,
  })

  const unset = useMutation<void, Error, RemoveAclEntryInput>({
    retry: false,
    mutationFn: async (input) => {
      const res = await apiFetch(folderAclEntryRoute(workspaceId!, slug!, input.subjectType, input.subjectId), {
        method: "DELETE",
      })
      await judge(res, "unshare folder")
    },
    onSettled: settle,
  })

  return { set, unset }
}

// ── The caller's own paths to a page (#2533) ───────────────────────────────

export interface UsePageAccessMeResult {
  /** `owner`, `role`, `crew:<slug>`, `panel_crew:<slug>`, `grant`, `folder:<slug>`. */
  paths: string[]
  loading: boolean
  error: string | null
}

/** How the CALLER reaches one page — the read that is open to every reader of it. */
export function usePageAccessMe(
  workspaceId: string | null | undefined,
  slug: string | null | undefined,
  enabled = true,
): UsePageAccessMeResult {
  const on = Boolean(workspaceId) && Boolean(slug) && enabled
  const query = useQuery({
    queryKey: pageFoldersKeys.accessMe(workspaceId ?? "", slug ?? ""),
    queryFn: async ({ signal }) => {
      const res = await apiFetch(pageAccessMeRoute(workspaceId!, slug!), { signal })
      const body = await judge(res, "page access")
      const raw = body && typeof body === "object" ? (body as { paths?: unknown }).paths : null
      return Array.isArray(raw) ? raw.map(trimmed).filter((p): p is string => p !== null) : []
    },
    enabled: on,
    retry: false,
  })
  return {
    paths: useMemo(() => query.data ?? [], [query.data]),
    loading: query.isPending && on,
    error: query.error ? (query.error as Error).message : null,
  }
}
