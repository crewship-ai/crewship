// @vitest-environment node
import { expect, it, vi } from 'vitest'
import { committed, fetcher, flush, proposal, response, setup } from './__tests__/capacity-red-fixture'

// Expected RED on8789ef07f: the real client canEdit/submit cap rejects change257
// even when every prior own receipt was confirmed and applied. Server is a
// transport fixture; this deliberately isolates the client from server caps.
it('C1 remains editable for the257th mutation after256 confirmed own changes', async () => {
  const f = setup()
  fetcher.mockImplementation((_url, init) => Promise.resolve(response(committed(JSON.parse(init.body).raw))))
  for (let revision = 0; revision < 256; revision++) {
    expect(f.control.submit(proposal, revision), `unexpected earlier refusal at${revision + 1}`).toBe(true)
    await vi.waitFor(() => expect(f.project).toHaveBeenCalledTimes(revision + 1))
    f.control.applied(true, revision + 1)
    await flush()
  }
  expect(f.state).toHaveBeenLastCalledWith('saved')
  expect(f.control.canEdit(), 'settled=256 must not permanently disable input').toBe(true)
  expect(f.control.submit(proposal, 256)).toBe(true)
})
