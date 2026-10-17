import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { BoardAgentWork } from '../board-agent-work'
import { BoardDiagramSource } from '../board-diagram-source'
import { apiFetch } from '@/lib/api-fetch'
import type { PageBoard } from '@/hooks/use-page-board'
import type { BoardRequestDetail } from '@/hooks/use-board-requests'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@/hooks/use-realtime', () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock('@/lib/boards/diagram-frame', () => ({ boardConversionSupported: () => true }))
vi.mock('@/lib/boards/diagram', () => ({ convertBoardDiagram: vi.fn(async () => [{ id: 'generated', type: 'rectangle', x: 80, y: 80 }]) }))
vi.mock('@/lib/boards/selection-preview', () => ({ renderSelectionPreview: vi.fn() }))
const board: PageBoard = { format: 'crewship-board-excalidraw-0.18.1-v1', seq: 4, can_write: true, records: [], placements: [] }
const initial = { id: 'job', kind: 'diagram', status: 'ready_to_insert', can_insert: true, base_seq: 3, instruction: 'Private diagram instructions', agent_name: 'Builder', target: { x: 80, y: 80, width: 640, height: 420 }, dialect: 'flowchart-v1', converter_version: '2.2.2', source_hash: 'digest', result_seq: null as number | null }
let client: QueryClient
beforeEach(() => { client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); vi.mocked(apiFetch).mockReset() })
afterEach(() => { cleanup(); client.clear(); vi.clearAllMocks() })

it('reads source only on explicit click, inserts only on approval, refreshes drift after Board change and purges on denied detail', async () => {
  let item = { ...initial }; let edited = false; let denied = false; let reads = 0; let inserts = 0
  vi.mocked(apiFetch).mockImplementation(async (url, _options) => {
    const path = new URL(String(url), 'https://studio.test').pathname
    if (path.endsWith('/insert')) { inserts++; item = { ...item, status: 'inserted', can_insert: false, result_seq: 5 } as typeof item; return Response.json({ seq: 5, result_seq: 5 }) }
    if (path.endsWith('/job')) { reads++; return denied ? Response.json({ error: 'Source access revoked' }, { status: 403 }) : Response.json({ ...item, mermaid_source: 'flowchart TD\nA --> B', edited_since_insert: edited }) }
    return Response.json({ can_dispatch: true, eligible_chats: [], requests: [item] })
  })
  const view = (seq: number) => <QueryClientProvider client={client}><BoardAgentWork workspaceId="ws" slug="design" board={{ ...board, seq }} selectedIds={[]} disabled={false} /></QueryClientProvider>
  const mounted = render(view(4))
  fireEvent.click(await screen.findByRole('button', { name: 'View Mermaid source' }))
  await screen.findByLabelText('Original Mermaid source')
  expect(reads).toBe(1); expect(inserts).toBe(0)
  fireEvent.click(screen.getByRole('button', { name: 'Insert diagram' }))
  await screen.findByText('Inserted at board sequence 5')
  expect(screen.queryByLabelText('Original Mermaid source')).toBeNull()
  expect(inserts).toBe(1)
  fireEvent.click(screen.getByRole('button', { name: 'View Mermaid source' }))
  await screen.findByText('The inserted drawing matches its recorded insertion baseline.')
  edited = true
  mounted.rerender(view(6))
  expect(screen.queryByLabelText('Original Mermaid source')).toBeNull()
  const beforeRefresh = reads
  fireEvent.click(screen.getByRole('button', { name: 'View Mermaid source' }))
  await screen.findByText(/inserted drawing changed after insertion/)
  expect(reads).toBe(beforeRefresh + 1)
  denied = true
  fireEvent.click(screen.getByRole('button', { name: 'Refresh Mermaid source' }))
  await screen.findByText('Source access revoked')
  expect(screen.queryByLabelText('Original Mermaid source')).toBeNull()
  expect(screen.queryByText('Private diagram instructions')).toBeNull()
  expect(inserts).toBe(1)
})

it('escapes hostile source, bounds source bytes and fences a stale page response', async () => {
  const detail = vi.fn(async (_id: string, _signal?: AbortSignal) => ({ ...initial, mermaid_source: '<img src=x onerror="alert(1)">', edited_since_insert: false }) as BoardRequestDetail)
  const props = { workspaceId: 'ws', slug: 'design', requestId: 'job', boardSeq: 4, status: 'ready_to_insert', detail }
  const mounted = render(<BoardDiagramSource {...props} />)
  expect(detail).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'View Mermaid source' }))
  await screen.findByText('<img src=x onerror="alert(1)">')
  expect(mounted.container.querySelector('img')).toBeNull()
  let release!: (value: BoardRequestDetail) => void
  detail.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh Mermaid source' }))
  const signal = detail.mock.calls.at(-1)?.[1] as AbortSignal | undefined
  mounted.rerender(<BoardDiagramSource {...props} workspaceId="other" slug="other" />)
  expect(signal?.aborted).toBe(true)
  await act(async () => release({ ...initial, mermaid_source: 'STALE PRIVATE SOURCE', edited_since_insert: false } as BoardRequestDetail))
  expect(screen.queryByText('STALE PRIVATE SOURCE')).toBeNull()
  detail.mockResolvedValueOnce({ ...initial, mermaid_source: 'é'.repeat(17000), edited_since_insert: false } as BoardRequestDetail)
  fireEvent.click(screen.getByRole('button', { name: 'View Mermaid source' }))
  await screen.findByRole('alert')
  expect(screen.queryByLabelText('Original Mermaid source')).toBeNull()
})
