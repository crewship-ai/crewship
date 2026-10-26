import { expect, type Page } from '@playwright/test'

interface QueueEvent { type: 'ready' | 'applied'; revision?: number; own?: boolean }
interface QueueTrace { events: QueueEvent[]; ambiguous: boolean; overflow: boolean }

/** A read-only listener on actual parent-owned MessageChannel ports. It does
 * not post/start/replay a message, call WASM, or replace native application data.
 * Queue consumption is deliberately not described as a GPU paint ACK. */
export function observeNativeQueue() {
  const browser = window as unknown as Window & typeof globalThis & { __wordcraftCEQueue: QueueTrace }
  const trace: QueueTrace = { events: [], ambiguous: false, overflow: false }
  browser.__wordcraftCEQueue = trace
  const Original = browser.MessageChannel
  let serial = 0, selected: number | undefined
  class ObservedChannel extends Original {
    constructor() {
      super()
      const channel = ++serial
      this.port1.addEventListener('message', event => {
        if (typeof event.data !== 'string' || event.data.length > 2048) return
        let value: Record<string, unknown>
        try { value = JSON.parse(event.data) } catch { return }
        if (!value || value.protocol !== 1 || typeof value !== 'object' || Array.isArray(value)) return
        let observed: QueueEvent
        if (value.type === 'wordcraft-command:ready' && Object.keys(value).length === 2) observed = { type: 'ready' }
        else if (value.type === 'wordcraft-command:applied' && Object.keys(value).length === 4 && typeof value.own === 'boolean' && Number.isSafeInteger(value.revision) && Number(value.revision) >= 0) observed = { type: 'applied', own: value.own, revision: Number(value.revision) }
        else return
        selected ??= channel
        if (selected !== channel) { trace.ambiguous = true; return }
        if (trace.events.length >= 1024) { trace.overflow = true; return }
        trace.events.push(observed)
      })
    }
  }
  Object.defineProperty(browser, 'MessageChannel', { value: ObservedChannel, configurable: true, writable: true })
}
export async function installNativeQueueObserver(page: Page) { await page.addInitScript(observeNativeQueue) }
export async function waitNativeQueue(page: Page, revision: string, initialRevision?: string, own?: boolean) {
  await expect.poll(async () => {
    const trace = await page.evaluate(() => (window as unknown as { __wordcraftCEQueue?: QueueTrace }).__wordcraftCEQueue)
    if (!trace || trace.ambiguous || trace.overflow) return false
    return trace.events.some(event => event.type === 'applied' && String(event.revision) === revision && (own === undefined || event.own === own))
      || own === undefined && initialRevision === revision && trace.events.some(event => event.type === 'ready')
  }, { timeout: 30_000, message: 'The actual native command queue did not consume this projection (this is not a paint ACK)' }).toBe(true)
}
