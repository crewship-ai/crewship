// What this instance can deliver a notification to, as one list.
//
// This lived inside the Catalog tab's component. It is here because the tab is
// gone — the same list now feeds the "Add integration" flow and the counts in
// the sub-bar, and two copies of "how many services are there" is exactly the
// contradiction this page already had once.

import type {
  NotificationProvider,
  NotificationProviderCategory,
} from "@/hooks/use-notification-channels"
import type { ServiceOption } from "./add-integration-dialog"

/** Section for the transports that are not third-party providers. */
export const BUILTIN_SECTION = "builtin"

/**
 * Entries added on top of the provider registry: e-mail and webhook.
 *
 * Tools/MCP is deliberately NOT in here. It is a different kind of
 * integration with its own connect flow, and folding it into this list is
 * what made the old catalog offer a card that could not be completed.
 */
export const CATALOG_EXTRA_ENTRIES = 2

/** Total notification services for a given provider registry size. */
export function catalogSize(providerCount: number): number {
  return providerCount + CATALOG_EXTRA_ENTRIES
}

/** Section list = the server's provider categories, plus the built-ins. */
export function catalogSections(
  serverCategories: NotificationProviderCategory[],
): NotificationProviderCategory[] {
  return [
    ...serverCategories,
    {
      key: BUILTIN_SECTION,
      label: "E-mail & webhook",
      hint: "Built in — no third-party service involved",
    },
  ]
}

/** Static export for callers that only need the built-in section's identity. */
export const CATALOG_SECTIONS = { BUILTIN_SECTION }

/**
 * Normalise the provider registry into pickable services.
 *
 * `usage` is connections-per-provider, so a service you already use says so
 * instead of looking untouched.
 */
export function buildServiceOptions(
  providers: NotificationProvider[],
  usage: Record<string, number>,
): ServiceOption[] {
  const fromProviders: ServiceOption[] = providers.map((p) => ({
    key: p.provider,
    label: p.label,
    blurb: p.blurb,
    // An older server sends no category; park those with the built-ins rather
    // than dropping them — an uncategorised provider still works.
    section: p.category || BUILTIN_SECTION,
    available: p.enabled,
    used: usage[p.provider] ?? 0,
  }))
  return [
    ...fromProviders,
    {
      key: "email",
      label: "E-mail",
      // Deliberately not gated on a "mail is configured" flag: no endpoint
      // reports one, and inferring it would be a guess rendered as fact. An
      // instance with no transport rejects the create with its own message,
      // which the form surfaces verbatim.
      blurb: "Send to any address, using this instance's mail transport",
      section: BUILTIN_SECTION,
      available: true,
      used: usage.email ?? 0,
    },
    {
      key: "webhook",
      label: "Webhook",
      blurb: "A signed POST to an endpoint you control",
      section: BUILTIN_SECTION,
      available: true,
      used: usage.webhook ?? 0,
    },
  ]
}
