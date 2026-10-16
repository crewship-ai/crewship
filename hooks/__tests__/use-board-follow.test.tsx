import { act, renderHook } from '@testing-library/react'
import { expect, it } from 'vitest'
import { useBoardFollow } from '../use-board-follow'
import type { PresenceMember } from '../use-content-presence'
const member: PresenceMember = { connection_id: 'peer-tab', user_id: 'peer', board_seq: 4, revision: null, cursor: { x: 20, y: 30, coordinate_space: 'board' } }
it('follows only an explicitly selected live other-human cursor and stops on interaction', () => {
  const { result } = renderHook(() => useBoardFollow([member, { ...member, connection_id: 'self-tab', user_id: 'self' }], 'self', true, 4, true))
  expect(result.current.eligible).toHaveLength(1); expect(result.current.focusPoint).toBeUndefined()
  act(() => result.current.select('fake'))
  expect(result.current.focusPoint).toBeUndefined()
  act(() => result.current.select('peer-tab'))
  expect(result.current.focusPoint).toMatchObject({ x: 20, y: 30 })
  act(() => result.current.stop())
  expect(result.current.focusPoint).toBeUndefined()
})
it('stops synchronously on version change and never resumes automatically', () => {
  const { result, rerender } = renderHook(({ seq }) => useBoardFollow([{ ...member, board_seq: seq }], 'self', true, seq, true), { initialProps: { seq: 4 } })
  act(() => result.current.select('peer-tab'))
  rerender({ seq: 5 }); expect(result.current.focusPoint).toBeUndefined()
  rerender({ seq: 4 }); expect(result.current.focusPoint).toBeUndefined()
})
it('stops on disconnect, access loss or a missing cursor', () => {
  const { result, rerender } = renderHook(({ available, enabled, members }) => useBoardFollow(members, 'self', available, 4, enabled), { initialProps: { available: true, enabled: true, members: [member] } })
  act(() => result.current.select('peer-tab'))
  rerender({ available: false, enabled: true, members: [member] }); expect(result.current.active).toBeUndefined()
  rerender({ available: true, enabled: true, members: [member] }); expect(result.current.active).toBeUndefined()
  act(() => result.current.select('peer-tab'))
  rerender({ available: true, enabled: false, members: [member] }); expect(result.current.active).toBeUndefined()
  rerender({ available: true, enabled: true, members: [member] }); act(() => result.current.select('peer-tab'))
  rerender({ available: true, enabled: true, members: [] }); expect(result.current.active).toBeUndefined()
})
