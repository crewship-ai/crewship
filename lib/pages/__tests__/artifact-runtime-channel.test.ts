import { describe, expect, it, vi } from 'vitest'
import { ARTIFACT_RUNTIME_ENABLED, ArtifactRuntimeChannel, parseArtifactRuntimeTelemetry } from '../artifact-runtime-channel'
import { artifactRuntimePreparationCSP, artifactRuntimePreparationDocument } from '../artifact-runtime-bootstrap'

function transport() {
  const port = { onmessage: null, onmessageerror: null, start: vi.fn(), close: vi.fn(), postMessage: vi.fn() }
  const events = vi.fn()
  const channel = new ArtifactRuntimeChannel(port as unknown as MessagePort, events)
  const receive = (data: unknown) => (port.onmessage as unknown as ((event: { data: unknown }) => void) | null)?.({ data })
  return { port, events, channel, receive }
}

describe('disabled artifact runtime preparation', () => {
  it('keeps activation off and transfers exactly one exact revision', () => {
    expect(ARTIFACT_RUNTIME_ENABLED).toBe(false)
    const { channel, port } = transport()
    const identity = { artifactId: 'page-a', revision: 7, profile: 'interactive-html-v1' as const }
    channel.load({ ...identity, privateToken: 'must-not-transfer' } as typeof identity, '<p>one</p>')
    expect(port.postMessage).toHaveBeenCalledWith({ type: 'load', protocol: 1, identity, html: '<p>one</p>' })
    expect(() => channel.load({ ...identity, revision: 8 }, '<p>two</p>')).toThrow()
  })
  it.each([
    { type: 'runAction', pageId: 'other-private-page' },
    { type: 'fetch', url: '/api/v1/credentials' },
    { type: 'heartbeat', instruction: 'read another page' },
    { type: 'scroll', x: Infinity, y: 0 },
    { type: 'size', width: 1, height: -1 },
  ])('closes instead of dispatching untrusted authority requests %j', input => {
    const { port, events, receive } = transport()
    receive(input)
    expect(events).not.toHaveBeenCalled()
    expect(port.close).toHaveBeenCalledOnce()
    expect(port.postMessage).not.toHaveBeenCalled()
  })
  it('bounds telemetry frequency and stops callbacks after teardown', () => {
    const { receive, events, port, channel } = transport()
    for (let n = 0; n < 31; n++) receive({ type: 'heartbeat' })
    expect(events).toHaveBeenCalledTimes(30)
    expect(port.close).toHaveBeenCalledOnce()
    channel.close()
    receive({ type: 'ready' })
    expect(events).toHaveBeenCalledTimes(30)
  })
  it('rejects malformed metadata before transferring content', () => {
    const { channel, port } = transport()
    expect(() => channel.load({ artifactId: 'a', revision: 0, profile: 'interactive-html-v1' }, 'secret')).toThrow()
    expect(port.postMessage).not.toHaveBeenCalled()
    expect(parseArtifactRuntimeTelemetry({ type: 'ready' })).toEqual({ type: 'ready' })
  })
  it('requires response-header sandbox and uses no credential or host RPC', () => {
    expect(artifactRuntimePreparationCSP('https://studio.example.test')).toContain('sandbox allow-scripts;')
    expect(() => artifactRuntimePreparationCSP('https://studio.example.test/path')).toThrow()
    const html = artifactRuntimePreparationDocument('https://studio.example.test')
    expect(html).toContain("event.source !== parent")
    expect(html).toContain('event.origin !== parentOrigin')
    expect(html).toContain('document.open(); document.write(data.html); document.close()')
    expect(html).not.toContain('fetch(')
  })
})
