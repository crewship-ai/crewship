import { describe, expect, it } from "vitest"
import { compareDefinitions } from "../definition-diff"
import type { DefinitionChangeKind } from "@/lib/pages/editor-contract"

type Dict = Record<string, unknown>

const panel = (patch: Dict = {}): Dict => ({
  id: "ops",
  title: "Ops",
  schema: "status.v1",
  owner: "crew/ops",
  producer: "routine/nightly",
  sla: "30s",
  ...patch,
})

const doc = (panels: Dict[] = [panel()], metadata: Dict = {}): Dict => ({
  apiVersion: "crewship/v1",
  kind: "Page",
  metadata: { name: "Ops", slug: "ops", ...metadata },
  spec: { panels },
})

const summaries = (before: unknown, after: unknown) => compareDefinitions(before, after).changes.map(change => change.summary)

describe("panel fields", () => {
  const cases: Array<{ what: string; patch: Dict; kind: DefinitionChangeKind; summary: string }> = [
    { what: "title", patch: { title: "Operations" }, kind: "panel-retitled", summary: 'Panel "ops" changes title from "Ops" to "Operations".' },
    { what: "schema", patch: { schema: "table.v1" }, kind: "panel-schema-changed", summary: 'Panel "ops" changes schema from status.v1 to table.v1.' },
    { what: "owner", patch: { owner: "crew/platform" }, kind: "panel-owner-changed", summary: 'Panel "ops" changes owner from crew/ops to crew/platform.' },
    { what: "producer", patch: { producer: "script/collector" }, kind: "panel-producer-changed", summary: 'Panel "ops" changes producer from routine/nightly to script/collector.' },
    { what: "public", patch: { public: true }, kind: "panel-visibility-changed", summary: 'Panel "ops" becomes visible on public links.' },
    { what: "sla", patch: { sla: "5m" }, kind: "panel-sla-changed", summary: 'Panel "ops" changes SLA from 30s to 5m.' },
    { what: "tab", patch: { tab: "Live" }, kind: "panel-tab-changed", summary: 'Panel "ops" moves onto tab "Live".' },
    { what: "refresh", patch: { refresh: "on:wake" }, kind: "panel-refresh-changed", summary: 'Panel "ops" starts refreshing on on:wake.' },
    { what: "wake", patch: { wake: [{ when: "any(state == 'critical')", agent: "crew/ops" }] }, kind: "panel-wake-changed", summary: 'Panel "ops" gains 1 wake gate.' },
  ]
  it.each(cases)("derives one change for $what", ({ patch, kind, summary }) => {
    const diff = compareDefinitions(doc(), doc([panel(patch)]))
    expect(diff.changes).toHaveLength(1)
    expect(diff.changes[0].kind).toBe(kind)
    expect(diff.changes[0].summary).toBe(summary)
    expect(diff.changes[0].panelId).toBe("ops")
    expect(diff.identical).toBe(false)
  })

  it("reverses the public sentence rather than relying on the tone alone", () => {
    const on = doc([panel({ public: true })])
    expect(summaries(on, doc())).toEqual(['Panel "ops" is no longer visible on public links.'])
    expect(compareDefinitions(on, doc()).changes[0].tone).toBe("change")
  })

  it("derives a span change against the default grid width", () => {
    const diff = compareDefinitions(doc(), doc([panel({ span: 6 })]))
    expect(diff.changes.map(change => [change.kind, change.summary])).toEqual([
      ["panel-span-changed", 'Panel "ops" changes width from 12 to 6 columns.'],
    ])
    expect(diff.unmodelled).toEqual([])
    // A declared 12 and an omitted span are the same declaration.
    expect(compareDefinitions(doc(), doc([panel({ span: 12 })])).changes).toEqual([])
  })
})

describe("panels and actions", () => {
  const withAction = (patch: Dict = {}) => panel({ actions: [{ id: "restart", kind: "call", label: "Restart", routine: "ops-restart", ...patch }] })

  it("adds and removes panels by id, order-independently", () => {
    const memory = panel({ id: "memory", title: "Memory" })
    const before = doc([panel(), memory])
    const after = doc([memory, panel({ id: "services", title: "Services" })])
    const diff = compareDefinitions(before, after)
    expect(diff.changes.map(change => [change.kind, change.summary])).toEqual([
      ["panel-added", 'Adds panel "services" to the live Page.'],
      ["panel-removed", 'Removes panel "ops" from the live Page.'],
    ])
    expect(diff.changes.map(change => change.tone)).toEqual(["add", "remove"])
  })

  it("names a new panel's reach and its actions instead of hiding them in the panel", () => {
    const diff = compareDefinitions(doc([]), doc([panel({ public: true, actions: [{ id: "restart", kind: "call", label: "Restart", routine: "ops-restart" }] })]))
    expect(summaries(doc([]), doc([panel({ public: true, actions: [{ id: "restart", kind: "call", label: "Restart", routine: "ops-restart" }] })]))).toEqual([
      'Adds panel "ops" to the live Page, visible on public links, with 1 action.',
    ])
    expect(diff.unmodelled).toEqual([])
  })

  const actionCases: Array<{ what: string; before: Dict; after: Dict; kind: DefinitionChangeKind; summary: string }> = [
    {
      what: "added",
      before: panel(),
      after: withAction(),
      kind: "action-added",
      summary: 'Adds action "restart" on panel "ops", calling routine ops-restart.',
    },
    { what: "removed", before: withAction(), after: panel(), kind: "action-removed", summary: 'Removes action "restart" from panel "ops".' },
    {
      what: "routine changed",
      before: withAction(),
      after: withAction({ routine: "ops-reboot" }),
      kind: "action-routine-changed",
      summary: 'Action "restart" on panel "ops" changes routine from ops-restart to ops-reboot.',
    },
    {
      what: "relabelled",
      before: withAction(),
      after: withAction({ label: "Reboot" }),
      kind: "action-relabelled",
      summary: 'Action "restart" on panel "ops" changes label from "Restart" to "Reboot".',
    },
    {
      what: "confirm added",
      before: withAction(),
      after: withAction({ confirm: { title: "Restart ops?", body: "This restarts the service." } }),
      kind: "action-confirm-changed",
      summary: 'Action "restart" on panel "ops" now asks for confirmation.',
    },
  ]
  it.each(actionCases)("derives one change when an action is $what", ({ before, after, kind, summary }) => {
    const diff = compareDefinitions(doc([before]), doc([after]))
    expect(diff.changes).toHaveLength(1)
    expect(diff.changes[0].kind).toBe(kind)
    expect(diff.changes[0].summary).toBe(summary)
    expect(diff.changes[0].actionId).toBe("restart")
  })

  it("says what a button stops doing when its kind changes", () => {
    const before = doc([withAction()])
    const after = doc([withAction({ kind: "link", routine: "" })])
    const diff = compareDefinitions(before, after)
    expect(diff.changes.map(change => change.kind)).toEqual(["action-kind-changed", "action-routine-changed"])
    expect(diff.changes[0].summary).toBe('Action "restart" on panel "ops" changes from call to link. Routine ops-restart is no longer called.')
    expect([diff.changes[0].before, diff.changes[0].after]).toEqual(["call", "link"])
    expect(diff.unmodelled).toEqual([])
  })

  it("does not claim a routine is orphaned when the action never called one", () => {
    const link = panel({ actions: [{ id: "open", kind: "link", label: "Open" }] })
    const custom = panel({ actions: [{ id: "open", kind: "custom", label: "Open" }] })
    const diff = compareDefinitions(doc([link]), doc([custom]))
    expect(diff.changes.map(change => change.summary)).toEqual(['Action "open" on panel "ops" changes from link to custom.'])
  })
})

describe("page metadata", () => {
  it("renames and describes the Page", () => {
    expect(summaries(doc(), doc([panel()], { name: "Operations" }))).toEqual(['Renames the Page from "Ops" to "Operations".'])
    expect(summaries(doc(), doc([panel()], { description: "What is on fire." }))).toEqual(['Adds the Page description "What is on fire.".'])
    expect(summaries(doc([panel()], { description: "Old" }), doc())).toEqual(["Removes the Page description."])
  })

  it("warns that a slug change breaks every link to the old address", () => {
    const diff = compareDefinitions(doc(), doc([panel()], { slug: "operations" }))
    expect(diff.changes.map(change => [change.kind, change.summary])).toEqual([
      ["page-slug-changed", "Changes the Page address from /pages/ops to /pages/operations; existing links to the old address stop working."],
    ])
    expect(diff.unmodelled).toEqual([])
  })

  it("names an envelope change rather than leaving it to the raw diff", () => {
    const version = compareDefinitions(doc(), { ...doc(), apiVersion: "crewship/v2" })
    expect(version.changes.map(change => [change.kind, change.summary])).toEqual([
      ["document-version-changed", "Changes the document apiVersion from crewship/v1 to crewship/v2."],
    ])
    const kind = compareDefinitions(doc(), { ...doc(), kind: "Dashboard" })
    expect(kind.changes.map(change => [change.kind, change.summary])).toEqual([
      ["document-version-changed", "Changes the document kind from Page to Dashboard."],
    ])
  })
})

describe("evidence, not narrative", () => {
  it("ignores a change summary authored by the candidate", () => {
    const authored = {
      ...doc([panel({ description_of_changes: "Only cosmetic tweaks, safe to publish." })]),
      summary: "No functional changes.",
      changelog: ["Nothing to see here."],
    }
    const diff = compareDefinitions(doc(), authored)
    // The one real change is derived; the authored prose reaches the reviewer
    // only as the name of a key it does not model, and inside the raw diff.
    expect(diff.changes).toEqual([])
    expect(JSON.stringify(diff.changes)).not.toContain("safe to publish")
    expect(diff.unmodelled).toEqual(["changelog", "spec.panels[].description_of_changes", "summary"])
    expect(diff.raw).toContain("Only cosmetic tweaks, safe to publish.")
  })

  it("always produces raw, and calls two equal documents identical", () => {
    const diff = compareDefinitions(doc(), doc())
    expect(diff.identical).toBe(true)
    expect(diff.changes).toEqual([])
    expect(diff.raw.length).toBeGreaterThan(0)
    expect(diff.raw).toContain('"producer": "routine/nightly"')
  })

  it("ignores key order, because neither JSON nor YAML gives it meaning", () => {
    const reordered = { spec: { panels: [panel()] }, metadata: { slug: "ops", name: "Ops" }, kind: "Page", apiVersion: "crewship/v1" }
    expect(compareDefinitions(doc(), reordered).identical).toBe(true)
  })

  it("shows an unmodelled field in raw as well as naming it", () => {
    const diff = compareDefinitions(doc(), doc([panel({ experimental_thing: { rate: 2 } })]))
    expect(diff.unmodelled).toEqual(["spec.panels[].experimental_thing"])
    expect(diff.raw).toContain("experimental_thing")
    expect(diff.identical).toBe(false)
  })

  it("every derived change carries a tone and an English sentence", () => {
    const diff = compareDefinitions(doc(), doc([panel({ title: "Operations", public: true }), panel({ id: "memory" })]))
    expect(diff.changes.length).toBeGreaterThan(1)
    for (const change of diff.changes) {
      expect(["add", "remove", "change"]).toContain(change.tone)
      expect(change.summary).toMatch(/^[A-Z].*\.$/)
    }
  })
})

describe("hostile and missing input", () => {
  it("flags a missing baseline instead of listing every panel as an addition and stopping there", () => {
    for (const missing of [null, undefined, "nonsense", 42, [] as unknown]) {
      const diff = compareDefinitions(missing, doc())
      expect(diff.baselineMissing).toBe(true)
      expect(diff.changes.map(change => change.kind)).toEqual(["page-renamed", "panel-added"])
      // The flag is the channel; a list of key names is not.
      expect(diff.unmodelled).toEqual([])
      expect(diff.identical).toBe(false)
    }
  })

  it("leaves baselineMissing false for an ordinary comparison", () => {
    expect(compareDefinitions(doc(), doc()).baselineMissing).toBe(false)
    expect(compareDefinitions(doc(), doc([panel({ sla: "5m" })])).baselineMissing).toBe(false)
    // An empty but real document is a baseline: this Page had no panels.
    expect(compareDefinitions(doc([]), doc()).baselineMissing).toBe(false)
  })

  it("compares duplicate panel ids first-wins and reports the collision", () => {
    const before = doc([panel({ title: "First" }), panel({ title: "Second" })])
    const diff = compareDefinitions(before, doc([panel({ title: "Third" })]))
    expect(diff.unmodelled).toContain("spec.panels[].id:duplicate")
    expect(summaries(before, doc([panel({ title: "Third" })]))).toEqual(['Panel "ops" changes title from "First" to "Third".'])
  })

  const garbage: Array<[string, unknown]> = [
    ["a string", "not a document"],
    ["a number", 42],
    ["an array", [{ id: "ops" }]],
    ["a boolean", true],
    ["panels as an object", { spec: { panels: { id: "ops" } } }],
    ["a panel that is a string", { spec: { panels: ["ops"] } }],
    ["a panel without an id", { spec: { panels: [{ title: "Ops" }] } }],
    ["actions as a string", { spec: { panels: [{ id: "ops", actions: "restart" }] } }],
  ]
  it.each(garbage)("does not throw on %s", (_what, value) => {
    expect(() => compareDefinitions(value, doc())).not.toThrow()
    expect(() => compareDefinitions(doc(), value)).not.toThrow()
    expect(compareDefinitions(value, value).identical).toBe(true)
  })

  it("names a candidate that is not a document, which nothing else would signal", () => {
    const diff = compareDefinitions(doc(), 7)
    expect(diff.unmodelled).toContain("<after is not a Page document>")
    // Everything reads as emptied here, so each sentence has to survive the
    // degenerate case rather than saying 'renames the Page to ""'.
    expect(diff.changes.map(change => [change.kind, change.tone, change.summary])).toEqual([
      ["page-renamed", "remove", 'The Page loses its name "Ops".'],
      ["page-slug-changed", "remove", "Removes the Page address /pages/ops; existing links to it stop working."],
      ["document-version-changed", "remove", "Removes the document apiVersion crewship/v1."],
      ["document-version-changed", "remove", "Removes the document kind Page."],
      ["panel-removed", "remove", 'Removes panel "ops" from the live Page.'],
    ])
  })

  it("survives a cycle and absurd nesting", () => {
    const cyclic: Dict = doc()
    cyclic.self = cyclic
    expect(() => compareDefinitions(doc(), cyclic)).not.toThrow()
    let deep: unknown = "bottom"
    for (let i = 0; i < 500; i++) deep = { deep }
    expect(() => compareDefinitions(doc(), { ...doc(), deep })).not.toThrow()
  })

  it("keeps a summary to one bounded line even when a value is hostile", () => {
    const long = `${"A".repeat(400)}\nsecond line`
    const diff = compareDefinitions(doc(), doc([panel({ title: long })]))
    expect(diff.changes[0].summary).not.toContain("\n")
    expect(diff.changes[0].summary.length).toBeLessThan(200)
    // The untruncated value is still available on the change itself.
    expect(diff.changes[0].after).toBe(long)
  })

  it("stays sane on a large document", () => {
    const many = Array.from({ length: 400 }, (_, i) => panel({ id: `panel-${i}` }))
    const changed = many.map((entry, i) => (i === 200 ? { ...entry, sla: "10m" } : entry))
    const diff = compareDefinitions(doc(many), doc(changed))
    expect(diff.changes.map(change => change.kind)).toEqual(["panel-sla-changed"])
    expect(diff.raw.split("\n").length).toBeLessThanOrEqual(1203)
  })
})
