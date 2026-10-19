"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { canonicalSlideDeck, slideDeckHashDomain, type SlideDeck } from "@/lib/slides/model"
import { isNativeSlideDeck } from "@/components/features/slides/native-model"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export interface PageSlidesInput {
  workspaceId: string
  pageId: string
  currentUserId: string | null | undefined
  /** Caller-generated identity/open epoch; never a credential. */
  activationKey: string
  enabled: boolean
}
export interface SavedSlidesState {
  page_id: string
  schema_version: 1
  revision: number
  order_version: number
  content_hash: string
  deck: SlideDeck
}
export type PageSlidesRead = { status: "loading" | "unavailable" | "denied" } | { status: "ready"; state: SavedSlidesState }
const nonempty = (value: unknown, max = 128): value is string => typeof value === "string" && value.trim().length > 0 && value.length <= max
const pageIdentity = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value)
const positive = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0
const fields = ["page_id", "schema_version", "revision", "order_version", "content_hash", "deck"]

async function admittedState(value: unknown, pageId: string): Promise<SavedSlidesState | undefined> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return
  const state = value as Record<string, unknown>
  if (Object.keys(state).length !== fields.length || !fields.every(field => Object.hasOwn(state, field))
    || state.page_id !== pageId || state.schema_version !== 1 || !positive(state.revision) || !positive(state.order_version)
    || typeof state.content_hash !== "string" || !/^[a-f0-9]{64}$/.test(state.content_hash)
    || !isNativeSlideDeck(state.deck) || state.deck.deck_id !== pageId) return
  // Reuse the Go-parity canonical serializer and domain, never ordinary JSON
  // serialization. Missing browser digest support cannot admit unverified data.
  if (!globalThis.crypto?.subtle) return
  const bytes = new TextEncoder().encode(slideDeckHashDomain(1) + canonicalSlideDeck(state.deck))
  const digest = await globalThis.crypto.subtle.digest("SHA-256", bytes)
  const hash = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")
  if (hash !== state.content_hash) return
  return state as unknown as SavedSlidesState
}

/** Current saved schema-1 content only. No editor state, write or query cache. */
export function usePageSlides({ workspaceId, pageId, currentUserId, activationKey, enabled }: PageSlidesInput) {
  const [generation, setGeneration] = useState(0)
  const refresh = useCallback(() => setGeneration(value => value + 1), [])
  useRealtimeEventSafe("page.updated", refresh)
  useRealtimeEventSafe("page.deleted", refresh)
  useRealtimeEventSafe("realtime.reconnected", refresh)
  const available = enabled === true && nonempty(workspaceId) && pageIdentity(pageId) && nonempty(currentUserId) && nonempty(activationKey, 1024)
  const scopeKey = JSON.stringify([workspaceId, pageId, currentUserId, activationKey, enabled === true, generation])
  const [settled, setSettled] = useState<{ scopeKey: string; read: PageSlidesRead }>()

  useEffect(() => {
    let active = true
    const controller = new AbortController()
    const current = () => active && !controller.signal.aborted
    setSettled({ scopeKey, read: { status: available ? "loading" : "unavailable" } })
    if (available) {
      void (async () => {
        try {
          const query = new URLSearchParams({ workspace_id: workspaceId })
          const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/slides?${query}`, { signal: controller.signal, cache: "no-store" })
          if (!current()) return
          if (!response.ok) {
            setSettled({ scopeKey, read: { status: [401, 403, 404].includes(response.status) ? "denied" : "unavailable" } })
            return
          }
          const value: unknown = await response.json()
          if (!current()) return
          const state = await admittedState(value, pageId)
          if (!current()) return
          setSettled({ scopeKey, read: state ? { status: "ready", state } : { status: "unavailable" } })
        } catch {
          if (current()) setSettled({ scopeKey, read: { status: "unavailable" } })
        }
      })()
    }
    return () => { active = false; controller.abort() }
  }, [scopeKey, available, pageId, workspaceId])

  // Scope changes conceal old data during render, before effect cleanup runs.
  const read: PageSlidesRead = !available ? { status: "unavailable" }
    : settled?.scopeKey === scopeKey ? settled.read : { status: "loading" }
  return { scopeKey, read }
}
