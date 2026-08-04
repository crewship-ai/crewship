import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"
import { MemoryExportButton } from "../memory-export-button"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

describe("MemoryExportButton", () => {
  let clicked: HTMLAnchorElement | null

  beforeEach(() => {
    apiFetch.mockReset()
    clicked = null
    global.URL.createObjectURL = vi.fn(() => "blob:x")
    global.URL.revokeObjectURL = vi.fn()
    // Capture the synthetic download without navigating.
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked = this
    })
  })
  afterEach(() => vi.restoreAllMocks())

  it("asks the SERVER for the bundle rather than building one", async () => {
    apiFetch.mockResolvedValue({ ok: true, blob: async () => new Blob(["zip"]) })
    render(<MemoryExportButton crewId="crew-1" agentSlug="alex" />)
    fireEvent.click(screen.getByTestId("memory-export-button"))

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    const url = apiFetch.mock.calls[0][0] as string
    expect(url).toContain("/api/v1/memory/export")
    expect(url).toContain("format=zip")
    expect(url).toContain("crew_id=crew-1")
    expect(url).toContain("agent_slug=alex")
  })

  it("omits agent_slug for the crew-shared scope", async () => {
    apiFetch.mockResolvedValue({ ok: true, blob: async () => new Blob(["zip"]) })
    render(<MemoryExportButton crewId="crew-1" />)
    fireEvent.click(screen.getByTestId("memory-export-button"))

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(apiFetch.mock.calls[0][0]).not.toContain("agent_slug")
  })

  it("downloads the archive under a name naming the scope", async () => {
    apiFetch.mockResolvedValue({ ok: true, blob: async () => new Blob(["zip"]) })
    render(<MemoryExportButton crewId="crew-1" agentSlug="alex" />)
    fireEvent.click(screen.getByTestId("memory-export-button"))

    await waitFor(() => expect(clicked).not.toBeNull())
    expect(clicked!.download).toBe("crewship-memory-alex.zip")
  })

  // An agent that has never written memory is not a failure, and a red
  // error for it would send the operator looking for a bug.
  it("says there is nothing yet rather than reporting a failure", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 404 })
    render(<MemoryExportButton crewId="crew-1" agentSlug="alex" />)
    fireEvent.click(screen.getByTestId("memory-export-button"))

    await waitFor(() => expect(screen.getByTestId("memory-export-error")).toBeInTheDocument())
    expect(screen.getByTestId("memory-export-error").textContent).toMatch(/no memory/i)
    expect(clicked).toBeNull()
  })

  it("reports a real failure and downloads nothing", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 500 })
    render(<MemoryExportButton crewId="crew-1" agentSlug="alex" />)
    fireEvent.click(screen.getByTestId("memory-export-button"))

    await waitFor(() => expect(screen.getByTestId("memory-export-error")).toBeInTheDocument())
    expect(screen.getByTestId("memory-export-error").textContent).toMatch(/500/)
    expect(clicked).toBeNull()
  })
})
