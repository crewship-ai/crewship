import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ArtifactGallery } from '../artifact-gallery'
import { ArtifactGalleryPreview } from '../artifact-gallery-preview'
import { toPageView } from '@/hooks/use-pages'
import DOMPurify from 'dompurify'

const preview = vi.hoisted(() => ({ query: { data: { html: '<h1>Report</h1><script>steal()</script>', revision: 2, head_rev: 2 }, error: null as Error | null }, calls: vi.fn() }))
vi.mock('@/hooks/use-page-artifact', () => ({ usePageArtifact: (...args: unknown[]) => { preview.calls(...args); return { query: preview.query } } }))
const supported = DOMPurify.isSupported
// Happy DOM cannot exercise DOMPurify's real-browser traversal. Test its
// supported fail-closed fallback here; browser security tests cover sanitizing.
afterEach(() => { cleanup(); DOMPurify.isSupported = supported })
beforeEach(() => { DOMPurify.isSupported = false; preview.query.error = null; preview.calls.mockClear() })

it('does not load card content until selected and keeps at most one iframe across cards and workspaces', () => {
  const pages = ['one', 'two'].map(id => toPageView({ id, slug: id, name: id, panels: [], has_artifact: true }))
  const props = { workspaceId: 'workspace-a', pages, loading: false, error: null, onSelect: vi.fn(), onOpenChat: vi.fn() }
  const view = render(<ArtifactGallery {...props} />)
  expect(preview.calls).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Preview of one' }))
  expect(view.container.querySelectorAll('iframe')).toHaveLength(1)
  expect(screen.getByTitle('Static preview of one')).toHaveAttribute('sandbox', '')
  expect(screen.getByTitle('Static preview of one').getAttribute('srcdoc')).not.toContain('<script>steal()')
  fireEvent.click(screen.getByRole('button', { name: 'Preview of two' }))
  expect(view.container.querySelectorAll('iframe')).toHaveLength(1)
  expect(screen.queryByTitle('Static preview of one')).not.toBeInTheDocument()
  view.rerender(<ArtifactGallery {...props} workspaceId="workspace-b" />)
  expect(view.container.querySelectorAll('iframe')).toHaveLength(0)
})

it('removes retained content as soon as a refresh fails, including lost access', () => {
  const view = render(<ArtifactGalleryPreview workspaceId="workspace" slug="report" name="Report" />)
  expect(screen.getByTitle('Static preview of Report')).toBeInTheDocument()
  preview.query.error = new Error('Access revoked')
  view.rerender(<ArtifactGalleryPreview workspaceId="workspace" slug="report" name="Report" />)
  expect(screen.queryByTitle('Static preview of Report')).not.toBeInTheDocument()
  expect(screen.getByRole('alert')).toHaveTextContent('Preview unavailable')
})
