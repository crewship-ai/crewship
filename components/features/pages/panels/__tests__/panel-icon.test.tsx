/**
 * The panel icon vocabulary (PRD `docs/prd/pages.md` §3, §9b.2) and the parity
 * that keeps it honest.
 *
 * The load-bearing assertion is the first one: the Go enum and this map name
 * the same set. The whole argument for a CLOSED icon vocabulary is that the
 * server must never accept a name the client cannot draw — a blank panel
 * header reads as a design decision rather than as an error, which is a
 * quieter failure than an unknown schema, and an unknown schema at least
 * renders a fallback that says so. That property is a property of TWO files
 * agreeing, so it is tested by reading both.
 *
 * Shaped after `lib/__tests__/concept-icons.test.ts`: the other definition is
 * the definition, this asserts the map follows it, and the dependency runs in
 * that direction on purpose.
 */
import { readFileSync } from "node:fs"
import path from "node:path"

import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import {
  PANEL_ICON,
  PANEL_ICON_NAMES,
  isPanelIconName,
  resolvePanelIcon,
  type PanelIconName,
} from "@/components/features/pages/panels/panel-icon"
import { PanelRenderer } from "@/components/features/pages/panels/registry"
import { CONCEPT_ICON } from "@/lib/concept-icons"

const ICONS_GO = path.resolve(__dirname, "../../../../../internal/pages/icons.go")
const FIXTURE_NOW = new Date("2026-08-12T12:00:00Z")

/** Every `IconX PanelIcon = "name"` the Go enum declares. */
function goVocabulary(): string[] {
  const source = readFileSync(ICONS_GO, "utf8")
  const names = [...source.matchAll(/\bIcon[A-Za-z]+\s+PanelIcon\s*=\s*"([a-z]+)"/g)].map((m) => m[1])
  if (names.length === 0) {
    throw new Error(`no PanelIcon constants found in ${ICONS_GO} — the parity check is not running`)
  }
  return names
}

function panelNode(): HTMLElement {
  const node = document.querySelector('[data-slot="panel"]')
  if (!node) throw new Error("no [data-slot=panel] rendered")
  return node as HTMLElement
}

function iconClassOf(node: HTMLElement): string {
  const svg = node.querySelector('[data-slot="panel-label"] svg')
  if (!svg) throw new Error("the panel header rendered no icon at all")
  return svg.getAttribute("class") ?? ""
}

describe("panel icon vocabulary", () => {
  it("resolves every name in the Go enum (internal/pages/icons.go)", () => {
    // The server accepts exactly these; each one must arrive at a real glyph.
    for (const name of goVocabulary()) {
      expect(isPanelIconName(name), `Go accepts "${name}" and the client cannot draw it`).toBe(true)
      expect(PANEL_ICON[name as PanelIconName], `"${name}" has no icon`).toBeTruthy()
    }
  })

  it("declares no name the Go enum would refuse", () => {
    // The other direction, and it matters just as much: a glyph here that the
    // server refuses is one an author can never select, and its presence
    // suggests otherwise to the next person reading this file.
    const go = new Set(goVocabulary())
    for (const name of PANEL_ICON_NAMES) {
      expect(go.has(name), `"${name}" is in PANEL_ICON but not in internal/pages/icons.go`).toBe(true)
    }
    expect(PANEL_ICON_NAMES).toHaveLength(go.size)
  })

  it("stays small enough to read in one go", () => {
    // Not arithmetic — the point of the feature. A vocabulary nobody can hold
    // in their head is one an author picks from by grepping, which is the open
    // string with extra steps.
    expect(PANEL_ICON_NAMES.length).toBeLessThanOrEqual(16)
  })

  it("reuses the product's icon for a concept that already has one", () => {
    // lib/concept-icons.ts exists so a concept wears one face everywhere.
    expect(PANEL_ICON.people).toBe(CONCEPT_ICON.crews)
    expect(PANEL_ICON.queue).toBe(CONCEPT_ICON.inbox)
  })

  it("does not give free memory the icon for what an agent remembers", () => {
    // Same word, two concepts. `CONCEPT_ICON.memory` is a Brain — what the
    // agent keeps between sessions — and this one is a stick of RAM. Reusing
    // it would be drift wearing the costume of consistency.
    expect(PANEL_ICON.memory).not.toBe(CONCEPT_ICON.memory)
  })

  it("never resolves an inherited object key", () => {
    for (const hostile of ["__proto__", "constructor", "toString", "hasOwnProperty"]) {
      expect(isPanelIconName(hostile)).toBe(false)
    }
  })
})

describe("panel icon resolution", () => {
  const fallback = CONCEPT_ICON.dashboard

  it("returns the declared icon", () => {
    expect(resolvePanelIcon("memory", fallback)).toBe(PANEL_ICON.memory)
  })

  it("falls back rather than returning nothing", () => {
    // Every one of these reaches a renderer that must draw SOMETHING.
    for (const bad of ["MemoryStick", "ram", "Memory", "", null, undefined, 7, {}, ["memory"]]) {
      expect(resolvePanelIcon(bad, fallback)).toBe(fallback)
    }
  })
})

describe("a panel header wears the icon its spec declared", () => {
  const data = { state: "fresh" as const, payload: { value: 42, unit: "%" } }

  it("draws the declared icon instead of the schema's", () => {
    render(
      <PanelRenderer
        panel={{ id: "pamet", schema: "metric.v1", title: "Volná paměť", icon: "memory" }}
        data={data}
        now={FIXTURE_NOW}
      />,
    )
    const node = panelNode()
    expect(node.getAttribute("data-panel-icon")).toBe("memory")
    expect(iconClassOf(node)).toContain("lucide-memory-stick")
  })

  it("gives two panels of the SAME schema two different headers", () => {
    // The whole reason the field exists. Three status.v1 panels on one page —
    // "is it running", "who is on call" — were three identical headers, and a
    // reader could not tell them apart at a glance.
    const { container } = render(
      <div>
        <PanelRenderer
          panel={{ id: "bezi", schema: "status.v1", title: "Jede to?", icon: "container" }}
          data={{ state: "fresh", payload: { items: [{ name: "api", state: "ok" }] } }}
          now={FIXTURE_NOW}
        />
        <PanelRenderer
          panel={{ id: "sluzba", schema: "status.v1", title: "Kdo drží službu", icon: "people" }}
          data={{ state: "fresh", payload: { items: [{ name: "pavel", state: "ok" }] } }}
          now={FIXTURE_NOW}
        />
      </div>,
    )
    const panels = [...container.querySelectorAll('[data-slot="panel"]')] as HTMLElement[]
    expect(panels).toHaveLength(2)
    expect(panels.map((p) => p.getAttribute("data-panel-icon"))).toEqual(["container", "people"])
    expect(iconClassOf(panels[0])).not.toBe(iconClassOf(panels[1]))
  })

  it("renders the schema's own icon when the panel declares none", () => {
    render(
      <PanelRenderer
        panel={{ id: "zatizeni", schema: "metric.v1", title: "Zátěž" }}
        data={data}
        now={FIXTURE_NOW}
      />,
    )
    const node = panelNode()
    expect(node.getAttribute("data-panel-icon")).toBe("schema")
    // metric.v1's own glyph, unchanged by this feature.
    expect(iconClassOf(node)).toContain("lucide-gauge")
  })

  it("falls back to the schema's icon for a name it cannot resolve, never to nothing", () => {
    // A page saved by a newer server, or a spec someone hand-edited past the
    // gate. The header must not go blank: an absent glyph is indistinguishable
    // from a deliberate design.
    render(
      <PanelRenderer
        panel={{ id: "budouci", schema: "metric.v1", title: "Z budoucnosti", icon: "quantum-flux" }}
        data={data}
        now={FIXTURE_NOW}
      />,
    )
    const node = panelNode()
    expect(node.getAttribute("data-panel-icon")).toBe("schema")
    expect(iconClassOf(node)).toContain("lucide-gauge")
  })

  it("keeps the sealed placeholder's lock, whatever the wire says", () => {
    // §11b.14: a sealed panel is serialised as exactly {panel_id, span,
    // sealed, owner_crew_name} — it carries no icon, and one arriving anyway
    // must not repaint a permission decision as a subject.
    render(
      <PanelRenderer
        panel={{ id: "ucetni", schema: "", sealed: true, owner_crew_name: "Účetní", icon: "money" }}
        data={{ state: "never_produced" }}
        now={FIXTURE_NOW}
      />,
    )
    expect(iconClassOf(panelNode())).toContain("lucide-lock")
  })

  it("does not colour the glyph — colour on this surface means state", () => {
    // §3: "Status colours are reserved. Green 'running' must never also mean
    // 'series 3'." The icon carries identity; the state carries colour, and a
    // second colour axis on the same glyph would collide with the first.
    for (const state of ["fresh", "stale", "failed"] as const) {
      const { unmount } = render(
        <PanelRenderer
          panel={{ id: "pamet", schema: "metric.v1", icon: "memory" }}
          data={{ state, payload: { value: 42 } }}
          now={FIXTURE_NOW}
        />,
      )
      const cls = iconClassOf(panelNode())
      expect(cls).toContain("text-muted-foreground-soft")
      expect(cls).not.toMatch(/text-(destructive|success|warning|primary)/)
      unmount()
    }
    // And the label the reader gets is still the state word, not the icon.
    expect(screen.queryByText(/memory/i)).toBeNull()
  })
})
