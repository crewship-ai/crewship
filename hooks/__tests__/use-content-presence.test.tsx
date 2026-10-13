import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useContentPresence, type ContentPresenceContext } from '../use-content-presence'
import type { RealtimeEvent } from '../use-realtime'
import type { WSStatus } from '../use-websocket'
let listener: (event: RealtimeEvent) => void
const transport = { status: 'connected' as WSStatus | null, sendPresence: vi.fn(), currentUserId: 'me' }
vi.mock('@/hooks/use-realtime', () => ({ usePresenceTransportSafe: () => transport, useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: (_type: string, callback: (event: RealtimeEvent) => void) => { listener = callback } }))
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(100000); transport.status = 'connected'; transport.sendPresence.mockClear() })
afterEach(() => { cleanup(); vi.useRealTimers() })
const context: ContentPresenceContext = { kind: 'board', pageId: 'page1', boardSeq: 7 }
const member = (id: string, user: string, seq = 7) => ({ connection_id: id, user_id: user, board_seq: seq, revision: null, cursor: { coordinate_space: 'board', x: 12, y: 24 } })
function snapshot(channel: string, members: unknown[]) { act(() => listener({ type: 'presence.snapshot', channel, payload: { schema_version: 1, members }, timestamp: new Date() })) }
it('sends explicit context heartbeat without client identity or content, and stops on unmount', () => {
  const view = renderHook(() => useContentPresence(context))
  expect(transport.sendPresence).toHaveBeenLastCalledWith('board:page1', { schema_version: 1, board_seq: 7 })
  act(() => vi.advanceTimersByTime(5000))
  expect(transport.sendPresence).toHaveBeenCalledTimes(2)
  view.unmount(); act(() => vi.advanceTimersByTime(10000))
  expect(transport.sendPresence).toHaveBeenCalledTimes(2)
})
it('isolates rooms and sequence positions while deduplicating multiple tabs of one human', () => {
  const view = renderHook(() => useContentPresence(context))
  snapshot('board:foreign', [member('foreign', 'other')])
  expect(view.result.current.confirmed).toBe(false)
  snapshot('board:page1', [member('one', 'me'), member('two', 'me'), member('three', 'other', 6)])
  expect(view.result.current.people).toBe(2)
  expect(view.result.current.here).toBe(1)
  expect(view.result.current.members.map(item => item.connection_id)).toEqual(['one', 'two'])
})
it('clears stale membership on disconnect, room changes and local TTL', () => {
  const view = renderHook(({ value }) => useContentPresence(value), { initialProps: { value: context } })
  snapshot('board:page1', [member('one', 'other')])
  act(() => vi.advanceTimersByTime(20000))
  expect(view.result.current.confirmed).toBe(false)
  snapshot('board:page1', [member('one', 'other')])
  transport.status = 'disconnected'; view.rerender({ value: context })
  expect(view.result.current.people).toBe(0)
  transport.status = 'connected'; view.rerender({ value: { kind: 'board', pageId: 'page2', boardSeq: 7 } })
  expect(view.result.current.people).toBe(0)
})
it('throttles finite board cursors and omits malformed coordinates without inventing a pointer', () => {
  const view = renderHook(() => useContentPresence(context))
  act(() => { view.result.current.setCursor({ x: 15, y: 25, coordinate_space: 'board' }); view.result.current.setCursor({ x: 30, y: 40, coordinate_space: 'board' }) })
  expect(transport.sendPresence).toHaveBeenCalledTimes(2)
  expect(transport.sendPresence).toHaveBeenLastCalledWith('board:page1', { schema_version: 1, board_seq: 7, cursor: { x: 15, y: 25, coordinate_space: 'board' } })
  act(() => view.result.current.setCursor({ x: Infinity, y: 20, coordinate_space: 'board' }))
  expect(transport.sendPresence).toHaveBeenCalledTimes(2)
  snapshot('board:page1', [{ ...member('one', 'other'), cursor: { x: NaN, y: 2, coordinate_space: 'board' } }])
  expect(view.result.current.members[0].cursor).toBeNull()
})
it('does not join or send when content access is unavailable', () => {
  const view = renderHook(() => useContentPresence(context, false))
  expect(view.result.current.available).toBe(false)
  expect(transport.sendPresence).not.toHaveBeenCalled()
})
