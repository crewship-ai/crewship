import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/crews",
}))
vi.mock("../crews-create", () => ({ CrewsNewMenu: () => <button type="button">New</button> }))

import { CrewPageBar } from "../crews-bar"

afterEach(() => cleanup())

const crew = { id: "c1", name: "Research", slug: "research" }

function openMenu(props: Partial<React.ComponentProps<typeof CrewPageBar>> = {}) {
  render(<CrewPageBar crew={crew} lead={null} onTerminal={vi.fn()} onRestartAgents={vi.fn()} onDelete={vi.fn()} {...props} />)
  fireEvent.pointerDown(screen.getByRole("button", { name: "Crew options" }), { button: 0, ctrlKey: false })
}

describe("Crew options menu (ODCH-CR-08)", () => {
  it("lists View as YAML and Download crewship.yaml between Restart agents and Copy CLI command", async () => {
    openMenu({ onViewYaml: vi.fn(), onDownloadYaml: vi.fn() })
    const items = (await screen.findAllByRole("menuitem")).map((i) => i.textContent?.trim())
    expect(items).toEqual(["Restart agents", "View as YAML", "Download crewship.yaml", "Copy CLI command", "Copy link", "Delete crew…"])
  })

  it("View as YAML and Download call their handlers", async () => {
    const onViewYaml = vi.fn()
    const onDownloadYaml = vi.fn()
    openMenu({ onViewYaml, onDownloadYaml })
    fireEvent.click(await screen.findByRole("menuitem", { name: "View as YAML" }))
    expect(onViewYaml).toHaveBeenCalledTimes(1)
    expect(onDownloadYaml).not.toHaveBeenCalled()
  })

  it("Download crewship.yaml calls the download handler", async () => {
    const onDownloadYaml = vi.fn()
    openMenu({ onViewYaml: vi.fn(), onDownloadYaml })
    fireEvent.click(await screen.findByRole("menuitem", { name: "Download crewship.yaml" }))
    expect(onDownloadYaml).toHaveBeenCalledTimes(1)
  })
})
