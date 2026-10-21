// Integrations is four spaces, named by who starts and who receives (Harbor 2):
//
//   Tools     agents act in other apps (Composio)
//   Triggers  something outside starts work (routine, agent and page webhooks)
//   Channels  Crewship reaches people (chat apps, email, push, incident tools)
//   Webhooks  Crewship sends events to your systems as signed JSON
//
// Channels and outgoing webhooks are one backend (notification channels); they
// split on `type === "webhook"`. The URL is `?space=&view=&id=&pane=`; the
// three-tab `?tab=&section=` links the page used before are read here so every
// old link (palette, Settings' retired tabs, Routines, docs) still lands.

import type { IncomingKind } from "./incoming-model"

export type IntegrationsSpace = "tools" | "triggers" | "channels" | "webhooks"

export const SPACES: IntegrationsSpace[] = ["tools", "triggers", "channels", "webhooks"]

/** The views each space's panel offers, first one = the default. */
export const SPACE_VIEWS = {
  tools: ["connected", "attention", "idle", "catalog", "crew-tools", "mcp"],
  triggers: ["all", "failing", "off"],
  channels: ["all", "failing", "paused", "mine", "preferences", "deliveries"],
  webhooks: ["all", "failing", "paused"],
} as const satisfies Record<IntegrationsSpace, readonly string[]>

export type SpaceView<S extends IntegrationsSpace = IntegrationsSpace> = (typeof SPACE_VIEWS)[S][number]

export interface IntegrationsRoute {
  space: IntegrationsSpace
  view: string
  /** The open object: a channel, a trigger, a connected account or a catalog app. */
  id: string | null
  /** The open object's tab. */
  pane: string | null
  /** Tools › Crew MCP servers: the server a "Connect" link elsewhere named. */
  server: string | null
  /** Triggers › Starts facet. */
  starts: IncomingKind | null
  /** Triggers: one routine, agent or page (id or slug). */
  target: string | null
}

const KINDS: IncomingKind[] = ["routine", "agent", "page"]
const isKind = (v: string | null): v is IncomingKind => v !== null && (KINDS as string[]).includes(v)
const isSpace = (v: string | null): v is IntegrationsSpace => v !== null && (SPACES as string[]).includes(v)

export function defaultView(space: IntegrationsSpace): string {
  return SPACE_VIEWS[space][0]
}

function viewOf(space: IntegrationsSpace, value: string | null): string {
  return value && (SPACE_VIEWS[space] as readonly string[]).includes(value) ? value : defaultView(space)
}

const LEGACY_NOTIFY: Record<string, string> = { connections: "all", preferences: "preferences", deliveries: "deliveries" }
// Agent access, Tools and Triggers were sections of their own; they now live
// on each app's page, so the link lands on the app list.
const LEGACY_TOOLS: Record<string, string> = {
  catalog: "catalog",
  accounts: "connected",
  agents: "connected",
  tools: "connected",
  triggers: "connected",
  mcp: "mcp",
  "crew-tools": "crew-tools",
}

export function parseIntegrationsRoute(search: string, fallback: IntegrationsSpace = "tools"): IntegrationsRoute {
  const p = new URLSearchParams(search)
  const empty = { id: null, pane: null, server: null, starts: null, target: null }

  const space = p.get("space")
  if (isSpace(space)) {
    const starts = space === "triggers" && isKind(p.get("starts")) ? (p.get("starts") as IncomingKind) : null
    return {
      space,
      view: viewOf(space, p.get("view")),
      id: p.get("id") || null,
      pane: p.get("pane") || null,
      server: space === "tools" ? p.get("server") || null : null,
      starts,
      target: starts ? p.get("target") || null : null,
    }
  }

  const tab = p.get("tab")
  const section = p.get("section")
  if (tab === "incoming") {
    const starts = isKind(section) ? section : null
    return { ...empty, space: "triggers", view: "all", starts, target: starts ? p.get("target") || null : null }
  }
  if (tab === "tools") {
    const view = (section && LEGACY_TOOLS[section]) || "connected"
    return { ...empty, space: "tools", view, server: view === "crew-tools" ? p.get("server") || null : null }
  }
  if (tab === "notifications" || (tab === null && section !== null && section in LEGACY_NOTIFY)) {
    return { ...empty, space: "channels", view: (section && LEGACY_NOTIFY[section]) || "all" }
  }
  return { ...empty, space: fallback, view: defaultView(fallback) }
}

const OWN_KEYS = ["space", "view", "id", "pane", "server", "starts", "target", "tab", "section"]

/** The canonical query for a route, keeping any key this page does not own. */
export function integrationsSearch(current: string, route: IntegrationsRoute): string {
  const p = new URLSearchParams(current)
  for (const k of OWN_KEYS) p.delete(k)
  const out = new URLSearchParams()
  out.set("space", route.space)
  if (route.view !== defaultView(route.space)) out.set("view", route.view)
  if (route.id) out.set("id", route.id)
  if (route.id && route.pane) out.set("pane", route.pane)
  if (route.space === "tools" && route.server) out.set("server", route.server)
  if (route.space === "triggers" && route.starts) {
    out.set("starts", route.starts)
    if (route.target) out.set("target", route.target)
  }
  for (const [k, v] of p) out.append(k, v)
  return out.toString()
}

export function isWebhookChannel(ch: { type: string }): boolean {
  return ch.type === "webhook"
}

/** One list from GET /notification-channels, two spaces. */
export function splitChannels<T extends { type: string }>(rows: T[]): { channels: T[]; webhooks: T[] } {
  const channels: T[] = []
  const webhooks: T[] = []
  for (const r of rows) (isWebhookChannel(r) ? webhooks : channels).push(r)
  return { channels, webhooks }
}
