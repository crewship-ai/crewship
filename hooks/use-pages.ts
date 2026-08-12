"use client"

/**
 * The Pages data layer — PRD `docs/prd/pages.md` §11 / §11b (wire), §4
 * (freshness), §9b (what the shell needs to say).
 *
 * React Query, `apiFetch`, `[resource, workspaceId, params?]` keys, and
 * `useRealtimeEvent` + `invalidateQueries` rather than polling — the
 * convention documented in CONTRIBUTING.md ("Frontend data fetching"), with
 * `hooks/use-inbox.ts` as the reference implementation.
 *
 * Two properties of this file are deliberate:
 *
 *  1. **Every normaliser is a pure exported function.** The panels are already
 *     tested against fixtures; what is left to get wrong is the translation
 *     from the wire into `PanelSpec` / `PanelSnapshot`, and the arithmetic
 *     behind "3 stale". Both are testable without rendering anything.
 *
 *  2. **It is tolerant on read and exact on meaning.** The API is being built
 *     in parallel (issue #1937) and §11b pins the shapes that matter —
 *     provenance is nested, SLA is `sla_seconds`, `never_produced` is a state
 *     the SERVER sends — but not the envelope around a list, nor the key the
 *     payload rides under. Where the PRD is silent this reads several shapes;
 *     where the PRD speaks it reads exactly one. What it never does is INVENT
 *     freshness: a state the server did not send stays unknown, and an unknown
 *     state is never counted as fresh (§9b.4 — `0` is measured, `—` is no
 *     basis to compute, and there is no third glyph).
 */

import { useCallback, useMemo } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeChannelSafe, useRealtimeEventSafe } from "@/hooks/use-realtime"
import {
  PANEL_STATES,
  type PanelSnapshot,
  type PanelSpec,
  type PanelState,
} from "@/components/features/pages/panels/types"

// ── The wire (§11b) ────────────────────────────────────────────────────────

/** §11b.4: provenance is a NESTED object, never flat fields. */
export interface WireProvenance {
  producer?: string | null
  run_id?: string | null
  produced_at?: string | null
}

export interface WirePanel {
  id?: string | null
  schema?: string | null
  title?: string | null
  /** `crew/<slug>` — the permission anchor, not a label (§10 `page_panels`). */
  owner?: string | null
  producer?: string | null
  /** §11b.3: `sla_seconds` on the wire. `sla` ("30s") is YAML sugar. */
  sla_seconds?: number | null
  sla?: string | number | null
  /** 1..12, consumed by the grid (§9). */
  span?: number | null
  /** §11b.8: four states, and the SERVER sends `never_produced`. */
  state?: string | null
  /** The payload. §11 does not fix the key; `data` is what the CLI's
   *  acceptance fixture uses, `payload` is what the column is called. */
  data?: unknown
  payload?: unknown
  provenance?: WireProvenance | null
  /** The failure reason (`pages.Verdict.Reason`). Internal vocabulary — the
   *  panel components already refuse to render it in a public view (§7.3.2b). */
  failure?: string | null
  reason?: string | null
}

export interface WirePage {
  id?: string | null
  slug?: string | null
  name?: string | null
  description?: string | null
  /** `crew/<slug>` or `user/<id>` — §10 stores one of the two. */
  owner?: string | null
  owner_crew_slug?: string | null
  owner_crew_name?: string | null
  /** An array on the detail route; a count on the index (both are read). */
  panels?: WirePanel[] | number | null
  panel_count?: number | null
  /** Per-state panel counts, when the index sends a rollup instead of panels. */
  panel_states?: Partial<Record<PanelState, number>> | null
  /** §10b.5d says the index returns "the stale count" — the Dashboard strip is
   *  described as a read-only view over data the page index already returns.
   *  These flat fields are that count, in the shape a handler is most likely to
   *  write it if it does not send a nested rollup. */
  stale_panels?: number | null
  failed_panels?: number | null
  never_produced_panels?: number | null
  fresh_panels?: number | null
  /** When the index sends the page's own worst-case verdict. */
  state?: string | null
  /** Newest payload across the page's panels. Distinct from `updated_at`,
   *  which §10 defines as the SPEC's mtime — a page whose spec was edited
   *  today has not necessarily received data today. */
  last_produced_at?: string | null
  updated_at?: string | null
}

// ── Normalising ────────────────────────────────────────────────────────────

const STATE_SET: ReadonlySet<string> = new Set<string>(PANEL_STATES)

/** Untrusted string in, `PanelState` or null out. Never guesses. */
export function toPanelState(value: unknown): PanelState | null {
  return typeof value === "string" && STATE_SET.has(value) ? (value as PanelState) : null
}

/**
 * Severity order for the page's own verdict: the worst panel is what the
 * index reports. `failed` outranks `stale` because a producer that ran and
 * failed is a stated fault, while stale is an inference from the clock;
 * `never_produced` outranks `fresh` because a page with an empty panel is not
 * finished being set up.
 */
const STATE_RANK: Record<PanelState, number> = {
  failed: 3,
  stale: 2,
  never_produced: 1,
  fresh: 0,
}

export function worstPanelState(states: readonly (PanelState | null)[]): PanelState | null {
  let worst: PanelState | null = null
  for (const s of states) {
    if (!s) continue
    if (worst === null || STATE_RANK[s] > STATE_RANK[worst]) worst = s
  }
  return worst
}

function trimmed(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : null
}

function spanOf(raw: WirePanel): number {
  const n = typeof raw.span === "number" ? Math.trunc(raw.span) : 0
  // `DefaultSpan = 12` (internal/pages/spec.go): a panel that declares no span
  // takes the full width. Zero would render a panel with no width at all.
  if (!Number.isFinite(n) || n <= 0) return 12
  return Math.min(12, n)
}

function slaSecondsOf(raw: WirePanel): number | undefined {
  if (typeof raw.sla_seconds === "number" && Number.isFinite(raw.sla_seconds)) {
    return raw.sla_seconds
  }
  if (typeof raw.sla === "number" && Number.isFinite(raw.sla)) return raw.sla
  return undefined
}

/** One panel, split into the two halves the renderer takes (§6: spec, payload). */
export interface PanelView {
  spec: PanelSpec
  snapshot: PanelSnapshot
  /** The declared producer — shown when nothing has ever been pushed. */
  producer: string | null
  /** Server-computed, or null when this build could not read one. */
  state: PanelState | null
}

export function toPanelView(raw: WirePanel, index = 0): PanelView {
  const id = trimmed(raw.id) ?? `panel-${index + 1}`
  const state = toPanelState(raw.state)
  const payload = raw.data !== undefined ? raw.data : raw.payload
  const prov = raw.provenance ?? null
  return {
    spec: {
      id,
      // Untrusted on purpose: the registry narrows it, and an unknown schema
      // costs one panel rather than the page (§9).
      schema: typeof raw.schema === "string" ? raw.schema : "",
      title: trimmed(raw.title) ?? undefined,
      owner: trimmed(raw.owner),
      span: spanOf(raw),
      sla_seconds: slaSecondsOf(raw),
    },
    snapshot: {
      // A state this build cannot read is NOT reported as fresh. `never_produced`
      // is the honest fallback: it renders the em dash plus the sentence that
      // says how to make data arrive, which is true of a panel we know nothing
      // about (§9b.3, §9b.4).
      state: state ?? "never_produced",
      payload: payload === undefined ? null : payload,
      provenance: prov
        ? {
            producer: trimmed(prov.producer),
            run_id: trimmed(prov.run_id),
            produced_at: trimmed(prov.produced_at),
          }
        : null,
      failure: trimmed(raw.failure) ?? trimmed(raw.reason),
    },
    producer: trimmed(raw.producer),
    state,
  }
}

/** Per-state panel tally for one page. `unknown` is its own bucket. */
export type PanelTally = Record<PanelState, number> & { unknown: number; total: number }

function emptyTally(): PanelTally {
  return { fresh: 0, stale: 0, failed: 0, never_produced: 0, unknown: 0, total: 0 }
}

/** A page as every Pages surface consumes it. */
export interface PageView {
  id: string
  slug: string
  name: string
  description: string | null
  /** `crew/<slug>` as authored — the facet key. */
  ownerRef: string | null
  /** What the OWNER facet prints. */
  ownerLabel: string | null
  /** Present on the detail route; null on an index that sent only counts. */
  panels: PanelView[] | null
  tally: PanelTally
  /** The page's worst panel state, or null when no panel state is known. */
  state: PanelState | null
  /** Newest payload across the page, in the sense of §4 — not the spec's mtime. */
  lastProducedAt: Date | null
  updatedAt: Date | null
}

function toDate(value: unknown): Date | null {
  const s = trimmed(value)
  if (!s) return null
  const d = new Date(s)
  return Number.isFinite(d.getTime()) ? d : null
}

function ownerLabelOf(raw: WirePage): string | null {
  const name = trimmed(raw.owner_crew_name)
  if (name) return name
  const slug = trimmed(raw.owner_crew_slug)
  if (slug) return slug
  const ref = trimmed(raw.owner)
  if (!ref) return null
  // "crew/lookout" reads as "lookout" in a facet list; the prefix is the same
  // on every row and spends width saying nothing.
  const cut = ref.indexOf("/")
  return cut >= 0 ? ref.slice(cut + 1) || ref : ref
}

function ownerRefOf(raw: WirePage): string | null {
  const ref = trimmed(raw.owner)
  if (ref) return ref
  const slug = trimmed(raw.owner_crew_slug)
  return slug ? `crew/${slug}` : null
}

export function toPageView(raw: WirePage): PageView {
  const panels = Array.isArray(raw.panels) ? raw.panels.map(toPanelView) : null
  const tally = emptyTally()

  if (panels) {
    tally.total = panels.length
    for (const p of panels) {
      if (p.state) tally[p.state] += 1
      else tally.unknown += 1
    }
  } else {
    // No panels on the wire: take the rollup if there is one, and let the
    // remainder stay `unknown` rather than assuming the rest are fine.
    const counts: Partial<Record<PanelState, number>> = {
      ...(raw.panel_states ?? {}),
    }
    // Flat fields fill in only what the nested rollup did not say, so a server
    // sending both cannot double-count.
    const flat: Array<[PanelState, number | null | undefined]> = [
      ["fresh", raw.fresh_panels],
      ["stale", raw.stale_panels],
      ["failed", raw.failed_panels],
      ["never_produced", raw.never_produced_panels],
    ]
    for (const [state, n] of flat) {
      if (counts[state] === undefined && typeof n === "number") counts[state] = n
    }
    const declared =
      typeof raw.panel_count === "number"
        ? raw.panel_count
        : typeof raw.panels === "number"
          ? raw.panels
          : 0
    let known = 0
    for (const s of PANEL_STATES) {
      const n = counts[s]
      if (typeof n === "number" && n > 0) {
        tally[s] = n
        known += n
      }
    }
    tally.total = Math.max(declared, known)
    tally.unknown = Math.max(0, tally.total - known)
  }

  const fromPanels = panels ? worstPanelState(panels.map((p) => p.state)) : null
  const fromRollup = worstPanelState(PANEL_STATES.filter((s) => tally[s] > 0))
  const state = fromPanels ?? toPanelState(raw.state) ?? fromRollup

  const producedAts = panels
    ? panels.map((p) => toDate(p.snapshot.provenance?.produced_at)).filter((d): d is Date => d != null)
    : []
  const newest = producedAts.length
    ? new Date(Math.max(...producedAts.map((d) => d.getTime())))
    : toDate(raw.last_produced_at)

  const slug = trimmed(raw.slug) ?? ""
  return {
    id: trimmed(raw.id) ?? slug,
    slug,
    name: trimmed(raw.name) ?? slug,
    description: trimmed(raw.description),
    ownerRef: ownerRefOf(raw),
    ownerLabel: ownerLabelOf(raw),
    panels,
    tally,
    state,
    lastProducedAt: newest,
    updatedAt: toDate(raw.updated_at),
  }
}

/**
 * The list envelope. §11 fixes the route and not the wrapper, and this repo
 * has both conventions in it — `/api/v1/agents` returns a bare array,
 * `/api/v1/inbox` returns `{rows}`. Reading either is three lines; guessing
 * wrong is a surface that renders nothing and looks like an empty workspace.
 */
export function normalizePageList(body: unknown): WirePage[] {
  if (Array.isArray(body)) return body as WirePage[]
  if (body && typeof body === "object") {
    const rec = body as Record<string, unknown>
    for (const key of ["pages", "rows", "items", "data"]) {
      if (Array.isArray(rec[key])) return rec[key] as WirePage[]
    }
  }
  return []
}

/** The detail route may wrap the record the same way. */
export function normalizePage(body: unknown): WirePage | null {
  if (!body || typeof body !== "object") return null
  const rec = body as Record<string, unknown>
  if (typeof rec.slug === "string" || Array.isArray(rec.panels)) return rec as WirePage
  const inner = rec.page
  if (inner && typeof inner === "object") return inner as WirePage
  return null
}

// ── What the shell says (§9b.2) ────────────────────────────────────────────

function sameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  )
}

export interface PagesSummary {
  /** Every page the viewer may see. */
  total: number
  /** Pages carrying at least one panel past its SLA. */
  stalePages: number
  /** Panels past their SLA, across every page. */
  stalePanels: number
  /** Pages that received a payload today. */
  updatedToday: number
  /** Pages with a failed panel, or one nothing has ever been pushed to. */
  needsAttention: number
  failedPanels: number
  neverProducedPanels: number
  /** False when no page carried a readable state — nothing may be claimed. */
  hasFreshnessBasis: boolean
}

export function summarisePages(pages: readonly PageView[], now: Date = new Date()): PagesSummary {
  const s: PagesSummary = {
    total: pages.length,
    stalePages: 0,
    stalePanels: 0,
    updatedToday: 0,
    needsAttention: 0,
    failedPanels: 0,
    neverProducedPanels: 0,
    hasFreshnessBasis: false,
  }
  for (const p of pages) {
    if (p.state !== null) s.hasFreshnessBasis = true
    if (p.tally.stale > 0) s.stalePages += 1
    s.stalePanels += p.tally.stale
    s.failedPanels += p.tally.failed
    s.neverProducedPanels += p.tally.never_produced
    if (p.tally.failed > 0 || p.tally.never_produced > 0) s.needsAttention += 1
    if (p.lastProducedAt && sameDay(p.lastProducedAt, now)) s.updatedToday += 1
  }
  return s
}

// ── Filtering (§9b.1) ──────────────────────────────────────────────────────

/** Facet state. Both facets are MULTI-select (#1776 — that is the whole point). */
export interface PageFilters {
  states: PanelState[]
  owners: string[]
}

export const EMPTY_PAGE_FILTERS: PageFilters = { states: [], owners: [] }

export function pageFilterCount(f: PageFilters): number {
  return f.states.length + f.owners.length
}

export function togglePageFilter<T extends string>(list: readonly T[], value: T): T[] {
  return list.includes(value) ? list.filter((v) => v !== value) : [...list, value]
}

/**
 * A page matches a STATUS pick when ANY of its panels is in that state — not
 * when its worst state happens to equal it. "Stale" means "show me what has
 * gone quiet", and a page with one stale panel among nine fresh ones is
 * exactly that; ranking it as `failed` because a tenth panel also broke would
 * hide it from the filter that was looking for it.
 */
export function matchesPageFilters(
  page: PageView,
  filters: PageFilters,
  search: string,
): boolean {
  const q = search.trim().toLowerCase()
  if (q) {
    const hay = [page.name, page.slug, page.description ?? "", page.ownerLabel ?? ""]
      .join(" ")
      .toLowerCase()
    if (!hay.includes(q)) return false
  }
  if (filters.states.length > 0) {
    const hit = filters.states.some((s) => page.tally[s] > 0)
    if (!hit) return false
  }
  if (filters.owners.length > 0) {
    if (!page.ownerRef || !filters.owners.includes(page.ownerRef)) return false
  }
  return true
}

/** How many pages each STATUS option would match, computed over the whole set. */
export function stateFacetCounts(pages: readonly PageView[]): Record<PanelState, number> {
  const counts: Record<PanelState, number> = { fresh: 0, stale: 0, failed: 0, never_produced: 0 }
  for (const p of pages) {
    for (const s of PANEL_STATES) {
      if (p.tally[s] > 0) counts[s] += 1
    }
  }
  return counts
}

export interface OwnerFacet {
  ref: string
  label: string
  count: number
}

/** The OWNER facet, derived from the loaded pages — no second fetch. */
export function ownerFacets(pages: readonly PageView[]): OwnerFacet[] {
  const map = new Map<string, OwnerFacet>()
  for (const p of pages) {
    if (!p.ownerRef) continue
    const cur = map.get(p.ownerRef)
    if (cur) cur.count += 1
    else map.set(p.ownerRef, { ref: p.ownerRef, label: p.ownerLabel ?? p.ownerRef, count: 1 })
  }
  return Array.from(map.values()).sort((a, b) => a.label.localeCompare(b.label))
}

// ── Query keys ─────────────────────────────────────────────────────────────

/** `[resource, workspaceId, params?]` — CONTRIBUTING.md. */
export const pagesKeys = {
  /** Everything for one workspace; the invalidation scope. */
  all: (workspaceId: string) => ["pages", workspaceId] as const,
  list: (workspaceId: string) => ["pages", workspaceId, { view: "list" }] as const,
  detail: (workspaceId: string, slug: string) => ["pages", workspaceId, { slug }] as const,
}

// ── Hooks ──────────────────────────────────────────────────────────────────

/**
 * §10b.5b: every page is live. One websocket already exists, so an open page
 * is one more subscription on it and the broadcast carries no payload — we
 * re-read through the authorised path, which is why there is only ever one
 * copy of the per-panel permission filter.
 *
 * `realtime.reconnected` is the gap: a socket that dropped and came back
 * missed every push in between, and this surface has no poll backstop.
 */
function usePagesRealtime(workspaceId: string | null | undefined, pageId?: string | null) {
  const qc = useQueryClient()
  const invalidate = useCallback(() => {
    if (!workspaceId) return
    qc.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
  }, [qc, workspaceId])

  useRealtimeEventSafe("page.panel.updated", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  useRealtimeChannelSafe(pageId ? `page:${pageId}` : null)
}

/** An HTTP failure that keeps its status, so a 404 can get its own empty state. */
export class PagesRequestError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = "PagesRequestError"
    this.status = status
  }
}

async function fetchJSON(url: string, signal: AbortSignal | undefined, what: string) {
  const res = await apiFetch(url, { signal })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    const detail =
      body && typeof body === "object" && typeof (body as { error?: string }).error === "string"
        ? (body as { error: string }).error
        : null
    throw new PagesRequestError(res.status, detail ?? `${what}: ${res.status}`)
  }
  return res.json()
}

export interface UsePagesResult {
  pages: PageView[]
  loading: boolean
  error: string | null
  refresh: () => void
}

/**
 * The index. Route is workspace-unscoped (§11b.1) with `workspace_id` on the
 * query string, the same way the CLI appends it and the same way `/api/v1/inbox`
 * is called from this app.
 */
export function usePages(workspaceId: string | null | undefined): UsePagesResult {
  const qc = useQueryClient()
  const query = useQuery({
    queryKey: pagesKeys.list(workspaceId ?? ""),
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams({ workspace_id: workspaceId! })
      const body = await fetchJSON(`/api/v1/pages?${params.toString()}`, signal, "pages")
      return normalizePageList(body)
    },
    enabled: Boolean(workspaceId),
    retry: false,
  })

  usePagesRealtime(workspaceId)

  const pages = useMemo(() => (query.data ?? []).map(toPageView), [query.data])

  return {
    pages,
    loading: query.isPending && Boolean(workspaceId),
    error: query.error ? (query.error as Error).message : null,
    refresh: () => {
      if (workspaceId) qc.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
    },
  }
}

export interface UsePageResult {
  page: PageView | null
  loading: boolean
  error: string | null
  /** True for a 404 — a slug that names nothing gets its own empty state. */
  notFound: boolean
}

/** One page, with its panels and their payloads. */
export function usePage(
  workspaceId: string | null | undefined,
  slug: string | null | undefined,
): UsePageResult {
  const query = useQuery({
    queryKey: pagesKeys.detail(workspaceId ?? "", slug ?? ""),
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams({ workspace_id: workspaceId! })
      const body = await fetchJSON(
        `/api/v1/pages/${encodeURIComponent(slug!)}?${params.toString()}`,
        signal,
        "page",
      )
      return normalizePage(body)
    },
    enabled: Boolean(workspaceId) && Boolean(slug),
    retry: false,
  })

  const page = useMemo(() => (query.data ? toPageView(query.data) : null), [query.data])
  usePagesRealtime(workspaceId, page?.id ?? null)

  const err = query.error as Error | null
  return {
    page,
    loading: query.isPending && Boolean(workspaceId) && Boolean(slug),
    error: err ? err.message : null,
    notFound: err instanceof PagesRequestError && err.status === 404,
  }
}
