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

afterEach(cleanup)

const crew = { id: "c1", name: "Research", slug: "research" }

function openMenu(props: Partial<React.ComponentProps<typeof CrewPageBar>> = {}) {
  render(<CrewPageBar crew={crew} lead={null} onTerminal={vi.fn()} {...props} />)
  fireEvent.pointerDown(screen.getByRole("button", { name: "Crew options" }), { button: 0, ctrlKey: false })
}

describe("Crew options menu (v67 crmenu)", () => {
  it("lists View as YAML and Download between Restart agents and Copy CLI command", () => {
    openMenu({ onRestartAgents: vi.fn(), onViewYaml: vi.fn(), onDownloadYaml: vi.fn(), onDelete: vi.fn() })
    const labels = screen.getAllByRole("menuitem").map((i) => i.textContent?.trim())
    expect(labels).toEqual([
      "Restart agents",
      "View as YAML",
      "Download crewship.yaml",
      "Copy CLI command",
      "Copy link",
      "Delete crew…",
    ])
  })

  it("calls the handlers", () => {
    const onViewYaml = vi.fn()
    const onDownloadYaml = vi.fn()
    openMenu({ onViewYaml, onDownloadYaml })
    fireEvent.click(screen.getByRole("menuitem", { name: /View as YAML/ }))
    expect(onViewYaml).toHaveBeenCalledOnce()
    cleanup()
    openMenu({ onViewYaml, onDownloadYaml })
    fireEvent.click(screen.getByRole("menuitem", { name: /Download crewship.yaml/ }))
    expect(onDownloadYaml).toHaveBeenCalledOnce()
  })
})
