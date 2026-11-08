import { act, cleanup, render } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

// The phone header joins the app's top bar (SubBar's page bar slot). Its
// heading for assistive technology once read "Artifacts / [object Object]":
// the crumbs are a React node, and the heading concatenated them as a string.
vi.mock("next/navigation", () => ({ usePathname: () => "/artifacts" }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => true }))

import { useAppStore } from "@/lib/store"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameCapabilities } from "../frame"

function withSlot() {
  const slot = document.createElement("div")
  slot.dataset.areaTitle = "Artifacts"
  document.body.appendChild(slot)
  act(() => useAppStore.getState().setPageBarSlot(slot))
  return slot
}

beforeEach(() => {
  document.body.innerHTML = ""
  act(() => useAppStore.getState().setPageBarSlot(null))
})
afterEach(cleanup)

describe("ArtifactFrame phone header", () => {
  it.each([
    ["ok", { ...NO_FRAME_CAPABILITIES, access: "ok", status: { key: "readonly" } }],
    ["denied", { ...NO_FRAME_CAPABILITIES, access: "denied" }],
    ["missing", { ...NO_FRAME_CAPABILITIES, access: "missing" }],
  ] as [string, FrameCapabilities][])("never prints [object Object] (access %s) and names the artifact", (access, caps) => {
    const slot = withSlot()
    render(<ArtifactFrame name="Template Crewship Docs" kind={{ kind: "docs" }} folder="Demo Lab" caps={caps} onHome={vi.fn()} layout="phone">
      <p>body</p>
    </ArtifactFrame>)
    expect(document.body.textContent).not.toContain("[object Object]")
    expect(slot.innerHTML).not.toContain("[object Object]")
    const heading = document.querySelector("h1")
    expect(heading?.textContent).toBe(access === "ok" ? "Artifacts / Template Crewship Docs"
      : access === "denied" ? "Artifacts / Restricted artifact" : "Artifacts / Artifact")
  })
})
