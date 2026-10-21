import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// COM-03: a failed load of the Crew tools section is the kit PageBanner (bad
// tone, alert) with Retry, not a hand-built destructive bar.

const m = vi.hoisted(() => ({ fetchCrewTools: vi.fn() }))
vi.mock("../crew-tools", async (orig) => ({ ...(await orig<typeof import("../crew-tools")>()), fetchCrewTools: m.fetchCrewTools }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => new Response("[]")) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { CrewToolsView } from "../views/crew-tools-view"

afterEach(() => { cleanup(); vi.clearAllMocks() })

const show = () => render(<CrewToolsView workspaceId="ws" search="" initialServerId={null} canManage />)

describe("Crew tools load failure (COM-03)", () => {
  it("renders through PageBanner with the alert role and bad tone", async () => {
    m.fetchCrewTools.mockRejectedValueOnce(new Error("tools unavailable"))
    show()
    const banner = await screen.findByRole("alert")
    expect(banner).toHaveAttribute("data-tone", "bad")
    expect(banner).toHaveClass("page-banner", "page-banner-bad")
    expect(banner).toHaveTextContent("tools unavailable")
    expect(document.querySelectorAll(".page-banner")).toHaveLength(1)
  })

  it("Retry loads again and clears the banner", async () => {
    m.fetchCrewTools.mockRejectedValueOnce(new Error("tools unavailable")).mockResolvedValueOnce([])
    show()
    fireEvent.click(within(await screen.findByRole("alert")).getByRole("button", { name: "Retry" }))
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
    expect(m.fetchCrewTools).toHaveBeenCalledTimes(2)
  })
})
