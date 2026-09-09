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

it('groups notes, orders the journal newest first and searches note content', async () => {
  vi.spyOn(global, 'fetch').mockResolvedValue(response([
    document('daily/2026-09-01.md', 'Old archived finding JANTAR-913'),
    document('CREW.md', '# Project context\n\n- Check navigation'),
    document('daily/2026-09-08.md', 'Latest journal entry'),
    document('pins.md', 'Pinned checklist'),
  ]))
  render(<MemoryWorkspace workspaceId="ws1" agentId="a1" />)
  expect(await screen.findByRole('heading', { name: 'Project context' })).toBeVisible()
  const nav = screen.getByRole('navigation', { name: 'Saved notes' })
  const buttons = [...nav.querySelectorAll('button')]
  expect(buttons.map(button => button.textContent)).toEqual([
    expect.stringContaining('Pinned notes'), expect.stringContaining('Team knowledge'),
    expect.stringContaining('daily/2026-09-08.md'), expect.stringContaining('daily/2026-09-01.md'),
  ])
  fireEvent.change(screen.getByRole('textbox', { name: 'Search notes' }), { target: { value: 'JANTAR-913' } })
  expect(await screen.findByText('Old archived finding JANTAR-913')).toBeVisible()
  expect(screen.queryByRole('heading', { name: 'Project context' })).not.toBeInTheDocument()
  fireEvent.change(screen.getByRole('textbox', { name: 'Search notes' }), { target: { value: 'absent-term' } })
  expect(screen.getByRole('heading', { name: 'No matching notes' })).toBeVisible()
  expect(screen.queryByText('No saved notes yet.')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Clear search' }))
  expect(screen.getByRole('heading', { name: 'Project context' })).toBeVisible()
})

it('resets the note search when switching knowledge scope', async () => {
  vi.spyOn(global, 'fetch').mockResolvedValue(new Response(JSON.stringify({
    scopes: { agent: 'available', crew: 'available' },
    documents: [document('AGENT.md', 'Agent unique phrase'), { ...document('CREW.md', 'Crew shared context'), id: 'crew:CREW.md', scope: 'crew' }],
  })))
  render(<MemoryWorkspace workspaceId="ws1" agentId="a1" />)
  await screen.findByText('Agent unique phrase')
  fireEvent.change(screen.getByRole('textbox', { name: 'Search notes' }), { target: { value: 'unique' } })
  fireEvent.click(screen.getByRole('button', { name: /Shared with crew/ }))
  expect(screen.getByRole('textbox', { name: 'Search notes' })).toHaveValue('')
  expect(screen.getByText('Crew shared context')).toBeVisible()
  expect(screen.queryByText('Agent unique phrase')).not.toBeInTheDocument()
})

it('renders personal notes as Markdown and confirms forgetting with the original fact key', async () => {
  const fetch = vi.spyOn(global, 'fetch').mockImplementation(async (url, init) => {
    const path = String(url)
    if (init?.method === 'DELETE') return new Response('{}')
    if (path.includes('/user-model')) return new Response(JSON.stringify({ exists: true, facts: [{ key: 'styl_odpovedi', value: 'Short and clear' }] }))
    if (path.includes('/peer-cards')) return new Response(JSON.stringify({ peers: [{ id: 'p1', agent_slug: 'alice', content: '# Working together\n\n- Confirm open questions' }] }))
    if (path.includes('/peer-consent')) return new Response(JSON.stringify({ opted_out: false }))
    return response([])
  })
  render(<MemoryWorkspace workspaceId="ws1" agentId="a1" />)
  await screen.findByText('No saved notes yet.')
  fireEvent.click(screen.getByRole('button', { name: 'About me' }))
  expect(await screen.findByRole('heading', { name: 'Working together' })).toBeVisible()
  expect(screen.getByRole('heading', { name: 'Styl odpovědí' })).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Forget Styl odpovědí' }))
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
  fireEvent.click(screen.getByRole('button', { name: 'Confirm', exact: true }))
  await waitFor(() => expect(fetch.mock.calls.some(([url, init]) => String(url).includes('/user-model/facts/styl_odpovedi?workspace_id=ws1') && init?.method === 'DELETE')).toBe(true))
})

it('groups newly named preferences without hiding unknown types', async () => {
  vi.spyOn(global, 'fetch').mockImplementation(async url => {
    const path = String(url)
    if (path.includes('/user-model')) return new Response(JSON.stringify({ exists: true, facts: [
      { key: 'communication.meeting_notes', value: 'Send a short recap' },
      { key: 'communication.review_questions', value: 'Bundle questions together' },
      { key: 'unexpected_new_notice', value: 'Retain this new preference' },
    ] }))
    if (path.includes('/peer-cards')) return new Response(JSON.stringify({ peers: [] }))
    if (path.includes('/peer-consent')) return new Response(JSON.stringify({ opted_out: false }))
    return response([])
  })
  render(<MemoryWorkspace workspaceId="ws1" agentId="a1" />)
  await screen.findByText('No saved notes yet.')
  fireEvent.click(screen.getByRole('button', { name: 'About me' }))
  const communication = await screen.findByRole('region', { name: 'Communication preferences' })
  expect(communication).toHaveTextContent('Send a short recap')
  expect(communication).toHaveTextContent('Bundle questions together')
  expect(screen.getByRole('region', { name: 'Other preferences' })).toHaveTextContent('Retain this new preference')
  expect(screen.getByRole('button', { name: 'Forget Unexpected new notice' })).toBeVisible()
})
