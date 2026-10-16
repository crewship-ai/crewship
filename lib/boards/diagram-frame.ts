import type { BoardElement } from './revision-adapter'
import type { BoardRequestGeometry } from '@/hooks/use-board-requests'
import { supportsPageApplications } from '@/lib/pages/runtime-support'
import { validateBoardDiagram } from './diagram-profile'
export function boardConversionSupported(url: string | undefined): boolean {
  if (!url || typeof window === 'undefined' || !supportsPageApplications(navigator.userAgent)) return false
  try { const runtime = new URL(url); return runtime.protocol === 'https:' && runtime.origin !== location.origin && !runtime.username && !runtime.password && !runtime.search && !runtime.hash && runtime.pathname === '/api/v1/pages/runtime/board-converter' } catch { return false }
}
/** The DOM-based converter runs only in an opaque frame on the server's
 * configured separate runtime site. It receives no service credentials. */
export async function convertBoardDiagram(source: string, target: BoardRequestGeometry, converterUrl?: string, signal?: AbortSignal): Promise<BoardElement[]> {
  const safe = validateBoardDiagram(source)
  if (!boardConversionSupported(converterUrl)) throw new Error('Isolated diagram conversion requires a configured separate runtime site and supported Chromium browser.')
  if (signal?.aborted) throw new DOMException('Conversion cancelled.', 'AbortError')
  return new Promise((resolve, reject) => {
    const nonce = crypto.randomUUID()
    const frame = document.createElement('iframe')
    frame.title = 'Isolated diagram conversion'; frame.sandbox.add('allow-scripts'); frame.referrerPolicy = 'no-referrer'
    frame.style.cssText = 'position:fixed;left:-10000px;top:0;width:1200px;height:900px;visibility:hidden;pointer-events:none'
    const channel = new MessageChannel()
    let settled = false; let admitted = false
    const finish = (error?: Error, elements?: BoardElement[]) => {
      if (settled) return; settled = true; clearTimeout(timer)
      signal?.removeEventListener('abort', abort); window.removeEventListener('message', ready); channel.port1.close(); channel.port2.close(); frame.remove()
      if (error) reject(error); else resolve(elements!)
    }
    const abort = () => finish(new DOMException('Conversion cancelled.', 'AbortError'))
    signal?.addEventListener('abort', abort, { once: true })
    const timer = setTimeout(() => finish(new Error('Isolated diagram conversion timed out. No board changes were submitted.')), 15000)
    const ready = (event: MessageEvent) => {
      if (admitted || event.source !== frame.contentWindow || event.origin !== 'null' || event.data?.type !== 'crewship.converter.ready' || event.data?.nonce !== nonce) return
      admitted = true
      channel.port1.onmessage = event => {
        try {
        const data = event.data
        if (data?.nonce !== nonce || data?.type !== 'crewship.converter.result') return finish(new Error('Invalid isolated converter response.'))
        if (typeof data.error === 'string') return finish(new Error(data.error.slice(0, 512)))
        const elements = data.elements
        if (!Array.isArray(elements) || !elements.length || elements.length > 500 || JSON.stringify(elements).length > 2 * 1024 * 1024) return finish(new Error('Isolated converter exceeded output limits.'))
        const types = new Set(['rectangle','ellipse','diamond','line','arrow','text','freedraw','frame'])
        if (elements.some(item => !item || typeof item.id !== 'string' || !types.has(item.type) || item.link != null || item.customData != null || !Number.isFinite(item.x) || !Number.isFinite(item.y))) return finish(new Error('Isolated converter returned unsupported geometry.'))
        finish(undefined, elements)
        } catch { finish(new Error('Invalid isolated converter response.')) }
      }
      frame.contentWindow!.postMessage({ type: 'crewship.converter.connect', nonce }, '*', [channel.port2])
      channel.port1.postMessage({ type: 'crewship.converter.input', nonce, source: safe, target })
    }
    window.addEventListener('message', ready)
    frame.src = `${converterUrl}#${encodeURIComponent(nonce)}`
    document.body.append(frame)
  })
}
