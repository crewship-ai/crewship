// @vitest-environment node
import { expect, it, vi } from 'vitest'
const { gitOutput } = vi.hoisted(() => ({ gitOutput: vi.fn() }))
vi.mock('node:child_process', () => ({ execFileSync: gitOutput }))
import { ENGINE_SHA, sourceIdentity } from './generate.mjs'

it('refuses another engine commit before reading or compiling source', () => {
  gitOutput.mockReturnValueOnce('different-sha\n')
  expect(() => sourceIdentity('/fixture')).toThrow('SHA mismatch')
})
it('refuses modified or untracked source before building the registry export', () => {
  gitOutput.mockReturnValueOnce(`${ENGINE_SHA}\n`).mockReturnValueOnce(' M crates/engine/src/cmd/text.rs\n')
  expect(() => sourceIdentity('/fixture')).toThrow('source drift')
  gitOutput.mockReturnValueOnce(`${ENGINE_SHA}\n`).mockReturnValueOnce('?? crates/engine/src/cmd/new.rs\n')
  expect(() => sourceIdentity('/fixture')).toThrow('source drift')
})
