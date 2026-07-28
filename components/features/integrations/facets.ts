// Facet builders for the two tabs.
//
// The explorer renders whatever FacetGroups it is handed; these turn each
// tab's data into that shape. Kept out of the explorer so the two tabs share
// one rendering and one interaction model while still filtering on completely
// different things.

import {
  KIND_LABEL,
  STATUS_LABEL,
  type ConnectionFilters,
  type ConnectionKind,
  type ConnectionRow,
  type ConnectionStatus,
} from "./connection-model"
import type { FacetGroup } from "./explorer"
import type { ComposioStatus } from "./composio-integrations"
import type { McpFilters } from "./mcp-filters"

const KIND_ORDER: ConnectionKind[] = ["chat", "push", "incident", "email", "webhook", "tools"]
const STATUS_ORDER: ConnectionStatus[] = [
  "delivering",
  "failing",
  "never_used",
  "unknown",
  "disabled",
]

const STATUS_DOT: Record<ConnectionStatus, string> = {
  delivering: "bg-emerald-400",
  failing: "bg-red-400",
  never_used: "bg-amber-400",
  disabled: "bg-muted-foreground/40",
  unknown: "bg-sky-400",
}

/**
 * Count a facet's buckets against the rows the OTHER facets already allow.
 *
 * So a count answers "what would I get if I clicked this" rather than "how
 * many exist somewhere". The facet being counted is excluded from its own
 * narrowing, or every bucket but the selected one would read 0 and the menu
 * would look broken.
 */
function countBy<K extends string>(
  rows: ConnectionRow[],
  filters: ConnectionFilters,
  ignore: keyof ConnectionFilters,
  key: (r: ConnectionRow) => K,
): Record<K, number> {
  const out = {} as Record<K, number>
  for (const r of rows) {
    if (ignore !== "kind" && filters.kind !== "all" && r.kind !== filters.kind) continue
    if (ignore !== "status" && filters.status !== "all" && r.status !== filters.status) continue
    if (ignore !== "scope" && filters.scope !== "all" && r.scope !== filters.scope) continue
    if (ignore !== "provider" && filters.provider && r.provider !== filters.provider) continue
    const k = key(r)
    out[k] = (out[k] ?? 0) + 1
  }
  return out
}

export function notificationFacets(
  rows: ConnectionRow[],
  filters: ConnectionFilters,
  onChange: (next: ConnectionFilters) => void,
): FacetGroup[] {
  const set = (patch: Partial<ConnectionFilters>) => onChange({ ...filters, ...patch })

  const kinds = countBy(rows, filters, "kind", (r) => r.kind)
  const statuses = countBy(rows, filters, "status", (r) => r.status)
  const scopes = countBy(rows, filters, "scope", (r) => r.scope)
  const providers = countBy(rows, filters, "provider", (r) => r.provider)

  const providerLabels = new Map<string, string>()
  for (const r of rows) providerLabels.set(r.provider, r.providerLabel)

  return [
    {
      key: "kind",
      label: "Kind",
      selected: filters.kind === "all" ? null : filters.kind,
      onSelect: (v) => set({ kind: (v as ConnectionKind) ?? "all" }),
      options: KIND_ORDER.filter((k) => (kinds[k] ?? 0) > 0 || filters.kind === k).map((k) => ({
        value: k,
        label: KIND_LABEL[k],
        count: kinds[k] ?? 0,
      })),
    },
    {
      key: "status",
      label: "Status",
      selected: filters.status === "all" ? null : filters.status,
      onSelect: (v) => set({ status: (v as ConnectionStatus) ?? "all" }),
      options: STATUS_ORDER.filter(
        (st) => (statuses[st] ?? 0) > 0 || filters.status === st,
      ).map((st) => ({
        value: st,
        label: STATUS_LABEL[st],
        count: statuses[st] ?? 0,
        dot: STATUS_DOT[st],
      })),
    },
    {
      key: "scope",
      label: "Scope",
      selected: filters.scope === "all" ? null : filters.scope,
      onSelect: (v) => set({ scope: (v as "workspace" | "personal") ?? "all" }),
      options: (["workspace", "personal"] as const)
        .filter((sc) => (scopes[sc] ?? 0) > 0 || filters.scope === sc)
        .map((sc) => ({
          value: sc,
          label: sc === "workspace" ? "Workspace" : "Personal",
          count: scopes[sc] ?? 0,
        })),
    },
    {
      key: "provider",
      label: "Service",
      selected: filters.provider,
      onSelect: (v) => set({ provider: v }),
      options: [...providerLabels.keys()]
        .filter((p) => (providers[p] ?? 0) > 0 || filters.provider === p)
        .sort((a, b) => (providers[b] ?? 0) - (providers[a] ?? 0) || a.localeCompare(b))
        .map((p) => ({
          value: p,
          label: providerLabels.get(p) ?? p,
          count: providers[p] ?? 0,
          mark: p,
        })),
    },
  ]
}

export function mcpFacets(
  status: ComposioStatus,
  filters: McpFilters,
  onChange: (next: McpFilters) => void,
): FacetGroup[] {
  const set = (patch: Partial<McpFilters>) => onChange({ ...filters, ...patch })
  return [
    {
      key: "toolkit",
      label: "Toolkit",
      selected: filters.toolkit,
      onSelect: (v) => set({ toolkit: v }),
      options: status.toolkits.map((t) => ({ value: t.slug, label: t.slug, count: t.count })),
    },
    {
      key: "user",
      label: "User",
      selected: filters.user,
      onSelect: (v) => set({ user: v }),
      options: status.users.map((u) => ({ value: u.id, label: u.id, count: u.count })),
    },
  ]
}
