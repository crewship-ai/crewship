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
  onChanged: () => void
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
    onChanged()
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

export function CanvasShell({ loading, error, notLoadedLabel, children }: CanvasShellProps) {
  if (loading) {
    return (
      <div className="px-6 md:px-8 lg:px-12 py-6 max-w-[1180px] mx-auto w-full">
        <Skeleton className="h-[600px] w-full rounded-xl" />
      </div>
    )
  }
  if (error) {
    return (
      <div className="px-6 md:px-8 lg:px-12 py-12 max-w-[1180px] mx-auto w-full text-center">
        <p className="text-sm text-red-300 mb-2">{notLoadedLabel}</p>
        <p className="text-xs text-muted-foreground">{error}</p>
      </div>
    )
  }
  return (
    <div className="px-6 md:px-8 lg:px-12 py-6 space-y-6 max-w-[1180px] mx-auto w-full">
      {children}
    </div>
  )
}


/**
 * Tab strip rendered under the canvas header. Generic over the tab id so the
 * caller's `tab` state stays strongly typed (`CrewTab` / `AgentTab`).
 */
export interface CanvasTabsProps<TTab extends string> {
  tabs: ReadonlyArray<{ id: TTab; label: string }>
  active: TTab
  onChange: (tab: TTab) => void
}

export function CanvasTabs<TTab extends string>({ tabs, active, onChange }: CanvasTabsProps<TTab>) {
  return (
    <div className="flex items-center gap-5 border-b border-white/8 -mx-6 md:-mx-8 lg:-mx-12 px-6 md:px-8 lg:px-12 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
      {tabs.map((t) => (
        <button
          key={t.id}
          type="button"
          onClick={() => onChange(t.id)}
          aria-selected={active === t.id}
          className={cn(
            "text-sm py-2 px-1 border-b-2 transition-colors shrink-0",
            active === t.id
              ? "border-blue-400 text-foreground"
              : "border-transparent text-muted-foreground hover:text-foreground/80",
          )}
        >
          {t.label}
        </button>
      ))}
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
