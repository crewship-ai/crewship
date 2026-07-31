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

  // #1530 gave Keeper its own mark in the shipped inbox: it is the security
  // gatekeeper, it wears a shield everywhere else in the product, and a cog made
  // its notices look like a settings change. That rule lived in the file this
  // release replaced, so it gets a test here — a rewrite is exactly how a
  // shipped detail goes missing.
  it("marks Keeper apart from the rest of the system senders", () => {
    const keeper = render(<ActorAvatar actor={{ kind: "system", id: "keeper", label: "Keeper", seed: "keeper" }} />)
    const keeperTile = keeper.container.firstElementChild as HTMLElement
    expect(keeperTile.className).toContain("text-success")

    const plain = render(<ActorAvatar actor={{ kind: "system", id: "consolidator", label: "consolidator" }} />)
    const plainTile = plain.container.firstElementChild as HTMLElement
    expect(plainTile.className).not.toContain("text-success")
    // Same fixed tone in every workspace — Keeper looking like a different
    // colour somewhere else is the opposite of recognisable.
    expect(keeperTile.querySelector("svg")).not.toEqual(plainTile.querySelector("svg"))
  })

  it("recognises Keeper's sub-senders too, not only the bare slug", () => {
    // keeper_skill_review, keeper_behavior, keeper_memory_health … are all
    // Keeper writing; #1530 matched only the exact "keeper" and left the rest
    // wearing the generic mark.
    const { container } = render(
      <ActorAvatar actor={{ kind: "system", id: "keeper_skill_review", label: "Keeper", seed: "keeper_skill_review" }} />,
    )
    expect((container.firstElementChild as HTMLElement).className).toContain("text-success")
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
