import { afterEach, beforeEach, describe, expect, it } from "vitest"
import { DASHBOARD_DATA_MESSAGE, installDashboardBindings, type DashboardDocument } from "../artifact-dashboard"

const token = "a".repeat(32) // Frame nonce fixture, not a credential.
const doc = (overrides: Partial<DashboardDocument> = {}): DashboardDocument => ({ id: "north", version: 1, value: { revenue: 1250, region: "North" }, updated_at: "2026-10-28T08:05:00Z", source_label: "Agent", source_kind: "agent", ...overrides })
function feed(documents: DashboardDocument[], seq = 1) {
  window.dispatchEvent(new MessageEvent("message", { source: window, origin: window.location.origin, data: { type: DASHBOARD_DATA_MESSAGE, token, code_version: 1, seq, collections: [{ collection: "sales", code_version: 1, documents }] } }))
}
const description = (element: Element) => document.getElementById(element.getAttribute("aria-describedby") ?? "")?.textContent
const tooltip = () => document.querySelector('[role="tooltip"]') as HTMLElement

beforeEach(() => {
  document.head.innerHTML = ""
  document.body.innerHTML = '<div data-source="sales" data-row="north"><span id="revenue" data-field="revenue"></span><meter id="gauge" data-field="revenue" min="0" max="5000"></meter></div><table><tbody data-source="sales"><tr data-row="*"><td data-field="region"></td></tr></tbody></table>'
  installDashboardBindings({ hostOrigin: window.location.origin, codeVersion: 1, collections: ["sales"], token })
})
afterEach(() => { document.head.innerHTML = ""; document.body.innerHTML = "" })

describe("Dashboard number provenance", () => {
  it("labels every rendered value with its dataset, producer and last update", () => {
    feed([doc()])
    const value = document.getElementById("revenue")!, gauge = document.getElementById("gauge")!
    const cell = document.querySelector("td[data-field]")!
    expect(value.textContent).toBe("1250")
    const note = "Source: sales/north.revenue · Agent · updated 2026-10-28 08:05 UTC"
    for (const element of [value, gauge]) {
      expect(description(element)).toBe(note)
      expect(element.getAttribute("data-crewship-provenance")).toBe("known")
      expect(element.getAttribute("tabindex")).toBe("0")
    }
    expect(description(cell)).toBe("Source: sales/north.region · Agent · updated 2026-10-28 08:05 UTC")
    expect(Array.from(document.querySelectorAll('aside[aria-label="Data sources"] p'), line => line.textContent)).toContain(note)
  })

  it("shows the source on keyboard focus and mouse hover, and hides it again", () => {
    feed([doc({ source_label: "You", source_kind: "human" })])
    const value = document.getElementById("revenue")!
    expect(tooltip().style.display).toBe("none")
    value.focus()
    expect(tooltip().style.display).toBe("block")
    expect(tooltip().textContent).toBe("Source: sales/north.revenue · You · updated 2026-10-28 08:05 UTC")
    value.blur()
    expect(tooltip().style.display).toBe("none")
    value.dispatchEvent(new MouseEvent("mouseover", { bubbles: true }))
    expect(tooltip().style.display).toBe("block")
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }))
    expect(tooltip().style.display).toBe("none")
    // Host-owned helpers stay outside comment selection like the source list.
    expect(tooltip().getAttribute("data-row")).toBe("")
  })

  it.each([
    ["an erased producer", doc({ source_label: "Former contributor", source_kind: "unknown" })],
    ["a missing producer label", doc({ source_label: "", source_kind: "agent" })],
  ])("does not present a value from %s as authoritative", (_name, document_) => {
    feed([document_])
    const value = document.getElementById("revenue")!
    expect(value.textContent).toBe("1250")
    expect(value.getAttribute("data-crewship-provenance")).toBe("missing")
    expect(description(value)).toBe("zdroj neuveden · sales/north.revenue · updated 2026-10-28 08:05 UTC")
  })

  it("marks a binding without a loaded dataset document as unsourced", () => {
    feed([])
    const value = document.getElementById("revenue")!
    expect(value.textContent).toBe("Unavailable")
    expect(value.getAttribute("data-crewship-provenance")).toBe("missing")
    expect(description(value)).toBe("zdroj neuveden · sales/north.revenue · not loaded")
  })

  it("replaces provenance on refresh instead of retaining an earlier source", () => {
    feed([doc()])
    feed([doc({ version: 2, source_label: "You", source_kind: "human", updated_at: "2026-10-29T09:00:00Z" })], 2)
    const value = document.getElementById("revenue")!
    expect(description(value)).toBe("Source: sales/north.revenue · You · updated 2026-10-29 09:00 UTC")
    expect(document.body.textContent).not.toContain("Agent")
  })
})
