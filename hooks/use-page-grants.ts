"use client"

/**
 * The Pages ACL and history data layer — PRD `docs/prd/pages.md` §7.1b
 * (three verbs, three subject kinds), §10b.1 (versions and rollback).
 *
 *   GET    /api/v1/pages/{slug}/grants     the whole ACL, live rows and inert
 *   PUT    /api/v1/pages/{slug}/grants     issue (or re-issue) one grant
 *   DELETE /api/v1/pages/{slug}/grants     revoke, by subject, optionally by level
 *   GET    /api/v1/pages/{slug}/versions   the retained history
 *   POST   /api/v1/pages/{slug}/rollback   restore one of them
 *
 * Three properties this file holds to, each mirroring something the server
 * already decided:
 *
 *  1. **It never decides whether a grant is worth anything.** `live` and
 *     `inert_reason` are the SERVER's use-time verdict — a grant is live only
 *     while the human who issued it could issue it again right now
 *     (`internal/api/pages_grants_authz.go`). Re-deriving that here would be a
 *     second implementation of the rule, with its own bugs, and the whole point
 *     of that file's `CASE` is that there is exactly one. So `live` is read
 *     STRICTLY as `=== true`: a row whose verdict this build could not read is
 *     shown inert, never live. Believing a grant works when it does not is the
 *     failure `inertReason()` was written to prevent, and the safe direction of
 *     that mistake is the pessimistic one.
 *
 *  2. **A refusal is the server's sentence, not a status code.** Both reads can
 *     legitimately 403 — the ACL names people and their crews, so §7.1 rule 3
 *     keeps it to the owner and workspace admins, and a version carries panels
 *     the caller may be sealed out of. Those refusals are written to be read,
 *     and they are carried through to the surface verbatim.
 *
 *  3. **Every write goes through `useApiMutation`.** The four rules from #1563
 *     — check `res.ok` first, say the server's words, never destroy what a retry
 *     needs, and never conflate a refusal with a transport failure — are that
 *     hook's whole reason to exist, and it also gives these three writes their
 *     `Idempotency-Key` and their 202/429 handling for free.
 *
 * React Query, `apiFetch`, `[resource, workspaceId, params]` keys — the
 * convention in CONTRIBUTING.md, and the same shape `hooks/use-pages.ts` uses,
 * whose `PagesRequestError` is reused here so a 403 keeps its status.
 */

import { useMemo } from "react"
import { useQuery } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { apiErrorMessage } from "@/lib/api-error"
import { ApiMutationError, useApiMutation, type UseApiMutationResult } from "@/hooks/use-api-mutation"
import { PagesRequestError, pagesKeys, type WirePage } from "@/hooks/use-pages"

// ── Vocabulary ─────────────────────────────────────────────────────────────
//
// The database's CHECK constraints and the wire's enum, in the client's copy
// of them. Closed on purpose: a level the UI offers that the schema refuses is
// a button that always 400s.

export const PAGE_GRANT_LEVELS = ["read", "produce", "write"] as const
export type PageGrantLevel = (typeof PAGE_GRANT_LEVELS)[number]

export const PAGE_SUBJECT_TYPES = ["user", "crew", "agent"] as const
export type PageSubjectType = (typeof PAGE_SUBJECT_TYPES)[number]

/**
 * What each verb actually means (§7.1b's own table).
 *
 * All three OPEN the page — `read` is the floor the other two build on, since a
 * grantee who may rewrite a page has to be able to look at it. What none of
 * them do is unseal a panel: a grant widens reach to the page and never to a
 * crew's data (§7.1 rule 3), so the `read` line says so where somebody issuing
 * one will read it, rather than leaving them to discover a page of sealed
 * placeholders and file it as a bug.
 *
 * An earlier build had a PAGE_GRANT_READ_CAVEAT here, warning that `read`
 * decided nothing at all. It decides page reach now, server-side, and the
 * warning went at the same commit the behaviour arrived.
 */
export const PAGE_GRANT_LEVEL_MEANING: Record<PageGrantLevel, string> = {
  read: "opens the page — panels stay sealed unless the grantee's own crew membership already opened them",
  produce: "opens the page, and may push payloads into named panels",
  write: "opens the page, and may edit its spec — add, remove and re-arrange panels",
}

export function isPageGrantLevel(value: unknown): value is PageGrantLevel {
  return typeof value === "string" && (PAGE_GRANT_LEVELS as readonly string[]).includes(value)
}

export function isPageSubjectType(value: unknown): value is PageSubjectType {
  return typeof value === "string" && (PAGE_SUBJECT_TYPES as readonly string[]).includes(value)
}

// ── The wire (mirrors internal/api/pages_grants.go) ────────────────────────

export interface WirePageGrant {
  subject_type?: string | null
  subject?: string | null
  subject_id?: string | null
  level?: string | null
  panels?: string[] | null
  /** The issuer, as a reference a human recognises (an email). */
  granted_by?: string | null
  /** NOT NULL by migration — §7.1b rule 1, only a human issues a grant. */
  granted_by_user_id?: string | null
  granted_at?: string | null
  live?: boolean | null
  inert_reason?: string | null
}

export interface WirePageGrants {
  page?: string | null
  grants?: WirePageGrant[] | null
  changed?: number | null
}

export interface WirePageVersion {
  seq?: number | null
  created_at?: string | null
  /** `user/<id>` or `agent/<slug>`; absent when the author was erased. */
  author?: string | null
  author_label?: string | null
  name?: string | null
  panel_count?: number | null
  current?: boolean | null
}

export interface WirePageVersions {
  page?: string | null
  retained?: number | null
  versions?: WirePageVersion[] | null
}

/** The page record plus the field `pageWire` sends that `WirePage` does not
 *  declare, because nothing rendering a panel grid needed it. */
export type WirePageDetail = WirePage & { created_at?: string | null }

// ── Normalising ────────────────────────────────────────────────────────────

function trimmed(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : null
}

/** One grant, as every settings surface consumes it. */
export interface PageGrant {
  subjectType: string
  subject: string
  subjectId: string
  level: string
  /**
   * The produce scope. EMPTY means "every panel on this page" — that is what a
   * NULL `panel_ids` column means (`pageGrantRecord.PanelIDs`: "nil = every
   * panel"), and it is meaningful for `produce` alone; the server refuses to
   * store a scope against any other level.
   */
  panels: string[]
  grantedBy: string
  grantedByUserID: string
  grantedAt: string | null
  live: boolean
  /** Why it is worth nothing, in the server's words. Null while it is live. */
  inertReason: string | null
}

export function toPageGrant(raw: WirePageGrant): PageGrant {
  // Strictly `=== true`. See property 1 in the file header: an unreadable
  // verdict renders inert, never live.
  const live = raw.live === true
  return {
    subjectType: trimmed(raw.subject_type) ?? "",
    subject: trimmed(raw.subject) ?? trimmed(raw.subject_id) ?? "",
    subjectId: trimmed(raw.subject_id) ?? "",
    level: trimmed(raw.level) ?? "",
    panels: Array.isArray(raw.panels)
      ? raw.panels.map((p) => trimmed(p)).filter((p): p is string => p !== null)
      : [],
    grantedBy: trimmed(raw.granted_by) ?? trimmed(raw.granted_by_user_id) ?? "",
    grantedByUserID: trimmed(raw.granted_by_user_id) ?? "",
    grantedAt: trimmed(raw.granted_at),
    live,
    inertReason: live ? null : trimmed(raw.inert_reason),
  }
}

/** `{page, grants}` is the envelope all three verbs answer with. A bare array
 *  is read too, for the same reason `normalizePageList` reads both. */
export function normalizePageGrants(body: unknown): PageGrant[] {
  if (Array.isArray(body)) return (body as WirePageGrant[]).map(toPageGrant)
  if (body && typeof body === "object") {
    const rec = body as WirePageGrants
    if (Array.isArray(rec.grants)) return rec.grants.map(toPageGrant)
  }
  return []
}

export interface PageVersion {
  seq: number
  createdAt: string | null
  /** `user/<id>` / `agent/<slug>` — null when the author was erased. */
  author: string | null
  /** The label to print: an email, an agent slug, or null. */
  authorLabel: string | null
  name: string | null
  panelCount: number
  current: boolean
}

export function toPageVersion(raw: WirePageVersion): PageVersion {
  return {
    seq: typeof raw.seq === "number" && Number.isFinite(raw.seq) ? raw.seq : 0,
    createdAt: trimmed(raw.created_at),
    author: trimmed(raw.author),
    authorLabel: trimmed(raw.author_label),
    name: trimmed(raw.name),
    panelCount: typeof raw.panel_count === "number" && raw.panel_count > 0 ? raw.panel_count : 0,
    current: raw.current === true,
  }
}

export function normalizePageVersions(body: unknown): PageVersion[] {
  if (Array.isArray(body)) return (body as WirePageVersion[]).map(toPageVersion)
  if (body && typeof body === "object") {
    const rec = body as WirePageVersions
    if (Array.isArray(rec.versions)) return rec.versions.map(toPageVersion)
  }
  return []
}

/** `crew/lookout` → `{kind: "crew", ref: "lookout"}`. Exactly one of the two
 *  arcs exists (§10's XOR CHECK), and which one it is changes what the owner
 *  line means, so the prefix is kept rather than trimmed away. */
export interface PageOwner {
  kind: "user" | "crew" | "unknown"
  ref: string
  /** What to print. A crew's display name when the wire carried one. */
  label: string
}

export function toPageOwner(page: WirePageDetail | null): PageOwner | null {
  const raw = trimmed(page?.owner)
  const crewName = trimmed(page?.owner_crew_name)
  const crewSlug = trimmed(page?.owner_crew_slug)
  if (!raw) {
    if (crewSlug || crewName) {
      return { kind: "crew", ref: crewSlug ?? crewName!, label: crewName ?? crewSlug! }
    }
    return null
  }
  const cut = raw.indexOf("/")
  const kind = cut >= 0 ? raw.slice(0, cut) : ""
  const ref = cut >= 0 ? raw.slice(cut + 1) : raw
  if (kind === "crew") return { kind: "crew", ref: ref || raw, label: crewName ?? ref ?? raw }
  if (kind === "user") return { kind: "user", ref: ref || raw, label: ref || raw }
  return { kind: "unknown", ref: raw, label: raw }
}

/** The page's panel count, from whichever shape the detail route sent. */
export function pagePanelCount(page: WirePageDetail | null): number {
  if (!page) return 0
  if (Array.isArray(page.panels)) return page.panels.length
  if (typeof page.panel_count === "number" && page.panel_count >= 0) return page.panel_count
  if (typeof page.panels === "number" && page.panels >= 0) return page.panels
  return 0
}

// ── Query keys ─────────────────────────────────────────────────────────────

export const pageAccessKeys = {
  grants: (workspaceId: string, slug: string) => ["page-grants", workspaceId, { slug }] as const,
  versions: (workspaceId: string, slug: string) => ["page-versions", workspaceId, { slug }] as const,
}

// ── Reads ──────────────────────────────────────────────────────────────────

function pageQueryString(workspaceId: string): string {
  return `?workspace_id=${encodeURIComponent(workspaceId)}`
}

async function readJSON(url: string, signal: AbortSignal | undefined, what: string) {
  const res = await apiFetch(url, { signal })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    // Property 2: the server's own sentence. Its 403s explain the rule they
    // are enforcing, and replacing them with "HTTP 403" throws that away.
    throw new PagesRequestError(res.status, apiErrorMessage(body, `${what}: ${res.status}`))
  }
  return res.json()
}

/** A read whose failure keeps the one distinction the surface acts on: a 403
 *  is not a broken request, it is the answer. */
interface GatedRead {
  loading: boolean
  /** The server's sentence for a 403 — the caller may not read this. */
  refusal: string | null
  /** Any other failure. */
  error: string | null
}

function gateOf(status: unknown, isPending: boolean, enabled: boolean): GatedRead {
  const err = status as Error | null
  const forbidden = err instanceof PagesRequestError && err.status === 403
  return {
    loading: isPending && enabled,
    refusal: forbidden ? err.message : null,
    error: err && !forbidden ? err.message : null,
  }
}

export interface UsePageGrantsResult extends GatedRead {
  grants: PageGrant[]
  /** Rows the server declared inert. Counted here so no surface has to. */
  inertCount: number
}

export function usePageGrants(
  workspaceId: string | null | undefined,
  slug: string | null | undefined,
  enabled = true,
): UsePageGrantsResult {
  const on = Boolean(workspaceId) && Boolean(slug) && enabled
  const query = useQuery({
    queryKey: pageAccessKeys.grants(workspaceId ?? "", slug ?? ""),
    queryFn: async ({ signal }) =>
      normalizePageGrants(
        await readJSON(
          `/api/v1/pages/${encodeURIComponent(slug!)}/grants${pageQueryString(workspaceId!)}`,
          signal,
          "grants",
        ),
      ),
    enabled: on,
    retry: false,
  })
  const grants = useMemo(() => query.data ?? [], [query.data])
  return {
    grants,
    inertCount: grants.reduce((n, g) => (g.live ? n : n + 1), 0),
    ...gateOf(query.error, query.isPending, on),
  }
}

export interface UsePageVersionsResult extends GatedRead {
  versions: PageVersion[]
}

export function usePageVersions(
  workspaceId: string | null | undefined,
  slug: string | null | undefined,
  enabled = true,
): UsePageVersionsResult {
  const on = Boolean(workspaceId) && Boolean(slug) && enabled
  const query = useQuery({
    queryKey: pageAccessKeys.versions(workspaceId ?? "", slug ?? ""),
    queryFn: async ({ signal }) =>
      normalizePageVersions(
        await readJSON(
          `/api/v1/pages/${encodeURIComponent(slug!)}/versions${pageQueryString(workspaceId!)}`,
          signal,
          "versions",
        ),
      ),
    enabled: on,
    retry: false,
  })
  return {
    versions: useMemo(() => query.data ?? [], [query.data]),
    ...gateOf(query.error, query.isPending, on),
  }
}

// ── Writes ─────────────────────────────────────────────────────────────────

export interface PageGrantWriteVariables {
  subjectType: PageSubjectType
  /** A REFERENCE, never an id: an email, a crew slug, an agent slug. The
   *  server resolves it, exactly as it resolves `owner: crew/lookout`. */
  subject: string
  level: PageGrantLevel
  /** Produce only. Empty covers every panel. */
  panels?: string[]
}

export interface PageGrantRevokeVariables {
  subjectType: string
  subject: string
  /** Omitted revokes EVERY level this subject holds — §7.1b's own example
   *  (`crewship page revoke <slug> --agent <slug>`) carries no level. */
  level?: string
}

export interface PageRollbackVariables {
  to: number
}

interface WriteCallbacks<TVariables> {
  onOk?: (variables: TVariables) => void
  onRefused?: (message: string) => void
}

/** The message any failure deserves: the server's words for a refusal, and a
 *  DIFFERENT sentence for a transport failure (#1563 rule 4 — telling someone
 *  their grant was refused when the network dropped sends them to fix a
 *  request the server never read). */
export function pageWriteFailureMessage(err: unknown): string {
  if (err instanceof ApiMutationError) return err.message
  if (err instanceof Error) return `Could not reach the server: ${err.message}`
  return "Could not reach the server"
}

function grantsInvalidation(workspaceId: string, slug: string) {
  return [pageAccessKeys.grants(workspaceId, slug)]
}

/** PUT — issue or re-issue one grant. */
export function usePageGrantWrite(
  workspaceId: string,
  slug: string,
  cb: WriteCallbacks<PageGrantWriteVariables> = {},
): UseApiMutationResult<PageGrantWriteVariables, unknown> {
  return useApiMutation<PageGrantWriteVariables, unknown>({
    request: (v) => ({
      input: `/api/v1/pages/${encodeURIComponent(slug)}/grants${pageQueryString(workspaceId)}`,
      init: {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          subject_type: v.subjectType,
          subject: v.subject,
          level: v.level,
          // Sent only where it means something. The server refuses a panel
          // list against read/write with a 400 rather than storing a scope
          // the code ignores, and mirroring that here keeps the form honest.
          ...(v.level === "produce" && v.panels && v.panels.length > 0
            ? { panels: v.panels }
            : {}),
        }),
      },
    }),
    invalidateKeys: grantsInvalidation(workspaceId, slug),
    onOk: (_data, v) => cb.onOk?.(v),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => cb.onRefused?.(pageWriteFailureMessage(err)),
  })
}

/**
 * DELETE — revoke.
 *
 * The subject rides on the query string because a DELETE body is optional in
 * HTTP and proxies drop it (`internal/api/pages_grants.go`): a revoke whose
 * subject went missing would delete nothing or everything, and both are
 * unacceptable for the one operation somebody runs when things have gone
 * wrong.
 */
export function usePageGrantRevoke(
  workspaceId: string,
  slug: string,
  cb: WriteCallbacks<PageGrantRevokeVariables> = {},
): UseApiMutationResult<PageGrantRevokeVariables, unknown> {
  return useApiMutation<PageGrantRevokeVariables, unknown>({
    request: (v) => {
      const params = new URLSearchParams({
        workspace_id: workspaceId,
        subject_type: v.subjectType,
        subject: v.subject,
      })
      if (v.level) params.set("level", v.level)
      return {
        input: `/api/v1/pages/${encodeURIComponent(slug)}/grants?${params.toString()}`,
        init: { method: "DELETE" },
      }
    },
    invalidateKeys: grantsInvalidation(workspaceId, slug),
    onOk: (_data, v) => cb.onOk?.(v),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => cb.onRefused?.(pageWriteFailureMessage(err)),
  })
}

/**
 * POST — roll the spec back to a retained version.
 *
 * Invalidates the page itself as well as the history: a rollback IS a save
 * (§10b.1), so it appends a new version AND changes what the grid renders.
 */
export function usePageRollback(
  workspaceId: string,
  slug: string,
  cb: WriteCallbacks<PageRollbackVariables> = {},
): UseApiMutationResult<PageRollbackVariables, unknown> {
  return useApiMutation<PageRollbackVariables, unknown>({
    request: (v) => ({
      input: `/api/v1/pages/${encodeURIComponent(slug)}/rollback${pageQueryString(workspaceId)}`,
      init: {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ to: v.to }),
      },
    }),
    invalidateKeys: [pageAccessKeys.versions(workspaceId, slug), pagesKeys.all(workspaceId)],
    onOk: (_data, v) => cb.onOk?.(v),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => cb.onRefused?.(pageWriteFailureMessage(err)),
  })
}
