import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { BoardAgentWork } from '../board-agent-work'
import { apiFetch } from '@/lib/api-fetch'
import type { PageBoard } from '@/hooks/use-page-board'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@/hooks/use-realtime', () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock('@/lib/boards/diagram-frame', () => ({ boardConversionSupported: () => true }))
vi.mock('@/lib/boards/diagram', () => ({ convertBoardDiagram: vi.fn(async () => [{ id: 'generated', type: 'rectangle', x: 80, y: 80 }]) }))
const board: PageBoard = { format: 'test', seq: 4, can_write: true, records: [{ element_id: 'shape', element_rev: 1, deleted: false, element: { id: 'shape', type: 'rectangle', x: 20, y: 30 } }], placements: [] }
let client: QueryClient
let denied = false
let lost = false
let rows: Record<string, unknown>[] = []
const writes: { path: string; body: Record<string, unknown> }[] = []
beforeEach(() => {
  denied = false; lost = false; rows = []; writes.length = 0
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  vi.mocked(apiFetch).mockImplementation(async (url, options) => {
    const path = new URL(String(url), 'https://studio.test').pathname
    if (denied) return new Response(JSON.stringify({ error: 'Access denied' }), { status: 403 })
    if (options?.method === 'POST') {
      const body = JSON.parse(String(options.body)); writes.push({ path, body })
      if (path.endsWith('/insert')) return new Response(JSON.stringify({ seq: 5, result_seq: 5 }))
      if (path.endsWith('/cancel')) return new Response(JSON.stringify({ id: 'job', status: 'cancelled' }))
      if (!rows.length) rows.push({ id: 'job', kind: 'diagram', status: 'queued', base_seq: body.base_seq, instruction: body.instruction, agent_name: 'Builder', selected_element_ids: body.selected_element_ids, selected_placement_ids: [], target: body.target })
      if (lost) { lost = false; throw new Error('Lost send response') }
      return new Response(JSON.stringify({ id: 'job' }), { status: 202 })
    }
    if (path.endsWith('/job')) return new Response(JSON.stringify({ ...rows[0], mermaid_source: 'flowchart TD\nA --> B', source_hash: 'digest', dialect: 'flowchart-v1', converter_version: '2.2.2', can_insert: true }))
    return new Response(JSON.stringify({ can_dispatch: true, eligible_chats: [{ chat_id: 'private', title: 'My private chat', agent_id: 'agent', agent_name: 'Builder' }], requests: rows }))
  })
})
afterEach(() => { cleanup(); client.clear(); vi.clearAllMocks() })
function mount() { const dirty = vi.fn(); const view = render(<QueryClientProvider client={client}><BoardAgentWork workspaceId="ws" slug="design" board={board} selectedIds={['shape']} disabled={false} onDirtyChange={dirty} /></QueryClientProvider>); return { ...view, dirty } }
it('requires explicit handoff and retains exact revision selection and submission on lost response', async () => {
  const view = mount()
  fireEvent.click(await screen.findByRole('button', { name: 'Generate diagram' }))
  expect(writes).toHaveLength(0)
  fireEvent.change(screen.getByLabelText('Private chat for board request'), { target: { value: 'private' } })
  fireEvent.change(screen.getByLabelText('Board request instructions'), { target: { value: 'Diagram this flow' } })
  expect(view.dirty).toHaveBeenLastCalledWith(true)
  lost = true
  fireEvent.click(screen.getByRole('button', { name: 'Send request' }))
  await screen.findByText(/Lost send response/)
  expect(screen.getByLabelText('Board request instructions')).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Retry submission' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(writes).toHaveLength(2); expect(writes[0].body).toEqual(writes[1].body)
  expect(writes[0].body).toMatchObject({ base_seq: 4, selected_element_ids: ['shape'], selected_placement_ids: [], submission_id: expect.any(String), dialect: 'flowchart-v1', converter_version: '2.2.2' })
  expect(rows).toHaveLength(1)
})
it('requires insertion approval after a ready result and uses current exact Board sequence', async () => {
  rows = [{ id: 'job', kind: 'diagram', status: 'ready_to_insert', can_insert: true, base_seq: 3, instruction: 'Ready drawing', agent_name: 'Builder', target: { x: 80, y: 80, width: 640, height: 420 } }]
  mount()
  const insert = await screen.findByRole('button', { name: 'Insert diagram' })
  expect(writes).toHaveLength(0)
  fireEvent.click(insert)
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatchObject({ path: '/api/v1/pages/design/board/requests/job/insert', body: { base_seq: 4, source_hash: 'digest', converter_version: '2.2.2', mutation_id: expect.any(String), upserts: [{ expected_rev: 0, element: { id: 'generated' } }] } })
})
it('removes authorized cached job content after access is denied', async () => {
  rows = [{ id: 'job', kind: 'artifact', status: 'running', base_seq: 4, instruction: 'Private instruction', agent_name: 'Builder' }]
  mount(); await screen.findByText('Private instruction')
  denied = true
  await act(async () => { await client.invalidateQueries({ queryKey: ['board-requests'] }) })
  await screen.findByText('Access denied')
  expect(screen.queryByText('Private instruction')).toBeNull()
})
