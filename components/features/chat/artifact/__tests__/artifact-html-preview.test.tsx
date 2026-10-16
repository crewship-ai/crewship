import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { ArtifactHTMLPreview } from '../artifact-html-preview'

const decode = vi.hoisted(() => vi.fn())
vi.mock('@/lib/pages/artifact-static', () => ({ staticArtifactDocument: (...args: unknown[]) => decode(...args) }))
afterEach(() => { cleanup(); decode.mockReset() })

it('disconnects pending/old frames and never displays stale source while a replacement is validated', async () => {
  const pending: { signal: AbortSignal; resolve: (document: string) => void }[] = []
  decode.mockImplementation((_source: string, signal: AbortSignal) => new Promise<string>(resolve => pending.push({ signal, resolve })))
  const view = render(<ArtifactHTMLPreview path="report.html" text="first" />)
  const initial = screen.getByTitle('Preview report.html')
  expect(initial).toHaveAttribute('srcdoc', '')
  await act(async () => pending[0].resolve('<h1>First source</h1>'))
  const first = screen.getByTitle('Preview report.html')
  expect(initial.isConnected).toBe(false)
  expect(first).toHaveAttribute('srcdoc', '<h1>First source</h1>')

  view.rerender(<ArtifactHTMLPreview path="report.html" text="second" />)
  const loading = screen.getByTitle('Preview report.html')
  expect(first.isConnected).toBe(false)
  expect(loading).toHaveAttribute('srcdoc', '')
  expect(pending[0].signal.aborted).toBe(true)

  view.rerender(<ArtifactHTMLPreview path="report.html" text="third" />)
  expect(screen.getByTitle('Preview report.html')).toHaveAttribute('srcdoc', '')
  expect(pending[1].signal.aborted).toBe(true)
  await act(async () => pending[2].resolve('<h1>Third source</h1>'))
  const third = screen.getByTitle('Preview report.html')
  expect(loading.isConnected).toBe(false)
  expect(third).toHaveAttribute('srcdoc', '<h1>Third source</h1>')
  await act(async () => pending[1].resolve('<h1>Late second source</h1>'))
  expect(screen.getByTitle('Preview report.html')).toBe(third)
  expect(third).toHaveAttribute('srcdoc', '<h1>Third source</h1>')
})
