import { cleanup, render, screen, act, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { PageRequestResult } from '../page-request-result'
import { apiFetch } from '@/lib/api-fetch'
import type { InboxItem } from '@/hooks/use-inbox'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
const clients: QueryClient[] = []
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.resetAllMocks() })
const item = { id: 'notice', workspace_id: 'ws', kind: 'message', payload: { subkind: 'page_request_result', request_kind: 'artifact', request_id: 'request', page_slug: 'untrusted', result_revision: 999 } } as unknown as InboxItem
function mount(value = item) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client)
  render(<QueryClientProvider client={client}><PageRequestResult item={value} /></QueryClientProvider>)
  return client
}
it('resolves the current authorized result instead of trusting notification slugs or revisions', async () => {
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ page_slug: 'report', revision: 2 })))
  mount()
  expect((await screen.findByRole('link', { name: 'View result' })).getAttribute('href')).toBe('/pages/report?revision=2')
  expect(apiFetch).toHaveBeenCalledWith('/api/v1/page-request-results/artifact/request?workspace_id=ws', expect.objectContaining({ signal: expect.any(AbortSignal) }))
})
it('removes the link when live access is revoked', async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({ page_slug: 'report' })))
  const client = mount()
  await screen.findByRole('link', { name: 'View result' })
  vi.mocked(apiFetch).mockResolvedValue(new Response('{}', { status: 404 }))
  await act(async () => { await client.invalidateQueries({ queryKey: ['page-request-result'] }) })
  await waitFor(() => expect(screen.queryByRole('link')).toBeNull())
  expect(screen.getByText(/no longer have access/)).toBeTruthy()
})
it('ignores malformed pointers and never fetches arbitrary payload URLs', () => {
  mount({ ...item, payload: { ...item.payload, request_kind: 'https://external.test' } })
  expect(apiFetch).not.toHaveBeenCalled()
  expect(screen.queryByRole('link')).toBeNull()
})
