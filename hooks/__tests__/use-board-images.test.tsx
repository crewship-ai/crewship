import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { useBoardImages } from '../use-board-images'
import { loadBoardImages } from '@/lib/boards/files'
vi.mock('@/lib/boards/files', () => ({ loadBoardImages: vi.fn(), BoardImageError: class extends Error {} }))
afterEach(() => vi.resetAllMocks())
it('ignores a late image response after switching boards', async () => {
  let resolveOld!: (value: never[]) => void
  vi.mocked(loadBoardImages).mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve })).mockResolvedValueOnce([])
  const { result, rerender } = renderHook(({ slug }) => useBoardImages('ws', slug, slug, ['image']), { initialProps: { slug: 'old' } })
  rerender({ slug: 'new' })
  await waitFor(() => expect(result.current.loading).toBe(false))
  await act(async () => resolveOld([{ id: 'private-old-file' }] as never[]))
  expect(result.current.files).toEqual([]); expect(result.current.identity).toContain('new')
})
