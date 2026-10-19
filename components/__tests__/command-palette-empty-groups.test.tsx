import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

import { apiFetch } from "@/lib/api-fetch"

// ODCH-G05 (v67 core.js cmdk): the empty palette is four groups, Recent /
// Quick actions / Go to / Switch workspace. The list of every settings page
// joins only while something is typed, like the other search results.

const h = vi.hoisted(() => ({ setWorkspaceId: vi.fn() }))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({
    workspaceId: "ws-test",
    role: "OWNER",
    workspaces: [
      { id: "ws-test", name: "Harbor", slug: "harbor" },
      { id: "ws-2", name: "Northstar Studio", slug: "northstar" },
    ],
    setWorkspaceId: h.setWorkspaceId,
  }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useIsInstanceAdmin: () => true,
  useSessionSafe: () => ({ data: { user: { id: "u-test" } }, status: "authenticated" }),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(), broadcastSessionExpired: vi.fn() }))

import { CommandPalette } from "../command-palette"

beforeEach(() => {
  h.setWorkspaceId.mockReset()
  vi.mocked(apiFetch).mockImplementation(async () => ({ ok: true, status: 200, json: async () => [] }) as unknown as Response)
  vi.mocked(localStorage.getItem).mockReturnValue(
    JSON.stringify([{ href: "/issues/ENG-2", label: "Rewrite the README", group: "Issues" }]),
  )
})
afterEach(cleanup)

const headings = () =>
  screen.getAllByRole("group").map((g) => document.getElementById(g.getAttribute("aria-labelledby") ?? "")?.textContent ?? "")

describe("CommandPalette — empty state groups", () => {
  it("is Recent, Quick actions, Go to and Switch workspace, in that order", async () => {
    render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    await screen.findByRole("group", { name: /go to/i })
    expect(headings()).toEqual(["Recent", "Quick actions", "Go to", "Switch workspace"])
  })

  it("offers the other workspaces without typing, and switches on select", async () => {
    render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    const g = await screen.findByRole("group", { name: /switch workspace/i })
    expect(within(g).queryByText("Harbor")).toBeNull()
    fireEvent.click(within(g).getByText("Northstar Studio"))
    expect(h.setWorkspaceId).toHaveBeenCalledWith("ws-2")
  })

  it("brings the settings pages in once something is typed, as before", async () => {
    render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    await screen.findByRole("group", { name: /go to/i })
    expect(screen.queryByRole("group", { name: /^settings$/i })).toBeNull()
    fireEvent.change(screen.getByPlaceholderText(/search issues/i), { target: { value: "appearance" } })
    const g = await screen.findByRole("group", { name: /^settings$/i })
    expect(within(g).getByText("Appearance")).toBeInTheDocument()
  })
})
