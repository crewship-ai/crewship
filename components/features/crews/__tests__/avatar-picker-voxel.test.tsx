import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import { AvatarPickerBody } from "@/components/features/crews/avatar-picker-dialog"
import { initialAgentDraft } from "@/components/features/crews/create-agent/types"

// #3789 — the picker offers Voxel Bot and Voxel Art, marked New, and says in
// one line how an animatable style behaves. Everything else about the layout
// stays as it was.

const HINT = "Moves gently; still when Reduce motion is on"

function renderBody(style: string | null, crewStyle: string | null = null) {
  const onChange = vi.fn()
  render(<AvatarPickerBody agentName="Filip" seed="filip" style={style} crewStyle={crewStyle} onChange={onChange} />)
  return onChange
}

function tile(label: string): HTMLElement {
  const grid = screen.getByTestId("avatar-style-grid")
  return within(grid).getByTitle(label)
}

// Uses the real appearance store; its storage reads fall back to memory.

describe("AvatarPickerBody — voxel styles", () => {
  it("shows Voxel Bot and Voxel Art in the style grid, first, each marked New", () => {
    renderBody("adventurer")
    for (const label of ["Voxel Bot", "Voxel Art"]) {
      expect(within(tile(label)).getByText("New")).toBeTruthy()
    }
    const titles = Array.from(screen.getByTestId("avatar-style-grid").querySelectorAll("button")).map((b) => b.getAttribute("title"))
    expect(titles.slice(0, 2)).toEqual(["Voxel Bot", "Voxel Art"])
  })

  it("marks no other style New", () => {
    renderBody("adventurer")
    expect(within(screen.getByTestId("avatar-style-grid")).getAllByText("New")).toHaveLength(2)
  })

  it("picks a voxel style like any other", () => {
    const onChange = renderBody("adventurer")
    fireEvent.click(tile("Voxel Art"))
    expect(onChange).toHaveBeenCalledWith({ seed: "filip", style: "voxel-art" })
  })

  it.each([
    ["Voxel Bot chosen", "voxel-bot", null, true],
    ["Voxel Art chosen", "voxel-art", null, true],
    ["following a crew with no style: the default, Voxel Bot", null, null, true],
    ["following a crew that uses Lorelei", null, "lorelei", false],
    ["Adventurer chosen", "adventurer", null, false],
  ] as const)("hint with %s", (_n, style, crewStyle, shown) => {
    renderBody(style, crewStyle)
    expect(screen.queryByText(HINT) !== null).toBe(shown)
  })

  it("names the new default in the follow-the-crew row", () => {
    renderBody(null)
    expect(screen.getByText(/the default, Voxel Bot/)).toBeTruthy()
  })
})

describe("create-agent panel", () => {
  it("starts a new agent on Voxel Bot", () => {
    expect(initialAgentDraft(null).avatarStyle).toBe("voxel-bot")
  })
})
