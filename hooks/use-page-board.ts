"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { pagesKeys } from "@/hooks/use-pages"
import type { BoardElement, BoardBatch } from "@/lib/boards/revision-adapter"
export interface PageBoardRecord { author_user_id?: string; author_agent_id?: string; author_run_id?: string; element_id: string; element_rev: number; element: BoardElement | null; deleted: boolean }
export interface BoardPlacement { mode?: "fixed" | "follow_latest"; id: string; placement_rev?: number; x: number; y: number; width: number; height: number; source?: { page_slug: string; revision: number; pinned_revision?: number; title: string } }
export interface PageBoard { format: string; seq: number; records: PageBoardRecord[]; placements: BoardPlacement[]; placement_tombstones?: { id: string; placement_rev: number; deleted: true }[]; can_write: boolean }
export class PageBoardError extends Error { constructor(message: string, public status: number) { super(message) } }
export type BoardWrite = { kind: "batch"; batch: BoardBatch } | { kind: "pin"; source_page_slug: string; source_revision: number; x: number; y: number; width: number; height: number } | { kind: "unpin"; id: string }
export function usePageBoard(workspace: string, slug: string) {
  const client = useQueryClient()
  const key = ["page-board", workspace, slug] as const
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/board`
  const params = new URLSearchParams({ workspace_id: workspace })
  const [accessError, setAccessError] = useState<PageBoardError | null>(null)
  const pending = useRef(new Map<string, string>())
  const receipt = useRef<import("@/lib/boards/command-history").BoardReceipt | null>(null)
  const [uncertain, setUncertain] = useState(false)
  useEffect(() => { pending.current.clear(); receipt.current = null; setUncertain(false); setAccessError(null) }, [workspace, slug])
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: ["page-board", workspace, slug] }) }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const read = async (signal?: AbortSignal): Promise<PageBoard> => {
    const response = await apiFetch(`${endpoint}?${params}`, { signal })
    const body = await response.json().catch(() => null)
    if (!response.ok) throw new PageBoardError(body?.error ?? "Could not load this board.", response.status)
    if (!Array.isArray(body?.records) || !Array.isArray(body?.placements)) throw new Error("Invalid board response.")
    setAccessError(null)
    return body
  }
  const query = useQuery({ queryKey: key, queryFn: ({ signal }) => read(signal), enabled: Boolean(workspace && slug) && !accessError, retry: false, gcTime: 0, refetchInterval: 60000, refetchIntervalInBackground: false })
  const denied = query.error instanceof PageBoardError && [401, 403, 404].includes(query.error.status)
  useEffect(() => { if (denied && !accessError) { setAccessError(query.error as PageBoardError); pending.current.clear(); client.removeQueries({ queryKey: ["page-board", workspace, slug], exact: true }) } }, [denied, accessError, query.error, client, workspace, slug])
  const write = useMutation({
    retry: false,
    mutationFn: async (input: { base_seq: number; operation: BoardWrite }) => {
      const stamp = JSON.stringify({ workspace, slug, input })
      if (!pending.current.has(stamp)) {
        if (pending.current.size >= 32) throw new Error("Too many unconfirmed changes. Retry an earlier save before creating another.")
        pending.current.set(stamp, crypto.randomUUID())
      }
      receipt.current = null
      setUncertain(true)
      const { operation } = input
      const path = operation.kind === "batch" ? `${endpoint}/batches` : operation.kind === "pin" ? `${endpoint}/placements` : `${endpoint}/placements/${encodeURIComponent(operation.id)}`
      const payload = operation.kind === "batch" ? operation.batch : operation.kind === "pin" ? { source_page_slug: operation.source_page_slug, source_revision: operation.source_revision, x: operation.x, y: operation.y, width: operation.width, height: operation.height, base_seq: input.base_seq } : { base_seq: input.base_seq }
      const response = await apiFetch(`${path}?${params}`, { method: operation.kind === "unpin" ? "DELETE" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...payload, mutation_id: pending.current.get(stamp) }), signal: AbortSignal.timeout(20000) })
      const body = await response.json().catch(() => null)
      if (!response.ok) { if (response.status < 500) setUncertain(false); throw new PageBoardError(body?.error ?? "Could not save the board. Retry the same save to check its outcome.", response.status) }
      // Retain identity until the authoritative read also succeeds. A lost GET
      // must not turn a committed but uncertain save into a second mutation.
      const canonical = await read()
      receipt.current = body
      setUncertain(false)
      pending.current.delete(stamp)
      client.setQueryData(key, canonical)
      await client.invalidateQueries({ queryKey: pagesKeys.all(workspace) })
      return canonical
    },
    onError: error => { if (error instanceof PageBoardError && [401, 403, 404, 409].includes(error.status)) invalidate() },
  })
  return { query: { ...query, data: denied || accessError ? undefined : query.data, error: accessError ?? query.error }, write, receipt, uncertain }
}
