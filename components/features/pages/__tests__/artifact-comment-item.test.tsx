import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { ArtifactCommentItem } from '../artifact-comment-item'
import { ArtifactCommentError, type ArtifactComment, type ArtifactThread, type ArtifactCommentChange, type ArtifactCommentHistory } from '@/hooks/use-artifact-threads'

afterEach(cleanup)
const comment: ArtifactComment = { id: 'comment', body: 'Original', author_user_id: 'human', created_at: '2026-10-13T00:00:00Z', can_edit: true, can_delete: true, can_view_history: true, version: 1 }
const thread: ArtifactThread = { id: 'thread', revision: 1, anchor: { schema_version: 1, type: 'document' }, status: 'open', version: 2, created_at: comment.created_at, updated_at: comment.created_at, created_by_user_id: 'human', comments: [comment] }
function props() { return { comment, thread, canComment: true, pending: false, onChange: vi.fn<(thread: ArtifactThread, change: ArtifactCommentChange) => Promise<void>>(async () => {}), onHistory: vi.fn<(threadId: string, commentId: string) => Promise<ArtifactCommentHistory[]>>(async () => []), onDirtyChange: vi.fn() } }
it('keeps an uncertain edit immutable and retries its original thread version', async () => {
  const p = props(); p.onChange.mockRejectedValueOnce(new Error('Connection lost'))
  const view = render(<ArtifactCommentItem {...p} />)
  fireEvent.click(screen.getByRole('button', { name: 'Edit comment' }))
  fireEvent.change(screen.getByLabelText('Edit comment text'), { target: { value: 'Changed' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
  await screen.findByText('Connection lost')
  expect(screen.getByLabelText('Edit comment text')).toBeDisabled()
  view.rerender(<ArtifactCommentItem {...p} thread={{ ...thread, version: 3 }} />)
  fireEvent.click(screen.getByRole('button', { name: 'Retry unchanged comment update' }))
  await waitFor(() => expect(p.onChange).toHaveBeenCalledTimes(2))
  expect(p.onChange.mock.calls[0]).toEqual(p.onChange.mock.calls[1])
  expect(p.onChange.mock.calls[1]).toEqual([{ ...thread, version: 2 }, { action: 'edit', commentId: 'comment', body: 'Changed' }])
})
it('requires explicit review after a definite stale-thread rejection', async () => {
  const p = props(); p.onChange.mockRejectedValueOnce(new ArtifactCommentError('Changed elsewhere', 409))
  const view = render(<ArtifactCommentItem {...p} />)
  fireEvent.click(screen.getByRole('button', { name: 'Delete comment' }))
  expect(p.onChange).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Confirm deletion' }))
  await screen.findByText('Changed elsewhere')
  view.rerender(<ArtifactCommentItem {...p} thread={{ ...thread, version: 3 }} />)
  expect(screen.getByRole('button', { name: 'Confirm deletion' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Use latest thread version' }))
  fireEvent.click(screen.getByRole('button', { name: 'Confirm deletion' }))
  await waitFor(() => expect(p.onChange).toHaveBeenCalledTimes(2))
  expect(p.onChange.mock.calls[1][0].version).toBe(3)
})
it('does not infer ownership from an agent reply made on behalf of a human', () => {
  render(<ArtifactCommentItem {...props()} comment={{ ...comment, author_agent_id: 'agent', author_run_id: 'run', can_edit: false, can_delete: false, can_view_history: false }} />)
  expect(screen.getByText('Agent reply')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Edit comment' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Delete comment' })).toBeNull()
})
it('toggles only the selected reaction with a captured thread version', async () => {
  const p = props()
  render(<ArtifactCommentItem {...p} comment={{ ...comment, can_react: true, reactions: [{ emoji: '👍', count: 2, reacted_by_me: true }] }} />)
  const reaction = screen.getByRole('button', { name: 'React 👍' })
  expect(reaction).toHaveAttribute('aria-pressed', 'true')
  fireEvent.click(reaction)
  await waitFor(() => expect(p.onChange).toHaveBeenCalledWith(thread, { action: 'react', commentId: 'comment', emoji: '👍', active: false }))
  expect(reaction).toHaveAttribute('aria-pressed', 'true')
})
it('discards a late history response after access is lost', async () => {
  const p = props()
  let finish!: (value: ArtifactCommentHistory[]) => void
  p.onHistory.mockImplementation(() => new Promise(resolve => { finish = resolve }))
  const view = render(<ArtifactCommentItem {...p} />)
  fireEvent.click(screen.getByRole('button', { name: 'View edit history' }))
  view.rerender(<ArtifactCommentItem {...p} canComment={false} comment={{ ...comment, can_view_history: false }} />)
  await act(async () => finish([]))
  expect(screen.queryByRole('list', { name: 'Comment edit history' })).toBeNull()
})
