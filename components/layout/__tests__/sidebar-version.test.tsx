import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
let role = "OWNER"
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role }) }))
vi.mock("@/components/ui/sidebar", () => ({
  SidebarMenuItem: ({ children }: { children: React.ReactNode }) => <li>{children}</li>,
  SidebarMenuButton: ({ children, asChild, tooltip: _t, size: _s, ...rest }: { children: React.ReactNode; asChild?: boolean; tooltip?: string; size?: string }) =>
    asChild ? <>{children}</> : <button {...rest}>{children}</button>,
}))

import { SidebarVersion, buildLabel, editionName } from "../sidebar-version"

const ok = (b: unknown) => ({ ok: true, json: async () => b })

beforeEach(() => {
  role = "OWNER"
  api.mockReset()
  api.mockImplementation(async (u: string) =>
    u.includes("version") ? ok({ current: "dev", commit: "d43bfd1f95636cbc", dirty: false }) : ok({ edition: "community" }))
})

describe("buildLabel", () => {
  it("shows a release tag as is and a dev build by its commit", () => {
    expect(buildLabel({ current: "v0.9.2", commit: "abc" })).toBe("v0.9.2")
    expect(buildLabel({ current: "dev", commit: "d43bfd1f95", dirty: true })).toBe("dev · d43bfd1+")
  })

  it("names the edition", () => {
    expect(editionName("community")).toBe("Community Edition")
    expect(editionName("acme")).toBe("Acme Edition")
  })
})

describe("SidebarVersion", () => {
  it("shows the edition and build, linking an admin to the overview", async () => {
    render(<SidebarVersion />)
    expect(await screen.findByText("Community Edition")).toBeInTheDocument()
    expect(screen.getByText("dev · d43bfd1")).toBeInTheDocument()
    expect(screen.getByRole("link").getAttribute("href")).toBe("/admin?tab=overview")
  })

  it("is a plain label for a member", async () => {
    role = "MEMBER"
    render(<SidebarVersion />)
    await screen.findByText("Community Edition")
    expect(screen.queryByRole("link")).toBeNull()
  })

  it("says nothing when the build cannot be read", async () => {
    api.mockImplementation(async () => ({ ok: false, json: async () => null }))
    const { container } = render(<SidebarVersion />)
    await waitFor(() => expect(api).toHaveBeenCalledTimes(2))
    expect(container.textContent).toBe("")
  })
})
