import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"
import { Link2, Settings } from "lucide-react"

let mobile = false
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))

import { DrillPage } from "../drill-page"

function renderPage() {
  return render(
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: Settings }}
      title="Crew links"
      icon={Link2}
      description="Who hands work to whom"
      nav={<div>nav</div>}
    >
      <div>content</div>
    </DrillPage>,
  )
}

// The nested page had the path twice: "Settings / Crew links" on the sub-bar
// and again at the top of its panel ("← Settings", "Crew links", the
// description), where the way back was small grey text. Now the sub-bar
// carries one bordered back button, the name and the description, and the
// panel starts with its own controls.
describe("DrillPage header", () => {
  beforeEach(() => { cleanup(); mobile = false })

  it("has exactly one way back, to the parent, labelled as a control", () => {
    renderPage()
    const back = screen.getAllByRole("link", { name: "Back to Settings" })
    expect(back).toHaveLength(1)
    expect(back[0]).toHaveAttribute("href", "/settings")
    expect(back[0]).toHaveTextContent("Settings")
  })

  it("names the page once, with its description on the sub-bar", () => {
    renderPage()
    expect(screen.getAllByText("Crew links")).toHaveLength(1)
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Crew links")
    expect(screen.getAllByText("Who hands work to whom")).toHaveLength(1)
  })

  it("keeps the back button on a phone as an arrow", () => {
    mobile = true
    renderPage()
    const back = screen.getByRole("link", { name: "Back to Settings" })
    expect(back).not.toHaveTextContent("Settings")
  })
})
