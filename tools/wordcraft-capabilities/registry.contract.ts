// @vitest-environment node
import { expect, it } from 'vitest'
import { main } from './generate.mjs'

it('checks the actual pinned compiled registry and source against the published catalog', () => {
  const source = process.env.WORDCRAFT_SOURCE
  expect(source, 'WORDCRAFT_SOURCE must name a clean owned pinned WordCraft clone').toBeTruthy()
  main(['--check', '--source', source!])
}, 1_200_000)
