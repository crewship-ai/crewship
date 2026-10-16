import { act, cleanup, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { afterEach, expect, it, vi } from 'vitest'
import { apiFetch } from '@/lib/api-fetch'
import { useBoardRequests } from '../use-board-requests'

vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@/hooks/use-realtime', () => ({ useRealtimeEventSafe: vi.fn() }))
afterEach(() => { cleanup(); vi.clearAllMocks() })

it('does not count definite rejections as unconfirmed submissions', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  let writes = 0
  vi.mocked(apiFetch).mockImplementation(async (_url, options) => {
    if (options?.method === 'POST') {
      writes++
      return new Response(JSON.stringify({ error: 'Board changed; review it.' }), { status: 409 })
    }
    return new Response(JSON.stringify({ requests: [], eligible_chats: [], can_dispatch: true }))
  })
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const { result } = renderHook(() => useBoardRequests('workspace', 'board'), { wrapper })
  for (let index = 0; index < 34; index++) {
    await act(async () => {
      await expect(result.current.send.mutateAsync({ intent_id: `intent-${index}`, kind: 'artifact', chat_id: 'chat', instruction: 'Build this screen', base_seq: index, selected_element_ids: [], selected_placement_ids: [], target: { x: 0, y: 0, width: 640, height: 480 } })).rejects.toThrow('Board changed; review it.')
    })
  }
  expect(writes).toBe(34)
  client.clear()
})
