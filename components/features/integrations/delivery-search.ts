/**
 * The one search box on /integrations used to filter Connections only; on
 * Deliveries and My preferences its placeholder changed and typing did
 * nothing (audit-fleet.md §5 item 8, §6 P2 22). These are the filters the
 * other two sections apply to the same box.
 */
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"

export function deliveryMatches(d: NotificationDelivery, q: string, channelName?: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return [d.category, d.title, d.status, d.error, channelName, d.source_kind]
    .some((f) => typeof f === "string" && f.toLowerCase().includes(needle))
}

export function filterDeliveries(
  deliveries: NotificationDelivery[],
  q: string,
  channelNameOf: (channelId: string) => string | undefined,
): NotificationDelivery[] {
  if (!q.trim()) return deliveries
  return deliveries.filter((d) => deliveryMatches(d, q, channelNameOf(d.channel_id)))
}

export function channelMatches(ch: { name?: string | null; provider?: string | null; type: string }, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return [ch.name, ch.provider, ch.type].some((f) => typeof f === "string" && f.toLowerCase().includes(needle))
}
