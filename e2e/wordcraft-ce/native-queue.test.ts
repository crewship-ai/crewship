import { expect, it, vi } from 'vitest'
import { observeNativeQueue } from './native-queue'

it('only listens to real port messages without posting, starting or mutating an engine', () => {
  const ports: { listener?: (event: { data: unknown }) => void; start: ReturnType<typeof vi.fn>; postMessage: ReturnType<typeof vi.fn> }[] = []
  class Channel {
    port1 = { start: vi.fn(), postMessage: vi.fn(), addEventListener: (kind: string, listener: (event: { data: unknown }) => void) => { expect(kind).toBe('message'); ports[ports.length - 1].listener = listener } }
    port2 = { start: vi.fn(), postMessage: vi.fn() }
    constructor() { ports.push(this.port1) }
  }
  const browser = { MessageChannel: Channel } as { MessageChannel: typeof Channel; __wordcraftCEQueue?: { events: unknown[]; ambiguous: boolean; overflow: boolean } }
  vi.stubGlobal('window', browser)
  try {
    observeNativeQueue()
    const first = new browser.MessageChannel()
    const send = (index: number, value: unknown) => ports[index].listener!({ data: JSON.stringify(value) })
    send(0, { protocol: 1, type: 'wordcraft-command:ready' })
    send(0, { protocol: 1, type: 'wordcraft-command:applied', own: true, revision: 4 })
    send(0, { protocol: 1, type: 'wordcraft-command:applied', own: true, revision: 4, fake: true })
    send(0, { protocol: 1, type: 'wordcraft-command:applied', own: true, revision: -1 })
    expect(browser.__wordcraftCEQueue?.events).toEqual([{ type: 'ready' }, { type: 'applied', own: true, revision: 4 }])
    expect(first.port1.start).not.toHaveBeenCalled()
    expect(first.port1.postMessage).not.toHaveBeenCalled()
    expect(first.port2.start).not.toHaveBeenCalled()
    expect(first.port2.postMessage).not.toHaveBeenCalled()
    new browser.MessageChannel()
    send(1, { protocol: 1, type: 'wordcraft-command:ready' })
    expect(browser.__wordcraftCEQueue?.ambiguous).toBe(true)
    for (let i = 0; i < 1100; i++) send(0, { protocol: 1, type: 'wordcraft-command:applied', own: false, revision: i })
    expect(browser.__wordcraftCEQueue?.events).toHaveLength(1024)
    expect(browser.__wordcraftCEQueue?.overflow).toBe(true)
  } finally { vi.unstubAllGlobals() }
})
