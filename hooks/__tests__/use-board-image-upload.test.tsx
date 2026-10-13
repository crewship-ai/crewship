import { act, renderHook } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { useBoardImageUpload } from '../use-board-image-upload'
import { uploadBoardImage } from '@/lib/boards/files'
import { apiFetch } from '@/lib/api-fetch'
vi.mock('@/lib/api-fetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@/lib/boards/files', () => ({ uploadBoardImage: vi.fn(), BoardImageError: class extends Error { status = 0 } }))
afterEach(() => vi.resetAllMocks())
it('does not read or place an upload from a previous board after navigation', async () => {
  let complete!: (value: never) => void
  vi.mocked(uploadBoardImage).mockImplementationOnce(() => new Promise(resolve => { complete = resolve }))
  const { result, rerender } = renderHook(({ slug }) => useBoardImageUpload('ws', slug, slug), { initialProps: { slug: 'old' } })
  let pending!: ReturnType<typeof result.current.upload>
  act(() => { pending = result.current.upload({ file: new File(['pixels'], 'image.png'), baseSeq: 1, mutationId: 'intent' }) })
  rerender({ slug: 'new' })
  await act(async () => complete({ file_id: 'private-old-file', seq: 2, result_seq: 2 } as never))
  expect(await pending).toBeNull(); expect(apiFetch).not.toHaveBeenCalled(); expect(result.current.pending).toBe(false)
})
