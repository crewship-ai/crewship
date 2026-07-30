import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { ActorAvatar, ActorLabel } from "../inbox-actor"

// One rule carries the whole distinction: square is a machine, circle is a
// person. It matters most in the archive, where casey (agent) asked and pavel
// (user) approved sit in one row — drawn alike, that reads as two agents.

afterEach(cleanup)

describe("ActorAvatar", () => {
  it("draws an agent as a rounded square with its own face", () => {
    const { container } = render(<ActorAvatar actor={{ kind: "agent", id: "casey", label: "casey", seed: "s" }} />)
    const img = container.querySelector("img")
    expect(img).toBeTruthy()
    expect(img?.className).toContain("rounded-md")
  })

  it("falls back to the label when an agent has no seed", () => {
    const { container } = render(<ActorAvatar actor={{ kind: "agent", id: "casey", label: "casey" }} />)
    expect(container.querySelector("img")).toBeTruthy()
  })

  it("draws a person as a circle with initials, not a generated face", () => {
    const { container } = render(<ActorAvatar actor={{ kind: "user", id: "pavel", label: "pavel" }} />)
    expect(container.querySelector("img")).toBeNull()
    expect(screen.getByText("pa")).toBeInTheDocument()
    expect(container.firstElementChild?.className).toContain("rounded-full")
  })

  it("draws routines, crews and the system as glyph tiles", () => {
    for (const kind of ["routine", "crew", "system"] as const) {
      const { container, unmount } = render(<ActorAvatar actor={{ kind, id: "x", label: "x" }} />)
      expect(container.querySelector("svg")).toBeTruthy()
      expect(container.firstElementChild?.className).toContain("rounded-md")
      unmount()
    }
  })

  it("scales to the three sizes the surface uses", () => {
    for (const [size, cls] of [[20, "h-5"], [24, "h-6"], [32, "h-8"]] as const) {
      const { container, unmount } = render(<ActorAvatar actor={{ kind: "user", id: "p", label: "p" }} size={size} />)
      expect(container.firstElementChild?.className).toContain(cls)
      unmount()
    }
  })
})

describe("ActorLabel", () => {
  it("names an agent without labelling the obvious", () => {
    render(<ActorLabel actor={{ kind: "agent", id: "casey", label: "casey" }} showKind />)
    expect(screen.getByText("casey")).toBeInTheDocument()
    expect(screen.queryByText("agent")).not.toBeInTheDocument()
  })

  it("spells out the kind for everything that is not an agent", () => {
    render(<ActorLabel actor={{ kind: "routine", id: "nightly", label: "nightly" }} showKind />)
    expect(screen.getByText("routine")).toBeInTheDocument()
  })

  it("stays silent about the kind unless asked", () => {
    render(<ActorLabel actor={{ kind: "system", id: "Keeper", label: "Keeper" }} />)
    expect(screen.queryByText("system")).not.toBeInTheDocument()
  })
})
