// @vitest-environment node
import { expect, it, vi } from 'vitest'
import { committed, fetcher, proposal, response, setup } from './__tests__/capacity-red-fixture'

// Expected RED on8789ef07f:409 enters unknown and never automatically looks up
// the original outcome. The future server already-committed receipt below is
// an existing response shape, not a fabricated rejection/permission packet.
// Recovery must ONLY query outcome: no automatic mutation retry/new op_id.
it('C1 automatically resolves a409 through read-only outcome lookup without replay', async () => {
  const f = setup()
  let original = ''
  fetcher.mockImplementation((_url, init) => {
    const packet = JSON.parse(init.body)
    if (packet.action === 'submit') {
      original = packet.raw
      return Promise.resolve(response({ error: 'command projection not confirmed' }, 409))
    }
    if (packet.action === 'outcome') {
      expect(packet.raw).toBe(original)
      return Promise.resolve(response(committed(original)))
    }
    throw Error('unexpected transport action')
  })
  expect(f.control.submit(proposal, 0)).toBe(true)
  await vi.waitFor(() => expect(f.state).toHaveBeenCalledWith('unknown'))
  await vi.advanceTimersByTimeAsync(10_000)
  const actions = fetcher.mock.calls.map(call => JSON.parse(call[1].body).action)
  expect(actions.filter(action => action === 'submit')).toHaveLength(1)
  expect(actions.filter(action => action === 'outcome'), 'expected read-only recovery polling after409').not.toHaveLength(0)
  await vi.waitFor(() => expect(f.project).toHaveBeenCalledOnce())
  f.control.applied(true, 1)
  expect(f.control.canEdit()).toBe(true)
})
