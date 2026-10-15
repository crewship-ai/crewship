import { act, cleanup, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useEffect, type ReactNode } from 'react'
import { apiFetch } from '@/lib/api-fetch'
import { usePageArtifact, type PageArtifact } from '../use-page-artifact'
import { usePageBoard, type PageBoard } from '../use-page-board'
import { planBoardMerge } from '@/lib/boards/collaboration-plan'
import type { BoardElement } from '@/lib/boards/revision-adapter'

// Deterministic hook integration: real QueryClient/query timers and production
// merge planner, simulated HTTP transport/event delivery. Not browser auth or
// a measurement of production latency.
const events = vi.hoisted(() => new Map<string, Set<() => void>>())
vi.mock('@/hooks/use-realtime', () => ({ useRealtimeEventSafe: (name: string, callback: () => void) => {
  useEffect(() => {
    const callbacks = events.get(name) ?? new Set<() => void>(); callbacks.add(callback); events.set(name, callbacks)
    return () => { callbacks.delete(callback) }
  }, [name, callback])
} }))
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@/hooks/use-pages', () => ({ pagesKeys: { all: (workspace: string) => ['pages', workspace] } }))
const clients: QueryClient[] = []
function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client)
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
}
async function settle() { await act(async () => { await vi.advanceTimersByTimeAsync(1) }) }
async function poll() { await act(async () => { await vi.advanceTimersByTimeAsync(60_001) }); await settle() }
async function emit(name: string) { await act(async () => { for (const callback of events.get(name) ?? []) callback() }); await settle() }
const artifact = (revision: number): PageArtifact => ({ head_rev: revision, revision, html: `revision ${revision}`, revisions: [], can_write: true })
const element = (id: string, x: number) => ({ id, type: 'rectangle', x, y: 0, width: 100, height: 100, groupIds: [], frameId: null }) as BoardElement
const board = (seq: number, x = 0): PageBoard => ({ format: 'crewship-board-excalidraw-0.18.1-v1', seq, can_write: true, placements: [], records: [{ element_id: 'shape', element_rev: seq, element: element('shape', x), deleted: false }] })
beforeEach(() => { vi.useFakeTimers(); events.clear(); vi.mocked(apiFetch).mockReset() })
afterEach(() => { cleanup(); for (const client of clients.splice(0)) client.clear(); vi.useRealTimers() })

describe('canonical Pages recovery without a durable event log', () => {
  it.each(['artifact', 'board'] as const)('%s converges by polling when the final update event is entirely dropped', async kind => {
    let canonical = kind === 'artifact' ? artifact(1) : board(1)
    vi.mocked(apiFetch).mockImplementation(async () => Response.json(canonical))
    const useRecovery = kind === 'artifact' ? usePageArtifact : usePageBoard
    const hook = renderHook(() => useRecovery('ws', 'report').query, { wrapper: wrapper() })
    await settle(); expect(hook.result.current.data).toEqual(canonical)
    canonical = kind === 'artifact' ? artifact(2) : board(2, 40)
    // No page.updated and no reconnect: the final notification is lost.
    await poll(); expect(hook.result.current.data).toEqual(canonical)
  })

  it.each(['artifact', 'board'] as const)('%s repairs a stale initial snapshot racing an update, then reconnects to current state', async kind => {
    const old = kind === 'artifact' ? artifact(1) : board(1)
    let canonical = old
    let release!: (response: Response) => void
    vi.mocked(apiFetch).mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
      .mockImplementation(async () => Response.json(canonical))
    const useRecovery = kind === 'artifact' ? usePageArtifact : usePageBoard
    const hook = renderHook(() => useRecovery('ws', 'report').query, { wrapper: wrapper() })
    // Snapshot already captured; server changes before that response arrives.
    canonical = kind === 'artifact' ? artifact(2) : board(2, 20)
    await emit('page.updated')
    await act(async () => release(Response.json(old))); await settle()
    await poll(); expect(hook.result.current.data).toEqual(canonical)
    canonical = kind === 'artifact' ? artifact(3) : board(3, 30)
    await emit('realtime.reconnected'); expect(hook.result.current.data).toEqual(canonical)
  })

  it('reconnect canonical state merges disjoint remote work while preserving the dirty local drawing', async () => {
    const base = board(1)
    base.records.push({ element_id: 'remote', element_rev: 1, element: element('remote', 200), deleted: false })
    let canonical = base
    vi.mocked(apiFetch).mockImplementation(async () => Response.json(canonical))
    const hook = renderHook(() => usePageBoard('ws', 'report'), { wrapper: wrapper() })
    await settle()
    const local = { elements: [element('shape', 70), element('remote', 200)], placements: [] }
    canonical = structuredClone(base); canonical.seq = 2
    canonical.records[1] = { ...canonical.records[1], element_rev: 2, element: element('remote', 240) }
    await emit('realtime.reconnected')
    const plan = planBoardMerge(base, local, hook.result.current.query.data!)
    expect(plan.kind).toBe('merged')
    expect(plan.draft.elements.find(item => item.id === 'shape')?.x).toBe(70)
    expect(plan.draft.elements.find(item => item.id === 'remote')?.x).toBe(240)
    expect(local.elements.find(item => item.id === 'remote')?.x).toBe(200)
  })

  it('polling does not mutate a local dirty Board proposal and the production merge planner detects overlap', async () => {
    const base = board(1)
    let canonical = base
    vi.mocked(apiFetch).mockImplementation(async () => Response.json(canonical))
    const hook = renderHook(() => usePageBoard('ws', 'report'), { wrapper: wrapper() })
    await settle()
    const local = { elements: [element('shape', 70)], placements: [] }
    const savedDraft = structuredClone(local)
    canonical = board(2, 40)
    await poll()
    expect(hook.result.current.query.data).toEqual(canonical)
    const plan = planBoardMerge(base, local, hook.result.current.query.data!)
    expect(plan.kind).toBe('conflict')
    expect(plan.draft).toEqual(savedDraft)
    expect(local).toEqual(savedDraft)
    expect(vi.mocked(apiFetch).mock.calls.every(([, init]) => !init?.method || init.method === 'GET')).toBe(true)
  })
})
