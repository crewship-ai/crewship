"use client"
import { useCallback, useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api-fetch'
import { useRealtimeEventSafe } from '@/hooks/use-realtime'
import type { ArtifactRequestTarget } from './use-artifact-requests'
import type { BoardElement } from '@/lib/boards/revision-adapter'
import type { RequestExecution } from '@/lib/pages/request-execution'
export interface BoardRequestGeometry { x: number; y: number; width: number; height: number }
export interface BoardAgentRequest extends RequestExecution {
  id: string; kind: 'diagram' | 'artifact'; status: string; base_seq: number; instruction: string
  selected_element_ids: string[]; selected_placement_ids: string[]; target: BoardRequestGeometry
  agent_id: string; agent_name: string; run_id: string; human_user_id: string
  created_at: string; updated_at: string; error: string; dialect: string; converter_version: string
  source_hash: string; can_insert: boolean; can_cancel: boolean; result_seq: number | null
  result?: { page_slug: string; title: string; revision: number }; inserted_by_user_id?: string
  pin_status?: 'pinned' | 'created_not_pinned' | ''; pin_reason?: string; result_placement_id?: string
}
export interface BoardRequestDetail extends BoardAgentRequest { converter_url?: string; mermaid_source: string; inserted_element_ids: string[]; edited_since_insert: boolean }
export interface BoardRequestDraft { intent_id: string; kind: 'diagram' | 'artifact'; chat_id: string; instruction: string; base_seq: number; selected_element_ids: string[]; selected_placement_ids: string[]; target: BoardRequestGeometry; preview_png?: string }
interface BoardRequests { converter_url?: string; can_dispatch: boolean; eligible_chats: ArtifactRequestTarget[]; requests: BoardAgentRequest[] }
export class BoardRequestError extends Error { constructor(message: string, public status: number) { super(message) } }
export function useBoardRequests(workspace: string, slug: string) {
  const client = useQueryClient()
  const key = ['board-requests', workspace, slug] as const
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/board/requests`
  const params = new URLSearchParams({ workspace_id: workspace })
  const identities = useRef(new Map<string, string>())
  const [denied, setDenied] = useState<BoardRequestError | null>(null)
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: ['board-requests', workspace, slug] }) }, [client, workspace, slug])
  useRealtimeEventSafe('page.updated', invalidate)
  useRealtimeEventSafe('run.completed', invalidate)
  useRealtimeEventSafe('run.failed', invalidate)
  useRealtimeEventSafe('realtime.reconnected', invalidate)
  const request = async <T,>(path: string, options?: RequestInit): Promise<T> => {
    const response = await apiFetch(`${path}?${params}`, options)
    const body = await response.json().catch(() => null)
    if (!response.ok) throw new BoardRequestError(body?.error ?? 'Board agent request unavailable.', response.status)
    return body as T
  }
  useEffect(() => { identities.current.clear(); setDenied(null) }, [workspace, slug])
  const query = useQuery<BoardRequests>({ queryKey: key, enabled: Boolean(workspace && slug) && !denied, gcTime: 0, retry: false, refetchIntervalInBackground: false,
    refetchInterval: state => state.state.data?.requests.some(item => ['queued', 'running'].includes(item.status)) ? 4000 : 60000,
    queryFn: async ({ signal }) => {
      const data = await request<BoardRequests>(endpoint, { signal })
      if (!Array.isArray(data?.requests) || !Array.isArray(data?.eligible_chats)) throw new Error('Invalid board request list.')
      return data
    } })
  const accessDenied = query.error instanceof BoardRequestError && [401,403,404].includes(query.error.status)
  useEffect(() => { if (accessDenied && !denied) { setDenied(query.error as BoardRequestError); identities.current.clear(); client.removeQueries({ queryKey: ['board-requests',workspace,slug], exact: true }) } }, [accessDenied, denied, query.error, client, workspace, slug])
  const mutate = async <T,>(kind: string, payload: object, path: string, identity?: string): Promise<T> => {
    const stamp = JSON.stringify({ workspace, slug, kind, payload, identity })
    if (!identities.current.has(stamp)) {
      if (identities.current.size >= 32) throw new BoardRequestError('Retry an unconfirmed operation before starting another.', 400)
      identities.current.set(stamp, crypto.randomUUID())
    }
    let result: T
    try {
      result = await request<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...payload, [kind === 'send' ? 'submission_id' : 'mutation_id']: identities.current.get(stamp) }), signal: AbortSignal.timeout(20000) })
    } catch (error) {
      // A definite rejection has no pending receipt; transport failures retain it.
      if (error instanceof BoardRequestError && error.status >= 400 && error.status < 500 && error.status !== 408) identities.current.delete(stamp)
      throw error
    }
    const resultId = (result as { id?: unknown } | null)?.id
    if (kind === 'send' && (typeof resultId !== 'string' || !resultId)) throw new Error('The submission result was not confirmed. Retry the unchanged request.')
    identities.current.delete(stamp)
    await client.invalidateQueries({ queryKey: key })
    return result
  }
  const send = useMutation({ retry: false, mutationFn: (draft: BoardRequestDraft) => {
    if (!draft.chat_id || !draft.instruction.trim() || new TextEncoder().encode(draft.instruction).length > 4096) throw new BoardRequestError('Choose a private chat and instructions within 4 KiB.', 400)
    const { intent_id: _intent, ...payload } = draft
    // Include local intent in idempotency identity without sending it as authority.
    return mutate<{id:string}>('send', { ...payload, dialect: 'flowchart-v1', converter_version: '2.2.2' }, endpoint, _intent)
  }, onError: invalidate })
  const cancel = useMutation({ retry: false, mutationFn: (id: string) => request(`${endpoint}/${encodeURIComponent(id)}/cancel`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', signal: AbortSignal.timeout(20000) }), onSuccess: invalidate, onError: invalidate })
  const insert = useMutation({ retry: false, mutationFn: (input: { id: string; base_seq: number; source_hash: string; upserts: {element: BoardElement;expected_rev:0}[] }) => {
    const { id, ...payload } = input
    return mutate<{seq:number;result_seq:number}>('insert', { ...payload, converter_version: '2.2.2' }, `${endpoint}/${encodeURIComponent(id)}/insert`)
  }, onSuccess: async () => { await client.invalidateQueries({ queryKey: ['page-board', workspace, slug] }) }, onError: invalidate })
  const detail = (id: string) => request<BoardRequestDetail>(`${endpoint}/${encodeURIComponent(id)}`, { signal: AbortSignal.timeout(20000) })
  return { query: { ...query, data: accessDenied || denied ? undefined : query.data, error: denied ?? query.error }, send, cancel, insert, detail }
}
