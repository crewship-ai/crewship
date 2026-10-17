import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { boardConversionSupported, convertBoardDiagram } from './diagram-frame'
const url = 'https://converter.example.net/api/v1/pages/runtime/board-converter'
const target = { x: 80, y: 80, width: 640, height: 420 }
class Port { onmessage: ((event: MessageEvent) => void) | null = null; close = vi.fn(); postMessage = vi.fn() }
class Channel { static latest: Channel; port1 = new Port(); port2 = new Port(); constructor() { Channel.latest = this } }
const sandboxDescriptor = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'sandbox')
beforeEach(() => {
  vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue('Mozilla/5.0 Chrome/135.0.0.0 Safari/537.36')
  vi.stubGlobal('MessageChannel', Channel)
  vi.spyOn(HTMLIFrameElement.prototype, 'src', 'set').mockImplementation(function(this: HTMLIFrameElement, value: string) { this.setAttribute('data-test-src', value) })
  vi.spyOn(HTMLIFrameElement.prototype, 'src', 'get').mockImplementation(function(this: HTMLIFrameElement) { return 'about:blank' })
  Object.defineProperty(HTMLIFrameElement.prototype, 'sandbox', { configurable: true, get() { return document.createElement('div').classList } })
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); document.querySelectorAll('iframe').forEach(frame => frame.remove()); if (sandboxDescriptor) Object.defineProperty(HTMLIFrameElement.prototype, 'sandbox', sandboxDescriptor); else Reflect.deleteProperty(HTMLIFrameElement.prototype, 'sandbox') })
it('withholds conversion on mobile, unsupported browser, absent runtime and same-origin runtime', () => {
  expect(boardConversionSupported(url)).toBe(true)
  expect(boardConversionSupported('')).toBe(false)
  expect(boardConversionSupported(`${location.origin}/api/v1/pages/runtime/board-converter`)).toBe(false)
  vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue('Mozilla/5.0 Android Chrome/135.0.0.0 Mobile')
  expect(boardConversionSupported(url)).toBe(false)
})
it('removes the opaque frame and closes both ports on navigation cancellation', async () => {
  const controller = new AbortController()
  const pending = convertBoardDiagram('flowchart TD\nA --> B', target, url, controller.signal)
  expect(document.querySelectorAll('iframe')).toHaveLength(1)
  controller.abort()
  await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  expect(document.querySelectorAll('iframe')).toHaveLength(0)
  expect(Channel.latest.port1.close).toHaveBeenCalledOnce(); expect(Channel.latest.port2.close).toHaveBeenCalledOnce()
})
it('ignores forged readiness and fails closed on cyclic child output', async () => {
  const pending = convertBoardDiagram('flowchart TD\nA --> B', target, url)
  const frame = document.querySelector('iframe')!
  const nonce = new URL(frame.getAttribute('data-test-src')!).hash.slice(1)
  const data = { type: 'crewship.converter.ready', nonce }
  window.dispatchEvent(new MessageEvent('message', { source: window, origin: 'null', data }))
  expect(Channel.latest.port1.postMessage).not.toHaveBeenCalled()
  vi.spyOn(frame.contentWindow!, 'postMessage').mockImplementation(() => undefined)
  window.dispatchEvent(new MessageEvent('message', { source: frame.contentWindow, origin: 'null', data }))
  expect(Channel.latest.port1.postMessage).toHaveBeenCalledOnce()
  const cyclic: unknown[] = []; cyclic.push(cyclic)
  Channel.latest.port1.onmessage!(new MessageEvent('message', { data: { type: 'crewship.converter.result', nonce, elements: cyclic } }))
  await expect(pending).rejects.toThrow('Invalid isolated converter response')
  expect(document.querySelectorAll('iframe')).toHaveLength(0)
})
