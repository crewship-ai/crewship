"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Skeleton } from "@/components/ui/skeleton"
import { fetchWithRetry } from "@/lib/fetch-with-retry"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"

// =============================================================================
// Shared scaffolding for the tabbed detail panes used by `crew-canvas` and
// `agent-canvas`. Both panes have the same fetch-then-detail loader, the same
// loading/error skeleton, the same outer container, the same tab strip, and a
// near-identical `Row` helper inside their settings/profile sections. Those
// concerns live here so each canvas stays focused on its own header + tab
// bodies.
//
// Nothing in this file is graph-canvas / drag-drop / viewport related —
// despite the file name, the existing canvases are tabbed detail screens.
// The shared scaffolding is intentionally narrow so the visible behaviour of
// either consumer cannot drift.
// =============================================================================


/**
 * Generic two-step entity fetch:
 *  1. list endpoint, find by slug
 *  2. detail endpoint for the full record
 *
 * Mirrors the existing `fetchCrew` / `fetchAgent` flow exactly, including the
 * AbortSignal pass-through and the "don't write state after abort" guard.
 */
export interface UseEntityFetchOptions<T> {
  workspaceId: string
  slug: string
  /** `?workspace_id=…` is appended automatically. */
  listUrl: string
  /** Receives the matched record's id. `?workspace_id=…` is appended. */
  detailUrl: (id: string) => string
  matchSlug: (record: T) => string
  /** Surfaced when the slug has no match in the list response. */
  notFoundMessage: string
  /** Fallback message when the list endpoint fails without an Error. */
  listErrorMessage: string
  /** Fallback message when the detail endpoint fails without an Error. */
  detailErrorMessage: string
}

export interface EntityFetchState<T> {
  entity: T | null
  setEntity: (entity: T | null) => void
  loading: boolean
  error: string | null
  refetch: (signal?: AbortSignal) => Promise<void>
}

export function useEntityFetch<T>({
  workspaceId,
  slug,
  listUrl,
  detailUrl,
  matchSlug,
  notFoundMessage,
  listErrorMessage,
  detailErrorMessage,
}: UseEntityFetchOptions<T>): EntityFetchState<T> {
  const [entity, setEntity] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const wsParam = `workspace_id=${workspaceId}`
  const listFull = listUrl.includes("?") ? `${listUrl}&${wsParam}` : `${listUrl}?${wsParam}`

  const refetch = useCallback(async (signal?: AbortSignal) => {
    try {
      const listRes = await fetchWithRetry(listFull, { signal })
      if (!listRes.ok) throw new Error(`${listErrorMessage} (${listRes.status})`)
      const list: T[] = await listRes.json()
      const match = list.find((r) => matchSlug(r) === slug)
      if (!match) throw new Error(notFoundMessage)
      const detailBase = detailUrl((match as unknown as { id: string }).id)
      const detailFull = detailBase.includes("?") ? `${detailBase}&${wsParam}` : `${detailBase}?${wsParam}`
      const detailRes = await fetchWithRetry(detailFull, { signal })
      if (!detailRes.ok) throw new Error(`${detailErrorMessage} (${detailRes.status})`)
      const detail: T = await detailRes.json()
      if (!signal?.aborted) {
        setEntity(detail)
        setError(null)
      }
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") return
      setError(err instanceof Error ? err.message : detailErrorMessage)
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  // listFull / detailUrl are derived from the inputs already in deps.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug, workspaceId])

  useEffect(() => {
    setLoading(true)
    const controller = new AbortController()
    void refetch(controller.signal)
    return () => controller.abort()
  }, [slug, refetch])

  return { entity, setEntity, loading, error, refetch }
}


/**
 * Shared PATCH helper. Both canvases issue the exact same shape of request:
 * `PATCH {basePath}/{id}?workspace_id=…`. The hook returns a `patch(body)`
 * function that updates local state from the response and pings `onChanged`,
 * mirroring the inline implementations in the two canvases.
 */
export interface UsePatchEntityOptions<T> {
  workspaceId: string
  entity: T | null
  /** Builds the full PATCH URL for the matched entity (no query string). */
  patchUrl: (entity: T) => string
  setEntity: (next: T) => void
  /** Receives the saved record — a rename has to be able to follow the slug. */
  onChanged: (updated: T) => void
}

export function usePatchEntity<T>({
  workspaceId,
  entity,
  patchUrl,
  setEntity,
  onChanged,
}: UsePatchEntityOptions<T>) {
  return useCallback(async (body: Record<string, unknown>) => {
    if (!entity) return
    const base = patchUrl(entity)
    const url = base.includes("?") ? `${base}&workspace_id=${workspaceId}` : `${base}?workspace_id=${workspaceId}`
    const res = await apiFetch(url, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(text || `HTTP ${res.status}`)
    }
    const updated: T = await res.json()
    setEntity(updated)
    onChanged(updated)
  }, [entity, workspaceId, patchUrl, setEntity, onChanged])
}


/**
 * Resets the active tab back to "overview" whenever the entity slug changes.
 * Both canvases ran the same lastSlug-ref dance inline; this is the single
 * source of truth.
 */
export function useResetTabOnSlugChange<TTab extends string>(
  slug: string,
  setTab: (tab: TTab) => void,
  defaultTab: TTab,
  extraReset?: () => void,
) {
  const lastSlug = useRef(slug)
  useEffect(() => {
    if (lastSlug.current !== slug) {
      setTab(defaultTab)
      extraReset?.()
      lastSlug.current = slug
    }
  }, [slug, setTab, defaultTab, extraReset])
}


/**
 * Outer wrapper used by both panes. Centralises the loading skeleton, the
 * error fallback, and the consistent page padding so a tweak to either
 * branch can't accidentally drift between crews and agents.
 */
export interface CanvasShellProps {
  loading: boolean
  error: string | null
  notLoadedLabel: string
  children: React.ReactNode
}

const SHELL = "@container px-6 md:px-8 lg:px-12 detail-width"

/**
 * Loading state shaped like the screen it precedes: a title line, a chip row,
 * then the same auto-fit card grid. The dashboard does this — its skeleton
 * repeats its real row grids verbatim — and the reason shows the moment you
 * compare. One full-width grey slab, which is what this used to be, resolves
 * by being replaced, so the page visibly jumps. A skeleton in the right shape
 * resolves by being filled in, and the cards land where the grey already was.
 */
function CanvasSkeleton() {
  return (
    <div className={`${SHELL} space-y-6 py-6`} aria-hidden>
      <div className="space-y-2.5">
        <Skeleton className="h-7 w-56 rounded-lg" />
        <Skeleton className="h-4 w-80 rounded-md" />
      </div>
      <div className="flex flex-wrap gap-1.5">
        {[64, 52, 72, 58, 48, 66].map((w, i) => (
          <Skeleton key={i} className="h-5 rounded-md" style={{ width: w }} />
        ))}
      </div>
      <div className="grid gap-3.5 grid-cols-[repeat(auto-fit,minmax(15.5rem,1fr))]">
        {Array.from({ length: 5 }, (_, i) => (
          <Skeleton key={i} className="h-[268px] rounded-xl" />
        ))}
      </div>
    </div>
  )
}

export function CanvasShell({ loading, error, notLoadedLabel, children }: CanvasShellProps) {
  if (loading) return <CanvasSkeleton />

  if (error) {
    return (
      <div className={`${SHELL} py-12 text-center`}>
        <p className="type-row mb-2 text-destructive">{notLoadedLabel}</p>
        <p className="type-meta text-muted-foreground">{error}</p>
      </div>
    )
  }
  return (
    // @container, not viewport breakpoints: the grids inside answer to how
    // wide THIS pane is. The sidebar collapses, the list pane takes a slice,
    // a drawer can open — the window width never described the room the cards
    // actually had.
    <div className={`${SHELL} space-y-6 py-6`}>
      {children}
    </div>
  )
}


/**
 * The two ids that pair a tab with the panel it controls. Both halves derive
 * them from the same prefix, so a caller cannot wire one and forget the other.
 */
export function canvasTabIds(idPrefix: string, tab: string) {
  return { tabId: `${idPrefix}-tab-${tab}`, panelId: `${idPrefix}-panel-${tab}` }
}

/**
 * Tab strip rendered under the canvas header. Generic over the tab id so the
 * caller's `tab` state stays strongly typed (`CrewTab` / `AgentTab`).
 *
 * The buttons carried `aria-selected` on an implicit `button` role, which that
 * attribute is not allowed on (axe: aria-allowed-attr) — a screen reader was
 * told nothing about selection and the strip did not read as a tab set. The
 * roles below are the actual fix; `idPrefix` is required rather than optional
 * so the tab→panel pairing cannot be skipped the way #1978 describes.
 */
export interface CanvasTabsProps<TTab extends string> {
  tabs: ReadonlyArray<{ id: TTab; label: string }>
  active: TTab
  onChange: (tab: TTab) => void
  /** Namespaces the tab/panel ids. Must match the sibling `CanvasTabPanel`. */
  idPrefix: string
  /** Names the tab set — two strips on one screen otherwise read alike. */
  label: string
}

export function CanvasTabs<TTab extends string>({
  tabs, active, onChange, idPrefix, label,
}: CanvasTabsProps<TTab>) {
  return (
    <div
      role="tablist"
      aria-label={label}
      className="flex items-center gap-5 border-b border-white/8 -mx-6 md:-mx-8 lg:-mx-12 px-6 md:px-8 lg:px-12 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]"
    >
      {tabs.map((t) => {
        const selected = active === t.id
        const { tabId, panelId } = canvasTabIds(idPrefix, t.id)
        return (
          <button
            key={t.id}
            id={tabId}
            type="button"
            role="tab"
            onClick={() => onChange(t.id)}
            aria-selected={selected}
            // Only the selected panel is mounted — the canvases render their
            // tabs as `{tab === "x" && <XTab/>}`. Pointing aria-controls at an
            // id that is not in the DOM is itself a violation
            // (aria-valid-attr-value), and the APG allows omitting it while
            // the panel is unrendered.
            aria-controls={selected ? panelId : undefined}
            className={cn(
              "text-sm py-2 px-1 border-b-2 transition-colors shrink-0",
              selected
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground/80",
            )}
          >
            {t.label}
          </button>
        )
      })}
    </div>
  )
}

/**
 * The panel half of the pairing. Wraps whatever the active tab renders, so the
 * tab's `aria-controls` resolves and the panel names itself from the tab —
 * without that, a screen reader announces "tab" and "tab panel" as unrelated
 * regions and switching tabs does not move the reading position (#1978).
 */
export interface CanvasTabPanelProps {
  /** Must match the sibling `CanvasTabs`. */
  idPrefix: string
  /** The currently active tab id. */
  active: string
  className?: string
  children: React.ReactNode
}

export function CanvasTabPanel({ idPrefix, active, className, children }: CanvasTabPanelProps) {
  const { tabId, panelId } = canvasTabIds(idPrefix, active)
  return (
    <div id={panelId} role="tabpanel" aria-labelledby={tabId} className={className}>
      {children}
    </div>
  )
}


/**
 * Two-column "label / control" row used by both Profile/Settings sections.
 * The two canvases each had their own copy with identical markup.
 */
export interface CanvasRowProps {
  label: string
  align?: "center" | "start"
  children: React.ReactNode
}

export function CanvasRow({ label, align = "center", children }: CanvasRowProps) {
  return (
    <div className={cn(
      "grid grid-cols-[180px_1fr] gap-4 px-4 py-2.5",
      align === "center" ? "items-center" : "items-start",
    )}>
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="flex items-center gap-2 min-w-0">{children}</div>
    </div>
  )
}
