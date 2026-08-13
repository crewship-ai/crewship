/**
 * `table.v1` — the card list a narrow panel collapses to (PRD §3, §9, §9b.4).
 *
 * The defect these tests are the fence around was found on a live page: a
 * `span: 4` table panel collapsed correctly and then drew its rows in an idiom
 * nothing else in the product uses — an 11px uppercase tracked label over a
 * value at the inherited 16px. Five keys per row, three rows, and the panel
 * read as a different application from the card beside it.
 *
 * So the assertions are on CLASSES, not on a screenshot, and the ones that
 * matter most are not literals at all: they compare the card's type classes
 * against what `components/layout/property-row.tsx` — the product's canonical
 * label/value pair — actually renders. A test that hard-codes `text-label`
 * passes forever after PropertyRow moves on; this one fails, which is the
 * point. The house scale changing must reach here.
 */
import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"

import { PropertyRow } from "@/components/layout/property-row"
import { PanelRenderer } from "../registry"
import { EM_DASH } from "../freshness"
import { FIXTURE_NOW, tableFixtures } from "../fixtures"

function cards(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('[data-slot="table-card"]'))
}

function cardLabels(card: HTMLElement): HTMLElement[] {
  return Array.from(card.querySelectorAll<HTMLElement>('[data-slot="table-card-label"]'))
}

function cardCells(card: HTMLElement): HTMLElement[] {
  return Array.from(card.querySelectorAll<HTMLElement>('dd[data-slot="table-cell"]'))
}

function tableCells(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('td[data-slot="table-cell"]'))
}

/**
 * The classes that set the TYPE of a run of text — size, weight, colour, case,
 * tracking, leading. Alignment is deliberately excluded: `text-right` is a
 * `text-*` class that says nothing about scale, it is the column's declared
 * alignment, and a value that carries it is obeying the payload rather than
 * departing from the house.
 */
const ALIGNMENT = new Set(["text-left", "text-center", "text-right"])

function typeTokens(el: Element): Set<string> {
  return new Set(
    el.className
      .split(/\s+/)
      .filter(Boolean)
      .filter((c) => /^(text-|font-|uppercase|lowercase|capitalize|tracking-|leading-)/.test(c))
      .filter((c) => !ALIGNMENT.has(c)),
  )
}

/** What PropertyRow actually renders today, read off the DOM rather than copied. */
function houseTypeTokens(): { label: Set<string>; value: Set<string> } {
  const { container } = render(<PropertyRow label="house">value</PropertyRow>)
  const row = container.firstElementChild!
  const label = row.children[0]
  const value = row.children[1]
  // The row itself carries `text-body`; the label overrides it and the value
  // restates it, so the row's own tokens belong to both.
  const rowTokens = typeTokens(row)
  const merge = (own: Set<string>) => {
    const out = new Set(own)
    // A size on the child wins over the size on the row.
    const hasSize = [...own].some((c) => /^text-(micro|label|body|default|heading|title|display)$/.test(c))
    for (const c of rowTokens) {
      if (hasSize && /^text-(micro|label|body|default|heading|title|display)$/.test(c)) continue
      out.add(c)
    }
    return out
  }
  return { label: merge(typeTokens(label)), value: merge(typeTokens(value)) }
}

describe("the card list is the house label/value pair, not a stat block", () => {
  it("labels carry PropertyRow's label type, and nothing else", () => {
    const house = houseTypeTokens()
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const labels = cardLabels(cards(container)[0])
    expect(labels).toHaveLength(3)
    for (const label of labels) {
      expect([...typeTokens(label)].sort()).toEqual([...house.label].sort())
    }
  })

  it("values carry PropertyRow's value type, and nothing else", () => {
    const house = houseTypeTokens()
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    // The `null` cell is muted on purpose (§9b.4) and is asserted separately.
    const measured = cardCells(cards(container)[0]).filter(
      (c) => c.getAttribute("data-basis") === "measured",
    )
    expect(measured.length).toBeGreaterThan(0)
    for (const cell of measured) {
      expect([...typeTokens(cell)].sort()).toEqual([...house.value].sort())
    }
  })

  /**
   * The regression, stated as the thing it must never be again. A label that
   * is an uppercase micro column head and a value left at the inherited size
   * is the stat-block idiom; in a table card it is a five-times-repeated
   * heading with nothing to head.
   */
  it("does not draw the old stat-block idiom", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const card = cards(container)[0]
    for (const label of cardLabels(card)) {
      expect(label.className).not.toMatch(/\buppercase\b/)
      expect(label.className).not.toMatch(/\btracking-wider\b/)
    }
    for (const cell of cardCells(card)) {
      expect(cell.className).not.toMatch(/\btext-(display|title|heading|default)\b/)
      // An unsized value inherits 16px from the card — which is how this
      // started. Every value states its size.
      expect(cell.className).toMatch(/\btext-body\b/)
    }
  })

  it("keeps PropertyRow's density: one hairline per pair, none under the last", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const card = cards(container)[0]
    const labels = cardLabels(card)
    const cells = cardCells(card)
    expect(labels).toHaveLength(cells.length)
    labels.forEach((label, i) => {
      const last = i === labels.length - 1
      expect(label.className.includes("border-b")).toBe(!last)
      expect(cells[i].className.includes("border-b")).toBe(!last)
      // Both halves of a pair share the padding, or the hairline steps.
      expect(label.className).toMatch(/\bpy-2\b/)
      expect(cells[i].className).toMatch(/\bpy-2\b/)
    })
  })

  /**
   * A `<dl>` with real `<dt>`/`<dd>` elements — the reason the card list does
   * not simply render `PropertyRow`, which is a div with a children slot. The
   * key/value relationship has to survive into the accessibility tree.
   */
  it("stays a description list", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const card = cards(container)[0]
    expect(card.querySelector("dl")).toBeTruthy()
    for (const label of cardLabels(card)) expect(label.tagName).toBe("DT")
    for (const cell of cardCells(card)) expect(cell.tagName).toBe("DD")
  })
})

describe("the column's declared alignment survives the collapse", () => {
  it("right-aligns a right column in the card form as well as in the table", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)

    // `open` and `closed` are declared `align: "right"`; `crew` is left.
    const rightInCards = cardCells(cards(container)[0]).filter((c) =>
      ["open", "closed"].includes(c.getAttribute("data-key") ?? ""),
    )
    expect(rightInCards).toHaveLength(2)
    for (const cell of rightInCards) expect(cell.className).toMatch(/\btext-right\b/)

    const leftInCard = cardCells(cards(container)[0]).find(
      (c) => c.getAttribute("data-key") === "crew",
    )!
    expect(leftInCard.className).toMatch(/\btext-left\b/)

    const rightInTable = tableCells(container).filter(
      (c) => c.getAttribute("data-key") === "open",
    )
    expect(rightInTable.length).toBeGreaterThan(0)
    for (const cell of rightInTable) expect(cell.className).toMatch(/\btext-right\b/)
  })
})

/**
 * Alignment of the FIGURES, which is a different job from alignment of the
 * cell. A column of ports lines up only if every digit has the same advance
 * width, and the producer decides whether it pushes `8083` or `"8083"` — so the
 * decision cannot be made from the JS type.
 */
describe("digits line up", () => {
  it("puts tabular numerals on every cell, in both forms", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const all = [...tableCells(container), ...cards(container).flatMap(cardCells)]
    expect(all.length).toBeGreaterThan(0)
    for (const cell of all) expect(cell.className).toMatch(/\btabular-nums\b/)
  })

  it("does not make it conditional on the payload's JSON type", () => {
    const stringy = {
      panel: { id: "flotila", schema: "table.v1", title: "Co kde běží", span: 4 },
      data: {
        state: "fresh" as const,
        payload: {
          columns: [
            { key: "klon", label: "klon" },
            { key: "port", label: "port", align: "right" as const },
          ],
          // The port arrives as a string from one producer and a number from
          // another. Both columns must line up.
          rows: [
            { klon: "crewship_1", port: "8081" },
            { klon: "crewship_3", port: 8083 },
          ],
        },
        provenance: { producer: "script/fleet.sh", produced_at: FIXTURE_NOW },
      },
    }
    const { container } = render(<PanelRenderer {...stringy} now={FIXTURE_NOW} />)
    const ports = cards(container)
      .flatMap(cardCells)
      .filter((c) => c.getAttribute("data-key") === "port")
    expect(ports).toHaveLength(2)
    for (const cell of ports) expect(cell.className).toMatch(/\btabular-nums\b/)
  })
})

/**
 * §9b.4 per cell, restated against the new markup. `Cell.IsNoData()` in
 * internal/pages/payload.go is true for JSON null and nothing else, and a card
 * list that drew a dash over `""` would disagree with the server about the one
 * glyph both sides must read the same way.
 */
describe("the em-dash rule holds per cell in the card form (§9b.4)", () => {
  it("measures 0 and \"\", and dashes only null", () => {
    const { container } = render(
      <PanelRenderer {...tableFixtures.emptyStringCell} now={FIXTURE_NOW} />,
    )
    const [first, second] = cards(container)

    const emptyString = cardCells(first).find((c) => c.getAttribute("data-key") === "crew")!
    expect(emptyString.getAttribute("data-basis")).toBe("measured")
    expect(emptyString.textContent).toBe("")

    const zero = cardCells(first).find((c) => c.getAttribute("data-key") === "open")!
    expect(zero.getAttribute("data-basis")).toBe("measured")
    expect(zero.textContent).toBe("0")

    const nul = cardCells(second).find((c) => c.getAttribute("data-key") === "open")!
    expect(nul.getAttribute("data-basis")).toBe("none")
    expect(nul.textContent).toBe(EM_DASH)
    expect(nul.className).toMatch(/\btext-muted-foreground-soft\b/)
  })

  it("says the same thing in the table form", () => {
    const { container } = render(
      <PanelRenderer {...tableFixtures.emptyStringCell} now={FIXTURE_NOW} />,
    )
    const bases = tableCells(container).map((c) => c.getAttribute("data-basis"))
    expect(bases.filter((b) => b === "none")).toHaveLength(1)
    const dashes = tableCells(container).filter((c) => c.textContent === EM_DASH)
    expect(dashes).toHaveLength(1)
  })
})

/**
 * §3: *"Status colours are reserved. Green 'running' must never also mean
 * 'series 3'."* A table cell tinted on the strength of its own value would be
 * a second, unlabelled status vocabulary — and the reader has no legend for it.
 * The one non-neutral tone a cell may take is the muted one on the em dash,
 * which reports an absence rather than a verdict.
 */
describe("no cell is coloured on the strength of its value", () => {
  const SEMANTIC = /\b(text|bg|border|ring|fill|stroke)-(success|warn|destructive|primary|info|notice|purple|gold)(\/|\b)/

  it.each([
    ["fresh", tableFixtures.fresh],
    ["with a measured zero and an empty string", tableFixtures.emptyStringCell],
    ["stale", tableFixtures.stale],
    ["positional rows", tableFixtures.positionalRows],
  ])("stays neutral: %s", (_name, fixture) => {
    const { container } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
    const all = [...tableCells(container), ...cards(container).flatMap(cardCells)]
    expect(all.length).toBeGreaterThan(0)
    for (const cell of all) {
      expect(cell.className).not.toMatch(SEMANTIC)
    }
  })

  it("does not tint a zero, a negative or a boolean", () => {
    const judgy = {
      panel: { id: "verdicts", schema: "table.v1", span: 4 },
      data: {
        state: "fresh" as const,
        payload: {
          columns: [
            { key: "zero" },
            { key: "negative", align: "right" as const },
            { key: "flag" },
          ],
          rows: [{ zero: 0, negative: -12, flag: false }],
        },
        provenance: { produced_at: FIXTURE_NOW },
      },
    }
    const { container } = render(<PanelRenderer {...judgy} now={FIXTURE_NOW} />)
    const all = [...tableCells(container), ...cards(container).flatMap(cardCells)]
    for (const cell of all) expect(cell.className).not.toMatch(SEMANTIC)
    // …and the values themselves are untouched: a boolean is a word, not a tick.
    const flag = cards(container).flatMap(cardCells).find((c) => c.getAttribute("data-key") === "flag")!
    expect(flag.textContent).toBe("no")
  })
})
