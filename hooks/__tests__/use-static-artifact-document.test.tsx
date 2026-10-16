import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { useStaticArtifactDocument } from '../use-static-artifact-document'

const render = vi.hoisted(() => vi.fn())
vi.mock('@/lib/pages/artifact-static', () => ({ staticArtifactDocument: (...args: unknown[]) => render(...args) }))
afterEach(() => { cleanup(); render.mockReset() })

it('clears a prior preview immediately and ignores late results after source replacement or loss', async () => {
  const pending: { source: string; signal: AbortSignal; resolve: (document: string) => void }[] = []
  render.mockImplementation((source: string, signal: AbortSignal) => new Promise<string>(resolve => pending.push({ source, signal, resolve })))
  const hook = renderHook(({ source }) => useStaticArtifactDocument(source), { initialProps: { source: 'first' as string | undefined } })
  await act(async () => pending[0].resolve('first preview'))
  expect(hook.result.current).toBe('first preview')
  hook.rerender({ source: 'second' })
  expect(hook.result.current).toBe('')
  expect(pending[0].signal.aborted).toBe(true)
  hook.rerender({ source: 'third' })
  await act(async () => pending[2].resolve('third preview'))
  expect(hook.result.current).toBe('third preview')
  await act(async () => pending[1].resolve('late second preview'))
  expect(hook.result.current).toBe('third preview')
  hook.rerender({ source: undefined })
  expect(hook.result.current).toBe('')
  expect(pending[2].signal.aborted).toBe(true)
})

it('uses a fixed inert fallback after an unexpected sanitizer error', async () => {
  render.mockRejectedValue(new Error('untrusted exception text'))
  const hook = renderHook(() => useStaticArtifactDocument('source'))
  await waitFor(() => expect(hook.result.current).toBe('<p>Static preview unavailable.</p>'))
  expect(hook.result.current).not.toContain('untrusted')
})
