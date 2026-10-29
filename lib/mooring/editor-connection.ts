/** Shared editor-side capability client. JSON and native document profiles use
 * this exact transport; neither profile owns window listeners or MessagePorts. */
import { MOORING_PROTOCOL, MOORING_MAX_MESSAGE_BYTES, bundleDigest, capabilityPort, decodeMessage, record, type CapabilityPort, type WindowEnvelope } from "./channel"

export interface EditorEnvironment {
  parent: unknown
  listen(handler: (event: WindowEnvelope & { ports?: CapabilityPort[] }) => void): () => void
  hello(message: unknown, targetOrigin: string): void
}
export interface EditorConnectionOptions {
  parentOrigin: string
  bundle: string
  namespace: string
  environment: EditorEnvironment
  maxHostMessageBytes?: number
  onConnect(): void
  onMessage(message: Record<string, unknown>, raw: string): void
  onError(message: string): void
}
export function createEditorConnection(options: EditorConnectionOptions) {
  const parentURL = new URL(options.parentOrigin)
  if (parentURL.origin !== options.parentOrigin || !["https:", "http:"].includes(parentURL.protocol) || !bundleDigest(options.bundle) || !/^[a-z][a-z0-9-]{0,63}$/.test(options.namespace)) throw new Error("Invalid host configuration")
  let port: CapabilityPort | undefined, closed = false
  const unlisten = options.environment.listen(event => {
    if (closed || event.source !== options.environment.parent || event.origin !== options.parentOrigin) return
    const data = event.data
    if (!record(data) || data.type !== `${options.namespace}:connect`) return
    if (port || data.protocol !== MOORING_PROTOCOL || event.ports?.length !== 1) { options.onError("Unsupported protocol or duplicate connection"); return }
    port = event.ports[0]
    port.onmessage = event => {
      if (closed) return
      const message = decodeMessage(event.data, options.maxHostMessageBytes ?? MOORING_MAX_MESSAGE_BYTES)
      if (!message) { options.onError("Invalid host message"); return }
      options.onMessage(message, event.data as string)
    }
    port.start?.(); options.onConnect()
  })
  return {
    get connected() { return !!port && !closed },
    hello() { if (!closed) options.environment.hello({ type: `${options.namespace}:hello`, protocol: MOORING_PROTOCOL, bundle: options.bundle }, options.parentOrigin) },
    send(raw: string): boolean {
      if (!port || closed) return false
      if (!decodeMessage(raw) || new TextEncoder().encode(raw).length > MOORING_MAX_MESSAGE_BYTES) { options.onError("Invalid editor message"); return false }
      try { port.postMessage(raw); return true } catch { options.onError("The editor connection is unavailable"); return false }
    },
    close() { closed = true; unlisten(); if (port) { port.onmessage = null; port.close() } },
  }
}

/** Real browser environment shared by native and TypeScript applications. */
export function createBrowserEditorEnvironment(parentOrigin: string, browser: Window = window): EditorEnvironment {
  return {
    parent: browser.parent,
    listen(handler) {
      const listener = (event: MessageEvent) => {
        if (event.source !== browser.parent || event.origin !== parentOrigin) return
        handler({ source: event.source, origin: event.origin, data: event.data, ports: event.ports.map(capabilityPort) })
      }
      browser.addEventListener("message", listener)
      return () => browser.removeEventListener("message", listener)
    },
    hello: (message, origin) => browser.parent.postMessage(message, origin),
  }
}
/** The host must observe iframe load before receiving hello; this schedules a
 * separate task after load, including when a native module initialized late. */
export function helloAfterLoad(hello: () => void, browser: Window = window): () => void {
  let timer: number | undefined
  const loaded = () => { timer = browser.setTimeout(hello, 0) }
  if (browser.document.readyState === "complete") loaded()
  else browser.addEventListener("load", loaded, { once: true })
  return () => { browser.removeEventListener("load", loaded); if (timer !== undefined) browser.clearTimeout(timer) }
}
