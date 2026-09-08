import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { PrivacySection } from "../privacy-section"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function mockPrivacy({ optedOut = false, cards = true, fail = false, mutationFails = false } = {}) {
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes('/user-model')) return json({ exists: false, facts: [] })
    if (init?.method && mutationFails) return json({ error: 'Not saved' }, 503)
    if (url.includes('/peer-consent')) {
      if (init?.method === 'PUT') optedOut = JSON.parse(String(init.body)).opted_out
      return fail ? json({}, 503) : json({ opted_out: optedOut })
    }
    if (url.includes('/peer-cards')) {
      if (init?.method === 'DELETE') cards = false
      return json({ peers: cards ? [{ id: 'c1', agent_slug: 'researcher', content: 'Prefers concise answers.' }] : [] })
    }
    throw new Error(`Unexpected endpoint: ${url}`)
  })
}
const mutation = (method: string) => apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === method)
beforeEach(() => { cleanup(); apiFetch.mockReset() })
describe('PrivacySection', () => {
  it('loads all personal sources in the selected workspace', async () => {
    mockPrivacy()
    render(<PrivacySection workspaceId="ws1" />)
    await screen.findByText('Personalization on')
    expect(apiFetch.mock.calls.length).toBe(3)
    for (const [url] of apiFetch.mock.calls) expect(url).toContain('workspace_id=ws1')
  })
  it('confirms opt-out before purging personal profiles and reports the new state', async () => {
    mockPrivacy()
    render(<PrivacySection workspaceId="ws1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Turn off and forget' }))
    expect(mutation('PUT')).toBeUndefined()
    fireEvent.click(screen.getByRole('button', { name: 'Confirm', exact: true }))
    await screen.findByText('Personalization off')
    expect(mutation('PUT')?.[0]).toContain('/peer-consent?workspace_id=ws1')
    expect(JSON.parse(String(mutation('PUT')?.[1].body))).toEqual({ opted_out: true })
  })
  it('can opt back in', async () => {
    mockPrivacy({ optedOut: true })
    render(<PrivacySection workspaceId="ws1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Enable', exact: true }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm', exact: true }))
    await screen.findByText('Personalization on')
    expect(JSON.parse(String(mutation('PUT')?.[1].body))).toEqual({ opted_out: false })
  })
  it('shows existing peer notes without claiming the generator is active', async () => {
    mockPrivacy()
    render(<PrivacySection workspaceId="ws1" />)
    await screen.findByText('Prefers concise answers.')
    // PrivacySection is now a thin wrapper over PersonalMemory, which
    // attributes each note with an @-prefixed agent handle.
    expect(screen.getByText('@researcher')).toBeVisible()
    expect(screen.getByText(/Automatic agent-specific profile generation is not available/)).toBeVisible()
  })
  it('does not offer deletion when no peer notes exist', async () => {
    mockPrivacy({ cards: false })
    render(<PrivacySection workspaceId="ws1" />)
    await screen.findByText('No saved preferences yet.')
    expect(screen.queryByRole('button', { name: 'Forget these notes' })).not.toBeInTheDocument()
  })
  it('confirms deletion and reloads the resulting empty notes', async () => {
    mockPrivacy()
    render(<PrivacySection workspaceId="ws1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Forget these notes' }))
    expect(mutation('DELETE')).toBeUndefined()
    fireEvent.click(screen.getByRole('button', { name: 'Confirm', exact: true }))
    await waitFor(() => expect(mutation('DELETE')).toBeDefined())
    expect(mutation('DELETE')?.[0]).toContain('/peer-cards?workspace_id=ws1')
    await waitFor(() => expect(screen.queryByText('Prefers concise answers.')).not.toBeInTheDocument())
  })
  it('does not infer consent after a failed load', async () => {
    mockPrivacy({ fail: true })
    render(<PrivacySection workspaceId="ws1" />)
    await screen.findByText(/Some personal data could not be loaded/)
    expect(screen.queryByText('Personalization on')).not.toBeInTheDocument()
    expect(screen.queryByText('Personalization off')).not.toBeInTheDocument()
  })
  it('preserves the existing notes and confirmation after a rejected deletion', async () => {
    mockPrivacy({ mutationFails: true })
    render(<PrivacySection workspaceId="ws1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Forget these notes' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm', exact: true }))
    await screen.findByText(/The change could not be saved/)
    expect(screen.getByText('Prefers concise answers.')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Confirm', exact: true })).toBeVisible()
  })
})
