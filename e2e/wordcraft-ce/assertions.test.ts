// @vitest-environment node
import { expect, it } from 'vitest'
import { referencedList, selectedProps, type CommittedState } from './contract'

const document = (runs: CommittedState['document']['body'][number]['runs']): CommittedState => ({
  engine_sha: '', revision: '1', principal_id: 'a', ids: ['p0'], native_digest: 'synthetic', document: { body: [{ kind: 'para', text: 'aaBBcc', runs }] },
})
it('detects formatting leaking outside a correct selected target', () => {
  const baseline = document([{ len: 6, props: {} }])
  const selectedOnly = document([{ len: 2 }, { len: 2, props: { bold: true } }, { len: 2 }])
  const wholeParagraph = document([{ len: 6, props: { bold: true } }])
  expect(selectedProps(selectedOnly, 2, 2)).toEqual(selectedProps(wholeParagraph, 2, 2))
  expect(selectedProps(selectedOnly, 0, 2)).toEqual(selectedProps(baseline, 0, 2))
  expect(selectedProps(wholeParagraph, 0, 2)).not.toEqual(selectedProps(baseline, 0, 2))
})
it('refuses incomplete run coverage of the selected bytes', () => {
  expect(() => selectedProps(document([{ len: 2 }]), 2, 2)).toThrow()
})

it('rejects undo that leaves a peer list reference without its definition', () => {
  const peer = { kind: 'para' as const, text: 'peer', runs: [{ len: 4 }], props: { numbering: { num: 2, level: 0 } } }
  const state = document([{ len: 6 }])
  state.document.numbering = { nums: [{ id: 2, abstractId: 3 }], abstracts: [{ id: 3, levels: [{ format: 'bullet' }] }] }
  expect(referencedList(state, peer)?.abstract?.id).toBe(3)
  delete state.document.numbering
  expect(() => referencedList(state, peer)).toThrow()
})
