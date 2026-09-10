import { useSyncExternalStore } from 'react'

export interface PagePanel {
  id: string
  title: string
  schema: string
  state: string
  data: unknown
  producedAt: string | null
}
export interface PageTheme { accent: string; background: string; surface: string; text: string; muted: string; border: string }
export interface PageSnapshot { slug: string; name: string; panels: PagePanel[]; theme?: PageTheme }
const themeKeys = ['accent', 'background', 'surface', 'text', 'muted', 'border'] as const
function applyTheme(theme?: PageTheme) {
  if (theme?.background && /^#[0-9a-f]{6}$/i.test(theme.background)) {
    const rgb = [1,3,5].map(i => parseInt(theme.background.slice(i,i+2),16)/255)
    document.documentElement.style.colorScheme = rgb[0]*.2126+rgb[1]*.7152+rgb[2]*.0722 > .5 ? 'light' : 'dark'
  }
  if (theme?.accent && /^#[0-9a-f]{6}$/i.test(theme.accent)) {
    const rgb = [1,3,5].map(i => parseInt(theme.accent.slice(i,i+2),16)/255).map(v => v <= .04045 ? v/12.92 : ((v+.055)/1.055)**2.4)
    document.documentElement.style.setProperty('--crewship-page-on-accent', rgb[0]*.2126+rgb[1]*.7152+rgb[2]*.0722 > .179 ? '#000000' : '#ffffff')
  }

  for (const key of themeKeys) {
    const value = theme?.[key]
    if (typeof value === 'string' && /^#[0-9a-f]{6}$/i.test(value)) document.documentElement.style.setProperty('--crewship-page-' + key, value)
  }
}
let snapshot: PageSnapshot | null = null
const listeners = new Set<() => void>()
declare global { interface Window { __crewshipPagesPort?: MessagePort } }
const port = window.__crewshipPagesPort
delete window.__crewshipPagesPort
if (port) {
  port.onmessage = (message) => {
    if (message.data?.type === 'crewship.pages.response/v1') {
      const pending = requests.get(message.data.id)
      if (!pending) return
      requests.delete(message.data.id)
      clearTimeout(pending.timer)
      if (message.data.error) pending.reject(new Error(message.data.error))
      else pending.resolve(message.data.result)
      return
    }
    if (message.data?.type !== 'crewship.pages.snapshot/v1') return
    snapshot = message.data.snapshot
    applyTheme(snapshot?.theme)
    try { for (const listener of listeners) listener() }
    finally { port.postMessage({ type: 'crewship.pages.snapshot-ack/v1', seq: message.data.seq }) }
  }
  port.start()
}
export function getSnapshot() { return snapshot }
export function subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener) } }
export function usePageSnapshot() { return useSyncExternalStore(subscribe, getSnapshot, () => null) }
export function usePageTheme() { return usePageSnapshot()?.theme ?? null }
export function usePanel(id: string) { return usePageSnapshot()?.panels.find(panel => panel.id === id) }

export interface ActionReceipt { pending_id: string; [key: string]: unknown }
export interface ActionStatus { pending_id: string; pending_status: string; run_id: string; run_status: string }
let requestID = 0
const requests = new Map<number, { resolve: (value: unknown) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>()
function request<T>(method: string, params: Record<string, unknown>): Promise<T> {
  if (!port) return Promise.reject(new Error('Crewship connection is unavailable.'))
  if (requests.size >= 32) return Promise.reject(new Error('Too many pending Page requests.'))
  return new Promise<T>((resolve, reject) => {
    const id = ++requestID
    const timer = setTimeout(() => {
      requests.delete(id)
      reject(new Error('Request timed out. A submitted action may still run; check its status before retrying with the same idempotency key.'))
    }, 90_000)
    requests.set(id, { resolve: value => resolve(value as T), reject, timer })
    try { port.postMessage({ type: 'crewship.pages.request/v1', id, method, params }) }
    catch (error) { clearTimeout(timer); requests.delete(id); reject(error) }
  })
}
// The host confirms the declared routine. A receipt means queued, not completed.
// Supply the same key for an explicit retry; the SDK never retries a write.
export function runAction(panelId: string, actionId: string, inputs: Record<string, unknown> = {}, options: { idempotencyKey?: string } = {}) {
  return request<ActionReceipt>('runAction', { panelId, actionId, inputs, idempotencyKey: options.idempotencyKey ?? Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('') })
}
export function getActionStatus(pendingId: string) { return request<ActionStatus>('getActionStatus', { pendingId }) }

export interface PanelHistory {
  items: { sequence: number; data: unknown; producedAt: string; state: string }[]
  next_before: number
  publication: number
}
// The server returns at most 20 readable snapshots and 1 MiB per request.
export function getPanelHistory(panelId: string, options: { limit?: number; before?: number } = {}) {
  return request<PanelHistory>('getPanelHistory', { panelId, ...options })
}
