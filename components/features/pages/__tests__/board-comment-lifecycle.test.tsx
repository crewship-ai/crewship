import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { BoardComments } from '../board-comments'
import { apiFetch } from '@/lib/api-fetch'
import type { BoardThread } from '@/hooks/use-board-threads'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@/hooks/use-realtime', () => ({ useRealtimeEventSafe: vi.fn() }))
let thread: BoardThread
let stale = false
let lost = false
let denied = false
let lostBeforeCommit = false
const writes: { method: string; url: string; body: Record<string, unknown> }[] = []
const receipts = new Map<string, BoardThread>()
let client: QueryClient
beforeEach(() => {
  stale = false; lost = false; denied = false; lostBeforeCommit = false; writes.length = 0; receipts.clear()
  thread = { id: 'thread', anchor: { schema_version: 1, type: 'board', board_seq: 1 }, status: 'open', version: 1, created_at: '2026-10-13T00:00:00Z', updated_at: '2026-10-13T00:00:00Z', created_by_user_id: 'human', comments: [{ id: 'comment', body: 'Original text', author_user_id: 'human', author_name: 'Alice', created_at: '2026-10-13T00:00:00Z', version: 1, can_edit: true, can_delete: true, can_react: true, can_view_history: true, reactions: [] }] }
  vi.mocked(apiFetch).mockImplementation(async (url, options) => {
    if (denied) return new Response(JSON.stringify({ error: 'Access revoked' }), { status: 403 })
    if (String(url).includes('/history')) return new Response(JSON.stringify({ history: [{ version: 1, action: 'created', body: 'Private historical text', actor_user_id: 'human', created_at: thread.created_at }] }))
    if (!options?.method) return new Response(JSON.stringify({ threads: [thread], can_comment: true, can_resolve: true }))
    const body = JSON.parse(String(options.body)); writes.push({ method: String(options.method), url: String(url), body })
    if (lostBeforeCommit) { lostBeforeCommit = false; throw new Error('Unconfirmed before commit') }
    if (stale) { stale = false; return new Response(JSON.stringify({ code: 'stale_thread', error: 'Changed' }), { status: 409 }) }
    const receipt = receipts.get(body.mutation_id)
    if (receipt) return new Response(JSON.stringify(receipt))
    if (body.base_version !== thread.version) return new Response(JSON.stringify({ code: 'stale_thread' }), { status: 409 })
    thread.version++
    if (options.method === 'PATCH') thread.comments[0].body = body.body
    if (options.method === 'DELETE') { thread.comments[0].body = ''; thread.comments[0].deleted_at = thread.created_at }
    if (options.method === 'PUT') thread.comments[0].reactions = [{ emoji: '👍', count: body.active ? 1 : 0, reacted_by_me: body.active }]
    receipts.set(body.mutation_id, structuredClone(thread))
    if (lost) { lost = false; throw new Error('Lost committed response') }
    return new Response(JSON.stringify(thread))
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
})
afterEach(() => { cleanup(); client.clear() })
function mount() {
  const dirty = vi.fn()
  render(<QueryClientProvider client={client}><BoardComments workspaceId="ws" slug="design" board={{ format: 'crewship-board-excalidraw-0.18.1-v1', seq: 1, can_write: true, records: [], placements: [] }} onDirtyChange={dirty} /></QueryClientProvider>)
  return { dirty, refresh: async () => { await act(async () => { await client.invalidateQueries({ queryKey: ['board-threads'] }) }) } }
}
it('retains edit text and original thread CAS after409 until explicit review of latest', async () => {
  const view = mount()
  fireEvent.click(await screen.findByRole('button', { name: 'Edit comment' }))
  fireEvent.change(screen.getByLabelText('Edit comment text'), { target: { value: 'My preserved edit' } })
  stale = true
  fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
  await screen.findAllByText(/This thread changed/)
  expect(screen.getByLabelText('Edit comment text')).toHaveValue('My preserved edit')
  expect(view.dirty).toHaveBeenLastCalledWith(true)
  thread.version = 2
  await view.refresh()
  expect(screen.getByRole('button', { name: 'Save comment' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Use latest thread version' }))
  fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
  await waitFor(() => expect(screen.queryByLabelText('Edit comment text')).toBeNull())
  expect(writes.map(item => [item.method, item.body.base_version])).toEqual([['PATCH', 1], ['PATCH', 2]])
  expect(writes[0].url).toContain('/board/threads/thread/comments/comment?')
})
it('retries an ambiguous committed edit with exact mutation ID and captured CAS after refresh', async () => {
  const view = mount()
  fireEvent.click(await screen.findByRole('button', { name: 'Edit comment' }))
  fireEvent.change(screen.getByLabelText('Edit comment text'), { target: { value: 'Committed once' } })
  lost = true
  fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
  await screen.findByRole('button', { name: 'Retry unchanged comment update' })
  await view.refresh()
  expect(screen.getByLabelText('Edit comment text')).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Retry unchanged comment update' }))
  await waitFor(() => expect(screen.queryByLabelText('Edit comment text')).toBeNull())
  expect(writes).toHaveLength(2)
  expect(writes[1].body).toEqual(writes[0].body)
  expect(thread.version).toBe(2)
})
it('uses typed reaction/delete routes and removes private history and cache on revocation', async () => {
  const view = mount()
  fireEvent.click(await screen.findByRole('button', { name: 'React 👍' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'React 👍' })).toHaveAttribute('aria-pressed', 'true'))
  expect(writes[0]).toMatchObject({ method: 'PUT', body: { base_version: 1, active: true } })
  expect(writes[0].url).toContain('/reactions/%F0%9F%91%8D?')
  fireEvent.click(screen.getByRole('button', { name: 'View edit history' }))
  await screen.findByText('Private historical text')
  fireEvent.click(screen.getByRole('button', { name: 'Delete comment' }))
  fireEvent.click(screen.getByRole('button', { name: 'Confirm deletion' }))
  await screen.findByText('Comment deleted')
  expect(writes[1]).toMatchObject({ method: 'DELETE', body: { base_version: 2 } })
  fireEvent.click(screen.getByRole('button', { name: 'View edit history' }))
  await screen.findByText('Private historical text')
  denied = true
  await view.refresh()
  await screen.findByText(/Your access to board comments changed/)
  expect(screen.queryByText('Private historical text')).toBeNull()
  expect(client.getQueryData(['board-threads', 'ws', 'design'])).toBeUndefined()
})

it('unlocks preserved edit after an ambiguous attempt receives a definitive409', async () => {
  mount()
  fireEvent.click(await screen.findByRole('button', { name: 'Edit comment' }))
  fireEvent.change(screen.getByLabelText('Edit comment text'), { target: { value: 'Keep after retry refusal' } })
  lostBeforeCommit = true
  fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
  await screen.findByRole('button', { name: 'Retry unchanged comment update' })
  stale = true
  fireEvent.click(screen.getByRole('button', { name: 'Retry unchanged comment update' }))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Retry unchanged comment update' })).toBeNull())
  expect(screen.getByLabelText('Edit comment text')).toBeEnabled()
  expect(screen.getByLabelText('Edit comment text')).toHaveValue('Keep after retry refusal')
})
