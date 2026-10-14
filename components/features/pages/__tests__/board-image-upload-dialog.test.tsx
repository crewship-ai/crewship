import { fireEvent, render, screen, cleanup } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { BoardImageUploadDialog } from '../board-image-upload-dialog'

afterEach(cleanup)
it('keeps the captured sequence and mutation on an exact failed retry', () => {
  const onUpload = vi.fn().mockResolvedValue(null)
  const props = { baseSeq: 4, pending: false, error: null, onClose: vi.fn(), onUpload }
  const { rerender } = render(<BoardImageUploadDialog {...props} />)
  const file = new File(['pixels'], 'local.png', { type: 'image/png' })
  fireEvent.change(screen.getByLabelText('Image file'), { target: { files: [file] } })
  fireEvent.click(screen.getByRole('button', { name: 'Upload and place' }))
  rerender(<BoardImageUploadDialog {...props} baseSeq={8} error="Temporary failure" />)
  fireEvent.click(screen.getByRole('button', { name: 'Upload and place' }))
  expect(onUpload).toHaveBeenCalledTimes(2)
  expect(onUpload.mock.calls[0][0]).toEqual(onUpload.mock.calls[1][0])
  expect(onUpload.mock.calls[1][0]).toMatchObject({ file, baseSeq: 4 })
})
it('rejects oversized input and disables submission while pending', () => {
  const props = { baseSeq: 0, pending: false, error: null, onClose: vi.fn(), onUpload: vi.fn().mockResolvedValue(null) }
  const { rerender } = render(<BoardImageUploadDialog {...props} />)
  const file = new File(['pixels'], 'large.png', { type: 'image/png' })
  Object.defineProperty(file, 'size', { value: 5 * 1024 * 1024 + 1 })
  fireEvent.change(screen.getByLabelText('Image file'), { target: { files: [file] } })
  expect(screen.getByRole('alert').textContent).toContain('5 MiB')
  expect(screen.getByRole('button', { name: 'Upload and place' })).toBeDisabled()
  rerender(<BoardImageUploadDialog {...props} pending />)
  expect(screen.getByLabelText('Image file')).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
})
