import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { apiFetch } from '@/lib/api-fetch'
import { BOARD_FORMAT } from '@/lib/boards/compatibility'
import { usePageBoard } from '../use-page-board'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@/hooks/use-realtime', () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock('@/hooks/use-pages', () => ({ pagesKeys: { all: (workspace: string) => ['pages', workspace] } }))
afterEach(() => { cleanup(); vi.resetAllMocks() })
it('keeps the supported cached scene read-only and refuses POST after discovering a future profile', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const saved = { format: BOARD_FORMAT, seq: 4, can_write: true, records: [{ element_id: 'shape', element_rev: 3, element: { id: 'shape', x: 40 }, deleted: false }], placements: [] }
  let response = saved
  vi.mocked(apiFetch).mockImplementation(async () => Response.json(response))
  const hook = renderHook(() => usePageBoard('ws', 'design'), { wrapper })
  await waitFor(() => expect(hook.result.current.query.data?.seq).toBe(4))
  response = { ...saved, format: 'crewship-board-excalidraw-0.19.0-v2', seq: 5 }
  const encoded = JSON.stringify(response)
  await act(async () => { await hook.result.current.query.refetch() })
  await waitFor(() => expect(hook.result.current.query.error?.message).toContain('compatible Crewship release'))
  expect(hook.result.current.query.data).toEqual({ ...saved, can_write: false })
  await act(async () => { await expect(hook.result.current.write.mutateAsync({ base_seq: 4, operation: { kind: 'batch', batch: { base_seq: 4, upserts: [], deletes: [] } } })).rejects.toThrow('unsupported saved format') })
  expect(vi.mocked(apiFetch).mock.calls.every(([, options]) => !options?.method)).toBe(true)
  expect(JSON.stringify(response)).toBe(encoded)
  expect(client.getQueryData(['page-board', 'ws', 'design'])).toEqual(saved)
  client.clear()
})
