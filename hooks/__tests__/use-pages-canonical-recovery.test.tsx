import { act, cleanup, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useEffect, type ReactNode } from 'react'
import { apiFetch } from '@/lib/api-fetch'
import { usePageArtifact, type PageArtifact } from '../use-page-artifact'

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
beforeEach(() => { vi.useFakeTimers(); events.clear(); vi.mocked(apiFetch).mockReset() })
afterEach(() => { cleanup(); for (const client of clients.splice(0)) client.clear(); vi.useRealTimers() })

describe('canonical Pages recovery without a durable event log', () => {
  it.each(['artifact'] as const)('%s converges by polling when the final update event is entirely dropped', async () => {
    let canonical = artifact(1)
    vi.mocked(apiFetch).mockImplementation(async () => Response.json(canonical))
    const hook = renderHook(() => usePageArtifact('ws', 'report').query, { wrapper: wrapper() })
    await settle(); expect(hook.result.current.data).toEqual(canonical)
    canonical = artifact(2)
    // No page.updated and no reconnect: the final notification is lost.
    await poll(); expect(hook.result.current.data).toEqual(canonical)
  })

  it.each(['artifact'] as const)('%s repairs a stale initial snapshot racing an update, then reconnects to current state', async () => {
    const old = artifact(1)
    let canonical = old
    let release!: (response: Response) => void
    vi.mocked(apiFetch).mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
      .mockImplementation(async () => Response.json(canonical))
    const hook = renderHook(() => usePageArtifact('ws', 'report').query, { wrapper: wrapper() })
    // Snapshot already captured; server changes before that response arrives.
    canonical = artifact(2)
    await emit('page.updated')
    await act(async () => release(Response.json(old))); await settle()
    await poll(); expect(hook.result.current.data).toEqual(canonical)
    canonical = artifact(3)
    await emit('realtime.reconnected'); expect(hook.result.current.data).toEqual(canonical)
  })

})
