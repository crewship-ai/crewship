"use client"
import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { SlidesAPIError } from "./use-page-slides"
import { validSelectedPage, type SelectedAdmission, type SelectedRequestPage, type SelectedSummary } from "@/lib/slides/selected"

export interface SelectedAttempt { kind: "admit" | "cancel"; path: string; body: SelectedAdmission | { page_id: string; schema_version: 1; mutation_id: string; if_version: number }; requestId?: string }
export function useSlidesSelected(workspace: string, slug: string, pageId: string) {
 const [page, setPage] = useState<SelectedRequestPage>(), [items, setItems] = useState<SelectedSummary[]>([])
 const [busy, setBusy] = useState(false), [pending, setPending] = useState<SelectedAttempt>(), [blocked, setBlocked] = useState(false), [error, setError] = useState<string>(), [status, setStatus] = useState("Loading agent requests…")
 const attempt = useRef<SelectedAttempt | undefined>(undefined), generation = useRef(0), readGeneration = useRef(0), inFlight = useRef(false)
 const request = useCallback(async (path: string, body?: unknown, cursor?: string) => {
  const query = new URLSearchParams({ workspace_id: workspace, limit: "25" }); if (cursor) query.set("cursor", cursor)
  const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}/slides/requests${path}?${query}`, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  const value = await response.json().catch(() => null)
  if (!response.ok) throw new SlidesAPIError(value?.error ?? `Agent request failed (${response.status}).`, response.status)
  return value
 }, [workspace, slug])
 const fail = useCallback((cause: unknown) => {
  if (cause instanceof SlidesAPIError && [401, 403, 404].includes(cause.status)) { readGeneration.current++; currentPage.current = undefined; setPage(undefined); setItems([]); setBlocked(true) }
  setError(cause instanceof Error ? cause.message : "Agent request unavailable.")
 }, [])
 const currentPage = useRef<SelectedRequestPage | undefined>(undefined)
 const refresh = useCallback(async (cursor?: string) => {
  const lifecycle = generation.current, read = ++readGeneration.current
  try {
   const value: unknown = await request("", undefined, cursor)
   if (lifecycle !== generation.current || read !== readGeneration.current) return
   if (!validSelectedPage(value, pageId)) throw new Error("Unsupported agent request response.")
   if (cursor && (!currentPage.current || currentPage.current.next_cursor !== cursor || currentPage.current.watermark !== value.watermark)) throw new Error("Agent request continuation changed. Refresh from the beginning.")
   currentPage.current = value
   setPage(value); setItems(previous => cursor ? [...previous, ...value.items.filter(item => !previous.some(old => old.id === item.id))] : value.items)
   if (!attempt.current) { setBlocked(false); setError(undefined); setStatus("Agent requests reflect their reported saved revisions.") }
  } catch (cause) { if (lifecycle === generation.current && read === readGeneration.current) fail(cause) }
 }, [request, pageId, fail])
 const refreshLatest = useRef(refresh); refreshLatest.current = refresh
 const invalidate = useCallback(() => { generation.current++; readGeneration.current++; attempt.current = undefined; inFlight.current = false }, [])
 useEffect(() => { invalidate(); currentPage.current = undefined; setPending(undefined); setItems([]); setPage(undefined); setBusy(false); setBlocked(false); void refreshLatest.current(); return invalidate }, [workspace, pageId, invalidate])
 async function send(proposed?: SelectedAttempt) {
  if (inFlight.current || blocked) return false
  const selected = attempt.current ?? proposed
  if (!selected || selected.body.page_id !== pageId) return false
  const lifecycle = generation.current
  attempt.current = structuredClone(selected); setPending(attempt.current); inFlight.current = true; setBusy(true); setError(undefined)
  try {
   const ack = await request(selected.path, selected.body)
   if (lifecycle !== generation.current) return false
   const expected = selected.kind === "cancel" && "if_version" in selected.body ? selected.body.if_version + 1 : undefined
   if (!ack || typeof ack.request_id !== "string" || !/^[a-zA-Z0-9_-]{1,128}$/.test(ack.request_id) || !Number.isSafeInteger(ack.version) || ack.version < 1 || ack.version > 8 || typeof ack.status !== "string" || typeof ack.duplicate !== "boolean" || (selected.kind === "admit" ? ack.id !== ack.request_id : ack.request_id !== selected.requestId || ack.version !== expected || !("mutation_id" in selected.body) || ack.mutation_id !== selected.body.mutation_id)) throw new Error("Invalid acknowledgement. Retry the original agent request.")
   attempt.current = undefined; setPending(undefined); setStatus(`Agent request ${ack.status}; original submission confirmed.`); await refresh(); return true
  } catch (cause) {
   if (lifecycle !== generation.current) return false
   if (cause instanceof SlidesAPIError && cause.status >= 400 && cause.status < 500 && ![408, 429].includes(cause.status)) { setBlocked(true); setStatus("Request rejected or conflicted. Original payload retained; no automatic rebase.") }
   else setStatus("Outcome uncertain. Retry sends the exact original payload and submission ID.")
   fail(cause); return false
  } finally { if (lifecycle === generation.current) { inFlight.current = false; setBusy(false) } }
 }
 async function discard() { if (inFlight.current) return; attempt.current = undefined; setPending(undefined); setBlocked(false); await refresh() }
 return { page, items, busy, pending, blocked, error, status, refresh, send, discard }
}
