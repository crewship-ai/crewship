import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { CrewRestartButton } from '../crew-restart-button'
const request = vi.fn()
vi.mock('@/lib/api-fetch', () => ({ apiFetch: (...args: unknown[]) => request(...args) }))
vi.mock('@/hooks/use-abilities', () => ({ useAbilities: () => ({ abilities: { can: () => true } }) }))
beforeEach(() => request.mockReset())
describe('crew container restart', () => {
  it('explains the shared impact and reports recreation on the next run', async () => {
    request.mockResolvedValue(new Response(JSON.stringify({ restarted: 2 })))
    const changed = vi.fn()
    render(<CrewRestartButton workspaceId="ws" crewId="crew-1" name="Design" onRestart={changed} />)
    fireEvent.click(screen.getByRole('button', { name: 'Restart container', exact: true }))
    expect(request).not.toHaveBeenCalled()
    expect(screen.getByText(/every agent in this crew/)).toBeVisible()
    fireEvent.click(screen.getAllByRole('button', { name: 'Restart container', exact: true }).at(-1)!)
    await screen.findByText(/fresh container will start on the next agent run/)
    expect(request).toHaveBeenCalledWith('/api/v1/crews/crew-1/restart-agents?workspace_id=ws', { method: 'POST' })
    expect(changed).toHaveBeenCalledOnce()
  })
  it('keeps a failed restart retryable without claiming success', async () => {
    request.mockResolvedValue(new Response('{}', { status: 503 }))
    const changed = vi.fn()
    render(<CrewRestartButton workspaceId="ws" crewId="crew-1" name="Design" onRestart={changed} />)
    fireEvent.click(screen.getByRole('button', { name: 'Restart container', exact: true }))
    fireEvent.click(screen.getAllByRole('button', { name: 'Restart container', exact: true }).at(-1)!)
    await waitFor(() => expect(screen.getAllByText(/could not be recycled/).length).toBeGreaterThan(0))
    expect(changed).not.toHaveBeenCalled()
    expect(screen.queryByText(/reset completed/)).not.toBeInTheDocument()
  })
})
