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
 * matter most are not literals at all — a test that hard-codes `text-label`
 * passes forever after the house scale moves on.
 *
 * **The type-parity assertions moved.** They used to compare this card's class
 * strings against what `components/layout/property-row.tsx` renders. The card
 * is now written in the Pages register (`app/globals.css`), so the two files no
 * longer spell the same size the same way and a string comparison would be
 * asserting a spelling rather than a scale. The real claim — that the card's
 * value and label roles resolve to the same `--typo-*` tokens PropertyRow's
 * `text-body`/`text-label` do — is asserted through the CSS in
 * `components/features/pages/__tests__/type-register.test.tsx`, which survives
 * a rename on either side. What stays here is what is local to this file: the
 * card names roles rather than pixels, it is not the stat-block idiom, and its
 * density is the one this panel deliberately chose.
 */
import { describe, it, expect } from "vitest"
import { fireEvent, render } from "@testing-library/react"

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
      .filter((c) =>
        /^(type-page-|text-|font-|uppercase|lowercase|capitalize|tracking-|leading-)/.test(c),
      )
      .filter((c) => !ALIGNMENT.has(c)),
  )
}

describe("the card list is the house label/value pair, not a stat block", () => {
  it("labels carry the register's meta role, and nothing else that sets type", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const labels = cardLabels(cards(container)[0])
    expect(labels).toHaveLength(3)
    for (const label of labels) {
      expect([...typeTokens(label)].sort()).toEqual([
        "font-medium",
        "text-muted-foreground",
        "type-page-meta",
      ])
    }
  })

  it("values carry the register's value role, and nothing else that sets type", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    // The `null` cell is muted on purpose (§9b.4) and is asserted separately.
    const measured = cardCells(cards(container)[0]).filter(
      (c) => c.getAttribute("data-basis") === "measured",
    )
    expect(measured.length).toBeGreaterThan(0)
    for (const cell of measured) {
      expect([...typeTokens(cell)].sort()).toEqual(["text-foreground", "type-page-value"])
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
      expect(cell.className).not.toMatch(/\btype-page-(metric|label)\b/)
      // An unsized value inherits 16px from the card — which is how this
      // started. Every value states its role.
      expect(cell.className).toMatch(/\btype-page-value\b/)
    }
  })

  /**
   * The density this panel chose, and it is deliberately NOT PropertyRow's.
   *
   * PropertyRow's rhythm — `py-2` and a hairline under every pair but the last
   * — is right for one property list. A collapsed table is N of them: on the
   * live `flotila` page, five columns by three rows is fifteen pairs and
   * fifteen rules in a quarter-width column, every one individually correct and
   * the stack a wall. Separation moved up a level, to the card, where the
   * grouping actually is; the card already has a border and a tinted surface
   * saying the same thing the interior rules were saying badly.
   *
   * This replaces "keeps PropertyRow's density", which asserted the two
   * measurements that changed. What it must NOT become is a test that pins
   * `py-1`: the claim is that the pair is separated once, at the card, and that
   * the two halves of a pair always share their padding — a pair whose halves
   * disagree steps, whether or not there is a rule to show it.
   */
  it("separates per card, not per pair", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const card = cards(container)[0]
    const labels = cardLabels(card)
    const cells = cardCells(card)
    expect(labels).toHaveLength(cells.length)
    expect(labels.length).toBeGreaterThan(1)

    for (const half of [...labels, ...cells]) {
      expect(half.className).not.toMatch(/\bborder-b\b/)
    }
    // The card is what carries the separation now.
    expect(card.className).toMatch(/\bborder\b/)

    const padding = (el: Element) => el.className.match(/\bpy-[\d.]+\b/)?.[0]
    labels.forEach((label, i) => {
      expect(padding(label)).toBeTruthy()
      expect(padding(cells[i])).toBe(padding(label))
    })
    // …and it is tighter than the single-list rhythm it departs from, which is
    // the whole point of departing.
    const pairPadding = Number(padding(labels[0])!.replace("py-", ""))
    expect(pairPadding).toBeLessThan(2)
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

/**
 * The bound on the card form's height (§11b.12).
 *
 * The wide table spends one row of height per payload row; the card form spends
 * one row per CELL, so a 200-row payload — the documented maximum, accepted
 * because it is *"more than anyone reads and more than we will virtualise"* —
 * is a panel with no upper bound on its height inside a page of panels that all
 * have one. The cap is on the card form alone, and that asymmetry is the reason
 * it exists rather than an oversight.
 *
 * The live page that prompted the density work has three rows, so none of this
 * fires there — the wall was fixed by the rhythm, not by hiding anything.
 */
describe("the card form caps its height, and says so", () => {
  function manyRows(n: number) {
    return {
      panel: { id: "fleet", schema: "table.v1", title: "Fleet", span: 4 },
      data: {
        state: "fresh" as const,
        payload: {
          columns: [{ key: "host" }, { key: "port", align: "right" as const }],
          rows: Array.from({ length: n }, (_, i) => ({ host: `host-${i}`, port: 8000 + i })),
        },
        provenance: { produced_at: FIXTURE_NOW },
      },
    }
  }

  it("draws every card while the list is short", () => {
    const { container } = render(<PanelRenderer {...manyRows(8)} now={FIXTURE_NOW} />)
    expect(cards(container)).toHaveLength(8)
    expect(container.querySelector('[data-slot="table-cards-toggle"]')).toBeNull()
  })

  it("caps a long list and names the true total, so nothing is hidden silently", () => {
    const { container } = render(<PanelRenderer {...manyRows(30)} now={FIXTURE_NOW} />)
    expect(cards(container)).toHaveLength(8)
    const toggle = container.querySelector('[data-slot="table-cards-toggle"]')!
    expect(toggle).toBeTruthy()
    expect(toggle.getAttribute("data-expanded")).toBe("false")
    expect(toggle.textContent).toContain("30")
  })

  it("shows all of them on a click, and folds back", () => {
    const { container } = render(<PanelRenderer {...manyRows(30)} now={FIXTURE_NOW} />)
    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-slot="table-cards-toggle"]',
    )!
    fireEvent.click(toggle)
    expect(cards(container)).toHaveLength(30)
    expect(toggle.getAttribute("data-expanded")).toBe("true")
    fireEvent.click(toggle)
    expect(cards(container)).toHaveLength(8)
  })

  it("never caps the wide table, which costs one row per row", () => {
    const { container } = render(<PanelRenderer {...manyRows(30)} now={FIXTURE_NOW} />)
    expect(container.querySelectorAll("table tbody tr")).toHaveLength(30)
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
