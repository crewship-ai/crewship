import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, cleanup, act } from "@testing-library/react"
import axe from "axe-core"

// a11y.spec.ts (Dashboard, Settings, Admin at 390 px): the SubBar's group in
// the top bar carried an aria-label on a role-less <div>. On a phone that
// group holds no text, so axe reports aria-prohibited-attr (serious). Every
// labelled SubBar container must have a role that permits the label.

let mobile = false
vi.mock("next/navigation", () => ({ usePathname: () => "/" }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))

import { useAppStore } from "@/lib/store"
import { SubBar } from "../sub-bar"

function withSlot(area: string) {
  const slot = document.createElement("div")
  slot.dataset.areaTitle = area
  document.body.appendChild(slot)
  act(() => useAppStore.getState().setPageBarSlot(slot))
  return slot
}

async function prohibited(): Promise<string[]> {
  const res = await axe.run(document.body, { runOnly: { type: "rule", values: ["aria-prohibited-attr"] } })
  return [...res.violations, ...res.incomplete].flatMap((v) => v.nodes.map((n) => n.html))
}

beforeEach(() => {
  cleanup()
  document.body.innerHTML = ""
  act(() => useAppStore.getState().setPageBarSlot(null))
  mobile = false
})

describe("SubBar labelled containers have a role", () => {
  for (const [label, isMobile] of [["desktop 1440", false], ["tablet 1024", false], ["phone 390", true]] as const) {
    it(`in the top bar slot at ${label}`, async () => {
      mobile = isMobile
      withSlot("Dashboard")
      render(<SubBar title="Dashboard" ariaLabel="Dashboard" heading />)
      expect(await prohibited()).toEqual([])
    })

    it(`with a section at ${label}`, async () => {
      mobile = isMobile
      withSlot("Settings")
      render(<SubBar title="Settings" section="Members" ariaLabel="Settings" heading />)
      expect(await prohibited()).toEqual([])
    })
  }

  it("without a slot (the in-page row)", async () => {
    render(<SubBar title="Admin Console" ariaLabel="Admin Console" heading />)
    expect(await prohibited()).toEqual([])
  })
})
