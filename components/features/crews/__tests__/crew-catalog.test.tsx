import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { CrewCatalog } from '../crew-catalog'
vi.mock('@/components/ui/agent-avatar', () => ({ AgentAvatar: () => null }))
afterEach(() => vi.restoreAllMocks())
it('searches and orders the full server catalog, retaining both on the next page', async () => {
  const requests: string[] = []
  vi.spyOn(global, 'fetch').mockImplementation(async input => {
    const url = String(input); requests.push(url)
    const offset = new URL(url, 'http://test').searchParams.get('offset')
    const rows = Array.from({ length: offset === '24' ? 1 : 24 }, (_, i) => ({ id: `${offset}-${i}`, slug: `crew-${offset}-${i}`, name: `Crew ${offset}-${i}`, _count: { agents: 0 } }))
    return new Response(JSON.stringify(rows), { headers: { 'X-Total-Count': '25' } })
  })
  const selectCrew = vi.fn()
  render(<CrewCatalog onCrewSelect={selectCrew} workspaceId="ws" agents={[]} onAgentSelect={vi.fn()} />)
  await screen.findByText('Crew 0-0')
  fireEvent.change(screen.getByLabelText('Search crews'), { target: { value: 'website' } })
  await waitFor(() => expect(requests.some(url => url.includes('q=website'))).toBe(true))
  fireEvent.change(screen.getByLabelText('Sort crews'), { target: { value: 'name' } })
  await waitFor(() => expect(requests.some(url => url.includes('q=website&order=name'))).toBe(true))
  fireEvent.click(await screen.findByRole('button', { name: 'Load more crews' }))
  await screen.findByText('Crew 24-0')
  expect(requests.at(-1)).toContain('q=website&order=name&limit=24&offset=24')
  fireEvent.click(screen.getByRole('link', { name: 'Crew 24-0', exact: true }))
  expect(selectCrew).toHaveBeenCalledWith('crew-24-0')
  fireEvent.click(screen.getByRole('button', { name: 'Show compact crew list' }))
  expect(screen.getByRole('button', { name: 'Show crew cards' })).toHaveAttribute('aria-pressed', 'true')
})
