/** Delivery progress only: an idle socket is healthy, an unconfirmed original
 * is not. This observer never sends, retries, changes identity or acknowledges
 * Saved. Only a receipt qualified by the existing SDK can confirm an original. */
export type DeliveryHealth = "connected" | "checking" | "disconnected"
export function observeDeliveryHealth(changed: (state: DeliveryHealth) => void) {
  const pending = new Map<string, ReturnType<typeof setTimeout>>()
  const stalled = new Set<string>()
  let closed = false, state: DeliveryHealth = "connected"
  const publish = (next: DeliveryHealth) => {
    if (state !== next) { state = next; changed(next) }
  }
  return {
    sent(id: string) {
      if (closed || pending.has(id)) return
      pending.set(id, setTimeout(() => {
        if (closed || !pending.has(id)) return
        stalled.add(id); publish("checking")
      }, 2_000))
    },
    confirmed(id: string) {
      if (closed) return
      clearTimeout(pending.get(id)); pending.delete(id); stalled.delete(id)
      if (!stalled.size) publish("connected")
    },
    close() {
      if (closed) return
      closed = true
      for (const timer of pending.values()) clearTimeout(timer)
      pending.clear(); stalled.clear(); publish("disconnected")
    },
  }
}
