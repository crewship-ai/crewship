import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

// Harbor 2 G03: the No-access state in its restricted variant.
const access = vi.hoisted(() => ({ surfaces: ["chat", "issues"] as string[] | null }))
vi.mock("@/hooks/use-access-mode", () => ({ useRestrictedSurfaces: () => access.surfaces }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => true }))

import { RestrictedUnavailable, screenName } from "../restricted-unavailable"

describe("RestrictedUnavailable", () => {
  it("names the screen by its navigation label (Vault for /credentials)", () => {
    expect(screenName("/credentials")).toBe("Vault")
    expect(screenName("/credentials/abc")).toBe("Vault")
    expect(screenName("/routines")).toBe("Routines")
    expect(screenName("/some-tool")).toBe("Some tool")
    render(<RestrictedUnavailable pathname="/credentials" />)
    expect(screen.getByRole("heading", { name: "Vault isn’t part of your access" })).toBeInTheDocument()
    expect(screen.getByText("You can open what was shared with you in this workspace.")).toBeInTheDocument()
  })

  it("offers the way to the first allowed screen and renders no admin names or Ask an admin", () => {
    render(<RestrictedUnavailable pathname="/routines" />)
    expect(screen.getByRole("link", { name: "Go to Chat" })).toHaveAttribute("href", "/chat")
    expect(screen.queryByText(/Admins:/)).toBeNull()
    expect(screen.queryByText(/Ask an admin/)).toBeNull()
    expect(screen.queryByText(/Your role is/)).toBeNull()
  })

  it("offers no way on when the session has no screens at all", () => {
    access.surfaces = []
    render(<RestrictedUnavailable pathname="/chat" />)
    expect(screen.getByRole("heading", { name: "Chat isn’t part of your access" })).toBeInTheDocument()
    expect(screen.queryByRole("link")).toBeNull()
    access.surfaces = ["chat", "issues"]
  })
})
