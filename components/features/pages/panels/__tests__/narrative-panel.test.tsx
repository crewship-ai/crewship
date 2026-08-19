/**
 * `narrative.v1` — the panel an AI agent writes (PRD §3, §8, §9, §9b.4).
 *
 * §8's ten rules are the specification for this panel, and four of them are
 * properties of the RENDERER rather than of the API boundary. Those four are
 * what this file pins, because a payload that was refused at the boundary can
 * still reach the renderer: from a row stored by an older build, from a
 * restored backup, or from a client talking to a server that has not been
 * upgraded. "It cannot get here" is not a rendering control.
 */
import { describe, it, expect } from "vitest"
import { render, screen, within } from "@testing-library/react"

import { PanelRenderer } from "../registry"
import { NarrativePanel } from "../narrative-panel"
import { EM_DASH } from "../freshness"
import { FIXTURE_NOW, narrativeFixtures } from "../fixtures"
import narrativeSchema from "@/schemas/panel.narrative.v1.json"

function valueNode(container: HTMLElement) {
  const node = container.querySelector('[data-slot="panel-value"]')
  if (!node) throw new Error("no [data-slot=panel-value] rendered")
  return node as HTMLElement
}

describe("narrative.v1 registration", () => {
  it("is routed to its own component, not to the pending placeholder", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.fresh} now={FIXTURE_NOW} />)
    expect(container.querySelector('[data-panel-schema="narrative.v1"]')).toBeTruthy()
    expect(screen.queryByText(/arrive in a later release/i)).toBeNull()
  })
})

/**
 * §8 rule 1: *"The agent fills a schema; it never emits markup, HTML, CSS or
 * code. `narrative.v1` accepts typed blocks, not a markdown blob."* The typed
 * blocks have to become real elements, or the type buys nothing.
 */
describe("typed blocks render as elements (§8 rule 1)", () => {
  it("renders a paragraph block as a paragraph", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.fresh} now={FIXTURE_NOW} />)
    const paragraphs = container.querySelectorAll('[data-slot="narrative-block"][data-kind="paragraph"]')
    expect(paragraphs).toHaveLength(2)
    expect(paragraphs[0].tagName).toBe("P")
    expect(paragraphs[0].textContent).toContain("128 invoices settled")
  })

  it("gathers consecutive list blocks into one unordered list", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.fresh} now={FIXTURE_NOW} />)
    const lists = container.querySelectorAll('[data-slot="narrative-list"]')
    // Three list blocks, ONE list: the schema gives a block a single string, so
    // a list exists only as a run of them — which is what keeps the agent from
    // ever describing nesting or indentation.
    expect(lists).toHaveLength(1)
    expect(lists[0].tagName).toBe("UL")
    expect(lists[0].querySelectorAll("li")).toHaveLength(3)
  })

  it("renders the verdict as the lead line", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.fresh} now={FIXTURE_NOW} />)
    const verdict = container.querySelector('[data-slot="narrative-verdict"]')
    expect(verdict).toBeTruthy()
    expect(verdict!.textContent).toContain("Two suppliers are late")
  })

  it("renders nothing where a verdict would be when there is none", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.noVerdict} now={FIXTURE_NOW} />)
    expect(container.querySelector('[data-slot="narrative-verdict"]')).toBeNull()
    // A verdict is prose, not a measurement, so its absence does NOT borrow the
    // em dash — §9b.4's glyph means "no basis to compute a value".
    expect(valueNode(container).textContent).not.toContain(EM_DASH)
    expect(valueNode(container).getAttribute("data-basis")).toBe("measured")
  })

  it("an unrecognised block kind degrades to a paragraph rather than selecting a renderer", () => {
    const { container } = render(
      <PanelRenderer {...narrativeFixtures.hostilePayload} now={FIXTURE_NOW} />,
    )
    // The `image` block from the hostile fixture renders as a PARAGRAPH of its
    // own text. It does not pick a component, and it does not vanish silently.
    const blocks = container.querySelectorAll('[data-slot="narrative-block"]')
    expect(blocks.length).toBeGreaterThan(0)
    for (const b of blocks) {
      expect(["paragraph", "list"]).toContain(b.getAttribute("data-kind"))
    }
  })

  it("renders markup as characters, never as markup (§8 rule 10)", () => {
    const { container } = render(
      <PanelRenderer {...narrativeFixtures.hostilePayload} now={FIXTURE_NOW} />,
    )
    expect(container.querySelector("script")).toBeNull()
    // The angle brackets survive as TEXT — which is the proof that the string
    // went through a React child and not through innerHTML.
    expect(container.textContent).toContain("<script>alert(1)</script>")
  })
})

/**
 * §8 rule 2: *"No images in agent-authored content. None. Not sanitised —
 * absent from the schema."* CamoLeak exfiltrated through a TRUSTED FIRST-PARTY
 * image proxy and CSP did not help, so neither an allow-list nor a header is
 * the control. Having no element that takes a source is.
 */
describe("no image can ever be drawn (§8 rule 2)", () => {
  it("draws no image element for any fixture, including a hostile payload", () => {
    for (const fixture of Object.values(narrativeFixtures)) {
      const { container, unmount } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
      expect(container.querySelectorAll("img")).toHaveLength(0)
      expect(container.querySelectorAll("picture, source, video, iframe, object, embed")).toHaveLength(0)
      // Not even as a background: nothing in this panel takes a URL at all.
      for (const el of container.querySelectorAll<HTMLElement>("[style]")) {
        expect(el.getAttribute("style") ?? "").not.toMatch(/url\(/i)
      }
      unmount()
    }
  })

  it("declares no image field in the published schema", () => {
    const doc = JSON.stringify(narrativeSchema).toLowerCase()
    for (const banned of ['"image"', '"image_url"', '"thumbnail"', '"src"', '"alt"', '"media"']) {
      expect(doc, `${banned} is declared`).not.toContain(`${banned}:`)
    }
  })
})

/**
 * §8 rule 3: *"No free-form links. A narrative block may reference an internal
 * Crewship entity by id (issue, run, page, agent) and the renderer builds the
 * URL. It may not carry a URL."* Slack AI's private-channel exfiltration was a
 * rendered link.
 */
describe("links are built by the renderer, never carried (§8 rule 3)", () => {
  it("builds an internal href from the ref's kind and id", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.entityRefs} now={FIXTURE_NOW} />)
    const refs = container.querySelectorAll('[data-slot="narrative-ref"]')
    expect(refs).toHaveLength(2)

    const issue = container.querySelector('[data-ref-kind="issue"]')!
    expect(issue.getAttribute("href")).toBe("/issues/1935")
    expect(issue.textContent).toContain("1935")

    const run = container.querySelector('[data-ref-kind="run"]')!
    expect(run.getAttribute("href")).toBe("/activity?run=run_8812")
  })

  it("emits no anchor whose href came from the payload", () => {
    for (const fixture of Object.values(narrativeFixtures)) {
      const { container, unmount } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
      for (const a of container.querySelectorAll("a")) {
        const href = a.getAttribute("href") ?? ""
        // Every href this panel can emit is relative and starts at a route the
        // renderer owns. No scheme, no host, no protocol-relative form.
        expect(href).toMatch(/^\/(issues|activity|pages|crews)/)
        expect(href).not.toMatch(/^[a-z][a-z0-9+.-]*:/i)
        expect(href).not.toMatch(/^\/\//)
        expect(href).not.toContain("evil.example")
      }
      unmount()
    }
  })

  it("refuses to link a ref whose kind is not in the renderer's route table", () => {
    const { container } = render(
      <PanelRenderer {...narrativeFixtures.hostilePayload} now={FIXTURE_NOW} />,
    )
    expect(container.querySelector('[data-ref-kind="webhook"]')).toBeNull()
    // The block's own text still renders — one bad ref costs the link, not the
    // sentence.
    expect(container.textContent).toContain("unknown noun")
  })

  it("refuses to grow a relative id into a path", () => {
    const { container } = render(
      <PanelRenderer {...narrativeFixtures.hostilePayload} now={FIXTURE_NOW} />,
    )
    for (const a of container.querySelectorAll("a")) {
      expect(a.getAttribute("href") ?? "").not.toContain("..")
    }
    expect(container.textContent).toContain("escaped")
  })

  it("declares no url field in the published schema", () => {
    const doc = JSON.stringify(narrativeSchema).toLowerCase()
    for (const banned of ['"url"', '"href"', '"link"', '"target"']) {
      expect(doc, `${banned} is declared`).not.toContain(`${banned}:`)
    }
  })
})

/**
 * §12 stages narrative.v1 text-only in v1 and its actions in v1.1, behind the
 * full §8 rule set — rules 4-7, the declared allow-list, the host-drawn
 * confirmation and the server-verified click token. A button here before any of
 * that exists would be a button with no token behind it, and §8 rule 6 says a
 * rendered button is not evidence of authorisation.
 */
describe("no actions ship with the text half (§12, §8 rules 4-7)", () => {
  it("renders no button or form for any narrative fixture", () => {
    for (const fixture of Object.values(narrativeFixtures)) {
      const { container, unmount } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
      expect(container.querySelectorAll("button, form, input, [role='button']")).toHaveLength(0)
      unmount()
    }
  })

  it("declares no actions array in the published schema", () => {
    const doc = JSON.stringify(narrativeSchema).toLowerCase()
    expect(doc).not.toContain('"actions":')
    expect(doc).not.toContain('"action":')
    // additionalProperties:false is what turns that absence into a rejection.
    expect(narrativeSchema.additionalProperties).toBe(false)
  })
})

/** The four states, and the em-dash rule at the panel level (§4, §9b.4). */
describe("narrative.v1 freshness", () => {
  it("fresh renders at full contrast", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.fresh} now={FIXTURE_NOW} />)
    expect(valueNode(container).className).not.toMatch(/opacity-/)
  })

  it("stale dims the prose and shows an absolute age", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.stale} now={FIXTURE_NOW} />)
    expect(valueNode(container).className).toMatch(/opacity-/)
    const age = container.querySelector('[data-slot="panel-age"]')!
    expect(age.textContent).toContain("2 h 15 min old")
    expect(age.textContent).not.toMatch(/ago|a while|recently|moments/i)
  })

  it("failed renders the em dash in the destructive tone and no prose", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.failed} now={FIXTURE_NOW} />)
    const value = valueNode(container)
    expect(value.textContent).toContain(EM_DASH)
    expect(value.className).toMatch(/text-destructive/)
    expect(container.querySelector('[data-slot="narrative-block"]')).toBeNull()
    expect(screen.getByText(/context window/)).toBeInTheDocument()
  })

  it("never-produced renders the em dash and names the next action", () => {
    const { container } = render(
      <PanelRenderer {...narrativeFixtures.neverProduced} now={FIXTURE_NOW} />,
    )
    expect(valueNode(container).getAttribute("data-basis")).toBe("none")
    expect(screen.getByText(/crewship page set/i)).toBeInTheDocument()
  })

  /**
   * §9b.4's distinction, at the only place narrative.v1 has one: the agent RAN
   * and had nothing to say. That is a measured emptiness — the same claim
   * `table.v1` makes with zero rows — and it is not the em dash.
   */
  it("an agent that produced no blocks is measured, not missing", () => {
    const { container } = render(<PanelRenderer {...narrativeFixtures.emptyBlocks} now={FIXTURE_NOW} />)
    const value = valueNode(container)
    expect(value.getAttribute("data-basis")).toBe("measured")
    expect(value.textContent).not.toContain(EM_DASH)
    expect(within(container).getByText(/produced no narrative/i)).toBeInTheDocument()
    // And it is NOT the never-produced instruction: there is nothing to fix.
    expect(screen.queryByText(/crewship page set/i)).toBeNull()
  })
})

describe("the panel component is exported and renders standalone", () => {
  it("renders without going through the registry", () => {
    const { container } = render(
      <NarrativePanel {...narrativeFixtures.fresh} now={FIXTURE_NOW} />,
    )
    expect(container.querySelector('[data-slot="narrative-verdict"]')).toBeTruthy()
  })
})
