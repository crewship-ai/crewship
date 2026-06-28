import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"
import { Spinner } from "@/components/ui/spinner"

// The codemod that replaced ~150 inline <Loader2 className="animate-spin"/>
// spinners routed them through this primitive. Those spinners were
// decorative and almost always sit next to visible text — frequently
// nested inside an existing role="status"/aria-live region (reconnect
// banners, realtime status). So the default MUST be decorative: a baked-in
// role="status" would create nested live regions and pollute announcements.
describe("Spinner", () => {
  it("is decorative by default (aria-hidden, no announcing role)", () => {
    const { container } = render(<Spinner />)
    const svg = container.querySelector("svg")!
    expect(svg.getAttribute("aria-hidden")).toBe("true")
    expect(svg.getAttribute("role")).toBeNull()
    expect(svg.getAttribute("aria-label")).toBeNull()
    expect(svg.classList.contains("animate-spin")).toBe(true)
  })

  it("keeps the caller's size class (no forced 16px default override)", () => {
    const { container } = render(<Spinner className="h-3 w-3" />)
    const svg = container.querySelector("svg")!
    expect(svg.classList.contains("h-3")).toBe(true)
    expect(svg.classList.contains("w-3")).toBe(true)
  })

  it("lets a standalone spinner opt back into the status role", () => {
    const { container } = render(
      <Spinner role="status" aria-label="Loading" aria-hidden={undefined} />,
    )
    const svg = container.querySelector("svg")!
    expect(svg.getAttribute("role")).toBe("status")
    expect(svg.getAttribute("aria-label")).toBe("Loading")
  })
})
