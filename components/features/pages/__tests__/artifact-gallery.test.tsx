import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { ArtifactGallery } from '../artifact-gallery'
import { EMPTY_PAGE_FILTERS, matchesPageFilters, pageFilterCount, toPageView } from '@/hooks/use-pages'

describe('artifact gallery', () => {
  it('uses content facets together with owner/search and displays actual agent provenance without fetching source', () => {
    const page = toPageView({ id: 'a', slug: 'report', name: 'Quarterly report', owner: 'user/owner', has_artifact: true, panels: [], artifact_summary: { head_rev: 7, author_user_id: 'human', author_name: 'Ada', author_agent_id: 'agent', author_agent_name: 'Analyst', published_at: '2026-10-13T12:00:00Z', last_activity_at: '2026-10-13T13:00:00Z' } })
    const filters = { ...EMPTY_PAGE_FILTERS, content: ['artifact' as const], owners: ['user/owner'] }
    expect(matchesPageFilters(page, filters, 'quarterly')).toBe(true)
    expect(matchesPageFilters(page, { ...filters, content: ['board'] }, '')).toBe(false)
    expect(pageFilterCount(filters)).toBe(2)
    const onSelect = vi.fn()
    render(<ArtifactGallery pages={[page]} loading={false} error={null} onSelect={onSelect} onOpenChat={vi.fn()} />)
    expect(screen.getByText('Revision 7')).toBeInTheDocument()
    expect(screen.getByText('Analyst on behalf of Ada')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Open static preview of Quarterly report' }))
    expect(onSelect).toHaveBeenCalledWith('report')
  })
  it('offers chat and import paths for an empty gallery and never shows stale cards on denial', () => {
    const create = vi.fn(), chat = vi.fn()
    const view = render(<ArtifactGallery pages={[]} loading={false} error={null} onSelect={vi.fn()} onCreate={create} onOpenChat={chat} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open chats' })); expect(chat).toHaveBeenCalledOnce()
    fireEvent.click(screen.getByRole('button', { name: 'Import HTML artifact' })); expect(create).toHaveBeenCalledOnce()
    view.rerender(<ArtifactGallery pages={[toPageView({ id: 'secret', slug: 'secret', name: 'Private title', panels: [] })]} loading={false} error="Access changed" onSelect={vi.fn()} onOpenChat={chat} />)
    expect(screen.queryByText('Private title')).not.toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('Access changed')
  })
})
