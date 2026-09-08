import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryWorkspace } from '../memory-workspace'

const document = (name: string, content: string) => ({ id: `agent:${name}`, name, scope: 'agent', content, state: 'available', bytes: content.length, history_path: `agent:alice/${name}` })
const response = (documents: unknown[]) => new Response(JSON.stringify({ documents, scopes: { agent: documents.length ? 'available' : 'empty' } }))
afterEach(() => vi.restoreAllMocks())
describe('Memory workspace', () => {
  it('shows the current note when versioning is disabled', async () => {
    vi.spyOn(global, 'fetch').mockImplementation(async (url) => String(url).includes('/versions') ? new Response(JSON.stringify({ entries: [], projection: { state: 'unavailable' } })) : response([document('AGENT.md', 'Live knowledge without history')]))
    render(<MemoryWorkspace workspaceId="ws1" agentId="a1" memoryEnabled />)
    await screen.findByText('Live knowledge without history')
    expect(screen.getByText('Live knowledge without history')).toBeVisible()
    const details = screen.getByText('Version history').closest('details')!
    details.open = true; fireEvent(details, new Event('toggle'))
    expect(await screen.findByText(/History is unavailable on this server/)).toBeVisible()
    expect(screen.getByText('Live knowledge without history')).toBeVisible()
  })
  it('does not call failed inventory an empty memory', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(new Response('{}', { status: 503 }))
    render(<MemoryWorkspace workspaceId="ws1" agentId="a1" />)
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not load/i)
    expect(screen.queryByText('No saved notes yet.')).not.toBeInTheDocument()
  })
  it('drops a late response when the selected agent changes', async () => {
    let resolveFirst!: (response: Response) => void
    vi.spyOn(global, 'fetch').mockImplementation(async (url) => String(url).includes('/a1/') ? new Promise<Response>((resolve) => { resolveFirst = resolve }) : response([document('BRIEF.md', 'New agent only')]))
    const view = render(<MemoryWorkspace workspaceId="ws1" agentId="a1" />)
    await waitFor(() => expect(resolveFirst).toBeDefined())
    view.rerender(<MemoryWorkspace workspaceId="ws1" agentId="a2" />)
    await screen.findByText('New agent only')
    resolveFirst(response([document('AGENT.md', 'Old private content')]))
    await waitFor(() => expect(screen.getByText('New agent only')).toBeVisible())
    expect(screen.queryByText('Old private content')).not.toBeInTheDocument()
  })
})

it('refreshes all personal resources from the main refresh button', async () => {
  let reads = 0
  vi.spyOn(global, 'fetch').mockImplementation(async (url) => {
    const path = String(url)
    if (path.includes('/user-model')) { reads++; return new Response(JSON.stringify({ exists: false, facts: [] })) }
    if (path.includes('/peer-cards')) return new Response(JSON.stringify({ peers: [] }))
    if (path.includes('/peer-consent')) return new Response(JSON.stringify({ opted_out: false }))
    return response([])
  })
  render(<MemoryWorkspace workspaceId="ws1" agentId="a1" />)
  await screen.findByText('No saved notes yet.')
  fireEvent.click(screen.getByRole('button', { name: 'About me' }))
  await screen.findByText('No saved preferences yet.')
  expect(reads).toBe(1)
  fireEvent.click(screen.getByRole('button', { name: 'Refresh', exact: true }))
  await screen.findByText('Personal memory refreshed.')
  expect(reads).toBe(2)
})
