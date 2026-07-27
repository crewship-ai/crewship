import { describe, expect, it, vi } from "vitest"

import { mcpFacets, notificationFacets } from "../facets"
import { EMPTY_FILTERS, type ConnectionRow } from "../connection-model"
import { EMPTY_MCP_FILTERS } from "../mcp-filters"
import type { ComposioStatus } from "../composio-integrations"

function row(over: Partial<ConnectionRow> = {}): ConnectionRow {
  return {
    id: "r1",
    kind: "chat",
    name: "Engineering alerts",
    detail: "discord",
    provider: "discord",
    providerLabel: "Discord",
    scope: "workspace",
    enabled: true,
    categories: [],
    status: "delivering",
    sent24h: 0,
    lastDelivery: null,
    source: "channel",
    readOnly: false,
    ...over,
  }
}

const STATUS: ComposioStatus = {
  loading: false,
  configured: true,
  keyLabel: "prod",
  counts: { apps: 1042, accounts: 8, users: 1, agentsBound: 1, agentsTotal: 7, endpoints: 1 },
  toolkits: [
    { slug: "gmail", count: 3 },
    { slug: "discord", count: 1 },
  ],
  users: [{ id: "pg-test", count: 8 }],
}

describe("notificationFacets", () => {
  it("counts a bucket against what the OTHER facets already allow", () => {
    // A count has to answer "what would I get if I clicked this", not "how
    // many exist somewhere" — otherwise the menu offers filters that come
    // back empty.
    const rows = [
      row({ id: "a", kind: "chat", scope: "workspace" }),
      row({ id: "b", kind: "chat", scope: "personal" }),
      row({ id: "c", kind: "push", provider: "ntfy", scope: "workspace" }),
    ]
    const groups = notificationFacets(rows, { ...EMPTY_FILTERS, scope: "workspace" }, () => {})
    const kind = groups.find((g) => g.key === "kind")!
    expect(kind.options.find((o) => o.value === "chat")?.count).toBe(1)
    expect(kind.options.find((o) => o.value === "push")?.count).toBe(1)
  })

  it("does not let a facet narrow its own counts to zero", () => {
    // Excluding the active facet from its own filter is what keeps the other
    // buckets clickable instead of all reading 0.
    const rows = [row({ id: "a", kind: "chat" }), row({ id: "b", kind: "push", provider: "ntfy" })]
    const groups = notificationFacets(rows, { ...EMPTY_FILTERS, kind: "chat" }, () => {})
    const kind = groups.find((g) => g.key === "kind")!
    expect(kind.options.find((o) => o.value === "push")?.count).toBe(1)
  })

  it("reports the selected value so the explorer can show a chip", () => {
    const groups = notificationFacets([row()], { ...EMPTY_FILTERS, kind: "chat" }, () => {})
    expect(groups.find((g) => g.key === "kind")?.selected).toBe("chat")
    expect(groups.find((g) => g.key === "status")?.selected).toBeNull()
  })

  it("maps 'all' onto no selection rather than a bucket named all", () => {
    const groups = notificationFacets([row()], EMPTY_FILTERS, () => {})
    for (const g of groups) expect(g.selected).toBeNull()
  })

  it("translates a cleared facet back to 'all' when the user deselects", () => {
    const onChange = vi.fn()
    const groups = notificationFacets([row()], { ...EMPTY_FILTERS, kind: "chat" }, onChange)
    groups.find((g) => g.key === "kind")!.onSelect(null)
    expect(onChange).toHaveBeenCalledWith({ ...EMPTY_FILTERS, kind: "all" })
  })

  it("gives each service option its own brand mark", () => {
    const groups = notificationFacets([row()], EMPTY_FILTERS, () => {})
    const service = groups.find((g) => g.key === "provider")!
    expect(service.options[0]).toMatchObject({ value: "discord", mark: "discord" })
  })

  it("hides buckets nothing falls into, but keeps a selected one visible", () => {
    // Dropping the selected bucket would make the active filter unclickable
    // from the very menu that set it.
    const rows = [row({ kind: "chat" })]
    const plain = notificationFacets(rows, EMPTY_FILTERS, () => {})
    expect(plain.find((g) => g.key === "kind")!.options.map((o) => o.value)).toEqual(["chat"])

    const withPush = notificationFacets(rows, { ...EMPTY_FILTERS, kind: "push" }, () => {})
    expect(withPush.find((g) => g.key === "kind")!.options.map((o) => o.value)).toContain("push")
  })
})

describe("mcpFacets", () => {
  it("offers toolkits and users with their counts", () => {
    const groups = mcpFacets(STATUS, EMPTY_MCP_FILTERS, () => {})
    expect(groups.map((g) => g.key)).toEqual(["toolkit", "user"])
    expect(groups[0].options).toEqual([
      { value: "gmail", label: "gmail", count: 3 },
      { value: "discord", label: "discord", count: 1 },
    ])
    expect(groups[1].options).toEqual([{ value: "pg-test", label: "pg-test", count: 8 }])
  })

  it("uses the same FacetGroup shape as the notifications tab", () => {
    // This is what lets one explorer render both. If the shapes drift, the
    // "same logic in every left bar" claim quietly stops being true.
    const mcp = mcpFacets(STATUS, EMPTY_MCP_FILTERS, () => {})
    const notify = notificationFacets([row()], EMPTY_FILTERS, () => {})
    const shape = (g: (typeof mcp)[number]) => Object.keys(g).sort()
    expect(shape(mcp[0])).toEqual(shape(notify[0]))
  })

  it("passes the chosen value straight through", () => {
    const onChange = vi.fn()
    const groups = mcpFacets(STATUS, EMPTY_MCP_FILTERS, onChange)
    groups[0].onSelect("gmail")
    expect(onChange).toHaveBeenCalledWith({ toolkit: "gmail", user: null })
  })
})
