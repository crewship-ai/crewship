import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ArtifactInteractiveFrame, type ArtifactInteractiveFrameProps } from '../artifact-interactive-frame'
vi.mock('@/lib/pages/runtime-support', () => ({ supportsPageApplications: () => true }))
const identity = { artifactId: 'a', revision: 7, profile: 'interactive-html-v1' as const }
const props = (): ArtifactInteractiveFrameProps => ({ runtimeUrl: 'https://runtime.artifacts.test/bootstrap', identity, html: '<p>private</p>', accessible: true, lease: vi.fn(async () => ({ expiresAt: Date.now() + 10_000 })) })
const navigation = new WeakMap<HTMLIFrameElement, string>()
beforeEach(() => {
  // Keep the real frame DOM/contentWindow, but unit tests never navigate it.
  vi.spyOn(HTMLIFrameElement.prototype, 'src', 'set').mockImplementation(function (this: HTMLIFrameElement, value: string) { navigation.set(this, value) })
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('isolated frame lifecycle preparation', () => {
  it('requests exact revision authority before navigation and tears down on access loss', async () => {
    const input = props()
    const view = render(<ArtifactInteractiveFrame {...input} />)
    await waitFor(() => expect(document.querySelector('iframe')).not.toBeNull())
    expect(input.lease).toHaveBeenCalledWith(identity, expect.any(AbortSignal))
    const frame = document.querySelector('iframe')!
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
    expect(frame.hasAttribute('credentialless')).toBe(true)
    view.rerender(<ArtifactInteractiveFrame {...input} accessible={false} />)
    expect(document.querySelector('iframe')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Access unavailable')
  })
  it('does not transfer HTML to forged source/origin handshakes', async () => {
    render(<ArtifactInteractiveFrame {...props()} />)
    await waitFor(() => expect(document.querySelector('iframe')).not.toBeNull())
    const frame = document.querySelector('iframe')!
    const send = vi.spyOn(frame.contentWindow!, 'postMessage')
    const nonce = new URL(navigation.get(frame)!).hash.slice(1)
    act(() => {
      window.dispatchEvent(new MessageEvent('message', { source: window, origin: 'null', data: { type: 'artifact.ready', nonce } }))
      window.dispatchEvent(new MessageEvent('message', { source: frame.contentWindow, origin: 'https://attacker.test', data: { type: 'artifact.ready', nonce } }))
    })
    expect(send).not.toHaveBeenCalled()
  })
  it('removes the frame on Stop, restarts with a fresh nonce, and aborts on unmount', async () => {
    const input = props()
    const view = render(<ArtifactInteractiveFrame {...input} />)
    await waitFor(() => expect(document.querySelector('iframe')).not.toBeNull())
    const previous = navigation.get(document.querySelector('iframe')!)
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(document.querySelector('iframe')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }))
    await waitFor(() => expect(document.querySelector('iframe')).not.toBeNull())
    expect(navigation.get(document.querySelector('iframe')!)).not.toBe(previous)
    const signal = vi.mocked(input.lease).mock.calls.at(-1)![1]
    view.unmount()
    expect(signal.aborted).toBe(true)
    expect(document.querySelector('iframe')).toBeNull()
  })
  it('removes the execution context and aborts the lease on malformed telemetry', async () => {
    const input = props()
    let hostPort!: { onmessage: ((event: { data: unknown }) => void) | null; close: ReturnType<typeof vi.fn>; start: ReturnType<typeof vi.fn>; onmessageerror: null; postMessage: ReturnType<typeof vi.fn> }
    vi.stubGlobal('MessageChannel', class {
      port1 = hostPort = { onmessage: null, close: vi.fn(), start: vi.fn(), onmessageerror: null, postMessage: vi.fn() }
      port2 = {}
    })
    const view = render(<ArtifactInteractiveFrame {...input} />)
    await waitFor(() => expect(document.querySelector('iframe')).not.toBeNull())
    const frame = document.querySelector('iframe')!
    vi.spyOn(frame.contentWindow!, 'postMessage').mockImplementation(() => {})
    const nonce = new URL(navigation.get(frame)!).hash.slice(1)
    act(() => window.dispatchEvent(new MessageEvent('message', { source: frame.contentWindow, origin: 'null', data: { type: 'artifact.ready', nonce } })))
    act(() => hostPort.onmessage?.({ data: { type: 'fetch', url: '/api/private' } }))
    expect(document.querySelector('iframe')).toBeNull()
    expect(vi.mocked(input.lease).mock.calls[0][1].aborted).toBe(true)
    expect(screen.getByRole('status')).toHaveTextContent('Runtime channel closed')
    view.unmount()
    vi.unstubAllGlobals()
  })
  it('fails closed on denied or unbounded leases without creating a frame', async () => {
    render(<ArtifactInteractiveFrame {...props()} lease={async () => ({ expiresAt: Infinity })} />)
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Access could not be verified'))
    expect(document.querySelector('iframe')).toBeNull()
  })
  it('removes an unresponsive bootstrap at the independent ready deadline', async () => {
    vi.useFakeTimers()
    render(<ArtifactInteractiveFrame {...props()} />)
    await act(async () => {})
    expect(document.querySelector('iframe')).not.toBeNull()
    await act(async () => { vi.advanceTimersByTime(5001) })
    expect(document.querySelector('iframe')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Runtime did not become ready')
  })
  it('expires a hung recheck without extending the old lease', async () => {
    vi.useFakeTimers()
    const lease = vi.fn().mockResolvedValueOnce({ expiresAt: Date.now() + 1000 }).mockImplementation(() => new Promise(() => {}))
    render(<ArtifactInteractiveFrame {...props()} lease={lease} />)
    await act(async () => {})
    expect(document.querySelector('iframe')).not.toBeNull()
    await act(async () => { vi.advanceTimersByTime(1001) })
    expect(document.querySelector('iframe')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Access lease expired')
  })
})
