import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ArtifactResultCards } from '../artifact-result-cards'
import { apiFetch } from '@/lib/api-fetch'
import type { ArtifactChatResult } from '@/hooks/use-artifact-chat-results'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@/hooks/use-realtime', () => ({ useRealtimeEventSafe: vi.fn() }))
let results: ArtifactChatResult[]
let status: number
const clients: QueryClient[] = []
beforeEach(() => {
  results = [{ page_slug: 'report', title: 'Saved report', revision: 2, created_at: '2026-10-13T18:00:00Z', agent_id: 'agent', agent_name: 'Ada', run_id: 'verified-run', on_behalf_of_user_id: 'human' }]
  status = 200
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify(status === 200 ? { results } : { error: 'Unavailable' }), { status }))
})
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.clearAllMocks() })
function mount(chatId = 'chat') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client)
  const content = (id: string) => <QueryClientProvider client={client}><ArtifactResultCards key={id} workspaceId="ws" chatId={id} isStreaming={false} currentUserId="human" /></QueryClientProvider>
  const view = render(content(chatId))
  return { client, select: (id: string) => view.rerender(content(id)) }
}
it('shows only verified exact revision links with truthful agent/on-behalf provenance and no writes', async () => {
  mount()
  expect((await screen.findByRole('link', { name: 'Open Saved report, revision 2' })).getAttribute('href')).toBe('/pages/report?revision=2')
  expect(screen.getByText('Revision 2 · Ada')).toBeTruthy()
  expect(screen.getByText('On behalf of you')).toBeTruthy()
  expect(vi.mocked(apiFetch).mock.calls.every(call => !call[1]?.method)).toBe(true)
})
it('replaces cards after a successful filtered list without retaining hidden results', async () => {
  const view = mount(); await screen.findByText('Saved report'); results = []
  await act(async () => { await view.client.invalidateQueries({ queryKey: ['artifact-chat-results'] }) })
  await waitFor(() => expect(screen.queryByText('Saved report')).toBeNull())
})
it('clears private card cache after authorization refusal', async () => {
  const view = mount(); await screen.findByText('Saved report'); status = 403
  await act(async () => { await view.client.invalidateQueries({ queryKey: ['artifact-chat-results'] }) })
  await waitFor(() => expect(screen.queryByText('Saved report')).toBeNull())
  expect(view.client.getQueryData(['artifact-chat-results', 'ws', 'chat'])).toEqual({ results: [] })
})
it('preserves the last authorized card on transient refresh failure and marks it stale', async () => {
  const view = mount(); await screen.findByText('Saved report'); status = 500
  await act(async () => { await view.client.invalidateQueries({ queryKey: ['artifact-chat-results'] }) })
  expect(screen.getByText('Saved report')).toBeTruthy()
  expect(await screen.findByText(/Showing the last authorized results/)).toBeTruthy()
})
it('never carries cards across selected chats or infers a publication from incomplete metadata', async () => {
  const view = mount(); await screen.findByText('Saved report')
  results = [{ ...results[0], run_id: '' }]; view.select('other-chat')
  await act(async () => { await view.client.refetchQueries({ queryKey: ['artifact-chat-results', 'ws', 'other-chat'] }) })
  await waitFor(() => expect(screen.queryByText('Saved report')).toBeNull())
})
