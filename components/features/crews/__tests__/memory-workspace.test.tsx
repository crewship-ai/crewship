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
    fireEvent.click(await screen.findByRole('button', { name: 'AGENT.md' }))
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
    fireEvent.click(await screen.findByRole('button', { name: 'BRIEF.md' }))
    resolveFirst(response([document('AGENT.md', 'Old private content')]))
    await waitFor(() => expect(screen.getByText('New agent only')).toBeVisible())
    expect(screen.queryByText('Old private content')).not.toBeInTheDocument()
  })
})
