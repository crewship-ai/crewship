import * as React from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import projection from "@/testdata/artifact-slides-rich-projection.json"
import v1 from "@/testdata/slides-schema-v1.json"
import type { SlideDeck } from "@/lib/slides/model"
import { arrowhead, gradientLine, slideIconNodes } from "@/lib/slides/rich"
import { parseArtifactSlides } from "@/lib/pages/artifact-slides"
import { isNativeSlideDeck, isRenderableSlideDeck } from "../native-model"
import { SlideCanvas } from "../slide-canvas"
import { SlidePresent } from "../slide-present"
import { SlidesEditor } from "../slides-editor"

// The fixture is the Go server's projection of the mirrored claude.ai deck
// (cmd/crewship TestMirrorCbbo_WebFixtureMatchesProjection keeps it exact).
const rich = () => structuredClone(projection.deck) as unknown as SlideDeck
const element = (deck: SlideDeck, id: string) => deck.slides.flatMap(slide => slide.elements).find(item => item.id === id)! as unknown as Record<string, Record<string, unknown>>
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe("schema-3 boundary", () => {
  it("renders the server's rich projection but never hands it to the native editor", () => {
    expect(isRenderableSlideDeck(rich())).toBe(true)
    expect(isNativeSlideDeck(rich())).toBe(false)
    expect(parseArtifactSlides(structuredClone(projection), "page")).not.toBeNull()
    render(<SlidesEditor deck={rich()} onChange={vi.fn()} />)
    expect(screen.getByRole("alert").textContent).toContain("unavailable")
  })
  it("keeps schema 1 admitted by both gates", () => {
    expect(isNativeSlideDeck(structuredClone(v1.deck))).toBe(true)
    expect(isRenderableSlideDeck(structuredClone(v1.deck))).toBe(true)
  })
  it.each<[string, (deck: SlideDeck) => void]>([
    ["unknown face", d => { element(d, "cover-title").text.font_family = "Arial" }],
    ["face without weight", d => { element(d, "cover-eyebrow").text.font_weight = 700 }],
    ["font size above 320", d => { element(d, "cover-title").text.font_size = 321 }],
    ["letter spacing above 100", d => { element(d, "cover-eyebrow").text.letter_spacing = 101 }],
    ["line height below 80", d => { element(d, "cover-title").text.line_height = 79 }],
    ["css in a colour", d => { element(d, "cover-panel").shape.gradient = { angle: 160, stops: [{ color: "#000000;background:url(x)", position: 0 }, { color: "#FFFFFF", position: 100 }] } }],
    ["decreasing stops", d => { element(d, "cover-panel").shape.gradient = { angle: 160, stops: [{ color: "#000000", position: 80 }, { color: "#FFFFFF", position: 10 }] } }],
    ["gradient extra key", d => { element(d, "cover-panel").shape.gradient = { angle: 160, href: "x", stops: [{ color: "#000000", position: 0 }, { color: "#FFFFFF", position: 100 }] } }],
    ["radius on ellipse", d => { Object.assign(element(d, "chart-bar1").shape, { kind: "ellipse" }) }],
    ["radius above half side", d => { element(d, "chart-bar1").shape.corner_radius = 49 }],
    ["line point outside", d => { (element(d, "flow-link1").shape.line as Record<string, unknown>).x2 = 97 }],
    ["unknown arrowhead", d => { (element(d, "flow-link1").shape.line as Record<string, unknown>).end = "url(#x)" }],
    ["unknown icon", d => { element(d, "cover-icon").icon.name = "skull" }],
    ["icon markup", d => { element(d, "cover-icon").icon.name = "<svg onload=alert(1)>" }],
    ["icon extra key", d => { element(d, "cover-icon").icon.svg = "<path d='M0 0'/>" }],
    ["unknown transition", d => { Object.assign(d.slides[0], { transition: "spin" }) }],
    ["rich field in schema 1", d => { d.schema_version = 1 }],
  ])("fails closed: %s", (_name, mutate) => {
    const deck = rich(); mutate(deck)
    expect(isRenderableSlideDeck(deck)).toBe(false)
    render(<SlideCanvas deck={deck} slideId="cover" />)
    expect(screen.getByRole("alert").textContent).toContain("unavailable")
  })
})

describe("schema-3 canvas", () => {
  it("draws typography from the bundled faces with weight, spacing and line height", () => {
    const { container } = render(<SlideCanvas deck={rich()} slideId="cover" />)
    const title = container.querySelector<HTMLElement>('[data-slide-content="cover-title"]')!
    expect(title.style.fontFamily).toBe('"IBM Plex Sans", sans-serif')
    expect(title.style.fontWeight).toBe("700")
    expect(title.style.lineHeight).toBe("1.05")
    expect(title.style.fontSize).toBe("144px")
    const eyebrow = container.querySelector<HTMLElement>('[data-slide-content="cover-eyebrow"]')!
    expect(eyebrow.style.fontFamily).toBe('"JetBrains Mono", monospace')
    expect(eyebrow.style.letterSpacing).toBe("4px")
  })
  it("paints the gradient panel with a per-canvas id and CSS angle geometry", () => {
    const { container } = render(<><SlideCanvas deck={rich()} slideId="cover" /><SlideCanvas deck={rich()} slideId="cover" /></>)
    const gradients = container.querySelectorAll("linearGradient")
    expect(gradients).toHaveLength(2)
    expect(gradients[0].id).not.toBe(gradients[1].id)
    expect(gradients[0].id).toMatch(/^slide-gradient-[A-Za-z0-9_-]+$/)
    const line = gradientLine(160, 720, 1080)
    expect(Number(gradients[0].getAttribute("x1"))).toBeCloseTo(line.x1)
    expect(Number(gradients[0].getAttribute("y2"))).toBeCloseTo(line.y2)
    expect(Array.from(gradients[0].querySelectorAll("stop")).map(stop => [stop.getAttribute("offset"), stop.getAttribute("stop-color")])).toEqual([["0%", "#16304F"], ["70%", "#0F1F33"]])
    const panel = container.querySelector('[data-element-id="cover-panel"] rect')!
    expect(panel.getAttribute("fill")).toBe(`url(#${gradients[0].id})`)
  })
  it("rounds rectangle corners inside the stroke", () => {
    const { container } = render(<SlideCanvas deck={rich()} slideId="features" />)
    const rect = container.querySelector('[data-element-id="features-card1"] rect')!
    expect(rect.getAttribute("rx")).toBe("23.5")
  })
  it("draws connectors with the shared triangle arrowhead, clipped to their box", () => {
    const { container } = render(<SlideCanvas deck={rich()} slideId="flow" />)
    const link = container.querySelector('[data-element-id="flow-link1"]')!
    const svg = link.querySelector("svg")!
    expect(svg.getAttribute("overflow")).toBe("hidden")
    const head = arrowhead(0, 12, 96, 12, 4)
    expect(link.querySelector("polygon")!.getAttribute("points")).toBe(head.points)
    expect(link.querySelector("line")!.getAttribute("x2")).toBe(String(head.bx))
  })
  it("draws icons only from the committed geometry, with no author markup", () => {
    const { container } = render(<SlideCanvas deck={rich()} slideId="cover" />)
    const icon = container.querySelector('[data-element-id="cover-icon"] svg')!
    expect(icon.getAttribute("stroke")).toBe("#E8833A")
    expect(icon.getAttribute("viewBox")).toBe("0 0 24 24")
    const nodes = slideIconNodes("users")!
    expect(Array.from(icon.children).map(child => child.tagName)).toEqual(nodes.map(([tag]) => tag))
    expect(icon.querySelector("circle")!.getAttribute("r")).toBe("4")
    for (const child of Array.from(icon.querySelectorAll("*"))) for (const attribute of Array.from(child.attributes)) expect(attribute.name).not.toMatch(/^on|href/)
  })
  it("leaves schema-1 typography exactly as before", () => {
    const { container } = render(<SlideCanvas deck={structuredClone(v1.deck)} slideId={v1.deck.slide_order[0]} />)
    for (const node of Array.from(container.querySelectorAll<HTMLElement>("[data-slide-content]"))) {
      expect(node.style.fontFamily).not.toContain("sans-serif")
      expect(node.style.fontWeight).toBe("")
      expect(node.style.letterSpacing).toBe("")
      expect(node.style.lineHeight).toBe("1.2")
    }
    expect(container.querySelector("linearGradient, [rx]")).toBeNull()
  })
})

describe("schema-3 Present", () => {
  const animate = vi.fn(() => ({ cancel: vi.fn() }))
  const motion = (reduce: boolean) => vi.spyOn(window, "matchMedia").mockImplementation(query => ({ matches: reduce && query.includes("reduce"), media: query, addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn(), onchange: null, dispatchEvent: vi.fn() }) as unknown as MediaQueryList)
  it("plays the slide's push and fade transitions on entry", () => {
    motion(false)
    Object.defineProperty(HTMLElement.prototype, "animate", { configurable: true, value: animate })
    render(<SlidePresent deck={rich()} initialSlideId="cover" onClose={vi.fn()} />)
    expect(animate).not.toHaveBeenCalled()
    act(() => { fireEvent.click(screen.getByRole("button", { name: "Next slide" })) })
    expect(animate).toHaveBeenLastCalledWith([{ transform: "translateX(100%)" }, { transform: "translateX(0)" }], expect.objectContaining({ duration: 400 }))
    act(() => { fireEvent.click(screen.getByRole("button", { name: "Next slide" })) })
    expect(animate).toHaveBeenLastCalledWith([{ opacity: 0 }, { opacity: 1 }], expect.objectContaining({ duration: 300 }))
  })
  it("shows slides at once with reduced motion", () => {
    motion(true)
    animate.mockClear()
    Object.defineProperty(HTMLElement.prototype, "animate", { configurable: true, value: animate })
    render(<SlidePresent deck={rich()} initialSlideId="cover" onClose={vi.fn()} />)
    act(() => { fireEvent.click(screen.getByRole("button", { name: "Next slide" })) })
    expect(animate).not.toHaveBeenCalled()
    expect(screen.getByText("2 / 4")).toBeTruthy()
  })
})

it("keeps the web icon set and arrowhead in step with the Go renderer's constants", () => {
  expect(slideIconNodes("users")).toBeDefined()
  expect(slideIconNodes("__proto__")).toBeUndefined()
  expect(slideIconNodes("constructor")).toBeUndefined()
  const head = arrowhead(0, 0, 100, 0, 4)
  expect([head.bx, head.by]).toEqual([84, 0])
  expect(head.points).toBe("100,0 84,8 84,-8")
  expect(React).toBeDefined()
})
