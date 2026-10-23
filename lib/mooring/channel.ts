/** Mooring's capability channel. The window handshake transfers one private port;
 * document content and mutations never travel over the public window channel. */
export const MOORING_PROTOCOL = 1
export const MOORING_MAX_MESSAGE_BYTES = 64 * 1024
export const MOORING_FRAME_ATTRIBUTES = Object.freeze({ sandbox: "allow-scripts", credentialless: true, referrerPolicy: "no-referrer" } as const)
export interface CapabilityPort {
  postMessage(value: string): void
  onmessage: ((event: { data: unknown }) => void) | null
  start?(): void
  close(): void
}
export interface CapabilityWindow { postMessage(value: unknown, origin: string, transfer?: unknown[]): void }
export interface WindowEnvelope { source: unknown; origin: string; data: unknown }
export const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value)
export const revision = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
export const bundleDigest = (value: unknown): value is string => typeof value === "string" && /^[0-9a-f]{64}$/.test(value)
/** null: unrelated message; otherwise the verified hello or a fail-closed reason. */
export function verifyEditorHello(event: WindowEnvelope, frame: unknown, options: { namespace: string; bundle: string; waiting: boolean; loads: number }): "accept" | "handshake" | "bundle" | null {
  if (!frame || event.source !== frame) return null
  if (event.origin !== "null" || !options.waiting) return "handshake"
  const data = event.data
  if (!record(data) || data.type !== `${options.namespace}:hello`) return null
  if (Object.keys(data).length !== 3 || !["type", "protocol", "bundle"].every(key => key in data) || data.protocol !== MOORING_PROTOCOL || !bundleDigest(options.bundle)) return "handshake"
  if (data.bundle !== options.bundle) return "bundle"
  return options.loads === 1 ? "accept" : "handshake"
}
export function transferEditorPort(frame: CapabilityWindow, channel: { port1: CapabilityPort; port2: unknown }, namespace: string, metadata: Record<string, unknown>, receive: (data: unknown) => void): CapabilityPort {
  channel.port1.onmessage = event => receive(event.data)
  channel.port1.start?.()
  // An opaque origin has no addressable targetOrigin. The caller must first
  // verifyEditorHello against its own iframe window in this same task.
  frame.postMessage({ ...metadata, type: `${namespace}:connect`, protocol: MOORING_PROTOCOL }, "*", [channel.port2])
  return channel.port1
}
export function decodeMessage(raw: unknown): Record<string, unknown> | null {
  if (typeof raw !== "string" || new TextEncoder().encode(raw).length > MOORING_MAX_MESSAGE_BYTES) return null
  try {
    const value: unknown = JSON.parse(raw)
    return record(value) && value.protocol === MOORING_PROTOCOL ? value : null
  } catch { return null }
}
/** Narrow native ports at the DOM boundary; tests can supply minimal ports. */
export function capabilityPort(port: MessagePort): CapabilityPort { return port as unknown as CapabilityPort }
export function createCapabilityChannel(): { port1: CapabilityPort; port2: MessagePort } {
  const channel = new MessageChannel()
  return { port1: capabilityPort(channel.port1), port2: channel.port2 }
}
